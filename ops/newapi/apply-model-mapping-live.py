#!/usr/bin/env python3
"""Hot-apply a newapi account credentials.model_mapping merge to prod WITHOUT a deploy.

This mirrors a tk_NNN_*_model_mapping.sql migration's effect at runtime: it merges
identity (or explicit) model_mapping keys onto ONE newapi account via `jsonb ||`,
enqueues a `scheduler_outbox account_changed` event so the running scheduler hot-reloads
the new allowlist (no restart), and verifies BEFORE/AFTER. Idempotent + guard-protected
(id + name + platform + channel_type + deleted_at) so a bare id cannot hit the wrong row
and the next release re-running the migration is a no-op.

Why: model_mapping changes normally land via a release (the migration runs on container
start). When a customer needs a model served NOW, this applies the SAME merge to prod
out-of-band. The migration MUST still land in git / the next release — this is a
hot-apply, not a new source of truth. See the tokenkey-onboard-model skill §4.

Subcommands
-----------
  check      Read-only: print the account's current model_mapping keys + guard fields.
  sync-live  Merge --add-identity / --add keys onto the account + scheduler_outbox +
             BEFORE/AFTER verify. --dry-run previews (guard match + BEFORE + plan, no write).
  --selftest Offline unit test of the additions/SQL building (no AWS).

SSM transport mirrors ops/pricing/manage-overlay-runtime.py: the shell is base64'd,
written to a FILE on the host and bash'd from the file (NOT piped to `bash` via stdin),
because an inner `docker exec -i psql` would otherwise slurp the rest of the script from
the shared stdin (silent truncation, still rc=0). JSON additions are decoded INSIDE
Postgres via convert_from(decode(...,'base64'),'UTF8') to avoid any shell/SQL quoting
hazard with model ids that contain dots. PROD ONLY (newapi accounts live only on the
prod control-plane DB; edges are anthropic relays).
"""
from __future__ import annotations

import argparse
import base64
import json
import re
import subprocess
import sys
from typing import NoReturn

PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
PSQL = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1"

# model ids / mapping keys are DashScope/DeepSeek canonical ids — lowercase, dots, dashes,
# underscores, slashes. Reject anything else so a key this tool WRITES can never carry a
# quote/space that breaks the SQL literal or the jsonb key set (the audit's schema-gate,
# applied at the one place that mutates model_mapping out-of-band).
_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]*$")
# guard-tuple name: a group/account display name (may be non-ASCII, e.g. "ds-官"); reject
# single quotes — the only char that breaks a PG string literal under
# standard_conforming_strings (the default), so this is sufficient SQL-literal safety.
_NAME_RE = re.compile(r"^[^']+$")
# platform is a fixed enum-like token (newapi/anthropic/openai/gemini/antigravity/grok);
# validate to a strict charset so it can never break the guard's SQL string literal.
_PLATFORM_RE = re.compile(r"^[A-Za-z0-9_-]+$")


def fail(msg: str) -> NoReturn:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


# --- pure helpers (selftest-covered, no I/O) ----------------------------------

def build_additions(add_identity: list[str], add_pairs: list[str]) -> dict:
    """{model: upstream} from --add-identity (key==value) and --add MODEL=UPSTREAM."""
    out: dict[str, str] = {}
    for m in add_identity:
        m = m.strip()
        if not m:
            continue
        if not _ID_RE.match(m):
            fail(f"--add-identity {m!r} is not a valid model id ({_ID_RE.pattern})")
        out[m] = m
    for p in add_pairs:
        if "=" not in p:
            fail(f"--add must be MODEL=UPSTREAM, got {p!r}")
        k, v = (s.strip() for s in p.split("=", 1))
        if not k or not v:
            fail(f"--add must be MODEL=UPSTREAM, got {p!r}")
        for x in (k, v):
            if not _ID_RE.match(x):
                fail(f"--add {p!r}: {x!r} is not a valid model id ({_ID_RE.pattern})")
        out[k] = v
    if not out:
        fail("no model_mapping additions (pass --add-identity MODEL and/or --add MODEL=UPSTREAM)")
    return out


