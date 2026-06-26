#!/usr/bin/env bash
# probe_reserved_resources.sh — shared __tk_probe_<scope>_group / __tk_probe_<scope>_key
# setup for catalog refresh (probe-servable-models.sh) and account-model probes.
#
# Reserved resources are per target host (prod DB or edge DB). Keys use direct routing;
# groups are exclusive and excluded from universal routing (__tk_probe_* prefix).
#
# Shell API (source this file; set PSQL or PSQL_ARRAY before calling):
#   tk_probe_scope_from_platform PLATFORM   -> prints scope slug
#   tk_probe_group_name SCOPE               -> prints group name
#   tk_probe_key_name SCOPE                 -> prints key name
#   tk_probe_ensure_group SCOPE PLATFORM    -> sets TK_PROBE_GROUP_ID
#   tk_probe_ensure_key SCOPE               -> sets TK_PROBE_KEY (requires GROUP_ID)
#   tk_probe_bind_account_ids SCOPE IDS     -> comma/space-separated account ids
#   tk_probe_bind_from_group SCOPE GNAME    -> copy schedulable accounts from source group
#   tk_probe_clear_bindings SCOPE           -> remove account_groups rows only
#   tk_probe_prepare_catalog SCOPE PLATFORM BIND_KIND BIND_VAL -> ensure + bind + key
#   tk_probe_reuse_lock_path SCOPE              -> lock file path (shared with account-model-probe)
#   tk_probe_acquire_reuse_lock SCOPE           -> flock until release (serializes account_groups mutations)
#   tk_probe_release_reuse_locks                -> release all locks held by this shell
# Do not enable errexit here — callers (probe-servable-models.sh) handle partial failures.
set -uo pipefail

TK_PROBE_USER_ID="${TK_PROBE_USER_ID:-1}"
TK_PROBE_LOCK_TIMEOUT_SECONDS="${TK_PROBE_LOCK_TIMEOUT_SECONDS:-${PROBE_LOCK_TIMEOUT_SECONDS:-120}}"
TK_PROBE_GROUP_ID=""
TK_PROBE_KEY=""
TK_PROBE_KEY_ID=""
TK_PROBE_LOCK_SCOPES=()
TK_PROBE_LOCK_FDS=()

tk_probe_sql_escape() {
	printf "%s" "$1" | sed "s/'/''/g"
}

tk_probe_psql() {
	if declare -p PSQL_ARRAY >/dev/null 2>&1; then
		"${PSQL_ARRAY[@]}" "$@"
	elif [ -n "${PSQL:-}" ]; then
		# shellcheck disable=SC2086
		$PSQL "$@"
	else
		echo "probe_reserved_resources: PSQL or PSQL_ARRAY not set" >&2
		return 1
	fi
}

# psql may append "UPDATE N" notices; keep only the first result line.
tk_probe_sql_scalar() {
	tk_probe_psql -c "$1" | head -n1 | tr -d '[:space:]'
}

tk_probe_scope_from_platform() {
	python3 - "$1" <<'PY'
import re
import sys

scope = re.sub(r"[^a-z0-9]+", "_", sys.argv[1].strip().lower()).strip("_")
print((scope or "platform")[:48])
PY
}

tk_probe_group_name() {
	printf '__tk_probe_%s_group' "$1"
}

tk_probe_key_name() {
	printf '__tk_probe_%s_key' "$1"
}

# Same lock path as tokenkey-account-model-probe (PROBE_REUSE_MODE=1) so catalog
# refresh and single-account probes never DELETE/rebind the same __tk_probe_* group concurrently.
tk_probe_reuse_lock_path() {
	printf '/tmp/tokenkey-account-model-probe-%s.lock' "$1"
}

tk_probe_acquire_reuse_lock() { # $1=scope
	local scope="$1" path fd i
	for i in "${!TK_PROBE_LOCK_SCOPES[@]}"; do
		if [ "${TK_PROBE_LOCK_SCOPES[$i]}" = "$scope" ]; then
			return 0
		fi
	done
	if ! command -v flock >/dev/null 2>&1; then
		echo "probe_reserved_resources: flock is required for __tk_probe_* reuse (scope=$scope)" >&2
		return 1
	fi
	if [[ ! "$TK_PROBE_LOCK_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$TK_PROBE_LOCK_TIMEOUT_SECONDS" -lt 1 ]]; then
		echo "probe_reserved_resources: TK_PROBE_LOCK_TIMEOUT_SECONDS must be a positive integer" >&2
		return 1
	fi
	path="$(tk_probe_reuse_lock_path "$scope")"
	exec {fd}>"$path"
	if ! flock -w "$TK_PROBE_LOCK_TIMEOUT_SECONDS" "$fd"; then
		echo "probe_reserved_resources: timed out waiting for probe reuse lock scope=$scope" >&2
		exec {fd}>&-
		return 1
	fi
	TK_PROBE_LOCK_SCOPES+=("$scope")
	TK_PROBE_LOCK_FDS+=("$fd")
}

