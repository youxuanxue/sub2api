#!/usr/bin/env python3
"""Hot-push the TK pricing overlay to prod runtime (settings) without a release.

The embedded backend/internal/service/tk_pricing_overlay.json is the compile
FLOOR. At runtime the gateway merges a settings blob
(SettingKeyTKPricingOverlayRuntime = "tk_pricing_overlay_runtime") OVER the
embedded floor (runtime wins on key conflict), so a newly-priced model surfaces
in /pricing + bills correctly WITHOUT a new image. git (the embedded JSON) stays
the single source of truth; this tool pushes that same JSON to prod's settings
and the next routine release folds it into the embed (the floor catches up).

Subcommands
-----------
  check         Read-only drift audit: repo overlay (== embedded floor) vs the
                live prod runtime settings blob. Reports:
                  - pending : priced in git but NOT yet hot-pushed (run sync-runtime)
                  - shadow  : runtime carries a DIFFERENT value than git for a key
                              (stale shadow — git changed, runtime not re-pushed)
                  - orphan  : runtime carries a key absent from git (野值)
                Exit 0 clean / 1 drift / 2 error.
  sync-runtime  Validate the repo overlay with scripts/checks/pricing-overlay.py,
                then SSM-UPSERT it into prod settings + PUBLISH settings_updated
                so every replica reloads immediately. PROD ONLY (billing/catalog
                run on prod; edges are Caddy relays and never read pricing).
  --selftest    Offline unit test of the drift logic (no AWS).

This mirrors ops/anthropic/manage-anthropic-config.py (sync-runtime/check shape).
"""
from __future__ import annotations

import argparse
import base64
import gzip
import importlib.util
import json
import subprocess
import sys
from pathlib import Path
from typing import NoReturn

REPO_ROOT = Path(__file__).resolve().parents[2]
OVERLAY_PATH = REPO_ROOT / "backend" / "internal" / "service" / "tk_pricing_overlay.json"
OVERLAY_GATE = REPO_ROOT / "scripts" / "checks" / "pricing-overlay.py"
SETTING_KEY = "tk_pricing_overlay_runtime"

PSQL = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1"
REDISCLI = "env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli"

# Shared prod SSM glue (resolve_prod_instance + run_shell_b64). importlib-loaded by path:
# the module dir is not on sys.path when this script runs directly (mirrors how
# audit-model-mapping.py loads edge_ssm_execution.py).
_ssm_spec = importlib.util.spec_from_file_location(
    "tk_ssm_execution", REPO_ROOT / "ops" / "stage0" / "ssm_execution.py")
_SSM = importlib.util.module_from_spec(_ssm_spec)
_ssm_spec.loader.exec_module(_SSM)


def fail(msg: str) -> NoReturn:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


# --- pure drift logic (selftest-covered, no I/O) ------------------------------

def overlay_entries(doc: dict) -> dict:
    """Drop provenance keys ("_meta"/"_doc"/...) — only real model entries."""
    return {k: v for k, v in doc.items() if not k.startswith("_")}


def _canon(entry) -> str:
    return json.dumps(entry, sort_keys=True, ensure_ascii=False)


def compute_overlay_drift(repo: dict, runtime: dict) -> dict:
    """repo = embedded floor (git); runtime = live settings blob.

    pending : key in repo, not in runtime (priced in git, not hot-pushed yet)
    shadow  : key in both but value differs (runtime stale vs git)
    orphan  : key in runtime, not in repo (野值 — hot-pushed then removed from git)
    """
    r = overlay_entries(repo)
    rt = overlay_entries(runtime)
    pending = sorted(k for k in r if k not in rt)
    orphan = sorted(k for k in rt if k not in r)
    shadow = sorted(k for k in r if k in rt and _canon(r[k]) != _canon(rt[k]))
    return {"pending": pending, "shadow": shadow, "orphan": orphan}


def drift_is_clean(drift: dict) -> bool:
    return not (drift["pending"] or drift["shadow"] or drift["orphan"])


# --- AWS / SSM I/O: resolve_prod_instance + run_shell_b64 live in ops/stage0/ssm_execution.py


def read_runtime_blob(instance_id: str) -> dict:
    shell = f"{PSQL} -c \"SELECT value FROM settings WHERE key='{SETTING_KEY}';\""
    b64 = base64.b64encode(shell.encode()).decode()
    out = _SSM.run_shell_b64(instance_id, b64, "overlay check: read runtime settings").strip()
    if not out:
        return {}
    try:
        return json.loads(out)
    except json.JSONDecodeError as e:
        fail(f"runtime settings blob is not valid JSON: {e}")


