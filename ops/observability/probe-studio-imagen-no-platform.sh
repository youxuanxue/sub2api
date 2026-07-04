#!/usr/bin/env bash
# probe-studio-imagen-no-platform.sh - read-only triage for Studio image
# "No platform in your plan can serve model ..." errors.
#
# Runs inside prod/edge via ops/observability/run-probe.sh. Outputs row_to_json
# rows and never prints API key secrets or request bodies.
set -u

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'
WINDOW_HOURS="${WINDOW_HOURS:-168}"
LIMIT="${LIMIT:-40}"
MODEL="${MODEL:-imagen-4.0-generate-001}"

validate_int() {
  local name="$1" value="$2"
  case "$value" in
    ''|*[!0-9]*) echo "bad ${name} (want positive integer)" >&2; exit 2 ;;
  esac
  if [ "$value" -le 0 ]; then
    echo "bad ${name} (want positive integer)" >&2
    exit 2
  fi
}

sql_quote() {
  local escaped
  escaped=$(printf '%s' "$1" | sed "s/'/''/g")
  printf "'%s'" "$escaped"
}

validate_int WINDOW_HOURS "$WINDOW_HOURS"
validate_int LIMIT "$LIMIT"
if [ "$LIMIT" -gt 200 ]; then
  echo "bad LIMIT (max 200 to keep SSM stdout bounded)" >&2
  exit 2
fi
if [ "${#MODEL}" -gt 256 ] || [[ "$MODEL" =~ [[:cntrl:]] ]]; then
  echo "bad MODEL" >&2
  exit 2
fi

Q_MODEL=$(sql_quote "$MODEL")

BASE_CTE="WITH bounds AS (
  SELECT now() - interval '${WINDOW_HOURS} hours' AS since,
         now() AS until
), base AS (
  SELECT l.*
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since
    AND l.created_at < b.until
    AND (
      l.model = ${Q_MODEL}
      OR l.requested_model = ${Q_MODEL}
      OR l.upstream_model = ${Q_MODEL}
      OR l.error_message ILIKE '%' || ${Q_MODEL} || '%'
      OR l.upstream_error_message ILIKE '%' || ${Q_MODEL} || '%'
    )
)"

echo "=== meta ==="
$PSQL -c "SELECT row_to_json(t) FROM (SELECT
  now() AT TIME ZONE 'UTC' AS now_utc,
  now() AT TIME ZONE 'Asia/Shanghai' AS now_cst,
  ${WINDOW_HOURS}::int AS window_hours,
  ${LIMIT}::int AS limit_rows,
  ${Q_MODEL}::text AS model_filter) t;" 2>&1

echo
echo "=== imagen summary ==="
$PSQL -c "${BASE_CTE}
SELECT row_to_json(t) FROM (SELECT
  count(*) AS rows,
  min(created_at) AT TIME ZONE 'UTC' AS first_at_utc,
  max(created_at) AT TIME ZONE 'UTC' AS last_at_utc,
  count(*) FILTER (WHERE error_message ILIKE '%No platform in your plan can serve%') AS no_platform_rows,
  count(DISTINCT user_id) FILTER (WHERE user_id IS NOT NULL) AS users,
  count(DISTINCT api_key_id) FILTER (WHERE api_key_id IS NOT NULL) AS api_keys
  FROM base) t;" 2>&1

echo
echo "=== imagen by key/error ==="
$PSQL -c "${BASE_CTE}
SELECT row_to_json(t) FROM (SELECT
  l.user_id,
  l.api_key_id,
  k.name AS key_name,
  k.routing_mode,
  k.group_id AS key_group_id,
  l.group_id AS error_group_id,
  l.request_path,
  l.inbound_endpoint,
  l.status_code,
  l.error_phase,
  l.error_type,
  left(COALESCE(NULLIF(l.error_message, ''), NULLIF(l.upstream_error_message, ''), ''), 220) AS msg,
  count(*) AS rows,
  min(l.created_at) AT TIME ZONE 'UTC' AS first_at_utc,
  max(l.created_at) AT TIME ZONE 'UTC' AS last_at_utc
  FROM base l
  LEFT JOIN api_keys k ON k.id = l.api_key_id AND k.deleted_at IS NULL
  GROUP BY 1,2,3,4,5,6,7,8,9,10,11,12
  ORDER BY rows DESC, last_at_utc DESC
  LIMIT 30) t;" 2>&1

