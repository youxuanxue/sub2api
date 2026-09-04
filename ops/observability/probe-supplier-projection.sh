#!/usr/bin/env bash
# Read-only post-release supplier-source -> managed-account projection check.
# Runtime scheduling state (status/schedulable) is intentionally not queried.
set -euo pipefail

. /tmp/resolve-app-container.sh
if ! APP_CONTAINER="$(tk_resolve_app_container auto)"; then
  echo '{"verdict":"setup_error","error":"app_container_unresolved"}'
  exit 2
fi

FINGERPRINT_KEY="$(docker exec "$APP_CONTAINER" sh -c 'printf %s "$TOTP_ENCRYPTION_KEY"')"
if [ -z "$FINGERPRINT_KEY" ]; then
  echo '{"verdict":"setup_error","error":"fingerprint_key_unavailable"}'
  exit 2
fi
export TOTP_ENCRYPTION_KEY="$FINGERPRINT_KEY"
unset FINGERPRINT_KEY

SNAPSHOT_SQL=$(cat <<'SQL'
SELECT jsonb_build_object(
  'sources', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', id,
      'supplier_name', supplier_name,
      'supplier_lane', supplier_lane,
      'channel_type', channel_type,
      'endpoint', endpoint,
      'credential_fingerprint', credential_fingerprint,
      'base_priority', base_priority,
      'account_concurrency', account_concurrency,
      'models', models
    ) ORDER BY id)
    FROM model_supplier_sources
  ), '[]'::jsonb),
  'accounts', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', a.id,
      'name', a.name,
      'platform', a.platform,
      'type', a.type,
      'channel_type', a.channel_type,
      'credentials', a.credentials,
      'extra', a.extra,
      'priority', a.priority,
      'concurrency', a.concurrency,
      'capability_id', a.protocol_endpoint_capability_id,
      'capability_key', c.capability_key,
      'capability_identity', c.identity,
      'supported_protocols', c.supported_protocols,
      'probe_evidence', c.probe_evidence,
      'capability_identity_conflict', c.identity_conflict
    ) ORDER BY a.id)
    FROM accounts a
    LEFT JOIN protocol_endpoint_capabilities c
      ON c.id = a.protocol_endpoint_capability_id
    WHERE a.deleted_at IS NULL
      AND COALESCE(a.extra, '{}'::jsonb) ? 'supplier_source_id'
  ), '[]'::jsonb)
)::text;
SQL
)

if ! SNAPSHOT="$(docker exec -i \
    -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=10s' \
    tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 \
    -c "$SNAPSHOT_SQL")"; then
  echo '{"verdict":"setup_error","error":"snapshot_query_failed"}'
  exit 2
fi

printf '%s\n' "$SNAPSHOT" | python3 /tmp/supplier_projection_check.py --snapshot -
unset SNAPSHOT TOTP_ENCRYPTION_KEY
