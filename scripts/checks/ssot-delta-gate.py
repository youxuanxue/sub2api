#!/usr/bin/env python3
"""ssot-delta-gate — live SSOT proof for catalog diffs only (no full-matrix scan).

Replaces the retired daily full SSOT gate (account-ban risk from ~356 HTTP probes).
Structural SSOT (servable ↔ priced ↔ display intent) stays in catalog-serving-drift.py
and display-coverage-gate.py (preflight, zero HTTP). This gate adds the runtime layer:
when a PR touches the catalog surface, probe ONLY the changed model ids via
ops/test/gateway_model_ssot_matrix.py gate --model ….

Catalog touch paths (any diff base..HEAD):
  - backend/internal/service/tk_served_models.json
  - backend/internal/service/pricing_catalog_supported_models_tk.go
  - backend/internal/service/tk_pricing_overlay.json
  - backend/migrations/tk_*.sql

Subcommands:
  paths-changed  — prints true/false (for GHA step outputs)
  needs-live     — prints true/false: catalog paths changed AND delta models need HTTP probe
  discover       — list model ids that would be live-probed
  check          — run focused gate (or --skip-live for preflight / offline)
  selftest       — fixture selftest

Exit: 0 ok/skip, 1 gate fail, 2 error.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
GO_REL = "backend/internal/service/pricing_catalog_supported_models_tk.go"
MANIFEST_REL = "backend/internal/service/tk_served_models.json"
OVERLAY_REL = "backend/internal/service/tk_pricing_overlay.json"
MIGRATION_PREFIX = "backend/migrations/tk_"
ALLOWLIST_PLATFORMS = ("anthropic", "openai", "gemini", "antigravity", "grok")
MATRIX = REPO / "ops/test/gateway_model_ssot_matrix.py"

MODEL_ID_RE = re.compile(r'"([a-zA-Z0-9][a-zA-Z0-9._-]{0,127})"\s*:\s*"')
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
    out: dict[str, set[str]] = {}
    for plat in ALLOWLIST_PLATFORMS:
        m = re.search(
            rf"servable-allowlist:begin {plat}(.*?)servable-allowlist:end {plat}",
            go_text,
            re.S,
        )
        out[plat] = set(re.findall(r'"([^"]+)":\s*\{\}', m.group(1))) if m else set()
    return out


def _read_at(base: str, rel: str) -> str:
    try:
        return _git("show", f"{base}:{rel}")
    except subprocess.CalledProcessError:
        return ""


def _load_manifest(text: str) -> dict[str, dict]:
    if not text.strip():
        return {}
    data = json.loads(text)
    entries = data.get("entries") or {}
    return {k: v for k, v in entries.items() if isinstance(v, dict)}


def _overlay_priced(entry: object) -> bool:
    if not isinstance(entry, dict):
        return False
    return (
        (entry.get("input_cost_per_token") or 0) > 0
        or (entry.get("output_cost_per_token") or 0) > 0
        or (entry.get("output_cost_per_image") or 0) > 0
        or (entry.get("output_cost_per_second") or 0) > 0
    )


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
    base_entries = _load_manifest(_read_at(base, MANIFEST_REL))
    head_entries = _load_manifest((REPO / MANIFEST_REL).read_text(encoding="utf-8"))
    out: set[str] = set()
    for key, head in head_entries.items():
        if not head.get("display"):
            continue
        model_id = str(head.get("model_id") or "").strip()
        if not model_id:
            continue
        base_entry = base_entries.get(key)
        if base_entry is None:
            out.add(model_id)
            continue
        if not base_entry.get("display"):
            out.add(model_id)
            continue
        for field in ("model_id", "served_on", "price_source", "price_key", "display"):
            if base_entry.get(field) != head.get(field):
                out.add(model_id)
                break
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
            f"ssot-delta-gate: skip-live ({len(models)} model(s) — CI ssot-delta-gate job runs live gate)"
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
            "entries": {
                "newapi/qwen3-8b": {
                    "platform": "newapi",
                    "model_id": "qwen3-8b",
                    "display": False,
                    "served_on": ["60"],
                    "price_source": "overlay",
                    "price_key": "qwen3-8b",
                }
            }
        }
    )
    head_manifest = json.dumps(
        {
            "entries": {
                "newapi/qwen3-8b": {
                    "platform": "newapi",
                    "model_id": "qwen3-8b",
                    "display": True,
                    "served_on": ["60"],
                    "price_source": "overlay",
                    "price_key": "qwen3-8b",
                }
            }
        }
    )
    bm = _load_manifest(base_manifest)
    hm = _load_manifest(head_manifest)
    assert hm["newapi/qwen3-8b"]["display"] is True
    assert bm["newapi/qwen3-8b"]["display"] is False

    line = '+                "glm-4.7-flash": "glm-4.7-flash",'
    m = MIGRATION_ADDED_MODEL_RE.match(line)
    assert m and m.group("id") == "glm-4.7-flash"

    assert _overlay_priced({"output_cost_per_image": 0.04})
    assert not _overlay_priced({"input_cost_per_token": 0})
    assert overlay_delta_models_from_payloads(
        {},
        {
            "hidden-priced": {"input_cost_per_token": 0.001},
            "shown-priced": {"input_cost_per_token": 0.001},
            "shown-free": {"input_cost_per_token": 0},
        },
        {"shown-priced", "shown-free"},
    ) == {"shown-priced"}

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
    st = sub.add_parser("selftest")
    st.set_defaults(func=cmd_selftest)
    args = ap.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
