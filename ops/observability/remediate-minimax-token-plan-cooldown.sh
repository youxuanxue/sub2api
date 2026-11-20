#!/usr/bin/env bash
# Apply MiniMax Token Plan remains → accounts.rate_limit_reset_at (+ Extra 5h/weekly).
# Delivered via run-probe.sh on an app node (needs egress to api.minimaxi.com).
#
# Default dry-run. Write only with:
#   APPLY=yes-minimax-token-plan-cooldown
#
# Optional:
#   ACCOUNT_IDS="578,591,592,593"   (default: all active minimax-111* newapi accounts)
set -euo pipefail

APPLY="${APPLY:-}"
ACCOUNT_IDS="${ACCOUNT_IDS:-}"

for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found" >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
PSQL="docker exec ${TK_PG} psql -U tokenkey -d tokenkey -X -A -t"
# -i is required when feeding SQL on stdin (TAT/docker otherwise drops the script).
PSQL_I="docker exec -i ${TK_PG} psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1"

echo "=== meta ==="
printf 'now_utc=%s\napply=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${APPLY:-<dry-run>}"

id_filter=""
if [ -n "$ACCOUNT_IDS" ]; then
  case "$ACCOUNT_IDS" in
    *[!0-9,]* ) echo "ACCOUNT_IDS must be comma-separated ints" >&2; exit 2 ;;
  esac
  id_filter="AND id IN (${ACCOUNT_IDS})"
fi

echo
echo "=== accounts_before ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, rate_limit_reset_at, last_used_at, schedulable
  FROM accounts
  WHERE deleted_at IS NULL
    AND lower(name) LIKE 'minimax-111%'
    ${id_filter}
  ORDER BY id
) t;
"

echo
echo "=== remains_and_plan ==="
# Build plan without printing API keys.
plan_json=$(
$PSQL -c "
SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json) FROM (
  SELECT id, name,
         COALESCE(credentials->>'api_key', credentials->>'key', '') AS api_key,
         COALESCE(credentials->>'base_url', '') AS base_url
  FROM accounts
  WHERE deleted_at IS NULL
    AND lower(name) LIKE 'minimax-111%'
    ${id_filter}
  ORDER BY id
) t;
" | head -1
)

# Host has python3 via run-probe; keys stay inside this subshell.
PLAN_OUT=$(ACCOUNT_JSON="$plan_json" python3 - <<'PY'
import json, os, ssl, urllib.request
from datetime import datetime, timezone

accounts = json.loads(os.environ["ACCOUNT_JSON"] or "[]")
ctx = ssl.create_default_context()
rows = []
for a in accounts:
    key = (a.get("api_key") or "").strip()
    base = (a.get("base_url") or "").lower()
    url = "https://api.minimaxi.com/v1/api/openplatform/coding_plan/remains"
    if "minimax.io" in base:
        url = "https://api.minimax.io/v1/api/openplatform/coding_plan/remains"
    item = {"id": a["id"], "name": a["name"]}
    if not key:
        item["error"] = "missing_key"
        rows.append(item)
        continue
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {key}", "Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=25) as resp:
            data = json.loads(resp.read().decode("utf-8", "replace"))
    except Exception as e:
        item["error"] = type(e).__name__
        rows.append(item)
        continue
    remains = data.get("model_remains") or []
    general = None
    for m in remains:
        if str(m.get("model_name", "")).lower() == "general":
            general = m
            break
    if general is None:
        item["error"] = "no_general"
        rows.append(item)
        continue

    def iso(v):
        if v is None:
            return None
        v = int(v)
        if v > 10_000_000_000:
            v = v / 1000.0
        return datetime.fromtimestamp(v, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    irem = general.get("current_interval_remaining_percent")
    wstatus = general.get("current_weekly_status")
    wrem = general.get("current_weekly_remaining_percent")
    iend = iso(general.get("end_time"))
    wend = iso(general.get("weekly_end_time"))
    item.update({
        "interval_remaining_percent": irem,
        "interval_end": iend,
        "weekly_status": wstatus,
        "weekly_remaining_percent": wrem,
        "weekly_end": wend,
    })
    action = "noop"
    cool_until = None
    window = None
    # Weekly hard-empty wins over 5h when both are spent.
    try:
        if wstatus == 1 and wrem is not None and float(wrem) <= 0 and wend:
            action, cool_until, window = "cool", wend, "weekly"
        elif irem is not None and float(irem) <= 0 and iend:
            action, cool_until, window = "cool", iend, "5h"
        elif irem is not None and float(irem) > 0:
            action = "clear_stale_or_keep"
    except (TypeError, ValueError):
        item["error"] = "bad_percent"
        rows.append(item)
        continue
    item["action"] = action
    item["cool_until"] = cool_until
    item["window"] = window
    rows.append(item)
print(json.dumps(rows, ensure_ascii=False))
PY
)
echo "$PLAN_OUT"

