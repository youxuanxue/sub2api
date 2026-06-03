#!/usr/bin/env bash
# Read-only probe: dump ops feishu/email alert config + ops_alert_rules summary.
# Sensitive fields (webhook_url, signing_secret) are reduced to *_present booleans.
# Container/db names follow the Stage0 convention; falls back to docker ps if absent.
set -euo pipefail

PG="${PG_CONTAINER:-tokenkey-postgres}"
DB="${PG_DB:-tokenkey}"
DBUSER="${PG_USER:-tokenkey}"

psql() { docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -X -A -t "$@"; }

echo "=== feishu_email_config (sanitized) ==="
psql -c "
WITH s AS (
  SELECT value::jsonb AS v FROM settings WHERE key = 'ops_email_notification_config'
)
SELECT COALESCE(json_build_object(
  'row_present', (SELECT count(*) FROM s) > 0,
  'feishu_enabled',            (SELECT (v->'feishu'->>'enabled')::boolean FROM s),
  'feishu_webhook_present',    (SELECT length(coalesce(v->'feishu'->>'webhook_url','')) > 0 FROM s),
  'feishu_secret_present',     (SELECT length(coalesce(v->'feishu'->>'signing_secret','')) > 0 FROM s),
  'feishu_rate_limit_per_hour',(SELECT v->'feishu'->>'rate_limit_per_hour' FROM s),
  'feishu_cooldown_seconds',   (SELECT v->'feishu'->>'cooldown_seconds' FROM s),
  'alert_email_enabled',       (SELECT (v->'alert'->>'enabled')::boolean FROM s),
  'report_email_enabled',      (SELECT (v->'report'->>'enabled')::boolean FROM s)
)::text, '{\"row_present\":false}');
"

echo "=== alert_rules (id,name,metric,op,threshold,severity,enabled,notify_email) ==="
psql -F $'\t' -c "
SELECT id, name, metric_type, operator, threshold, severity, enabled, notify_email
FROM ops_alert_rules
ORDER BY id;
" 2>/dev/null || echo "(ops_alert_rules table missing or schema differs)"

echo "=== alert_rules_count_by_severity ==="
psql -F $'\t' -c "
SELECT severity, enabled, count(*)
FROM ops_alert_rules
GROUP BY severity, enabled
ORDER BY severity, enabled;
" 2>/dev/null || echo "(ops_alert_rules aggregation skipped)"

echo "=== recent_alert_events_14d (severity,status,count) ==="
psql -F $'\t' -c "
SELECT severity, status, count(*)
FROM ops_alert_events
WHERE created_at >= now() - interval '14 days'
GROUP BY severity, status
ORDER BY severity, status;
" 2>/dev/null || echo "(ops_alert_events table missing or schema differs)"