def build_merge_sql(account_id: int, name: str, platform: str, channel_type: int,
                    additions_b64: str) -> str:
    """Idempotent guard-protected jsonb || merge + scheduler_outbox, additions decoded in PG.

    A SINGLE data-modifying-CTE statement (atomic on its own — no BEGIN/COMMIT needed, and
    keeping it one statement lets the ops-sql-coverage execution test run it as-is).
    """
    return (
        "WITH upd AS (\n"
        "  UPDATE accounts\n"
        "  SET credentials = jsonb_set(credentials, '{model_mapping}',\n"
        "        COALESCE(credentials -> 'model_mapping', '{}'::jsonb)\n"
        f"        || convert_from(decode('{additions_b64}', 'base64'), 'UTF8')::jsonb),\n"
        "      updated_at = NOW()\n"
        f"  WHERE id = {account_id} AND name = '{name}' AND platform = '{platform}'\n"
        f"    AND channel_type = {channel_type} AND deleted_at IS NULL\n"
        "  RETURNING id\n"
        ")\n"
        "INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)\n"
        "SELECT 'account_changed', id, NULL, NULL FROM upd;"
    )


def keys_array_sql(keys: list[str]) -> str:
    """Postgres text[] literal of the (validated) keys for a jsonb ?& presence check."""
    return "array[" + ", ".join("'" + k + "'" for k in sorted(keys)) + "]"


