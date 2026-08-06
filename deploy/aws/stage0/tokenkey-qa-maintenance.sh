#!/bin/bash
# Hourly QA archive-only maintenance (Phase 2). Cleanup remains disabled until Phase 4.
# Owner: design-prod-qa-24h-s3-lifecycle.md §7; invoked by tokenkey-qa-maintenance.timer (*:15 UTC).
set -euo pipefail
cd /var/lib/tokenkey
app_container=tokenkey
if [ -r active-color ]; then
  color=$(sed -n '1p' active-color 2>/dev/null | tr -d '[:space:]')
  case "${color}" in
    blue|green) app_container="tokenkey-${color}" ;;
  esac
fi
if ! sudo docker ps --format '{{.Names}}' | grep -qx "${app_container}"; then
  logger -t tokenkey-qa-maintenance "skip ${app_container} not running"
  exit 0
fi
logger -t tokenkey-qa-maintenance "archive_only_start container=${app_container}"
if ! sudo docker exec "${app_container}" /app/sub2api \
  --qa-maintenance-once \
  --confirm=tokenkey-prod-qa-maintenance-v1; then
  logger -t tokenkey-qa-maintenance "archive_only_failed"
  exit 1
fi
logger -t tokenkey-qa-maintenance "archive_only_done"
