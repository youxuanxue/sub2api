#!/usr/bin/env bash
# Write remediation: drop claude-fable-5-1 from prod accounts 91 and 150.
# One-shot audit/reproducibility helper for the 2026-09-18 empty-pool menu止血.
# Opt-in only: CONFIRM=drop-fable-5-1-mapping
# Targets and drop key are hardcoded literals in SQL — do not reintroduce env
# interpolation into the query text.
#
# Restore owner after Cursor Fable data-policy acknowledgement + discovery live
# gate (#2216): do NOT reverse this SQL by hand. Converge live mappings with
# manage-account-model-mapping-runtime.py apply-accounts against the Go bundle
# (Cursor override + bedrock floor already require claude-fable-5-1).
set -euo pipefail

CONFIRM="${CONFIRM:-}"

PSQL=(docker exec -i
  -e 'PGOPTIONS=-c lock_timeout=500ms -c statement_timeout=15s'
  tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

echo "=== policy ==="
echo "confirm=$CONFIRM account_ids=91,150 drop_key=claude-fable-5-1"

echo "=== before ==="
"${PSQL[@]}" <<'SQL'
SELECT row_to_json(t) FROM (
  SELECT a.id, a.name, a.platform, a.status, a.schedulable,
         COALESCE((
           SELECT jsonb_object_agg(k, a.credentials->'model_mapping'->k)
           FROM jsonb_object_keys(COALESCE(a.credentials->'model_mapping','{}'::jsonb)) k
           WHERE k ILIKE '%fable%'
         ), '{}'::jsonb) AS fable_mapping
  FROM accounts a
  WHERE a.id = ANY(ARRAY[91,150]::bigint[])
    AND a.deleted_at IS NULL
  ORDER BY a.id
) t;
SQL

if [[ "$CONFIRM" != "drop-fable-5-1-mapping" ]]; then
  echo '{"verdict":"dry_run","error":"set CONFIRM=drop-fable-5-1-mapping to apply"}'
  exit 0
fi

echo "=== apply ==="
"${PSQL[@]}" <<'SQL'
UPDATE accounts AS a
SET
  credentials = jsonb_set(
    a.credentials,
    '{model_mapping}',
    COALESCE(a.credentials->'model_mapping', '{}'::jsonb) - 'claude-fable-5-1',
    true
  ),
  updated_at = NOW()
WHERE a.id = ANY(ARRAY[91,150]::bigint[])
  AND a.deleted_at IS NULL
  AND COALESCE(a.credentials->'model_mapping', '{}'::jsonb) ? 'claude-fable-5-1';
SQL

echo "=== after ==="
"${PSQL[@]}" <<'SQL'
SELECT row_to_json(t) FROM (
  SELECT a.id, a.name, a.platform, a.status, a.schedulable,
         COALESCE((
           SELECT jsonb_object_agg(k, a.credentials->'model_mapping'->k)
           FROM jsonb_object_keys(COALESCE(a.credentials->'model_mapping','{}'::jsonb)) k
           WHERE k ILIKE '%fable%'
         ), '{}'::jsonb) AS fable_mapping,
         (COALESCE(a.credentials->'model_mapping','{}'::jsonb) ? 'claude-fable-5-1') AS still_has_drop_key
  FROM accounts a
  WHERE a.id = ANY(ARRAY[91,150]::bigint[])
    AND a.deleted_at IS NULL
  ORDER BY a.id
) t;
SQL

echo "=== remaining holders of claude-fable-5-1 ==="
"${PSQL[@]}" <<'SQL'
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name, 'schedulable', a.schedulable, 'status', a.status
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.deleted_at IS NULL
  AND a.credentials ? 'model_mapping'
  AND jsonb_typeof(a.credentials->'model_mapping') = 'object'
  AND (
    a.credentials->'model_mapping' ? 'claude-fable-5-1'
    OR EXISTS (
      SELECT 1 FROM jsonb_each_text(a.credentials->'model_mapping') e
      WHERE e.value = 'claude-fable-5-1'
    )
  );
SQL