def load_repo_overlay() -> dict:
    try:
        return json.loads(OVERLAY_PATH.read_text())
    except (OSError, json.JSONDecodeError) as e:
        fail(f"cannot read repo overlay {OVERLAY_PATH}: {e}")


# --- subcommands --------------------------------------------------------------

def cmd_check(_args) -> int:
    repo = load_repo_overlay()
    inst = _SSM.resolve_prod_instance()
    runtime = read_runtime_blob(inst)
    drift = compute_overlay_drift(repo, runtime)
    print(f"prod runtime overlay entries: {len(overlay_entries(runtime))} | "
          f"git/embedded entries: {len(overlay_entries(repo))}")
    if drift_is_clean(drift):
        print("OK: prod runtime overlay is consistent with git (embedded floor).")
        return 0
    if drift["pending"]:
        print(f"  pending (priced in git, not hot-pushed — run sync-runtime): {drift['pending']}")
    if drift["shadow"]:
        print(f"  shadow (runtime value != git — stale, re-push or GC): {drift['shadow']}")
    if drift["orphan"]:
        print(f"  orphan (runtime has key absent from git — 野值): {drift['orphan']}")
    return 1


def cmd_sync_runtime(args) -> int:
    # 1. validate the repo overlay with the SAME gate the PR ran.
    gate = subprocess.run([sys.executable, str(OVERLAY_GATE)], cwd=str(REPO_ROOT))
    if gate.returncode != 0:
        fail("pricing-overlay.py gate failed; refusing to push an invalid overlay")
    overlay_bytes = OVERLAY_PATH.read_bytes()
    # sanity: must parse + be non-empty
    doc = json.loads(overlay_bytes)
    if not overlay_entries(doc):
        fail("repo overlay has no model entries; refusing to push")

    if args.dry_run:
        print(f"DRY-RUN: would UPSERT settings[{SETTING_KEY}] on prod "
              f"({len(overlay_entries(doc))} entries) + PUBLISH settings_updated.")
        return 0

    inst = _SSM.resolve_prod_instance()
    # Idempotency: skip the UPSERT + PUBLISH if the runtime already matches git, so a manual
    # retry or a double-fire cron doesn't churn the settings row / re-publish needlessly.
    if drift_is_clean(compute_overlay_drift(doc, read_runtime_blob(inst))):
        print("runtime already in sync with git (embedded floor + runtime overlay) — nothing to push.")
        return 0
    # Transport: GZIP then base64 so the SSM SendCommand parameter stays well under AWS's
    # 97KB limit. The raw overlay is ~100KB+ (long per-entry `source` strings); base64 of
    # that alone exceeds 97KB -> MaxDocumentSizeExceeded. gzip shrinks the repetitive JSON
    # ~6x. On the host we gunzip and re-base64 (host-side command length is NOT SSM-limited)
    # and decode it INSIDE Postgres via convert_from(decode(...,'base64'),'UTF8'): base64 is
    # pure ASCII so it is safe inside the single-quoted SQL literal, and this avoids the
    # psql :'v' variable interpolation which silently fails in -c mode (syntax error at ":").
    gz_b64 = base64.b64encode(gzip.compress(overlay_bytes)).decode()
    if gzip.decompress(base64.b64decode(gz_b64)) != overlay_bytes:
        fail("gzip roundtrip mismatch; refusing to push")  # never touch prod on a bad encode
    # Decode on the host, re-base64 the plain JSON, decode that inside Postgres. The stored
    # `value` is the exact overlay JSON (byte-identical to the old :'v' path); `check` reads
    # it back unchanged.
    upsert = (
        f"INSERT INTO settings (key, value, updated_at) VALUES "
        f"('{SETTING_KEY}', convert_from(decode('$JSON_B64','base64'),'UTF8'), NOW()) "
        "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();"
    )
    shell = (
        "set -uo pipefail\n"
        f"PSQL='{PSQL}'\n"
        f"RC='{REDISCLI}'\n"
        f"JSON_B64=\"$(echo {gz_b64} | base64 -d | gunzip | base64 | tr -d '\\n')\"\n"
        "echo '=== upsert tk_pricing_overlay_runtime ==='\n"
        f"$PSQL -c \"{upsert}\" </dev/null && echo UPSERT_OK\n"
        "echo '=== publish settings_updated (fan-out reload) ==='\n"
        # Best-effort: the UPSERT above is the durable truth; PUBLISH only makes the reload
        # immediate. Surface (don't swallow) a failure so the operator knows replicas will
        # lag to the poll interval instead of reloading now.
        "$RC PUBLISH settings_updated refresh </dev/null || echo 'WARN: redis PUBLISH failed; replicas reload within the pricing poll interval, not immediately'\n"
        "echo '=== settings_after ==='\n"
        f"$PSQL -c \"SELECT key, length(value) AS bytes FROM settings WHERE key='{SETTING_KEY}';\" </dev/null\n"
    )
    b64 = base64.b64encode(shell.encode()).decode()
    if len(b64) > 90_000:  # headroom under the 97KB SSM SendCommand parameter ceiling
        fail(f"encoded sync payload is {len(b64)}B (>90KB) even gzipped; overlay too large "
             f"for SSM SendCommand — stage via S3 instead")
    out = _SSM.run_shell_b64(inst, b64, "overlay sync-runtime: upsert + publish")
    print(out)
    if "UPSERT_OK" not in out:
        fail("UPSERT did not report success — inspect the SSM output above (psql error? guard?)")
    # Post-sync verify: re-read the settings row (DB truth, not the in-memory replica cache)
    # and confirm it now matches git. Catches a silently-partial/failed write that still
    # returned Success (the SSM stdout-truncation class of bug that motivated this hardening).
    post = compute_overlay_drift(doc, read_runtime_blob(inst))
    if not drift_is_clean(post):
        fail(f"sync reported success but post-sync verify shows drift: {post}")
    print("synced + verified: prod runtime overlay == git.")
    return 0