tk_probe_release_reuse_locks() {
	local i fd
	for i in "${!TK_PROBE_LOCK_SCOPES[@]}"; do
		fd="${TK_PROBE_LOCK_FDS[$i]}"
		flock -u "$fd" 2>/dev/null || true # preflight-allow: swallow
		exec {fd}>&- 2>/dev/null || true # preflight-allow: swallow
	done
	TK_PROBE_LOCK_SCOPES=()
	TK_PROBE_LOCK_FDS=()
}

tk_probe_ensure_group() { # $1=scope $2=platform
	local scope="$1" platform="$2"
	local group_name
	group_name="$(tk_probe_group_name "$scope")"
	# Idempotent upsert keyed on the partial unique index groups_name_unique_active
	# (UNIQUE (name) WHERE deleted_at IS NULL). A prior CTE form (INSERT in one CTE,
	# then a separate UPDATE ... FROM picked) returned EMPTY on first-ever creation:
	# the final UPDATE scans `groups` at the statement-start snapshot, which does NOT
	# include the row the same statement's CTE just inserted, so RETURNING matched
	# 0 rows and the scope read as config_error (only hosts where the probe group
	# already existed from an earlier run worked). ON CONFLICT DO UPDATE RETURNING id
	# returns the id in BOTH the insert and the conflict-update path.
	TK_PROBE_GROUP_ID="$(tk_probe_sql_scalar "
INSERT INTO groups (
  name, description, platform, rate_multiplier, is_exclusive, status,
  subscription_type, default_validity_days, claude_code_only,
  model_routing_enabled, model_routing, sort_order, rpm_limit, created_at, updated_at
) VALUES (
  '$(tk_probe_sql_escape "$group_name")',
  'reserved reusable catalog/account probe group; direct probe key only; excluded from universal routing',
  '$(tk_probe_sql_escape "$platform")',
  1.0, true, 'active',
  'standard', 30, false,
  false, '{}'::jsonb, 2147483000, 0, NOW(), NOW()
)
ON CONFLICT (name) WHERE (deleted_at IS NULL)
DO UPDATE SET
  description = EXCLUDED.description,
  platform = EXCLUDED.platform,
  rate_multiplier = 1.0,
  is_exclusive = true,
  status = 'active',
  subscription_type = 'standard',
  default_validity_days = 30,
  claude_code_only = false,
  model_routing_enabled = false,
  model_routing = '{}'::jsonb,
  sort_order = 2147483000,
  rpm_limit = 0,
  updated_at = NOW()
RETURNING id;
")"
	if [[ ! "$TK_PROBE_GROUP_ID" =~ ^[0-9]+$ ]]; then
		echo "probe_reserved_resources: failed to ensure group for scope=$scope" >&2
		return 1
	fi
	tk_probe_psql -c "
INSERT INTO user_allowed_groups (user_id, group_id, created_at)
VALUES (${TK_PROBE_USER_ID}, ${TK_PROBE_GROUP_ID}, NOW())
ON CONFLICT (user_id, group_id) DO NOTHING;
" >/dev/null
}

tk_probe_ensure_key() { # $1=scope
	local scope="$1" key_name new_key
	key_name="$(tk_probe_key_name "$scope")"
	if [[ ! "$TK_PROBE_GROUP_ID" =~ ^[0-9]+$ ]]; then
		echo "probe_reserved_resources: ensure_key requires TK_PROBE_GROUP_ID" >&2
		return 1
	fi
	local existing_id
	existing_id="$(tk_probe_sql_scalar "
SELECT COALESCE((
  SELECT id::text FROM api_keys
  WHERE group_id = ${TK_PROBE_GROUP_ID}
    AND name = '$(tk_probe_sql_escape "$key_name")'
    AND deleted_at IS NULL
  ORDER BY id
  LIMIT 1
), '');
")"
	if [ -n "$existing_id" ]; then
		TK_PROBE_KEY_ID="$existing_id"
		TK_PROBE_KEY="$(tk_probe_sql_scalar "
UPDATE api_keys
SET
  user_id = ${TK_PROBE_USER_ID},
  status = 'active',
  routing_mode = 'direct',
  quota = 0,
  quota_used = 0,
  rate_limit_5h = 0,
  rate_limit_1d = 0,
  rate_limit_7d = 0,
  usage_5h = 0,
  usage_1d = 0,
  usage_7d = 0,
  updated_at = NOW()
WHERE id = ${TK_PROBE_KEY_ID}
  AND group_id = ${TK_PROBE_GROUP_ID}
  AND name = '$(tk_probe_sql_escape "$key_name")'
  AND deleted_at IS NULL
RETURNING key;
")"
	else
		new_key="sk-tkprobe-$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(18).replace("-", "").replace("_", "")[:24])
PY
)"
		TK_PROBE_KEY_ID="$(tk_probe_sql_scalar "
