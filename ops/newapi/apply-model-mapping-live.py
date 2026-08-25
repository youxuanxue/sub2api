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
  sync-live   Merge --add-identity / --add keys onto the account + scheduler_outbox +
              BEFORE/AFTER verify. --dry-run previews (guard match + BEFORE + plan, no write).
  remove-live Remove --remove keys from the account + scheduler_outbox + fail-closed
              BEFORE/AFTER verify. --dry-run previews without writing.
  --selftest  Offline unit test of the additions/removals and SQL building (no AWS).

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
import importlib.util
import json
import re
import sys
from pathlib import Path
from typing import NoReturn

REPO_ROOT = Path(__file__).resolve().parents[2]
PSQL = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1"

# Shared prod SSM glue (resolve_prod_instance + run_shell_b64). importlib-loaded by path:
# the module dir is not on sys.path when this script runs directly (mirrors how
# audit-model-mapping.py loads edge_ssm_execution.py).
_ssm_spec = importlib.util.spec_from_file_location(
    "tk_ssm_execution", REPO_ROOT / "ops" / "stage0" / "ssm_execution.py")
_SSM = importlib.util.module_from_spec(_ssm_spec)
_ssm_spec.loader.exec_module(_SSM)

# model ids / mapping keys are DashScope/DeepSeek canonical ids — lowercase, dots, dashes,
# underscores, slashes. Reject anything else so a key this tool WRITES can never carry a
# quote/space that breaks the SQL literal or the jsonb key set (the audit's schema-gate,
# applied at the one place that mutates model_mapping out-of-band).
_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]*$")
# guard-tuple name: a group/account display name (may be non-ASCII, e.g. "ds-官").
# It is embedded in both a PG single-quoted literal and a remote shell double-quoted
# argument, so reject the metacharacters that can break either layer.
_NAME_RE = re.compile(r"^[^'\"`$\\\r\n]+$")
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


def build_removals(remove_keys: list[str]) -> list[str]:
    """Validated, de-duplicated, sorted model_mapping keys to DELETE.

    Same _ID_RE gate as the additions path: a key this tool writes into the `- text[]`
    literal can never carry a quote or space that breaks the SQL literal.
    """
    out: set[str] = set()
    for m in remove_keys:
        m = m.strip()
        if not m:
            continue
        if not _ID_RE.match(m):
            fail(f"--remove {m!r} is not a valid model id ({_ID_RE.pattern})")
        out.add(m)
    if not out:
        fail("no model_mapping removals (pass --remove MODEL)")
    return sorted(out)


def validate_guard_fields(name: str, platform: str) -> None:
    """Reject values that cannot be embedded in both SQL and the remote shell."""
    if not _NAME_RE.fullmatch(name):
        fail(f"--name {name!r} contains an SQL or shell literal breaker")
    if not _PLATFORM_RE.fullmatch(platform):
        fail(f"--platform {platform!r} must match {_PLATFORM_RE.pattern} "
             f"(newapi/anthropic/openai/gemini/antigravity/grok)")


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


