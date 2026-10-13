#!/usr/bin/env python3
"""Generate first-output timeout writable copies from SSOT, or fail on drift.

Single owner of numbers: ops/stage0/first_output_timeout_ssot.py

Workflow:
  1. Edit ops/stage0/first_output_timeout_ssot.py
  2. python3 scripts/checks/first-output-timeout-ssot.py --apply
  3. Commit SSOT + generated files

  python3 scripts/checks/first-output-timeout-ssot.py --check   # CI / preflight
  python3 scripts/checks/first-output-timeout-ssot.py --selftest

Generated regions are marked with `first-output-timeout-ssot:begin/end`.
Env fallbacks (`KEY=${KEY:-N}`) outside markers are rewritten for EXPECTED_ENV
keys (compose insert helpers inside remediate, etc.).
"""

from __future__ import annotations

import argparse
import pathlib
import re
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
STAGE0 = REPO_ROOT / "ops" / "stage0"
sys.path.insert(0, str(STAGE0))


def _purge_ssot_bytecode() -> None:
    """Drop stale SSOT .pyc so --apply/--check never read a mutated cache."""
    cache = STAGE0 / "__pycache__"
    if not cache.is_dir():
        return
    for path in cache.glob("first_output_timeout_ssot*.pyc"):
        try:
            path.unlink()
        except OSError:
            pass
    sys.modules.pop("first_output_timeout_ssot", None)


_purge_ssot_bytecode()
from first_output_timeout_ssot import (  # noqa: E402
    EXPECTED_ENV,
    EXPECTED_GO_STREAM_SECONDS,
    EXPECTED_VIPER,
    OPENAI_FORCE_HIGH_BODY_BYTES,
)

BEGIN = "first-output-timeout-ssot:begin"
END = "first-output-timeout-ssot:end"

CONFIG_GO = REPO_ROOT / "backend" / "internal" / "config" / "config.go"
STREAM_GO = (
    REPO_ROOT / "backend" / "internal" / "service" / "stream_first_output_budget_tk.go"
)
OPENAI_FIRST_OUTPUT_GO = (
    REPO_ROOT / "backend" / "internal" / "service" / "openai_first_output_timeout.go"
)
COMPOSE_INJECT = STAGE0 / "deploy_via_ssm_bluegreen.sh"
CONFIG_EXAMPLE = REPO_ROOT / "deploy" / "config.example.yaml"
REMEDIATE = REPO_ROOT / "ops" / "observability" / "remediate-first-output-timeouts.sh"

TARGETS = (
    CONFIG_GO,
    STREAM_GO,
    OPENAI_FIRST_OUTPUT_GO,
    COMPOSE_INJECT,
    CONFIG_EXAMPLE,
    REMEDIATE,
)


def _read(path: pathlib.Path) -> str:
    return path.read_text(encoding="utf-8")


def _write(path: pathlib.Path, text: str) -> None:
    path.write_text(text, encoding="utf-8")