INSERT INTO api_keys (
  user_id, key, name, group_id, status, routing_mode,
  quota, quota_used, rate_limit_5h, rate_limit_1d, rate_limit_7d,
  usage_5h, usage_1d, usage_7d, created_at, updated_at
) VALUES (
  ${TK_PROBE_USER_ID},
  '$(tk_probe_sql_escape "$new_key")',
  '$(tk_probe_sql_escape "$key_name")',
  ${TK_PROBE_GROUP_ID},
  'active',
  'direct',
  0, 0, 0, 0, 0,
  0, 0, 0, NOW(), NOW()
) RETURNING id;
")"
		TK_PROBE_KEY="$new_key"
	fi
	if [[ ! "$TK_PROBE_KEY_ID" =~ ^[0-9]+$ ]] || [ -z "$TK_PROBE_KEY" ]; then
		echo "probe_reserved_resources: failed to ensure key for scope=$scope" >&2
		return 1
	fi
}

tk_probe_clear_bindings() { # $1=scope
	local scope="$1" group_name group_id
	group_name="$(tk_probe_group_name "$scope")"
	group_id="$(tk_probe_sql_scalar "
SELECT COALESCE((
  SELECT id::text FROM groups
  WHERE name = '$(tk_probe_sql_escape "$group_name")' AND deleted_at IS NULL
  ORDER BY id LIMIT 1
), '');
")"
	if [[ "$group_id" =~ ^[0-9]+$ ]]; then
		tk_probe_psql -c "DELETE FROM account_groups WHERE group_id = ${group_id};" >/dev/null
	fi
}

tk_probe_bind_account_ids() { # $1=scope $2=ids (comma/space)
	local scope="$1" ids_raw="$2" id
	if [[ ! "$TK_PROBE_GROUP_ID" =~ ^[0-9]+$ ]]; then
		echo "probe_reserved_resources: bind_account_ids requires TK_PROBE_GROUP_ID" >&2
		return 1
	fi
	tk_probe_psql -c "DELETE FROM account_groups WHERE group_id = ${TK_PROBE_GROUP_ID};" >/dev/null
	ids_raw="$(printf '%s' "$ids_raw" | tr ',' ' ')"
	for id in $ids_raw; do
		[ -z "$id" ] && continue
		if [[ ! "$id" =~ ^[0-9]+$ ]]; then
			echo "probe_reserved_resources: invalid account id '$id'" >&2
			return 1
		fi
		tk_probe_psql -c "
INSERT INTO account_groups (account_id, group_id, priority, created_at)
SELECT ${id}, ${TK_PROBE_GROUP_ID}, 1, NOW()
WHERE EXISTS (
  SELECT 1 FROM accounts
  WHERE id = ${id} AND deleted_at IS NULL
)
ON CONFLICT (account_id, group_id) DO NOTHING;
" >/dev/null
	done
	local bound
	bound="$(tk_probe_sql_scalar "
SELECT COUNT(*)::text FROM account_groups WHERE group_id = ${TK_PROBE_GROUP_ID};
")"
	if [ "${bound:-0}" = "0" ]; then
		echo "probe_reserved_resources: no accounts bound for scope=$scope (ids=$ids_raw)" >&2
		return 1
	fi
}

