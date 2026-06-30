#!/usr/bin/env bash
# edge_oauth_pool_probe.sh — count schedulable Edge native OAuth/Kiro accounts.
#
# Runs ON the edge host (via run-probe / SSM). Uses the same eligibility rules as
# edge_native_anthropic_smoke.sh but only prints an integer count on stdout.
#
# Env:
#   ANTHROPIC_SOURCE_GROUP  anthropic OAuth pool group (default: default)
set -euo pipefail

ANTHROPIC_SOURCE_GROUP="${ANTHROPIC_SOURCE_GROUP:-default}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

"${PSQL[@]}" -c "
SELECT COUNT(DISTINCT a.id)
FROM accounts a
LEFT JOIN account_groups ag ON ag.account_id = a.id
LEFT JOIN groups g ON g.id = ag.group_id
  AND g.name = '${ANTHROPIC_SOURCE_GROUP//\'/''}'
  AND g.deleted_at IS NULL
WHERE a.platform IN ('anthropic', 'kiro')
  AND a.deleted_at IS NULL
  AND a.schedulable = true
  AND a.status = 'active'
  AND a.type IN ('oauth', 'setup_token')
  AND (
    (a.platform = 'anthropic' AND g.id IS NOT NULL)
    OR a.platform = 'kiro'
  );
" | tr -d '[:space:]'
