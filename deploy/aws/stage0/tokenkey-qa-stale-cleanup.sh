#!/bin/bash
# Regenerated into stage0-single-ec2.yaml by: deploy/aws/stage0/build-cfn.sh
# Retention is set at boot via /etc/tokenkey/qa-stale-retention.env (QaStaleRetentionDays).
set -euo pipefail
RETENTION_DAYS="${TOKENKEY_QA_STALE_RETENTION_DAYS:-1.5}"
if ! [[ "${RETENTION_DAYS}" =~ ^(0|[1-9][0-9]*)(\.[0-9]+)?$ ]]; then
  logger -t tokenkey-qa-stale-cleanup "invalid TOKENKEY_QA_STALE_RETENTION_DAYS=${TOKENKEY_QA_STALE_RETENTION_DAYS:-}"
  exit 1
fi
if awk -v d="${RETENTION_DAYS}" 'BEGIN { exit !(d == 0) }'; then
  logger -t tokenkey-qa-stale-cleanup "skip disabled QaStaleRetentionDays=0"
  exit 0
fi
if ! sudo docker ps --format '{{.Names}}' | grep -qx tokenkey-postgres; then
  logger -t tokenkey-qa-stale-cleanup "skip tokenkey-postgres not running"
  exit 0
fi
install -d -m 0755 /var/lib/tokenkey/app/qa_blobs /var/lib/tokenkey/app/qa_dlq 2>/dev/null || true

PGPASS="$(sudo grep '^POSTGRES_PASSWORD=' /var/lib/tokenkey/.env | cut -d= -f2-)"
if [ -z "${PGPASS}" ]; then
  logger -t tokenkey-qa-stale-cleanup "skip no POSTGRES_PASSWORD"
  exit 0
fi
NET="$(sudo docker inspect tokenkey-postgres --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}')"

logger -t tokenkey-qa-stale-cleanup "qa_records_delete_start retention_days=${RETENTION_DAYS}"
if ! sudo docker run --rm --network "${NET}" -e PGPASSWORD="${PGPASS}" postgres:16-alpine \
  psql -h tokenkey-postgres -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 \
  -c "DELETE FROM qa_records WHERE created_at < NOW() - ('${RETENTION_DAYS}'::numeric * interval '1 day');"; then
  logger -t tokenkey-qa-stale-cleanup "qa_records_delete_failed"
  exit 1
fi

# Align file pruning with fractional days (find -mtime is whole days only).
PRUNE_MINUTES="$(awk -v d="${RETENTION_DAYS}" 'BEGIN { printf "%d", d * 24 * 60 + 0.5 }')"
sudo find /var/lib/tokenkey/app/qa_blobs /var/lib/tokenkey/app/qa_dlq -mindepth 1 -type f \
  -mmin +"${PRUNE_MINUTES}" -delete 2>/dev/null || true
sudo find /var/lib/tokenkey/app/qa_blobs /var/lib/tokenkey/app/qa_dlq -depth -mindepth 1 -type d \
  -empty -delete 2>/dev/null || true

logger -t tokenkey-qa-stale-cleanup "cleanup_done"
