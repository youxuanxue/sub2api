#!/usr/bin/env bash
# edge_anthropic_oauth_schedulable_probe.sh — count schedulable Anthropic OAuth accounts
# in the edge default scheduling group (excludes Kiro and apikey stubs).
#
# Runs ON the edge host (via run-probe / SSM). Prints a single integer on stdout.
#
# Env:
#   ANTHROPIC_SOURCE_GROUP  anthropic OAuth pool group (default: default)
set -euo pipefail

ANTHROPIC_SOURCE_GROUP="${ANTHROPIC_SOURCE_GROUP:-default}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

"${PSQL[@]}" -c "
SELECT COUNT(DISTINCT a.id)
FROM accounts a
INNER JOIN account_groups ag ON ag.account_id = a.id
INNER JOIN groups g ON g.id = ag.group_id
  AND g.name = '${ANTHROPIC_SOURCE_GROUP//\'/''}'
  AND g.deleted_at IS NULL
WHERE a.platform = 'anthropic'
  AND a.deleted_at IS NULL
  AND a.schedulable = true
  AND a.status = 'active'
  AND a.type IN ('oauth', 'setup_token');
" | tr -d '[:space:]'
