#!/usr/bin/env bash
# Soft-delete legacy __tk_probe_* rows that are superseded by canonical reusable scopes.
# Canonical rows match probe_reserved_resources.sh tk_probe_canonical_scope() for the
# current catalog refresh, endpoint route-gate matrix, and media parity probes.
#
# Rows are soft-deleted (deleted_at set); probe scripts recreate canonical rows on demand.
set -euo pipefail

APPLY="${TK_PROBE_PRUNE_APPLY:-0}"

usage() {
	cat <<'USAGE'
Usage:
  bash ops/observability/prune-probe-resources.sh [--apply]

Default is dry-run. --apply soft-deletes non-canonical __tk_probe_* api_keys/groups.

Canonical keep set (reused by live probe scripts):
  anthropic_srcgrp_1_cc, anthropic_srcgrp_1_kiro, kiro_srcgrp_1_kiro,
  openai_srcgrp_2, gemini_srcgrp_16, newapi_srcgrp_16, newapi_srcgrp_18,
  newapi_srcgrp_5, antigravity_srcgrp_21, grok_srcgrp_25
USAGE
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--apply)
		APPLY=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "prune-probe-resources: unknown arg: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

PSQL_ARRAY=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

tk_psql() {
	"${PSQL_ARRAY[@]}" "$@"
}

# Scopes kept by tk_probe_canonical_scope for current ops scripts.
CANONICAL_SCOPES=(
	anthropic_srcgrp_1_cc
	anthropic_srcgrp_1_kiro
	kiro_srcgrp_1_kiro
	openai_srcgrp_2
	gemini_srcgrp_16
	newapi_srcgrp_16
	newapi_srcgrp_18
	newapi_srcgrp_5
	antigravity_srcgrp_21
	grok_srcgrp_25
)

keep_sql_values() {
	local scope first=1
	printf '('
	for scope in "${CANONICAL_SCOPES[@]}"; do
		if [ "$first" = 1 ]; then
			first=0
		else
			printf ', '
		fi
		printf "'__tk_probe_%s_group', '__tk_probe_%s_key'" "$scope" "$scope"
	done
	printf ')'
}

KEEP_NAMES="$(keep_sql_values)"

report() {
	local label="$1"
	printf 'snapshot=%s\n' "$label"
	tk_psql <<SQL
SELECT 'probe_group_rows=' || COUNT(*) FROM groups
WHERE name LIKE '\_\_tk\_probe\_%' ESCAPE '\' AND deleted_at IS NULL;
SELECT 'probe_key_rows=' || COUNT(*) FROM api_keys
WHERE deleted_at IS NULL AND name LIKE '\_\_tk\_probe\_%' ESCAPE '\';
SELECT 'prune_candidates_groups=' || COUNT(*) FROM groups
WHERE name LIKE '\_\_tk\_probe\_%' ESCAPE '\'
  AND deleted_at IS NULL
  AND name NOT IN ${KEEP_NAMES};
SELECT 'prune_candidates_keys=' || COUNT(*) FROM api_keys
WHERE deleted_at IS NULL
  AND name LIKE '\_\_tk\_probe\_%' ESCAPE '\'
  AND name NOT IN ${KEEP_NAMES};
SELECT 'kept_group_names=' || COALESCE(string_agg(name, ', ' ORDER BY name), '')
FROM groups
WHERE deleted_at IS NULL AND name IN ${KEEP_NAMES};
SQL
}

apply_prune() {
	tk_psql <<SQL
BEGIN;
WITH doomed_groups AS (
  SELECT id, name FROM groups
  WHERE name LIKE '\_\_tk\_probe\_%' ESCAPE '\'
    AND deleted_at IS NULL
    AND name NOT IN ${KEEP_NAMES}
),
deleted_bindings AS (
  DELETE FROM account_groups
  WHERE group_id IN (SELECT id FROM doomed_groups)
  RETURNING 1
),
deleted_keys AS (
  UPDATE api_keys
  SET deleted_at = NOW(), status = 'disabled', updated_at = NOW()
  WHERE deleted_at IS NULL
    AND (
      name IN (SELECT replace(name, '_group', '_key') FROM doomed_groups)
      OR group_id IN (SELECT id FROM doomed_groups)
    )
    AND name NOT IN ${KEEP_NAMES}
  RETURNING 1
),
deleted_groups AS (
  UPDATE groups
  SET deleted_at = NOW(), status = 'disabled', updated_at = NOW()
  WHERE id IN (SELECT id FROM doomed_groups)
  RETURNING 1
)
SELECT 'deleted_account_group_bindings=' || COUNT(*) FROM deleted_bindings
UNION ALL
SELECT 'soft_deleted_probe_keys=' || COUNT(*) FROM deleted_keys
UNION ALL
SELECT 'soft_deleted_probe_groups=' || COUNT(*) FROM deleted_groups;
COMMIT;
SQL
}

printf 'mode=%s\n' "$([ "$APPLY" = 1 ] && echo apply || echo dry_run)"
report current
if [ "$APPLY" = 1 ]; then
	apply_prune
	report after
fi
