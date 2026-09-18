#!/usr/bin/env bash
# Write remediation: drop claude-fable-5-1 from specific account model_mapping.
# Opt-in: CONFIRM=drop-fable-5-1-mapping
# Target accounts default: 91,150 (bedrock-2 + Cursor; both unschedulable holders).
set -euo pipefail

CONFIRM="${CONFIRM:-}"
ACCOUNT_IDS="${ACCOUNT_IDS:-91,150}"
DROP_KEY="${DROP_KEY:-claude-fable-5-1}"

PSQL=(docker exec -i
  -e 'PGOPTIONS=-c lock_timeout=500ms -c statement_timeout=15s'
  tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

echo "=== policy ==="
echo "confirm=$CONFIRM account_ids=$ACCOUNT_IDS drop_key=$DROP_KEY"

echo "=== before ==="
"${PSQL[@]}" <<SQL
SELECT row_to_json(t) FROM (
  SELECT a.id, a.name, a.platform, a.status, a.schedulable,
         COALESCE((
           SELECT jsonb_object_agg(k, a.credentials->'model_mapping'->k)
           FROM jsonb_object_keys(COALESCE(a.credentials->'model_mapping','{}'::jsonb)) k
           WHERE k ILIKE '%fable%'
         ), '{}'::jsonb) AS fable_mapping
  FROM accounts a
  WHERE a.id = ANY(string_to_array('$ACCOUNT_IDS', ',')::bigint[])
    AND a.deleted_at IS NULL
  ORDER BY a.id
) t;
SQL

if [[ "$CONFIRM" != "drop-fable-5-1-mapping" ]]; then
  echo '{"verdict":"dry_run","error":"set CONFIRM=drop-fable-5-1-mapping to apply"}'
  exit 0
fi

echo "=== apply ==="
"${PSQL[@]}" <<SQL
UPDATE accounts AS a
SET
  credentials = jsonb_set(
    a.credentials,
    '{model_mapping}',
    COALESCE(a.credentials->'model_mapping', '{}'::jsonb) - '$DROP_KEY',
    true
  ),
  updated_at = NOW()
WHERE a.id = ANY(string_to_array('$ACCOUNT_IDS', ',')::bigint[])
  AND a.deleted_at IS NULL
  AND COALESCE(a.credentials->'model_mapping', '{}'::jsonb) ? '$DROP_KEY';
SQL

echo "=== after ==="
"${PSQL[@]}" <<SQL
SELECT row_to_json(t) FROM (
  SELECT a.id, a.name, a.platform, a.status, a.schedulable,
         COALESCE((
           SELECT jsonb_object_agg(k, a.credentials->'model_mapping'->k)
           FROM jsonb_object_keys(COALESCE(a.credentials->'model_mapping','{}'::jsonb)) k
           WHERE k ILIKE '%fable%'
         ), '{}'::jsonb) AS fable_mapping,
         (COALESCE(a.credentials->'model_mapping','{}'::jsonb) ? '$DROP_KEY') AS still_has_drop_key
  FROM accounts a
  WHERE a.id = ANY(string_to_array('$ACCOUNT_IDS', ',')::bigint[])
    AND a.deleted_at IS NULL
  ORDER BY a.id
) t;
SQL

echo "=== remaining holders of $DROP_KEY ==="
"${PSQL[@]}" <<SQL
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name, 'schedulable', a.schedulable, 'status', a.status
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.deleted_at IS NULL
  AND a.credentials ? 'model_mapping'
  AND jsonb_typeof(a.credentials->'model_mapping') = 'object'
  AND (
    a.credentials->'model_mapping' ? '$DROP_KEY'
    OR EXISTS (
      SELECT 1 FROM jsonb_each_text(a.credentials->'model_mapping') e
      WHERE e.value = '$DROP_KEY'
    )
  );
SQL
