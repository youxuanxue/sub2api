#!/usr/bin/env bash
# Read-only exclusive-group orphan inventory on prod (data-invariant check).
set -euo pipefail

SQL_PATH="/tmp/exclusive_group_orphans.sql"
if [ ! -s "$SQL_PATH" ]; then
  echo "exclusive_group_orphans.sql missing on host" >&2
  exit 1
fi

PG_USER="${PG_USER:-tokenkey}"
PG_DB="${PG_DB:-tokenkey}"

# shellcheck disable=SC1091
. "${TK_RESOLVE_APP_CONTAINER_SH:-/tmp/resolve-app-container.sh}"
eval "$(python3 "$(dirname "$0")/../lib/resolve_app_container.py" snippet postgres --docker "sudo docker")"
sudo docker exec -i "$TK_PG" psql -U "$PG_USER" -d "$PG_DB" -At -F '|' < "$SQL_PATH"