#!/usr/bin/env bash
# Read-only daily error evidence for ops-daily-diagnostics.
#
# Output is sectioned JSON. It deliberately excludes request/response bodies,
# error messages, user identifiers, API keys, and client IPs so the resulting
# artifact is safe to pass to the repository-only repair workflow.
set -euo pipefail

WINDOW_HOURS="${WINDOW_HOURS:-24}"
case "$WINDOW_HOURS" in
  ''|*[!0-9]*) echo "[probe-daily-error-ledger] WINDOW_HOURS must be a positive integer" >&2; exit 2 ;;
esac
if [ "$WINDOW_HOURS" -lt 1 ] || [ "$WINDOW_HOURS" -gt 168 ]; then
  echo "[probe-daily-error-ledger] WINDOW_HOURS must be between 1 and 168" >&2
  exit 2
fi

DOCKER_BIN="${DOCKER_BIN:-docker}"
ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"

psql_query() {
  "$DOCKER_BIN" exec tokenkey-postgres psql \
    -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c "$1"
}

resolve_app_container() {
  local color candidate
  if [ -r "$ACTIVE_COLOR_FILE" ]; then
    color="$(tr -d '[:space:]' < "$ACTIVE_COLOR_FILE" 2>/dev/null || true)"
    case "$color" in
      blue|green)
        candidate="tokenkey-$color"
        if "$DOCKER_BIN" inspect "$candidate" >/dev/null 2>&1; then
          printf '%s\n' "$candidate"
          return 0
        fi
        ;;
    esac
  fi
  for candidate in tokenkey tokenkey-blue tokenkey-green; do
    if "$DOCKER_BIN" inspect "$candidate" >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

APP_CONTAINER="$(resolve_app_container || true)"
RUNTIME_IMAGE=""
RUNTIME_STARTED_AT=""
if [ -n "$APP_CONTAINER" ]; then
  RUNTIME_IMAGE="$($DOCKER_BIN inspect "$APP_CONTAINER" --format '{{.Config.Image}}' 2>/dev/null || true)"
  RUNTIME_STARTED_AT="$($DOCKER_BIN inspect "$APP_CONTAINER" --format '{{.State.StartedAt}}' 2>/dev/null || true)"
fi

echo "=== meta ==="
python3 - "$WINDOW_HOURS" "$APP_CONTAINER" "$RUNTIME_IMAGE" "$RUNTIME_STARTED_AT" <<'PY'
import datetime as dt
import json
import sys

hours, container, image, started_at = sys.argv[1:]
now = dt.datetime.now(dt.timezone.utc)
print(json.dumps({
    "status": "ok",
    "generated_at": now.isoformat().replace("+00:00", "Z"),
    "window_hours": int(hours),
    "window_since": (now - dt.timedelta(hours=int(hours))).isoformat().replace("+00:00", "Z"),
    "window_until": now.isoformat().replace("+00:00", "Z"),
    "runtime_container": container or None,
    "runtime_image": image or None,
    "runtime_started_at": started_at or None,
}, separators=(",", ":"), sort_keys=True))
PY

HAS_TABLES="$(psql_query "SELECT to_regclass('public.usage_logs') IS NOT NULL AND to_regclass('public.ops_error_logs') IS NOT NULL;")"
if [ "$HAS_TABLES" != "t" ]; then
  echo "=== skip ==="
  echo '{"reason":"usage_logs or ops_error_logs is unavailable"}'
  exit 0
fi