tk_probe_bind_from_group() { # $1=scope $2=source_group_name
	local scope="$1" source_group="$2"
	if [[ ! "$TK_PROBE_GROUP_ID" =~ ^[0-9]+$ ]]; then
		echo "probe_reserved_resources: bind_from_group requires TK_PROBE_GROUP_ID" >&2
		return 1
	fi
	tk_probe_psql -c "DELETE FROM account_groups WHERE group_id = ${TK_PROBE_GROUP_ID};" >/dev/null
	tk_probe_psql -c "
INSERT INTO account_groups (account_id, group_id, priority, created_at)
SELECT ag.account_id, ${TK_PROBE_GROUP_ID}, COALESCE(ag.priority, 1), NOW()
FROM account_groups ag
JOIN groups sg ON sg.id = ag.group_id
JOIN accounts a ON a.id = ag.account_id
WHERE sg.name = '$(tk_probe_sql_escape "$source_group")'
  AND sg.deleted_at IS NULL
  AND a.deleted_at IS NULL
  AND a.schedulable = true
ON CONFLICT (account_id, group_id) DO NOTHING;
" >/dev/null
	local bound
	bound="$(tk_probe_sql_scalar "
SELECT COUNT(*)::text FROM account_groups WHERE group_id = ${TK_PROBE_GROUP_ID};
")"
	if [ "${bound:-0}" = "0" ]; then
		echo "probe_reserved_resources: no schedulable accounts copied from group '$source_group' for scope=$scope" >&2
		return 1
	fi
}

# Like tk_probe_bind_from_group but only copies accounts whose NAME matches a SQL LIKE
# pattern. Used to split one source group into named sub-pools (e.g. the prod `claude`
# group holds both `cc-*` anthropic-OAuth mirrors and `kiro-*` Kiro mirrors; the prod
# relay-health probe needs each sub-pool on its own probe key).
tk_probe_bind_from_group_like() { # $1=scope $2=source_group_name $3=name_like_pattern
	local scope="$1" source_group="$2" name_like="$3"
	if [[ ! "$TK_PROBE_GROUP_ID" =~ ^[0-9]+$ ]]; then
		echo "probe_reserved_resources: bind_from_group_like requires TK_PROBE_GROUP_ID" >&2
		return 1
	fi
	tk_probe_psql -c "DELETE FROM account_groups WHERE group_id = ${TK_PROBE_GROUP_ID};" >/dev/null
	tk_probe_psql -c "
INSERT INTO account_groups (account_id, group_id, priority, created_at)
SELECT ag.account_id, ${TK_PROBE_GROUP_ID}, COALESCE(ag.priority, 1), NOW()
FROM account_groups ag
JOIN groups sg ON sg.id = ag.group_id
JOIN accounts a ON a.id = ag.account_id
WHERE sg.name = '$(tk_probe_sql_escape "$source_group")'
  AND sg.deleted_at IS NULL
  AND a.deleted_at IS NULL
  AND a.schedulable = true
  AND a.name LIKE '$(tk_probe_sql_escape "$name_like")'
ON CONFLICT (account_id, group_id) DO NOTHING;
" >/dev/null
	local bound
	bound="$(tk_probe_sql_scalar "
SELECT COUNT(*)::text FROM account_groups WHERE group_id = ${TK_PROBE_GROUP_ID};
")"
	if [ "${bound:-0}" = "0" ]; then
		echo "probe_reserved_resources: no schedulable accounts copied from group '$source_group' name LIKE '$name_like' for scope=$scope" >&2
		return 1
	fi
}

# Ensure group+key and bind accounts. BIND_KIND: account_ids | source_group | group_like
# (group_like bind_val = "GROUP|PATTERN", split on the first '|').
tk_probe_prepare_catalog() { # $1=scope $2=platform $3=bind_kind $4=bind_val
	local scope="$1" platform="$2" bind_kind="$3" bind_val="$4"
	TK_PROBE_GROUP_ID=""
	TK_PROBE_KEY=""
	TK_PROBE_KEY_ID=""
	if ! tk_probe_acquire_reuse_lock "$scope"; then
		return 1
	fi
	# Stop at the first failure (errexit is intentionally off, see header) so a
	# failed group/key ensure surfaces ONE clean error instead of cascading into
	# the downstream re-validation guards (3 stderr lines for the same root cause).
	tk_probe_ensure_group "$scope" "$platform" || return 1
	tk_probe_ensure_key "$scope" || return 1
	case "$bind_kind" in
	account_ids) tk_probe_bind_account_ids "$scope" "$bind_val" ;;
	source_group) tk_probe_bind_from_group "$scope" "$bind_val" ;;
	group_like) tk_probe_bind_from_group_like "$scope" "${bind_val%%|*}" "${bind_val#*|}" ;;
	*)
		echo "probe_reserved_resources: unknown bind_kind '$bind_kind'" >&2
		return 1
		;;
	esac
}
