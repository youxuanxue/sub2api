#!/usr/bin/env python3
"""ssot-delta-gate — SSOT delta discovery for catalog diffs.

Structural catalog contracts stay in catalog-serving-drift.py (preflight, zero
HTTP). PR CI runs this gate with --skip-live. Post-deploy closeout owns focused
live HTTP proof via endpoint-compat-audit, or this script can run the same proof
when explicitly invoked without --skip-live after rollout.

Catalog touch paths (any diff base..HEAD):
  - backend/internal/service/tk_served_models.json
  - backend/internal/service/pricing_catalog_supported_models_tk.go
  - backend/internal/service/tk_pricing_overlay.json
  - backend/migrations/tk_*.sql

Subcommands:
  paths-changed  — prints true/false (for GHA step outputs)
  needs-live     — prints true/false: catalog paths changed AND delta models need post-deploy HTTP proof
  discover       — list model ids that need post-deploy HTTP proof
  check          — list pending-live ids with --skip-live, or run focused live gate
  selftest       — fixture selftest

Exit: 0 ok/skip, 1 gate fail, 2 error.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
PRICING_DIR = REPO / "ops" / "pricing"
if str(PRICING_DIR) not in sys.path:
    sys.path.insert(0, str(PRICING_DIR))
from pricing_registry import has_complete_price
from servable_allowlist import ALLOWLIST_PLATFORMS, parse_allowlist_maps

GO_REL = "backend/internal/service/pricing_catalog_supported_models_tk.go"
MANIFEST_REL = "backend/internal/service/tk_served_models.json"
OVERLAY_REL = "backend/internal/service/tk_pricing_overlay.json"
MIGRATION_PREFIX = "backend/migrations/tk_"
MATRIX = REPO / "ops/test/gateway_model_ssot_matrix.py"

_manifest_spec = importlib.util.spec_from_file_location(
    "tk_served_models_manifest",
    REPO / "ops" / "pricing" / "served_models_manifest.py",
)
_MANIFEST = importlib.util.module_from_spec(_manifest_spec)
_manifest_spec.loader.exec_module(_MANIFEST)

MODEL_ID_RE = re.compile(r'"([a-zA-Z0-9][a-zA-Z0-9._-]{0,127})"\s*:\s*"')
MODEL_ID_FULL_RE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$")
MAX_FOCUSED_MODELS = 50
MIGRATION_ADDED_MODEL_RE = re.compile(
    r'^\+\s*"(?P<id>[a-zA-Z0-9][a-zA-Z0-9._-]{0,127})"\s*:\s*"(?P=id)"'
)


def _git(*args: str) -> str:
    return subprocess.check_output(["git", "-C", str(REPO), *args], text=True)


def _base_resolves(base: str) -> bool:
    try:
        _git("rev-parse", "--verify", "--quiet", f"{base}^{{commit}}")
        return True
    except subprocess.CalledProcessError:
        return False


def changed_paths(base: str) -> list[str]:
    if not _base_resolves(base):
        return []
    raw = _git("diff", "--name-only", f"{base}...HEAD")
    return [p for p in raw.splitlines() if p.strip()]


def catalog_paths_changed(base: str) -> bool:
    for path in changed_paths(base):
        if path in (MANIFEST_REL, GO_REL, OVERLAY_REL):
            return True
        if path.startswith(MIGRATION_PREFIX) and path.endswith(".sql"):
            return True
    return False


def parse_allowlist(go_text: str) -> dict[str, set[str]]:
    return parse_allowlist_maps(go_text)


def _read_at(base: str, rel: str) -> str:
    try:
        return _git("show", f"{base}:{rel}")
    except subprocess.CalledProcessError:
        return ""


def _load_manifest(text: str) -> dict[str, object]:
    if not text.strip():
        return {}
    return _MANIFEST.parse_manifest_text(text).by_model()


def _overlay_priced(entry: object) -> bool:
    return has_complete_price(entry)


def local_displayed_pricing_models() -> set[str]:
    proc = subprocess.run(
        [
            sys.executable,
            str(MATRIX),
            "list",
            "--source",
            "local-pricing",
            "--format",
            "json",
        ],
        cwd=REPO,
        text=True,
        capture_output=True,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            "failed to derive local pricing projection for ssot delta gate: "
            + (proc.stderr.strip() or proc.stdout.strip())
        )
    payload = json.loads(proc.stdout)
    rows = payload.get("rows") or []
    return {str(row.get("model") or "").strip() for row in rows if row.get("model")}


def overlay_delta_models_from_payloads(
    base_overlay: dict[str, object],
    head_overlay: dict[str, object],
    displayed_models: set[str],
) -> set[str]:
    out: set[str] = set()
    for key, head_entry in head_overlay.items():
        if key == "_meta":
            continue
        base_entry = base_overlay.get(key)
        if (
            _overlay_priced(head_entry)
            and not _overlay_priced(base_entry)
            and key in displayed_models
        ):
            out.add(key)
    return out


def manifest_delta_models(base: str) -> set[str]:
    base_text = _read_at(base, MANIFEST_REL)
    head_text = (REPO / MANIFEST_REL).read_text(encoding="utf-8")
    base_entries = _load_manifest(base_text)
    head_entries = _load_manifest(head_text)
    out: set[str] = set()
    for model_id, head in head_entries.items():
        if not head.display:
            continue
        base_entry = base_entries.get(model_id)
        if base_entry is None:
            out.add(model_id)
            continue
        if not base_entry.display:
            out.add(model_id)
            continue
        if base_entry.projection() != head.projection():
            out.add(model_id)
    return out


def allowlist_delta_models(base: str) -> set[str]:
    base_al = parse_allowlist(_read_at(base, GO_REL))
    head_al = parse_allowlist((REPO / GO_REL).read_text(encoding="utf-8"))
    out: set[str] = set()
    for plat in ALLOWLIST_PLATFORMS:
        out |= head_al.get(plat, set()) - base_al.get(plat, set())
    return out


def overlay_delta_models(base: str) -> set[str]:
    try:
        base_overlay = json.loads(_read_at(base, OVERLAY_REL) or "{}")
    except json.JSONDecodeError:
        base_overlay = {}
    try:
        head_overlay = json.loads((REPO / OVERLAY_REL).read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return set()
    return overlay_delta_models_from_payloads(
        base_overlay,
        head_overlay,
        local_displayed_pricing_models(),
    )


def migration_delta_models(base: str) -> set[str]:
    if not _base_resolves(base):
        return set()
    out: set[str] = set()
    for path in changed_paths(base):
        if not path.startswith(MIGRATION_PREFIX) or not path.endswith(".sql"):
            continue
        try:
            diff = _git("diff", f"{base}...HEAD", "--", path)
        except subprocess.CalledProcessError:
            continue
        for line in diff.splitlines():
            if not line.startswith("+") or line.startswith("+++"):
                continue
            m = MIGRATION_ADDED_MODEL_RE.match(line)
            if m:
                out.add(m.group("id"))
                continue
            for mid in MODEL_ID_RE.findall(line):
                if mid not in {"model_mapping", "credentials", "jsonb"}:
                    out.add(mid)
    return out


def discover_models(base: str) -> set[str]:
    if not catalog_paths_changed(base):
        return set()
    models: set[str] = set()
    models |= manifest_delta_models(base)
    models |= allowlist_delta_models(base)
    models |= overlay_delta_models(base)
    models |= migration_delta_models(base)
    return {m for m in models if m and m != "_meta"}


def run_focused_gate(models: set[str], *, base_url: str, key: str) -> int:
    cmd = [
        sys.executable,
        str(MATRIX),
        "gate",
        "--show-excluded",
        "--require-rows",
        "--source",
        "local-pricing",
        "--base-url",
        base_url,
    ]
    for model in sorted(models):
        cmd.extend(["--model", model])
    env = os.environ.copy()
    env["TK_FULLTEST_KEY"] = key
    env.setdefault("TK_FULLTEST_BASE_URL", base_url)
    print(f"ssot-delta-gate: running focused gate for {len(models)} model(s)")
    for model in sorted(models):
        print(f"    model: {model}")
    proc = subprocess.run(cmd, cwd=REPO, env=env)
    return proc.returncode


def parse_explicit_models(raw: str) -> set[str]:
    values = [value for value in re.split(r"[\s,]+", raw.strip()) if value]
    if not values:
        raise ValueError("at least one model id is required")
    invalid = sorted({value for value in values if not MODEL_ID_FULL_RE.fullmatch(value)})
    if invalid:
        raise ValueError("invalid model id(s): " + ", ".join(invalid))
    models = set(values)
    if len(models) > MAX_FOCUSED_MODELS:
        raise ValueError(
            f"focused gate accepts at most {MAX_FOCUSED_MODELS} unique model ids"
        )
    return models


def cmd_paths_changed(args) -> int:
    if not _base_resolves(args.base):
        print("false")
        return 0
    print("true" if catalog_paths_changed(args.base) else "false")
    return 0


def cmd_needs_live(args) -> int:
    if not _base_resolves(args.base) or not catalog_paths_changed(args.base):
        print("false")
        return 0
    print("true" if discover_models(args.base) else "false")
    return 0


def cmd_discover(args) -> int:
    if not _base_resolves(args.base):
        print(f"ssot-delta-gate: skip (base {args.base!r} not resolvable)", file=sys.stderr)
        return 0
    if not catalog_paths_changed(args.base):
        print("ssot-delta-gate: skip (no catalog-surface paths changed)")
        return 0
    models = discover_models(args.base)
    if not models:
        print("ssot-delta-gate: ok (catalog changed but no models require live probe)")
        return 0
    for model in sorted(models):
        print(model)
    return 0


def cmd_check(args) -> int:
    if not _base_resolves(args.base):
        print(f"ssot-delta-gate: skip (base {args.base!r} not resolvable locally)")
        return 0
    if not catalog_paths_changed(args.base):
        print("ssot-delta-gate: skip (no catalog-surface paths changed)")
        return 0
    models = discover_models(args.base)
    if not models:
        print("ssot-delta-gate: ok (catalog changed; deletions/config-only — no live probe)")
        return 0
    if args.skip_live:
        print(
            f"ssot-delta-gate: skip-live ({len(models)} model(s) — post-deploy closeout runs live proof)"
        )
        for model in sorted(models):
            print(f"    pending-live: {model}")
        return 0
    key = args.key or os.environ.get("TK_FULLTEST_KEY", "")
    if not key:
        print("ssot-delta-gate: error: TK_FULLTEST_KEY required for live gate", file=sys.stderr)
        return 2
    base_url = args.base_url or os.environ.get(
        "TK_FULLTEST_BASE_URL", "https://api.tokenkey.dev"
    )
    rc = run_focused_gate(models, base_url=base_url, key=key)
    if rc == 0:
        print("ssot-delta-gate: ok (focused live gate passed)")
    return rc


def cmd_focused(args) -> int:
    try:
        models = parse_explicit_models(args.models)
    except ValueError as exc:
        print(f"ssot-delta-gate: error: {exc}", file=sys.stderr)
        return 2
    key = args.key or os.environ.get("TK_FULLTEST_KEY", "")
    if not key:
        print("ssot-delta-gate: error: TK_FULLTEST_KEY required for live gate", file=sys.stderr)
        return 2
    base_url = args.base_url or os.environ.get(
        "TK_FULLTEST_BASE_URL", "https://api.tokenkey.dev"
    )
    rc = run_focused_gate(models, base_url=base_url, key=key)
    if rc == 0:
        print("ssot-delta-gate: ok (explicit focused live gate passed)")
    return rc


def cmd_selftest(_args) -> int:
    base_go = (
        "// servable-allowlist:begin openai\n"
        '\t"gpt-5": {},\n'
        "// servable-allowlist:end openai\n"
    )
    head_go = (
        "// servable-allowlist:begin openai\n"
        '\t"gpt-5": {},\n\t"gpt-5.6-sol": {},\n'
        "// servable-allowlist:end openai\n"
    )
    bal, hal = parse_allowlist(base_go), parse_allowlist(head_go)
    assert hal["openai"] - bal["openai"] == {"gpt-5.6-sol"}

    base_manifest = json.dumps(
        {
            "schema_version": 3,
            "entries": {
                "qwen3-8b": {
                    "display": False,
                    "channel_type": 17,
                }
            }
        }
    )
    head_manifest = json.dumps(
        {
            "schema_version": 3,
            "entries": {
                "qwen3-8b": {
                    "display": True,
                    "channel_type": 17,
                }
            }
        }
    )
    bm = _load_manifest(base_manifest)
    hm = _load_manifest(head_manifest)
    assert hm["qwen3-8b"].display is True
    assert bm["qwen3-8b"].display is False
    equivalent = json.loads(base_manifest)
    equivalent["entries"]["qwen3-8b"]["display"] = True
    assert (
        _load_manifest(json.dumps(equivalent))["qwen3-8b"].projection()
        == hm["qwen3-8b"].projection()
    )
    invalid_manifest = json.dumps({"entries": {}})
    try:
        _load_manifest(invalid_manifest)
    except _MANIFEST.ManifestError:
        pass
    else:
        raise AssertionError("invalid manifest must fail closed")

    line = '+                "glm-4.7-flash": "glm-4.7-flash",'
    m = MIGRATION_ADDED_MODEL_RE.match(line)
    assert m and m.group("id") == "glm-4.7-flash"

    assert _overlay_priced({"mode": "image_generation", "output_cost_per_image": 0.04})
    assert not _overlay_priced({"mode": "chat", "input_cost_per_token": 0})
    assert overlay_delta_models_from_payloads(
        {},
        {
            "hidden-priced": {"mode": "chat", "input_cost_per_token": 0.001, "output_cost_per_token": 0.001},
            "shown-priced": {"mode": "chat", "input_cost_per_token": 0.001, "output_cost_per_token": 0.001},
            "shown-free": {"mode": "chat", "input_cost_per_token": 0},
        },
        {"shown-priced", "shown-free"},
    ) == {"shown-priced"}
    assert parse_explicit_models("model-a, model-b\nmodel-a") == {"model-a", "model-b"}
    for invalid in ("", "model/a", "model;echo", "-leading"):
        try:
            parse_explicit_models(invalid)
        except ValueError:
            pass
        else:
            raise AssertionError(f"accepted invalid explicit model input: {invalid!r}")
    try:
        parse_explicit_models(" ".join(f"model-{i}" for i in range(MAX_FOCUSED_MODELS + 1)))
    except ValueError:
        pass
    else:
        raise AssertionError("accepted an unbounded focused model set")

    print("ssot-delta-gate selftest: PASS")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="focused live SSOT gate for catalog diffs")
    sub = ap.add_subparsers(dest="cmd", required=True)
    for name in ("paths-changed", "needs-live", "discover", "check"):
        p = sub.add_parser(name)
        p.add_argument("--base", default="origin/main")
        if name == "check":
            p.add_argument(
                "--skip-live",
                action="store_true",
                help="preflight/offline: list pending models without HTTP",
            )
            p.add_argument("--key", default="", help="override TK_FULLTEST_KEY")
            p.add_argument("--base-url", default="", help="override TK_FULLTEST_BASE_URL")
        p.set_defaults(func=globals()[f"cmd_{name.replace('-', '_')}"])
    focused = sub.add_parser(
        "focused", help="run a focused live gate for explicit comma/space-separated model ids"
    )
    focused.add_argument("--models", required=True)
    focused.add_argument("--key", default="", help="override TK_FULLTEST_KEY")
    focused.add_argument("--base-url", default="", help="override TK_FULLTEST_BASE_URL")
    focused.set_defaults(func=cmd_focused)
    st = sub.add_parser("selftest")
    st.set_defaults(func=cmd_selftest)
    args = ap.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
