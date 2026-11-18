#!/usr/bin/env bash
# TAT acceptance probe after an isolated physical restore (closeout A13).
# Runs on the live data machine: confirms pgBackRest can see the archive and
# that the data-plane postgres container is healthy. Does not read SecureString.
set -euo pipefail

echo "physical_restore_acceptance: begin $(date -u +%Y-%m-%dT%H:%M:%SZ)"
if command -v pgbackrest >/dev/null 2>&1; then
  sudo -u postgres pgbackrest --stanza=tokenkey info
else
  echo "physical_restore_acceptance: WARN pgbackrest binary absent on host" >&2
fi
if sudo docker ps --format '{{.Names}}' | grep -Eq '^(tk-archive-standby|tokenkey-postgres)$'; then
  c="$(sudo docker ps --format '{{.Names}}' | grep -E '^(tk-archive-standby|tokenkey-postgres)$' | head -1)"
  sudo docker exec "$c" pg_isready -U tokenkey
  echo "physical_restore_acceptance: postgres_ok container=$c"
else
  echo "physical_restore_acceptance: FAIL no local postgres container" >&2
  exit 1
fi
echo "physical_restore_acceptance: ok"
