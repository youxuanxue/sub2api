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
import json
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
OVERLAY_PATH = REPO_ROOT / "backend" / "internal" / "service" / "tk_pricing_overlay.json"
OVERLAY_GATE = REPO_ROOT / "scripts" / "checks" / "pricing-overlay.py"
SETTING_KEY = "tk_pricing_overlay_runtime"

PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"

PSQL = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1"
REDISCLI = "env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli"


def fail(msg: str) -> "NoReturn":  # type: ignore[name-defined]
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


# --- AWS / SSM I/O ------------------------------------------------------------

def resolve_prod_instance() -> str:
    try:
        out = subprocess.check_output(
            ["aws", "cloudformation", "describe-stacks", "--region", PROD_REGION,
             "--stack-name", PROD_STACK,
             "--query", "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue",
             "--output", "text"], text=True).strip()
    except subprocess.CalledProcessError as e:
        fail(f"describe-stacks failed for {PROD_STACK}/{PROD_REGION}: {e}")
    if not out or out == "None":
        fail(f"no InstanceId resolvable for {PROD_STACK}/{PROD_REGION}")
    return out


def ssm_run_shell(instance_id: str, shell_b64: str, comment: str) -> str:
    """Run a base64-encoded shell script on prod via SSM; return stdout."""
    command = f"set -euo pipefail\necho {shell_b64} | base64 -d | bash"
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            ["aws", "ssm", "send-command", "--region", PROD_REGION,
             "--instance-ids", instance_id, "--document-name", "AWS-RunShellScript",
             "--comment", comment, "--parameters", params,
             "--query", "Command.CommandId", "--output", "text"], text=True).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed ({comment}): {e}")
    subprocess.run(["aws", "ssm", "wait", "command-executed", "--region", PROD_REGION,
                    "--command-id", cid, "--instance-id", instance_id], check=False)
    inv = json.loads(subprocess.check_output(
        ["aws", "ssm", "get-command-invocation", "--region", PROD_REGION,
         "--command-id", cid, "--instance-id", instance_id, "--output", "json"], text=True))
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:1200]
        fail(f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')} ({comment})\n  stderr: {err}")
    return (inv.get("StandardOutputContent") or "").strip()


def read_runtime_blob(instance_id: str) -> dict:
    shell = f"{PSQL} -c \"SELECT value FROM settings WHERE key='{SETTING_KEY}';\""
    b64 = base64.b64encode(shell.encode()).decode()
    out = ssm_run_shell(instance_id, b64, "overlay check: read runtime settings").strip()
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
    inst = resolve_prod_instance()
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

    inst = resolve_prod_instance()
    value_b64 = base64.b64encode(overlay_bytes).decode()
    # Pass the JSON value through a psql variable with :'v' quoting — psql escapes
    # embedded single quotes (source strings may contain apostrophes), so no manual
    # quoting hazard. base64 carries the multi-line JSON intact to the host.
    upsert = (
        f"INSERT INTO settings (key, value, updated_at) VALUES ('{SETTING_KEY}', :'v', NOW()) "
        "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();"
    )
    shell = (
        "set -euo pipefail\n"
        f"PSQL='{PSQL}'\n"
        f"RC='{REDISCLI}'\n"
        f"VAL=\"$(echo {value_b64} | base64 -d)\"\n"
        "echo '=== upsert tk_pricing_overlay_runtime ==='\n"
        f"$PSQL -v v=\"$VAL\" -c \"{upsert}\"\n"
        "echo '=== publish settings_updated (fan-out reload) ==='\n"
        # Best-effort: the UPSERT above is the durable truth; PUBLISH only makes the
        # reload immediate. Surface a failure (don't swallow it) so the operator knows
        # replicas will lag to the poll interval instead of reloading now.
        "$RC PUBLISH settings_updated refresh || echo 'WARN: redis PUBLISH failed; replicas reload within the pricing poll interval, not immediately'\n"
        "echo '=== settings_after ==='\n"
        f"$PSQL -c \"SELECT key, length(value) AS bytes FROM settings WHERE key='{SETTING_KEY}';\"\n"
    )
    b64 = base64.b64encode(shell.encode()).decode()
    out = ssm_run_shell(inst, b64, "overlay sync-runtime: upsert + publish")
    print(out)
    print("synced. Verify with: manage-overlay-runtime.py check")
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