echo
echo "=== imagen samples ==="
$PSQL -c "${BASE_CTE}
SELECT row_to_json(t) FROM (SELECT
  l.id,
  l.created_at AT TIME ZONE 'UTC' AS ts_utc,
  l.created_at AT TIME ZONE 'Asia/Shanghai' AS ts_cst,
  COALESCE(NULLIF(l.request_id, ''), NULLIF(l.client_request_id, ''), '') AS request_id,
  l.user_id,
  l.api_key_id,
  k.name AS key_name,
  k.routing_mode,
  k.group_id AS key_group_id,
  l.group_id AS error_group_id,
  l.account_id,
  l.model,
  l.requested_model,
  l.upstream_model,
  l.request_path,
  l.inbound_endpoint,
  l.status_code,
  l.error_phase,
  l.error_type,
  l.error_owner,
  left(COALESCE(NULLIF(l.error_message, ''), NULLIF(l.upstream_error_message, ''), ''), 260) AS msg
  FROM base l
  LEFT JOIN api_keys k ON k.id = l.api_key_id AND k.deleted_at IS NULL
  ORDER BY l.created_at DESC, l.id DESC
  LIMIT ${LIMIT}) t;" 2>&1

echo
echo "=== no-platform any model ==="
$PSQL -c "WITH bounds AS (
  SELECT now() - interval '${WINDOW_HOURS} hours' AS since,
         now() AS until
), base AS (
  SELECT l.*
  FROM ops_error_logs l, bounds b
  WHERE l.created_at >= b.since
    AND l.created_at < b.until
    AND l.error_message ILIKE '%No platform in your plan can serve%'
)
SELECT row_to_json(t) FROM (SELECT
  COALESCE(NULLIF(model, ''), NULLIF(requested_model, ''), NULLIF(upstream_model, ''), '<missing>') AS model,
  request_path,
  inbound_endpoint,
  status_code,
  count(*) AS rows,
  min(created_at) AT TIME ZONE 'UTC' AS first_at_utc,
  max(created_at) AT TIME ZONE 'UTC' AS last_at_utc,
  left(max(error_message), 220) AS sample_msg
  FROM base
  GROUP BY 1,2,3,4
  ORDER BY rows DESC, last_at_utc DESC
  LIMIT 40) t;" 2>&1

echo
echo "=== entitled newapi image groups for affected users ==="
$PSQL -c "${BASE_CTE}, users AS (
  SELECT DISTINCT user_id FROM base WHERE user_id IS NOT NULL
), entitled AS (
  SELECT DISTINCT u.user_id, g.id AS group_id, g.name AS group_name, g.platform,
         g.status AS group_status, g.allow_image_generation, g.sort_order
  FROM users u
  JOIN user_allowed_groups uag ON uag.user_id = u.user_id
  JOIN groups g ON g.id = uag.group_id
  WHERE g.deleted_at IS NULL
    AND g.platform = 'newapi'
)
SELECT row_to_json(t) FROM (SELECT
  e.user_id,
  e.group_id,
  e.group_name,
  e.group_status,
  e.allow_image_generation,
  e.sort_order,
  count(a.id) FILTER (WHERE a.id IS NOT NULL) AS bound_accounts,
  count(a.id) FILTER (
    WHERE a.status = 'active'
      AND a.schedulable = true
      AND (a.expires_at IS NULL OR a.expires_at > now())
      AND (a.overload_until IS NULL OR a.overload_until <= now())
      AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= now())
      AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= now())
  ) AS repo_schedulable_like_accounts,
  count(a.id) FILTER (WHERE a.credentials->'model_mapping' ? ${Q_MODEL}) AS mapping_exact_accounts,
  string_agg(
    DISTINCT concat(
      a.id::text, ':', COALESCE(a.name, ''), ':ct=', COALESCE(a.channel_type::text, ''),
      ':type=', COALESCE(a.type, ''),
      ':active=', (a.status = 'active')::text,
      ':sched=', COALESCE(a.schedulable::text, ''),
      ':maps=', COALESCE((a.credentials->'model_mapping' ? ${Q_MODEL})::text, 'false')
    ),
    ', ' ORDER BY concat(
      a.id::text, ':', COALESCE(a.name, ''), ':ct=', COALESCE(a.channel_type::text, ''),
      ':type=', COALESCE(a.type, ''),
      ':active=', (a.status = 'active')::text,
      ':sched=', COALESCE(a.schedulable::text, ''),
      ':maps=', COALESCE((a.credentials->'model_mapping' ? ${Q_MODEL})::text, 'false')
    )
  ) FILTER (WHERE a.id IS NOT NULL) AS account_samples
  FROM entitled e
  LEFT JOIN account_groups ag ON ag.group_id = e.group_id
  LEFT JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL AND a.platform = e.platform
  GROUP BY 1,2,3,4,5,6
  ORDER BY e.sort_order, e.group_id
  LIMIT 60) t;" 2>&1
