#!/usr/bin/env bash
# Write remediation: neutralize groups.model_routing JSON arrays.
#
# Ent expects map[string][]int64 (JSON object). An array (including []) makes
# ListActiveGroups fail fleet-wide and turns universal-key routing into 500
# ("Failed to prepare authorized candidates").
#
# Delivered via: bash ops/observability/run-probe.sh --target prod \
#   --script ops/observability/remediate-model-routing-array.sh
# Requires APPLY=1 to mutate. Soft-deletes ephemeral sticky probe groups.
set -euo pipefail

APPLY="${APPLY:-0}"
PSQL=(docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

echo "=== before: array-typed model_routing (any status) ==="
"${PSQL[@]}" -c "
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', id,
  'name', name,
  'status', status,
  'deleted', deleted_at IS NOT NULL,
  'mr_type', jsonb_typeof(model_routing),
  'mr', left(model_routing::text, 80)
) ORDER BY id), '[]'::jsonb)::text
FROM groups -- ops-allow-soft-deleted: inventory must include soft-deleted probe leftovers
WHERE model_routing IS NOT NULL
  AND jsonb_typeof(model_routing) = 'array';
"

if [ "$APPLY" != "1" ]; then
  echo '{"verdict":"dry_run"}'
  exit 0
fi

echo "=== apply: coerce array→object; soft-delete sticky probe leftovers ==="
"${PSQL[@]}" <<'SQL'
BEGIN;

UPDATE groups
SET model_routing = '{}'::jsonb,
    updated_at = NOW()
WHERE model_routing IS NOT NULL
  AND jsonb_typeof(model_routing) = 'array'; -- ops-allow-soft-deleted: coerce shape even on soft-deleted rows

WITH doomed AS (
  SELECT id FROM groups
  WHERE deleted_at IS NULL
    AND (
      name LIKE '\_\_tk\_probe\_sticky%' ESCAPE '\'
      OR (name LIKE '\_\_tk\_probe\_%' ESCAPE '\' AND name LIKE '%sticky237%')
    )
)
DELETE FROM account_groups WHERE group_id IN (SELECT id FROM doomed);

WITH doomed AS (
  SELECT id FROM groups
  WHERE deleted_at IS NULL
    AND (
      name LIKE '\_\_tk\_probe\_sticky%' ESCAPE '\'
      OR (name LIKE '\_\_tk\_probe\_%' ESCAPE '\' AND name LIKE '%sticky237%')
    )
),
keys AS (
  UPDATE api_keys
  SET deleted_at = NOW(), status = 'disabled', updated_at = NOW()
  WHERE deleted_at IS NULL
    AND (
      group_id IN (SELECT id FROM doomed)
      OR name LIKE '\_\_tk\_probe\_sticky%' ESCAPE '\'
      OR name LIKE '%sticky237%'
    )
  RETURNING id
)
UPDATE groups g
SET deleted_at = NOW(), status = 'disabled', updated_at = NOW()
FROM doomed d
WHERE g.id = d.id
RETURNING g.id, g.name;

COMMIT;
SQL

echo
echo "=== after: array-typed model_routing remaining ==="
"${PSQL[@]}" -c "
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', id, 'name', name, 'mr_type', jsonb_typeof(model_routing)
) ORDER BY id), '[]'::jsonb)::text
FROM groups -- ops-allow-soft-deleted: confirm no array-shaped model_routing remains anywhere
WHERE model_routing IS NOT NULL
  AND jsonb_typeof(model_routing) = 'array';
"

echo '{"verdict":"applied"}'
