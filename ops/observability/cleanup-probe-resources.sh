#!/usr/bin/env bash
# Disable reserved __tk_probe_* resources after diagnostics so Studio key/group
# pickers are not dominated by probe-only fixtures. The rows are intentionally
# retained: probe_reserved_resources.sh reactivates the canonical rows on demand.
# For legacy scope clutter, see ops/observability/prune-probe-resources.sh.
set -euo pipefail

APPLY="${TK_PROBE_CLEANUP_APPLY:-0}"
LIMIT="${TK_PROBE_CLEANUP_LIMIT:-40}"
PSQL_ARRAY=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

usage() {
	cat <<'USAGE'
Usage:
  bash ops/observability/cleanup-probe-resources.sh [--apply] [--limit N]

Default is dry-run. --apply deletes account_groups bindings for __tk_probe_*
groups and disables matching probe groups/API keys; it never deletes rows.
USAGE
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--apply)
		APPLY=1
		shift
		;;
	--limit)
		LIMIT="${2:-}"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "cleanup-probe-resources: unknown arg: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

if ! [[ "$LIMIT" =~ ^[0-9]+$ ]] || [ "$LIMIT" -lt 1 ]; then
	echo "cleanup-probe-resources: --limit must be a positive integer" >&2
	exit 1
fi
if [[ "$APPLY" != "0" && "$APPLY" != "1" ]]; then
	echo "cleanup-probe-resources: TK_PROBE_CLEANUP_APPLY must be 0 or 1" >&2
	exit 1
fi

tk_psql() {
	"${PSQL_ARRAY[@]}" "$@"
}

print_snapshot() {
	local label="$1"
	printf 'snapshot=%s\n' "$label"
	tk_psql <<SQL
WITH probe_groups AS (
  SELECT id, name, status
  FROM groups
  WHERE name LIKE '\_\_tk\_probe\_%' ESCAPE '\'
    AND deleted_at IS NULL
),
probe_keys AS (
  SELECT k.id, k.name, k.status
  FROM api_keys k
  WHERE k.deleted_at IS NULL
    AND (
      k.name LIKE '\_\_tk\_probe\_%' ESCAPE '\'
      OR k.group_id IN (SELECT id FROM probe_groups)
    )
),
probe_bindings AS (
  SELECT ag.account_id, ag.group_id
  FROM account_groups ag
  WHERE ag.group_id IN (SELECT id FROM probe_groups)
)
SELECT 'active_probe_groups=' || COUNT(*) FROM probe_groups WHERE status = 'active'
UNION ALL
SELECT 'active_probe_keys=' || COUNT(*) FROM probe_keys WHERE status = 'active'
UNION ALL
SELECT 'probe_account_group_bindings=' || COUNT(*) FROM probe_bindings
UNION ALL
SELECT 'probe_group_rows=' || COUNT(*) FROM probe_groups
UNION ALL
SELECT 'probe_key_rows=' || COUNT(*) FROM probe_keys
UNION ALL
SELECT 'active_group_sample=' || COALESCE(string_agg(name, ', ' ORDER BY name), '')
FROM (SELECT name FROM probe_groups WHERE status = 'active' ORDER BY name LIMIT ${LIMIT}) s
UNION ALL
SELECT 'active_key_sample=' || COALESCE(string_agg(name, ', ' ORDER BY name), '')
FROM (SELECT name FROM probe_keys WHERE status = 'active' ORDER BY name LIMIT ${LIMIT}) s;
SQL
}

apply_cleanup() {
	tk_psql <<'SQL'
BEGIN;
WITH probe_groups AS (
  SELECT id
  FROM groups
  WHERE name LIKE '\_\_tk\_probe\_%' ESCAPE '\'
    AND deleted_at IS NULL
),
deleted_bindings AS (
  DELETE FROM account_groups
  WHERE group_id IN (SELECT id FROM probe_groups)
  RETURNING 1
),
disabled_keys AS (
  UPDATE api_keys
  SET status = 'disabled',
      updated_at = NOW()
  WHERE deleted_at IS NULL
    AND status <> 'disabled'
    AND (
      name LIKE '\_\_tk\_probe\_%' ESCAPE '\'
      OR group_id IN (SELECT id FROM probe_groups)
    )
  RETURNING 1
),
disabled_groups AS (
  UPDATE groups
  SET status = 'disabled',
      updated_at = NOW()
  WHERE deleted_at IS NULL
    AND status <> 'disabled'
    AND name LIKE '\_\_tk\_probe\_%' ESCAPE '\'
  RETURNING 1
)
SELECT 'deleted_account_group_bindings=' || COUNT(*) FROM deleted_bindings
UNION ALL
SELECT 'disabled_probe_keys=' || COUNT(*) FROM disabled_keys
UNION ALL
SELECT 'disabled_probe_groups=' || COUNT(*) FROM disabled_groups;
COMMIT;
SQL
}

if [ "$APPLY" = "1" ]; then
	printf 'mode=apply\n'
	print_snapshot before
	apply_cleanup
	print_snapshot after
else
	printf 'mode=dry_run\n'
	print_snapshot current
fi
