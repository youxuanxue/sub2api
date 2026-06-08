#!/bin/bash
# probe-user-traffic-window.sh — read-only prod user request/error snapshot for a time window.
# Runs on TokenKey host via run-probe.sh.
#
# Env:
#   USER_ID       required
#   WINDOW_MINUTES default 5
set -u

USER_ID="${USER_ID:?USER_ID required}"
WINDOW_MINUTES="${WINDOW_MINUTES:-5}"
PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

echo "=== probe_user_traffic user_id=$USER_ID window_minutes=$WINDOW_MINUTES db_now=$(date -u +%Y-%m-%dT%H:%M:%SZ) ==="

echo "=== user ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, email, username, status FROM users WHERE id=$USER_ID AND deleted_at IS NULL
) t;"

echo "=== usage_logs summary (success path) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT count(*) AS reqs,
         count(*) FILTER (WHERE total_cost > 0) AS billed_reqs,
         round(sum(total_cost)::numeric, 4) AS total_cost,
         min(created_at) AS first_at,
         max(created_at) AS last_at
  FROM usage_logs
  WHERE user_id=$USER_ID
    AND created_at >= now() - interval '${WINDOW_MINUTES} minutes'
) t;"

echo "=== usage_logs by_minute ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT to_char(date_trunc('minute', created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD HH24:MI') AS minute_utc,
         count(*) AS reqs,
         round(sum(total_cost)::numeric, 4) AS cost
  FROM usage_logs
  WHERE user_id=$USER_ID
    AND created_at >= now() - interval '${WINDOW_MINUTES} minutes'
  GROUP BY 1 ORDER BY 1
) t;"

echo "=== usage_logs by_model top ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT model, count(*) AS n, round(sum(total_cost)::numeric, 4) AS cost
  FROM usage_logs
  WHERE user_id=$USER_ID
    AND created_at >= now() - interval '${WINDOW_MINUTES} minutes'
  GROUP BY 1 ORDER BY n DESC LIMIT 8
) t;"

echo "=== ops_error_logs by_status ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT status_code, upstream_status_code, count(*) AS n,
         min(created_at) AS first_at, max(created_at) AS last_at
  FROM ops_error_logs
  WHERE user_id=$USER_ID
    AND created_at >= now() - interval '${WINDOW_MINUTES} minutes'
  GROUP BY 1,2 ORDER BY n DESC
) t;"

echo "=== ops_error_logs samples ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') AS ts_utc,
         status_code, upstream_status_code, model, request_path, account_id,
         left(error_message, 160) AS error_message
  FROM ops_error_logs
  WHERE user_id=$USER_ID
    AND created_at >= now() - interval '${WINDOW_MINUTES} minutes'
  ORDER BY created_at DESC
  LIMIT 12
) t;"

echo "=== gateway http completed (docker, last ${WINDOW_MINUTES}m) ==="
SINCE="${WINDOW_MINUTES}m"
python3 - "$USER_ID" "$SINCE" <<'PY'
import json, re, subprocess, sys
uid = int(sys.argv[1])
since = sys.argv[2]
proc = subprocess.run(
    ["docker", "logs", "tokenkey", "--since", since],
    capture_output=True, text=True, check=False,
)
marker = "http request completed"
json_re = re.compile(r"\{.*\}\s*$")
by_status = {}
rows = []
for line in proc.stdout.splitlines():
    if marker not in line:
        continue
    m = json_re.search(line)
    if not m:
        continue
    try:
        o = json.loads(m.group(0))
    except json.JSONDecodeError:
        continue
    if o.get("user_id") != uid:
        continue
    sc = o.get("status_code")
    by_status[sc] = by_status.get(sc, 0) + 1
    rows.append(o)
print(json.dumps({
    "matched_total": len(rows),
    "by_status_code": by_status,
    "samples": [{
        "completed_at": r.get("completed_at"),
        "status_code": r.get("status_code"),
        "upstream_status_code": r.get("upstream_status_code"),
        "path": r.get("path"),
        "model": r.get("model"),
        "account_id": r.get("account_id"),
        "latency_ms": r.get("latency_ms"),
    } for r in rows[-8:]],
}, ensure_ascii=False))
PY
