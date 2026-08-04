#!/bin/bash
# Daily GHCR app-tag prune + dangling Docker image cleanup.
# Invoked by tokenkey-ghcr-prune-daily.timer on prod EC2 and Lightsail edges.
# SSOT: deploy/aws/stage0/tokenkey-ghcr-prune-daily.sh
set -euo pipefail

if [ "${1:-}" = "--selftest" ]; then
  grep -q 'TOKENKEY_GHCR_KEEP_TAGS' "$0"
  grep -q 'docker image prune' "$0"
  exit 0
fi

PRUNE=/usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
[ -x "$PRUNE" ] || PRUNE=/usr/local/bin/tokenkey-prune-ghcr-app-tags.sh
if [ -x "$PRUNE" ]; then
  KEEP="${TOKENKEY_GHCR_KEEP_TAGS:-3}"
  logger -t tokenkey-ghcr-prune-daily "start keep_tags=${KEEP}"
  if ! env TOKENKEY_GHCR_KEEP_TAGS="${KEEP}" "$PRUNE"; then
    logger -t tokenkey-ghcr-prune-daily "ghcr tag prune failed (non-fatal)"
  fi
else
  logger -t tokenkey-ghcr-prune-daily "skip no prune script installed"
fi
docker image prune -f >/dev/null 2>&1 || true
logger -t tokenkey-ghcr-prune-daily "done"