def _gofmt(text: str) -> str:
    """Normalize Go sources so --check matches `gofmt -w`."""
    proc = subprocess.run(
        ["gofmt"],
        input=text,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        raise ValueError(f"gofmt failed: {proc.stderr.strip() or proc.returncode}")
    return proc.stdout


def _large_body_go_expr() -> str:
    if OPENAI_FORCE_HIGH_BODY_BYTES == 1_048_576:
        return "1 * 1024 * 1024"
    return str(OPENAI_FORCE_HIGH_BODY_BYTES)


def replace_regions(text: str, new_body: str) -> tuple[str, int]:
    """Replace every marked region body. new_body should end with newline."""
    if not new_body.endswith("\n"):
        new_body += "\n"
    lines = text.splitlines(keepends=True)
    out: list[str] = []
    i = 0
    count = 0
    while i < len(lines):
        if BEGIN in lines[i]:
            out.append(lines[i])
            i += 1
            while i < len(lines) and END not in lines[i]:
                i += 1
            out.append(new_body)
            if i >= len(lines) or END not in lines[i]:
                raise ValueError(f"missing {END} marker after {BEGIN}")
            out.append(lines[i])
            i += 1
            count += 1
            continue
        out.append(lines[i])
        i += 1
    return "".join(out), count


def rewrite_env_fallbacks(text: str) -> str:
    for key, value in EXPECTED_ENV.items():
        text = re.sub(
            rf"{re.escape(key)}=\$\{{{re.escape(key)}:-\d+\}}",
            f"{key}=${{{key}:-{value}}}",
            text,
        )
    return text


def rewrite_remediate_summary_json(text: str) -> str:
    stream = EXPECTED_ENV["GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS"]
    stream_high = EXPECTED_ENV[
        "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS"
    ]
    openai = EXPECTED_ENV["GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS"]
    openai_high = EXPECTED_ENV[
        "GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS"
    ]
    return re.sub(
        r'\{"apply":true,"stream":\d+,"stream_high":\d+,"openai":\d+,"openai_high":\d+,"container":"',
        f'{{"apply":true,"stream":{stream},"stream_high":{stream_high},'
        f'"openai":{openai},"openai_high":{openai_high},"container":"',
        text,
        count=1,
    )


def body_config_go() -> str:
    lines = [f'\tviper.SetDefault("{key}", {value})' for key, value in EXPECTED_VIPER.items()]
    return "\n".join(lines) + "\n"


def body_config_example() -> str:
    lines = [
        "  # Generated — edit ops/stage0/first_output_timeout_ssot.py then:",
        "  #   python3 scripts/checks/first-output-timeout-ssot.py --apply",
    ]
    for key, value in EXPECTED_VIPER.items():
        yaml_key = key.split(".", 1)[1]
        lines.append(f"  {yaml_key}: {value}")
    return "\n".join(lines) + "\n"


def body_stream_go() -> str:
    lines = [
        f"\t{name} = {seconds} * time.Second"
        for name, seconds in EXPECTED_GO_STREAM_SECONDS.items()
    ]
    return "\n".join(lines) + "\n"


def body_openai_large_body() -> str:
    return (
        "\t// OPENAI_FORCE_HIGH_BODY_BYTES from first_output_timeout_ssot.py\n"
        f"\topenAIFirstOutputLargeBodyBytes = {_large_body_go_expr()}\n"
    )


def body_compose_env() -> str:
    lines = [
        f"      - {key}=${{{key}:-{value}}}" for key, value in EXPECTED_ENV.items()
    ]
    return "\n".join(lines) + "\n"


def body_remediate_want() -> str:
    stream = EXPECTED_ENV["GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS"]
    stream_high = EXPECTED_ENV[
        "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS"
    ]
    openai = EXPECTED_ENV["GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS"]
    openai_high = EXPECTED_ENV[
        "GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS"
    ]
    comment_lines = "\n".join(f"#   {k}={v}" for k, v in EXPECTED_ENV.items())
    return (
        "# Generated — edit ops/stage0/first_output_timeout_ssot.py then:\n"
        "#   python3 scripts/checks/first-output-timeout-ssot.py --apply\n"
        "#\n"
        "# Target:\n"
        f"{comment_lines}\n"
        f"WANT_STREAM={stream}\n"
        f"WANT_STREAM_HIGH={stream_high}\n"
        f"WANT_OPENAI={openai}\n"
        f"WANT_OPENAI_HIGH={openai_high}\n"
    )


def render_path(path: pathlib.Path, text: str) -> str:
    if path == CONFIG_GO:
        out, n = replace_regions(text, body_config_go())
        if n != 1:
            raise ValueError(f"{path.name}: expected 1 marked region, got {n}")
        return _gofmt(out)
    if path == CONFIG_EXAMPLE:
        out, n = replace_regions(text, body_config_example())
        if n != 1:
            raise ValueError(f"{path.name}: expected 1 marked region, got {n}")
        return out
    if path == STREAM_GO:
        out, n = replace_regions(text, body_stream_go())
        if n != 1:
            raise ValueError(f"{path.name}: expected 1 marked region, got {n}")
        return _gofmt(out)
    if path == OPENAI_FIRST_OUTPUT_GO:
        out, n = replace_regions(text, body_openai_large_body())
        if n != 1:
            raise ValueError(f"{path.name}: expected 1 marked region, got {n}")
        return _gofmt(out)
    if path == COMPOSE_INJECT:
        out, n = replace_regions(text, body_compose_env())
        if n < 2:
            raise ValueError(f"{path.name}: expected ≥2 marked regions (blue/green), got {n}")
        return rewrite_env_fallbacks(out)
    if path == REMEDIATE:
        out, n = replace_regions(text, body_remediate_want())
        if n != 1:
            raise ValueError(f"{path.name}: expected 1 marked region, got {n}")
        out = rewrite_env_fallbacks(out)
        return rewrite_remediate_summary_json(out)
    raise ValueError(f"unknown target {path}")


def render_all() -> dict[pathlib.Path, str]:
    return {path: render_path(path, _read(path)) for path in TARGETS}


def drift_errors(rendered: dict[pathlib.Path, str]) -> list[str]:
    errors: list[str] = []
    for path, want in rendered.items():
        got = _read(path)
        if got != want:
            errors.append(
                f"{path.relative_to(REPO_ROOT)} drifted — "
                "run: python3 scripts/checks/first-output-timeout-ssot.py --apply"
            )
    return errors


def apply_all() -> list[pathlib.Path]:
    changed: list[pathlib.Path] = []
    for path, want in render_all().items():
        if _read(path) != want:
            _write(path, want)
            changed.append(path)
    return changed


def _selftest() -> int:
    # Marker replace round-trip.
    sample = (
        "head\n"
        f"\t// {BEGIN}\n"
        "\told\n"
        f"\t// {END}\n"
        "tail\n"
    )
    out, n = replace_regions(sample, "\tnew\n")
    assert n == 1, n
    assert "\tnew\n" in out and "old" not in out

    # Env fallback rewrite.
    key = "GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS"
    val = EXPECTED_ENV[key]
    rewritten = rewrite_env_fallbacks(f"x {key}=${{{key}:-999}} y")
    assert f"{key}=${{{key}:-{val}}}" in rewritten, rewritten

    # Render must succeed against the live tree (markers present).
    try:
        rendered = render_all()
    except Exception as exc:  # noqa: BLE001 — selftest surface
        print(f"FAIL render_all: {exc}")
        return 1
    if drift_errors(rendered):
        # Tree may already be applied; re-render after apply should be clean.
        apply_all()
        rendered = render_all()
    errs = drift_errors(rendered)
    if errs:
        print("FAIL selftest still drifted after apply:")
        for e in errs:
            print(f"  - {e}")
        return 1

    # Synthetic drift detection.
    path = CONFIG_GO
    original = _read(path)
    try:
        broken, _ = replace_regions(
            original,
            '\tviper.SetDefault("gateway.stream_first_output_timeout_seconds", 15)\n',
        )
        _write(path, broken)
        if not drift_errors(render_all()):
            print("FAIL synthetic drift not detected")
            return 1
        apply_all()
        if drift_errors(render_all()):
            print("FAIL apply did not clear synthetic drift")
            return 1
    finally:
        _write(path, original)
        apply_all()

    print("PASS region replace")
    print("PASS env fallback rewrite")
    print("PASS render/apply/check loop")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument(
        "--apply",
        action="store_true",
        help="rewrite generated owners from ops/stage0/first_output_timeout_ssot.py",
    )
    mode.add_argument(
        "--check",
        action="store_true",
        help="fail if generated owners drifted (default)",
    )
    mode.add_argument("--selftest", action="store_true")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        return _selftest()

    missing = [p for p in TARGETS if not p.is_file()]
    if missing:
        for p in missing:
            print(f"FAIL: missing {p.relative_to(REPO_ROOT)}", file=sys.stderr)
        return 1

    for path in TARGETS:
        text = _read(path)
        if BEGIN not in text or END not in text:
            print(
                f"FAIL: {path.relative_to(REPO_ROOT)} missing {BEGIN}/{END} markers",
                file=sys.stderr,
            )
            return 1

    if args.apply:
        changed = apply_all()
        if not args.quiet:
            if changed:
                print("applied first-output timeout SSOT →")
                for p in changed:
                    print(f"  - {p.relative_to(REPO_ROOT)}")
            else:
                print("ok: generated owners already match SSOT")
        return 0

    # Default / --check
    try:
        errors = drift_errors(render_all())
    except ValueError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1
    if errors:
        if not args.quiet:
            print("FAIL: first-output timeout generated owners drifted from SSOT:")
            for err in errors:
                print(f"  - {err}")
        else:
            for err in errors:
                print(f"FAIL: {err}")
        return 1
    if not args.quiet:
        print("ok: first-output timeout generated owners match SSOT")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
