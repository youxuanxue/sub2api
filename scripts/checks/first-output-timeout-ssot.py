#!/usr/bin/env python3
"""Fail when first-output timeout owners drift from ops/stage0/first_output_timeout_ssot.py.

Writable copies that have bitten before (compose inject without OpenAI lines,
viper defaults, remediate WANT_*, Go stream defaults, config.example.yaml) must
all agree with the SSOT table. Live-host value drift is covered separately by
assert-live-host-state.sh (prod) /
assert-first-output-timeouts.sh (edges) → live_host_state_verdict.py.
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
STAGE0 = REPO_ROOT / "ops" / "stage0"
sys.path.insert(0, str(STAGE0))
from first_output_timeout_ssot import (  # noqa: E402
    EXPECTED_ENV,
    EXPECTED_GO_STREAM_SECONDS,
    EXPECTED_VIPER,
)

CONFIG_GO = REPO_ROOT / "backend" / "internal" / "config" / "config.go"
STREAM_GO = (
    REPO_ROOT / "backend" / "internal" / "service" / "stream_first_output_budget_tk.go"
)
COMPOSE_INJECT = STAGE0 / "deploy_via_ssm_bluegreen.sh"
CONFIG_EXAMPLE = REPO_ROOT / "deploy" / "config.example.yaml"
REMEDIATE = REPO_ROOT / "ops" / "observability" / "remediate-first-output-timeouts.sh"


def _read(path: pathlib.Path) -> str:
    return path.read_text(encoding="utf-8")


def check_viper_defaults(text: str) -> list[str]:
    errors = []
    for key, value in EXPECTED_VIPER.items():
        needle = f'viper.SetDefault("{key}", {value})'
        if needle not in text:
            errors.append(f"config.go missing {needle}")
    return errors


def check_config_example(text: str) -> list[str]:
    errors = []
    # yaml keys are bare (no gateway. prefix)
    for key, value in EXPECTED_VIPER.items():
        yaml_key = key.split(".", 1)[1]
        pattern = re.compile(
            rf"(?m)^\s*{re.escape(yaml_key)}:\s*{value}\s*(?:#.*)?$"
        )
        if not pattern.search(text):
            errors.append(
                f"deploy/config.example.yaml missing `{yaml_key}: {value}`"
            )
    return errors


def check_compose_inject(text: str) -> list[str]:
    errors = []
    for key, value in EXPECTED_ENV.items():
        needle = f"{key}=${{{key}:-{value}}}"
        count = text.count(needle)
        if count < 2:
            # blue + green blocks
            errors.append(
                f"deploy_via_ssm_bluegreen.sh expected ≥2× `{needle}` (got {count})"
            )
    return errors


def check_remediate(text: str) -> list[str]:
    errors = []
    want_map = {
        "WANT_STREAM": EXPECTED_ENV["GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS"],
        "WANT_STREAM_HIGH": EXPECTED_ENV[
            "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS"
        ],
        "WANT_OPENAI": EXPECTED_ENV["GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS"],
        "WANT_OPENAI_HIGH": EXPECTED_ENV[
            "GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS"
        ],
    }
    for name, value in want_map.items():
        pattern = re.compile(rf"(?m)^{name}={value}\s*$")
        if not pattern.search(text):
            errors.append(f"remediate-first-output-timeouts.sh missing `{name}={value}`")
    for key, value in EXPECTED_ENV.items():
        comment_line = f"#   {key}={value}"
        if comment_line not in text:
            errors.append(
                f"remediate-first-output-timeouts.sh target comment missing `{comment_line}`"
            )
    return errors


def check_go_stream_defaults(text: str) -> list[str]:
    errors = []
    for name, seconds in EXPECTED_GO_STREAM_SECONDS.items():
        pattern = re.compile(
            rf"(?m)^\s*{re.escape(name)}\s*=\s*{seconds}\s*\*\s*time\.Second\s*$"
        )
        if not pattern.search(text):
            errors.append(
                f"stream_first_output_budget_tk.go missing `{name} = {seconds} * time.Second`"
            )
    return errors


def evaluate(
    *,
    config_go: str,
    stream_go: str,
    compose: str,
    example: str,
    remediate: str,
) -> list[str]:
    errors: list[str] = []
    errors.extend(check_viper_defaults(config_go))
    errors.extend(check_go_stream_defaults(stream_go))
    errors.extend(check_compose_inject(compose))
    errors.extend(check_config_example(example))
    errors.extend(check_remediate(remediate))
    return errors


def _selftest() -> int:
    good_config = "\n".join(
        f'viper.SetDefault("{k}", {v})' for k, v in EXPECTED_VIPER.items()
    )
    good_stream = "\n".join(
        f"\t{name} = {sec} * time.Second"
        for name, sec in EXPECTED_GO_STREAM_SECONDS.items()
    )
    good_compose = "\n".join(
        f"      - {k}=${{{k}:-{v}}}" for k, v in EXPECTED_ENV.items()
    )
    # blue + green
    good_compose = good_compose + "\n" + good_compose
    good_example = "\n".join(
        f"  {k.split('.', 1)[1]}: {v}" for k, v in EXPECTED_VIPER.items()
    )
    good_remediate = "\n".join(
        [
            f"#   {k}={v}" for k, v in EXPECTED_ENV.items()
        ]
        + [
            f"WANT_STREAM={EXPECTED_ENV['GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS']}",
            f"WANT_STREAM_HIGH={EXPECTED_ENV['GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS']}",
            f"WANT_OPENAI={EXPECTED_ENV['GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS']}",
            f"WANT_OPENAI_HIGH={EXPECTED_ENV['GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS']}",
        ]
    )

    cases = []
    cases.append(
        (
            "aligned owners → no errors",
            evaluate(
                config_go=good_config,
                stream_go=good_stream,
                compose=good_compose,
                example=good_example,
                remediate=good_remediate,
            ),
            [],
        )
    )

    want_stream = EXPECTED_VIPER["gateway.stream_first_output_timeout_seconds"]
    bad_viper = good_config.replace(
        f'viper.SetDefault("gateway.stream_first_output_timeout_seconds", {want_stream})',
        'viper.SetDefault("gateway.stream_first_output_timeout_seconds", 15)',
    )
    got = evaluate(
        config_go=bad_viper,
        stream_go=good_stream,
        compose=good_compose,
        example=good_example,
        remediate=good_remediate,
    )
    cases.append(
        (
            "viper stream default drift → error",
            any(
                "stream_first_output_timeout_seconds" in e and f", {want_stream})" in e
                for e in got
            ),
            True,
        )
    )

    want_env = EXPECTED_ENV["GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS"]
    bad_compose = good_compose.replace(f":-{want_env}}}", ":-15}", 1)
    got = evaluate(
        config_go=good_config,
        stream_go=good_stream,
        compose=bad_compose,
        example=good_example,
        remediate=good_remediate,
    )
    cases.append(
        (
            "compose inject drift → error",
            any("GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS" in e for e in got),
            True,
        )
    )

    ok = True
    for name, got, want in cases:
        passed = got == want
        print(f"{'PASS' if passed else 'FAIL'} {name}")
        if not passed:
            print(f"    got={got!r} want={want!r}")
            ok = False
    return 0 if ok else 1


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        return _selftest()

    missing = [
        p
        for p in (CONFIG_GO, STREAM_GO, COMPOSE_INJECT, CONFIG_EXAMPLE, REMEDIATE)
        if not p.is_file()
    ]
    if missing:
        for p in missing:
            print(f"FAIL: missing {p.relative_to(REPO_ROOT)}", file=sys.stderr)
        return 1

    errors = evaluate(
        config_go=_read(CONFIG_GO),
        stream_go=_read(STREAM_GO),
        compose=_read(COMPOSE_INJECT),
        example=_read(CONFIG_EXAMPLE),
        remediate=_read(REMEDIATE),
    )
    if errors:
        if not args.quiet:
            print("FAIL: first-output timeout owners drifted from SSOT:")
            for err in errors:
                print(f"  - {err}")
            print(
                "  reconcile with ops/stage0/first_output_timeout_ssot.py "
                "(single owner of intended numbers)"
            )
        else:
            for err in errors:
                print(f"FAIL: {err}")
        return 1
    if not args.quiet:
        print("ok: first-output timeout owners match SSOT")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