# --- SQL self-check registry (ops-sql-coverage gate; doctrine: manage-anthropic-config.py)
# Every *_sql generator must be enumerated here (so ops/anthropic/test_ops_sql_execute.py
# runs it against a real Postgres) or exempted with a reason. No runner-shaped symbols here.
SELF_CHECK_EXEMPT: dict[str, str] = {}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for the ops-sql-coverage real-Postgres self-check."""
    sample_b64 = base64.b64encode(b'{"qwen3.6-27b":"qwen3.6-27b"}').decode()
    return [
        ("build_merge_sql", build_merge_sql(60, "Qwen", "newapi", 17, sample_b64)),
        # keys_array_sql returns a text[] fragment; wrap it in a runnable presence check.
        ("keys_array_sql", "SELECT '{}'::jsonb ?& " + keys_array_sql(["qwen3.6-27b", "qwen3-8b"])),
    ]


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
    if not re.match(r"^i-[0-9a-f]{8,}$", out):
        fail(f"no valid InstanceId for {PROD_STACK}/{PROD_REGION} (got {out!r})")
    return out


def ssm_run_shell(instance_id: str, shell_b64: str, comment: str) -> str:
    """Run a base64-encoded shell script on prod via SSM; return stdout.

    The decoded script is written to a FILE and bash'd from the file (NOT piped to bash via
    stdin) so an inner `docker exec -i psql` cannot slurp the rest of the script from the
    shared stdin. `set -uo pipefail` (no -e) so a non-zero inner script still lets us
    capture rc, clean up, and propagate the exit code.
    """
    command = (
        "set -uo pipefail\n"
        f"echo {shell_b64} | base64 -d > /tmp/.mm_apply_$$.sh\n"
        "bash /tmp/.mm_apply_$$.sh; rc=$?\n"
        "rm -f /tmp/.mm_apply_$$.sh\n"
        "exit $rc"
    )
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
    try:
        inv = json.loads(subprocess.check_output(
            ["aws", "ssm", "get-command-invocation", "--region", PROD_REGION,
             "--command-id", cid, "--instance-id", instance_id, "--output", "json"], text=True))
    except (subprocess.CalledProcessError, ValueError) as e:
        fail(f"ssm get-command-invocation failed ({comment}): {e}")
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:2000]
        fail(f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')} "
             f"({comment})\n  stderr: {err}")
    return (inv.get("StandardOutputContent") or "").strip()


# --- subcommands --------------------------------------------------------------

def cmd_check(args) -> int:
    inst = resolve_prod_instance()
    shell = (
        "set -uo pipefail\n"
        f"PSQL='{PSQL}'\n"
        "echo '=== account guard row (1 = exists) ==='\n"
        f"$PSQL -c \"SELECT id, name, platform, channel_type, status FROM accounts "
        f"WHERE id={args.account_id} AND deleted_at IS NULL;\" </dev/null\n"
        "echo '=== model_mapping keys ==='\n"
        f"$PSQL -c \"SELECT string_agg(k, ', ' ORDER BY k) FROM "
        f"(SELECT jsonb_object_keys(credentials->'model_mapping') k FROM accounts "
        f"WHERE id={args.account_id} AND deleted_at IS NULL) s;\" </dev/null\n"
    )
    print(ssm_run_shell(inst, base64.b64encode(shell.encode()).decode(),
                        f"model_mapping check acct {args.account_id}"))
    return 0


def cmd_sync_live(args) -> int:
    if not _NAME_RE.match(args.name):
        fail(f"--name {args.name!r} must not contain a single quote (SQL literal safety)")
    if not _PLATFORM_RE.match(args.platform):
        fail(f"--platform {args.platform!r} must match {_PLATFORM_RE.pattern} "
             f"(newapi/anthropic/openai/gemini/antigravity/grok)")
    additions = build_additions(args.add_identity or [], args.add or [])
    additions_json = json.dumps(additions, ensure_ascii=False, separators=(",", ":"))
    additions_b64 = base64.b64encode(additions_json.encode()).decode()
    keys_arr = keys_array_sql(list(additions))
    plan = ", ".join(f"{k}->{v}" for k, v in sorted(additions.items()))
    print(f"account {args.account_id} ({args.name}, {args.platform}, ct={args.channel_type})"
          f"  merge: {plan}")

    if args.dry_run:
        print("DRY-RUN: would jsonb|| the above onto credentials.model_mapping + "
              "enqueue scheduler_outbox account_changed. No write.")
        inst = resolve_prod_instance()
        shell = (
            "set -uo pipefail\n"
            f"PSQL='{PSQL}'\n"
            "echo '=== guard match (1 row expected) ==='\n"
            f"$PSQL -c \"SELECT id, name, platform, channel_type FROM accounts "
            f"WHERE id={args.account_id} AND name='{args.name}' AND platform='{args.platform}' "
            f"AND channel_type={args.channel_type} AND deleted_at IS NULL;\" </dev/null\n"
            "echo '=== BEFORE: all added keys already present? (t/f) ==='\n"
            f"$PSQL -c \"SELECT coalesce((credentials->'model_mapping') ?& {keys_arr}, false) "
            f"FROM accounts WHERE id={args.account_id} AND deleted_at IS NULL;\" </dev/null\n"
        )
        print(ssm_run_shell(inst, base64.b64encode(shell.encode()).decode(),
                            f"model_mapping dry-run acct {args.account_id}"))
        return 0

    merge_sql = build_merge_sql(args.account_id, args.name, args.platform,
                                args.channel_type, additions_b64)
    sql_b64 = base64.b64encode(merge_sql.encode()).decode()
    inst = resolve_prod_instance()
    shell = (
        "set -uo pipefail\n"
        f"PSQL='{PSQL}'\n"
        "echo '=== guard match (1 row expected; 0 = wrong id/name/platform/channel_type) ==='\n"
        f"$PSQL -c \"SELECT id, name, platform, channel_type FROM accounts "
        f"WHERE id={args.account_id} AND name='{args.name}' AND platform='{args.platform}' "
        f"AND channel_type={args.channel_type} AND deleted_at IS NULL;\" </dev/null\n"
        "echo '=== APPLY (jsonb || merge + scheduler_outbox) ==='\n"
        # psql reads the multi-statement SQL from stdin (the decode pipe); safe because the
        # outer script runs from a FILE, so docker exec -i does not eat the script.
        f"echo {sql_b64} | base64 -d | $PSQL && echo APPLY_OK\n"
        "echo '=== AFTER: all added keys present? (expect t) ==='\n"
        f"$PSQL -c \"SELECT (credentials->'model_mapping') ?& {keys_arr} "
        f"FROM accounts WHERE id={args.account_id} AND deleted_at IS NULL;\" </dev/null\n"
        "echo '=== model_mapping keys now ==='\n"
        f"$PSQL -c \"SELECT string_agg(k, ', ' ORDER BY k) FROM "
        f"(SELECT jsonb_object_keys(credentials->'model_mapping') k FROM accounts "
        f"WHERE id={args.account_id} AND deleted_at IS NULL) s;\" </dev/null\n"
        "echo '=== scheduler_outbox account_changed (last 2 min) ==='\n"
        f"$PSQL -c \"SELECT count(*) FROM scheduler_outbox WHERE account_id={args.account_id} "
        f"AND event_type='account_changed' AND created_at > now() - interval '2 min';\" </dev/null\n"
    )
    out = ssm_run_shell(inst, base64.b64encode(shell.encode()).decode(),
                        f"model_mapping sync-live acct {args.account_id}")
    print(out)
    if "APPLY_OK" not in out:
        fail("APPLY did not report success — inspect the output above (guard match? psql error?)")
    print(f"applied. Verify servability with: probe-servable-models.sh "
          f"(DASHSCOPE_CHAT_MODELS / GEMINI_CHAT_MODELS = {' '.join(sorted(additions))})")
    return 0


def _selftest() -> int:
    failures: list[str] = []
    # additions building
    if build_additions(["qwen3.6-27b"], []) != {"qwen3.6-27b": "qwen3.6-27b"}:
        failures.append("identity add wrong")
    if build_additions([], ["a=b", "c-d=e.f"]) != {"a": "b", "c-d": "e.f"}:
        failures.append("pair add wrong")
    # invalid id rejected
    for bad in (["bad'id"], ["has space"]):
        try:
            build_additions(bad, [])
            failures.append(f"invalid id {bad} not rejected")
        except SystemExit:
            pass
    # keys array literal
    if keys_array_sql(["b", "a"]) != "array['a', 'b']":
        failures.append("keys_array_sql wrong/ordering")
    # guard-field validation regexes reject the SQL-literal escape char (injection gate)
    if _PLATFORM_RE.match("newapi'; DROP") or not _PLATFORM_RE.match("newapi"):
        failures.append("_PLATFORM_RE wrong (must reject quotes, accept newapi)")
    if _NAME_RE.match("Qwen'; x") or not _NAME_RE.match("ds-官"):
        failures.append("_NAME_RE wrong (must reject quotes, accept non-ASCII)")
    # merge SQL shape: guard tuple + jsonb || + scheduler_outbox + decode
    sql = build_merge_sql(60, "Qwen", "newapi", 17, "QQ==")
    for needle in ("id = 60", "name = 'Qwen'", "platform = 'newapi'", "channel_type = 17",
                   "deleted_at IS NULL", "|| convert_from(decode('QQ==', 'base64')",
                   "scheduler_outbox", "account_changed"):
        if needle not in sql:
            failures.append(f"merge SQL missing {needle!r}")
    if failures:
        print("SELFTEST FAILED:")
        for f in failures:
            print(f"  - {f}")
        return 1
    print("selftest ok: additions / id-validation / keys-array / merge-SQL shape")
    return 0


def main() -> int:
    if "--selftest" in sys.argv:
        return _selftest()
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--selftest", action="store_true", help="offline unit test (no AWS)")
    sub = ap.add_subparsers(dest="cmd")

    c = sub.add_parser("check", help="read-only: account model_mapping keys + guard fields")
    c.add_argument("--account-id", type=int, required=True)
    c.set_defaults(fn=cmd_check)

    s = sub.add_parser("sync-live", help="hot-apply a model_mapping merge to prod")
    s.add_argument("--account-id", type=int, required=True)
    s.add_argument("--name", required=True, help="guard: accounts.name (must match exactly)")
    s.add_argument("--channel-type", type=int, required=True, help="guard: accounts.channel_type")
    s.add_argument("--platform", default="newapi", help="guard: accounts.platform (default newapi)")
    s.add_argument("--add-identity", action="append", metavar="MODEL",
                   help="add identity mapping MODEL->MODEL (repeatable)")
    s.add_argument("--add", action="append", metavar="MODEL=UPSTREAM",
                   help="add non-identity mapping (repeatable)")
    s.add_argument("--dry-run", action="store_true", help="preview guard match + BEFORE; no write")
    s.set_defaults(fn=cmd_sync_live)

    args = ap.parse_args()
    if not getattr(args, "fn", None):
        ap.print_help()
        return 2
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