echo "=== totals ==="
psql_query "
WITH bounds AS (
  SELECT now() AS until,
         now() - interval '${WINDOW_HOURS} hour' AS current_since,
         now() - interval '$((WINDOW_HOURS * 2)) hour' AS previous_since
), successes AS (
  SELECT
    COUNT(*) FILTER (WHERE u.created_at >= b.current_since AND u.created_at < b.until)::bigint AS current_success,
    COUNT(*) FILTER (WHERE u.created_at >= b.previous_since AND u.created_at < b.current_since)::bigint AS previous_success
  FROM usage_logs u CROSS JOIN bounds b
  WHERE u.created_at >= b.previous_since AND u.created_at < b.until
), errors AS (
  SELECT
    COUNT(*) FILTER (WHERE e.created_at >= b.current_since AND e.status_code >= 400)::bigint AS current_error_total,
    COUNT(*) FILTER (WHERE e.created_at >= b.current_since AND e.status_code >= 400 AND COALESCE(e.error_owner,'') IN ('provider','platform'))::bigint AS current_error_sla,
    COUNT(*) FILTER (WHERE e.created_at >= b.current_since AND e.status_code >= 400 AND COALESCE(e.error_owner,'') = 'client')::bigint AS current_client_faults,
    COUNT(*) FILTER (WHERE e.created_at >= b.previous_since AND e.created_at < b.current_since AND e.status_code >= 400)::bigint AS previous_error_total,
    COUNT(*) FILTER (WHERE e.created_at >= b.previous_since AND e.created_at < b.current_since AND e.status_code >= 400 AND COALESCE(e.error_owner,'') IN ('provider','platform'))::bigint AS previous_error_sla,
    COUNT(*) FILTER (
      WHERE e.created_at >= b.current_since
        AND COALESCE(e.status_code,0) < 400
        AND COALESCE(e.error_owner,'') = 'provider'
        AND jsonb_typeof(COALESCE(e.upstream_errors, '[]'::jsonb)) = 'array'
        AND jsonb_array_length(COALESCE(e.upstream_errors, '[]'::jsonb)) > 0
    )::bigint AS current_recovered_requests
  FROM ops_error_logs e CROSS JOIN bounds b
  WHERE e.created_at >= b.previous_since AND e.created_at < b.until
    AND COALESCE(e.is_count_tokens, FALSE) = FALSE
)
SELECT row_to_json(t) FROM (
  SELECT
    s.current_success,
    e.current_error_total,
    e.current_error_sla,
    e.current_client_faults,
    e.current_recovered_requests,
    (s.current_success + e.current_error_total) AS current_request_total,
    CASE WHEN (s.current_success + e.current_error_total) > 0
      THEN round(100.0 * (s.current_success + e.current_client_faults)::numeric /
        (s.current_success + e.current_error_total)::numeric, 3)
      ELSE 0 END AS current_sla_percent,
    s.previous_success,
    e.previous_error_total,
    e.previous_error_sla,
    (s.previous_success + e.previous_error_total) AS previous_request_total
  FROM successes s CROSS JOIN errors e
) t;"

echo "=== clusters ==="
psql_query "
WITH bounds AS (
  SELECT now() AS until,
         now() - interval '${WINDOW_HOURS} hour' AS current_since,
         now() - interval '$((WINDOW_HOURS * 2)) hour' AS previous_since,
         now() - interval '7 day' AS baseline_since
), normalized AS (
  SELECT
    COALESCE(e.status_code,0) AS status_code,
    COALESCE(NULLIF(e.error_owner,''),'unknown') AS owner,
    COALESCE(NULLIF(e.error_phase,''),'unknown') AS phase,
    COALESCE(NULLIF(e.error_type,''),'unknown') AS error_type,
    left(regexp_replace(COALESCE(e.platform,''), E'[\\n\\r\\t]+', ' ', 'g'), 64) AS platform,
    left(regexp_replace(COALESCE(NULLIF(e.requested_model,''), NULLIF(e.model,''), '(unknown)'), E'[\\n\\r\\t]+', ' ', 'g'), 120) AS model,
    left(regexp_replace(COALESCE(NULLIF(e.request_path,''), NULLIF(e.inbound_endpoint,''), '(unknown)'), E'[\\n\\r\\t]+', ' ', 'g'), 120) AS endpoint,
    e.account_id,
    e.group_id,
    e.created_at
  FROM ops_error_logs e CROSS JOIN bounds b
  WHERE e.created_at >= b.baseline_since AND e.created_at < b.until
    AND COALESCE(e.status_code,0) >= 400
    AND COALESCE(e.is_count_tokens, FALSE) = FALSE
), bucketed AS (
  SELECT
    n.status_code, n.owner, n.phase, n.error_type, n.platform, n.model, n.endpoint,
    date_bin('5 minutes', n.created_at, timestamptz 'epoch') AS slot_5m,
    COUNT(*)::bigint AS count_5m
  FROM normalized n CROSS JOIN bounds b
  WHERE n.created_at >= b.current_since
  GROUP BY 1,2,3,4,5,6,7,8
), peaks AS (
  SELECT status_code, owner, phase, error_type, platform, model, endpoint,
         MAX(count_5m)::bigint AS max_count_5m
  FROM bucketed
  GROUP BY 1,2,3,4,5,6,7
), grouped AS (
  SELECT
    n.status_code, n.owner, n.phase, n.error_type, n.platform, n.model, n.endpoint,
    COUNT(*) FILTER (WHERE n.created_at >= b.current_since)::bigint AS current_count,
    COUNT(*) FILTER (WHERE n.created_at >= b.previous_since AND n.created_at < b.current_since)::bigint AS previous_count,
    COUNT(*)::bigint AS baseline_7d_count,
    COUNT(DISTINCT (n.created_at AT TIME ZONE 'UTC')::date)::int AS active_days_7d,
    MIN(n.created_at) AS first_seen_7d,
    MAX(n.created_at) AS last_seen,
    (array_agg(DISTINCT n.account_id) FILTER (WHERE n.account_id IS NOT NULL))[1:5] AS account_ids,
    (array_agg(DISTINCT n.group_id) FILTER (WHERE n.group_id IS NOT NULL))[1:5] AS group_ids
  FROM normalized n CROSS JOIN bounds b
  GROUP BY n.status_code, n.owner, n.phase, n.error_type, n.platform, n.model, n.endpoint
)
SELECT row_to_json(t) FROM (
  SELECT g.*, COALESCE(p.max_count_5m,0) AS max_count_5m
  FROM grouped g
  LEFT JOIN peaks p USING (status_code, owner, phase, error_type, platform, model, endpoint)
  WHERE g.current_count > 0
  ORDER BY g.current_count DESC, g.status_code DESC, g.owner, g.platform, g.model, g.endpoint
  LIMIT 30
) t;"