if [ "$APPLY" != "yes-minimax-token-plan-cooldown" ]; then
  echo
  echo "=== dry-run ==="
  echo "set APPLY=yes-minimax-token-plan-cooldown to write rate_limit_reset_at + Extra + scheduler_outbox"
  echo "=== done ==="
  exit 0
fi

echo
echo "=== apply ==="
plan_file="$(mktemp)"
sql_file="$(mktemp)"
trap 'rm -f "$plan_file" "$sql_file"' EXIT
printf '%s' "$PLAN_OUT" >"$plan_file"
python3 - "$plan_file" >"$sql_file" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1], encoding="utf-8"))
stmts = []
for r in rows:
    if r.get("error"):
        continue
    aid = int(r["id"])
    action = r.get("action")
    if action == "cool" and r.get("cool_until"):
        until = r["cool_until"]
        window = r.get("window") or "5h"
        if window == "weekly":
            util_key, reset_key, sampled_key = (
                "newapi_weekly_utilization",
                "newapi_weekly_reset",
                "newapi_weekly_sampled_at",
            )
        else:
            util_key, reset_key, sampled_key = (
                "newapi_5h_utilization",
                "newapi_5h_reset",
                "newapi_5h_sampled_at",
            )
        # newapi_account_window_lock must be set: without it, matching
        # newapi_*_reset ≈ rate_limit_reset_at is treated as a Volc-style
        # false window lock — AccountWideRateLimitResetAt hides the cool from
        # admin UI and scheduling (see newAPIAccountWindowLockIgnoredAt).
        stmts.append(f"""
BEGIN;
UPDATE accounts SET
  rate_limited_at = now(),
  rate_limit_reset_at = TIMESTAMPTZ '{until}',
  extra = COALESCE(extra, '{{}}'::jsonb) || jsonb_build_object(
    'newapi_account_window_lock', 1.0,
    'newapi_usage_window_observed', 1.0,
    '{util_key}', 1.0,
    '{reset_key}', EXTRACT(EPOCH FROM TIMESTAMPTZ '{until}')::bigint,
    '{sampled_key}', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')
  ),
  updated_at = now()
WHERE id = {aid};
INSERT INTO scheduler_outbox (event_type, account_id)
VALUES ('account_changed', {aid});
COMMIT;
""")
    elif action == "clear_stale_or_keep":
        # Clear expired / already-elapsed cooldowns only; never shorten a future lock.
        stmts.append(f"""
BEGIN;
UPDATE accounts SET
  rate_limited_at = NULL,
  rate_limit_reset_at = NULL,
  updated_at = now()
WHERE id = {aid}
  AND rate_limit_reset_at IS NOT NULL
  AND rate_limit_reset_at <= now();
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', {aid}
WHERE EXISTS (
  SELECT 1 FROM accounts
  WHERE id = {aid}
    AND deleted_at IS NULL
    AND rate_limit_reset_at IS NULL
);
COMMIT;
""")
sys.stdout.write("\n".join(stmts))
PY
$PSQL_I <"$sql_file"
echo "applied_sql_bytes=$(wc -c <"$sql_file")"

echo
echo "=== accounts_after ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, rate_limited_at, rate_limit_reset_at,
         extra->>'newapi_5h_utilization' AS util_5h,
         extra->>'newapi_5h_reset' AS reset_5h_unix,
         extra->>'newapi_weekly_utilization' AS util_weekly,
         extra->>'newapi_weekly_reset' AS reset_weekly_unix
  FROM accounts
  WHERE deleted_at IS NULL
    AND lower(name) LIKE 'minimax-111%'
    ${id_filter}
  ORDER BY id
) t;
"

echo
echo "=== done ==="