def cmd_selftest(_args) -> int:
    repo = {
        "_meta": {"note": "provenance"},
        "qwen3-8b": {"input_cost_per_token": 1.0, "litellm_provider": "dashscope"},
        "qwen3-32b": {"input_cost_per_token": 2.0, "litellm_provider": "dashscope"},
        "qwen3-235b-a22b": {"input_cost_per_token": 3.0, "litellm_provider": "dashscope"},
    }
    cases = [
        ("clean", repo, {"qwen3-8b": repo["qwen3-8b"], "qwen3-32b": repo["qwen3-32b"],
                         "qwen3-235b-a22b": repo["qwen3-235b-a22b"]},
         {"pending": [], "shadow": [], "orphan": []}),
        ("pending", repo, {"qwen3-8b": repo["qwen3-8b"]},
         {"pending": ["qwen3-235b-a22b", "qwen3-32b"], "shadow": [], "orphan": []}),
        ("shadow", repo,
         {"qwen3-8b": {"input_cost_per_token": 9.9, "litellm_provider": "dashscope"},
          "qwen3-32b": repo["qwen3-32b"], "qwen3-235b-a22b": repo["qwen3-235b-a22b"]},
         {"pending": [], "shadow": ["qwen3-8b"], "orphan": []}),
        ("orphan", repo,
         {**{k: v for k, v in overlay_entries(repo).items()},
          "ghost-model": {"input_cost_per_token": 1.0}},
         {"pending": [], "shadow": [], "orphan": ["ghost-model"]}),
        ("provenance-ignored", repo,
         {**{k: v for k, v in overlay_entries(repo).items()}, "_meta": {"x": 1}},
         {"pending": [], "shadow": [], "orphan": []}),
    ]
    ok = True
    for name, r, rt, want in cases:
        got = compute_overlay_drift(r, rt)
        if got != want:
            ok = False
            print(f"  FAIL {name}: got {got} want {want}")
        else:
            print(f"  PASS {name}")
    print("selftest ok" if ok else "selftest FAILED")
    return 0 if ok else 1


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--selftest", action="store_true", help="offline drift-logic test")
    sub = ap.add_subparsers(dest="cmd")
    sub.add_parser("check", help="read-only drift audit (git vs prod runtime)")
    sp = sub.add_parser("sync-runtime", help="hot-push repo overlay to prod settings")
    sp.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        return cmd_selftest(args)
    if args.cmd == "check":
        return cmd_check(args)
    if args.cmd == "sync-runtime":
        return cmd_sync_runtime(args)
    ap.print_help()
    return 2


if __name__ == "__main__":
    sys.exit(main())