echo "=== recovered ==="
psql_query "
WITH bounds AS (
  SELECT now() AS until, now() - interval '${WINDOW_HOURS} hour' AS current_since
)
SELECT row_to_json(t) FROM (
  SELECT
    left(regexp_replace(COALESCE(e.platform,''), E'[\\n\\r\\t]+', ' ', 'g'), 64) AS platform,
    left(regexp_replace(COALESCE(NULLIF(e.requested_model,''), NULLIF(e.model,''), '(unknown)'), E'[\\n\\r\\t]+', ' ', 'g'), 120) AS model,
    left(regexp_replace(COALESCE(NULLIF(e.request_path,''), NULLIF(e.inbound_endpoint,''), '(unknown)'), E'[\\n\\r\\t]+', ' ', 'g'), 120) AS endpoint,
    COUNT(*)::bigint AS recovered_requests,
    SUM(jsonb_array_length(COALESCE(e.upstream_errors, '[]'::jsonb)))::bigint AS failed_attempts
  FROM ops_error_logs e CROSS JOIN bounds b
  WHERE e.created_at >= b.current_since AND e.created_at < b.until
    AND COALESCE(e.status_code,0) < 400
    AND COALESCE(e.error_owner,'') = 'provider'
    AND jsonb_typeof(COALESCE(e.upstream_errors, '[]'::jsonb)) = 'array'
    AND jsonb_array_length(COALESCE(e.upstream_errors, '[]'::jsonb)) > 0
    AND COALESCE(e.is_count_tokens, FALSE) = FALSE
  GROUP BY 1,2,3
  ORDER BY recovered_requests DESC, failed_attempts DESC
  LIMIT 10
) t;"

echo "=== access_clusters ==="
if [ -z "$APP_CONTAINER" ]; then
  echo '{"status":"skip","reason":"active app container unavailable"}'
else
  ACCESS_LOG="$(mktemp)"
  trap 'rm -f "$ACCESS_LOG"' EXIT
  "$DOCKER_BIN" logs "$APP_CONTAINER" --since "${WINDOW_HOURS}h" >"$ACCESS_LOG" 2>&1 || true
  python3 - "$ACCESS_LOG" <<'PY'
import collections
import json
import re
import sys

path = sys.argv[1]
marker = "http request completed"
json_re = re.compile(r"\{.*\}\s*$")
counts = collections.Counter()
minutes = collections.Counter()
parsed = 0
with open(path, "r", encoding="utf-8", errors="replace") as fh:
    for line in fh:
        if marker not in line:
            continue
        match = json_re.search(line)
        if not match:
            continue
        try:
            item = json.loads(match.group(0))
            status = int(item.get("status_code"))
        except (json.JSONDecodeError, TypeError, ValueError):
            continue
        parsed += 1
        if status < 400:
            continue
        endpoint = str(item.get("path") or "(unknown)").replace("\n", " ")[:120]
        model = str(item.get("model") or "(unknown)").replace("\n", " ")[:120]
        counts[(status, endpoint, model)] += 1
        timestamp = str(item.get("completed_at") or "")
        minute = timestamp[:16] if len(timestamp) >= 16 else "unknown"
        minutes[(status, endpoint, model, minute)] += 1

for (status, endpoint, model), count in sorted(counts.items(), key=lambda pair: (-pair[1], pair[0]))[:15]:
    peak = max((n for (s, e, m, _), n in minutes.items() if (s, e, m) == (status, endpoint, model)), default=count)
    print(json.dumps({
        "status": "ok",
        "status_code": status,
        "endpoint": endpoint,
        "model": model,
        "current_count": count,
        "max_count_1m": peak,
    }, separators=(",", ":"), sort_keys=True))
if not counts:
    print(json.dumps({"status": "ok", "current_count": 0, "lines_parsed": parsed}, separators=(",", ":"), sort_keys=True))
PY
fi