def build_remove_sql(account_id: int, name: str, platform: str, channel_type: int,
                     keys: list[str]) -> str:
    """Idempotent guard-protected jsonb key DELETE + scheduler_outbox.

    Mirrors build_merge_sql exactly but uses jsonb `- text[]` instead of `||`, so an
    account that must STOP serving a model id can be corrected out-of-band with the same
    guard tuple and the same hot-reload event. Idempotent: `-` on an absent key is a
    no-op, and every other mapping entry is left untouched.

    A SINGLE data-modifying-CTE statement (atomic on its own), same as the merge path.
    """
    return (
        "WITH upd AS (\n"
        "  UPDATE accounts\n"
        "  SET credentials = jsonb_set(credentials, '{model_mapping}',\n"
        "        COALESCE(credentials -> 'model_mapping', '{}'::jsonb)\n"
        f"        - {keys_array_sql(keys)}::text[]),\n"
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


def build_remove_live_shell(
    account_id: int,
    name: str,
    platform: str,
    channel_type: int,
    keys: list[str],
    sql_b64: str,
    psql: str = PSQL,
) -> str:
    """Build the fail-closed remote shell for one guarded mapping removal."""
    keys_arr = keys_array_sql(keys)
    guard = (
        f"id={account_id} AND name='{name}' AND platform='{platform}' "
        f"AND channel_type={channel_type}"
    )
    return (
        "set -euo pipefail\n"
        f"PSQL='{psql}'\n"
        "echo '=== guard match (exactly 1 row required) ==='\n"
        f"guard_count=\"$($PSQL -c \"SELECT count(*) FROM accounts WHERE {guard} "
        "AND deleted_at IS NULL;\" </dev/null)\"\n"
        "if [ \"$guard_count\" != \"1\" ]; then\n"
        "  echo \"ERROR: guard matched $guard_count accounts (expected 1)\" >&2\n"
        "  exit 1\n"
        "fi\n"
        "echo '=== BEFORE: which of the target keys are present? ==='\n"
        f"$PSQL -c \"SELECT string_agg(k, ', ' ORDER BY k) FROM "
        f"(SELECT jsonb_object_keys(credentials->'model_mapping') k FROM accounts "
        f"WHERE {guard} AND deleted_at IS NULL) s WHERE k = ANY({keys_arr});\" </dev/null\n"
        "echo '=== APPLY (jsonb - text[] + scheduler_outbox) ==='\n"
        f"echo {sql_b64} | base64 -d | $PSQL\n"
        "echo '=== AFTER: any target key still present? (must be f) ==='\n"
        f"after_present=\"$($PSQL -c \"SELECT coalesce((credentials->'model_mapping') ?| "
        f"{keys_arr}, false) FROM accounts WHERE {guard} AND deleted_at IS NULL;\" </dev/null)\"\n"
        "if [ \"$after_present\" != \"f\" ]; then\n"
        "  echo \"ERROR: target keys remain after removal (value=$after_present)\" >&2\n"
        "  exit 1\n"
        "fi\n"
        "echo '=== model_mapping keys now ==='\n"
        f"$PSQL -c \"SELECT string_agg(k, ', ' ORDER BY k) FROM "
        f"(SELECT jsonb_object_keys(credentials->'model_mapping') k FROM accounts "
        f"WHERE {guard} AND deleted_at IS NULL) s;\" </dev/null\n"
        "echo '=== scheduler_outbox account_changed (last 2 min) ==='\n"
        f"$PSQL -c \"SELECT count(*) FROM scheduler_outbox WHERE account_id={account_id} "
        "AND event_type='account_changed' AND created_at > now() - interval '2 min';\" </dev/null\n"
        "echo APPLY_OK\n"
    )


# --- SQL self-check registry (ops-sql-coverage gate; doctrine: manage-anthropic-config.py)
# Every *_sql generator must be enumerated here (so ops/anthropic/test_ops_sql_execute.py
# runs it against a real Postgres) or exempted with a reason. No runner-shaped symbols here.
SELF_CHECK_EXEMPT: dict[str, str] = {}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for the ops-sql-coverage real-Postgres self-check."""
    sample_b64 = base64.b64encode(b'{"qwen3.6-27b":"qwen3.6-27b"}').decode()
    return [
        ("build_merge_sql", build_merge_sql(60, "Qwen", "newapi", 17, sample_b64)),
        ("build_remove_sql", build_remove_sql(60, "Qwen", "newapi", 17, ["qwen3.6-27b"])),
        # keys_array_sql returns a text[] fragment; wrap it in a runnable presence check.
        ("keys_array_sql", "SELECT '{}'::jsonb ?& " + keys_array_sql(["qwen3.6-27b", "qwen3-8b"])),
    ]


# --- AWS / SSM I/O: resolve_prod_instance + run_shell_b64 live in ops/stage0/ssm_execution.py


# --- subcommands --------------------------------------------------------------

def cmd_check(args) -> int:
    inst = _SSM.resolve_prod_instance()
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
    print(_SSM.run_shell_b64(inst, base64.b64encode(shell.encode()).decode(),
                        f"model_mapping check acct {args.account_id}"))
    return 0


def cmd_sync_live(args) -> int:
    validate_guard_fields(args.name, args.platform)
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
        inst = _SSM.resolve_prod_instance()
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
        print(_SSM.run_shell_b64(inst, base64.b64encode(shell.encode()).decode(),
                            f"model_mapping dry-run acct {args.account_id}"))
        return 0

    merge_sql = build_merge_sql(args.account_id, args.name, args.platform,
                                args.channel_type, additions_b64)
    sql_b64 = base64.b64encode(merge_sql.encode()).decode()
    inst = _SSM.resolve_prod_instance()
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
    out = _SSM.run_shell_b64(inst, base64.b64encode(shell.encode()).decode(),
                        f"model_mapping sync-live acct {args.account_id}")
    print(out)
    if "APPLY_OK" not in out:
        fail("APPLY did not report success — inspect the output above (guard match? psql error?)")
    print(f"applied. Verify servability with: probe-servable-models.sh "
          f"(DASHSCOPE_CHAT_MODELS / GEMINI_CHAT_MODELS = {' '.join(sorted(additions))})")
    return 0


def cmd_remove_live(args) -> int:
    """Hot-remove model_mapping keys from ONE newapi account (inverse of sync-live).

    Use when an account must STOP being scheduled for a model id — e.g. an alias was
    mapped onto an account whose upstream does not serve it, so every routed request
    fail-closes. The companion migration in git MUST be corrected in the same change,
    or the next release re-adds the key.
    """
    validate_guard_fields(args.name, args.platform)
    keys = build_removals(args.remove or [])
    keys_arr = keys_array_sql(keys)
    print(f"account {args.account_id} ({args.name}, {args.platform}, ct={args.channel_type})"
          f"  remove: {', '.join(keys)}")

    inst = _SSM.resolve_prod_instance()
    guard_and_before = (
        "echo '=== guard match (1 row expected; 0 = wrong id/name/platform/channel_type) ==='\n"
        f"$PSQL -c \"SELECT id, name, platform, channel_type FROM accounts "
        f"WHERE id={args.account_id} AND name='{args.name}' AND platform='{args.platform}' "
        f"AND channel_type={args.channel_type} AND deleted_at IS NULL;\" </dev/null\n"
        "echo '=== BEFORE: which of the target keys are present? ==='\n"
        f"$PSQL -c \"SELECT string_agg(k, ', ' ORDER BY k) FROM "
        f"(SELECT jsonb_object_keys(credentials->'model_mapping') k FROM accounts "
        f"WHERE id={args.account_id} AND deleted_at IS NULL) s WHERE k = ANY({keys_arr});\" "
        "</dev/null\n"
    )

    if args.dry_run:
        print("DRY-RUN: would jsonb `- text[]` the above keys off credentials.model_mapping + "
              "enqueue scheduler_outbox account_changed. No write.")
        shell = "set -uo pipefail\n" f"PSQL='{PSQL}'\n" + guard_and_before
        print(_SSM.run_shell_b64(inst, base64.b64encode(shell.encode()).decode(),
                                 f"model_mapping remove dry-run acct {args.account_id}"))
        return 0

    remove_sql = build_remove_sql(args.account_id, args.name, args.platform,
                                  args.channel_type, keys)
    sql_b64 = base64.b64encode(remove_sql.encode()).decode()
    shell = build_remove_live_shell(
        args.account_id,
        args.name,
        args.platform,
        args.channel_type,
        keys,
        sql_b64,
    )
    out = _SSM.run_shell_b64(inst, base64.b64encode(shell.encode()).decode(),
                             f"model_mapping remove-live acct {args.account_id}")
    print(out)
    if "APPLY_OK" not in out:
        fail("APPLY did not report success — inspect the output above (guard match? psql error?)")
    print("removed. Correct the companion tk_*.sql migration in git so the next release "
          "does not re-add these keys.")
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
    # Guard-field validation rejects both SQL and remote-shell literal breakers.
    if _PLATFORM_RE.fullmatch("newapi'; DROP") or not _PLATFORM_RE.fullmatch("newapi"):
        failures.append("_PLATFORM_RE wrong (must reject quotes, accept newapi)")
    if any(_NAME_RE.fullmatch(bad) for bad in ("Qwen'; x", 'Qwen"x', "Qwen$(id)", "Qwen`id`")) \
            or not _NAME_RE.fullmatch("ds-官"):
        failures.append("_NAME_RE wrong (must reject SQL/shell breakers, accept non-ASCII)")
    # merge SQL shape: guard tuple + jsonb || + scheduler_outbox + decode
    sql = build_merge_sql(60, "Qwen", "newapi", 17, "QQ==")
    for needle in ("id = 60", "name = 'Qwen'", "platform = 'newapi'", "channel_type = 17",
                   "deleted_at IS NULL", "|| convert_from(decode('QQ==', 'base64')",
                   "scheduler_outbox", "account_changed"):
        if needle not in sql:
            failures.append(f"merge SQL missing {needle!r}")
    # removals building: validated, de-duplicated, sorted
    if build_removals(["b", "a", "b", " "]) != ["a", "b"]:
        failures.append("build_removals wrong (want dedup+sort, blanks skipped)")
    for bad in (["bad'id"], ["has space"]):
        try:
            build_removals(bad)
            failures.append(f"invalid remove id {bad} not rejected")
        except SystemExit:
            pass
    try:
        build_removals([])
        failures.append("empty removals not rejected")
    except SystemExit:
        pass
    # remove SQL shape: same guard tuple, but jsonb `- text[]` instead of `||`
    rsql = build_remove_sql(39, "ds-官", "newapi", 43, ["deepseek-v4-flash-0731"])
    for needle in ("id = 39", "name = 'ds-官'", "platform = 'newapi'", "channel_type = 43",
                   "deleted_at IS NULL", "- array['deepseek-v4-flash-0731']::text[]",
                   "scheduler_outbox", "account_changed"):
        if needle not in rsql:
            failures.append(f"remove SQL missing {needle!r}")
    if "||" in rsql:
        failures.append("remove SQL must not contain a jsonb || merge")
    if failures:
        print("SELFTEST FAILED:")
        for f in failures:
            print(f"  - {f}")
        return 1
    print("selftest ok: additions / removals / validation / keys-array / SQL shape")
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

    r = sub.add_parser("remove-live",
                       help="hot-remove model_mapping keys from prod (inverse of sync-live)")
    r.add_argument("--account-id", type=int, required=True)
    r.add_argument("--name", required=True, help="guard: accounts.name (must match exactly)")
    r.add_argument("--channel-type", type=int, required=True, help="guard: accounts.channel_type")
    r.add_argument("--platform", default="newapi", help="guard: accounts.platform (default newapi)")
    r.add_argument("--remove", action="append", metavar="MODEL",
                   help="model_mapping key to delete (repeatable)")
    r.add_argument("--dry-run", action="store_true", help="preview guard match + BEFORE; no write")
    r.set_defaults(fn=cmd_remove_live)

    args = ap.parse_args()
    if not getattr(args, "fn", None):
        ap.print_help()
        return 2
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
