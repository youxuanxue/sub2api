#!/usr/bin/env bash
# probe-media-parity-postrelease.sh - focused paid direct-vs-universal media parity probe.
#
# Runs on prod via ops/observability/run-probe.sh. It reuses the canonical
# source-group probe keys (__tk_probe_<platform>_srcgrp_<gid>_key) for direct
# requests, then compares them with one existing universal key that is entitled
# to both the Vertex and Grok prod groups. Keys are never printed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPANION="$SCRIPT_DIR/probe_reserved_resources.sh"
if [ ! -f "$COMPANION" ]; then
	COMPANION="$SCRIPT_DIR/../pricing/probe_reserved_resources.sh"
fi
if [ ! -f "$COMPANION" ]; then
	echo "probe-media-parity: missing probe_reserved_resources.sh companion" >&2
	exit 2
fi
# shellcheck source=../pricing/probe_reserved_resources.sh
. "$COMPANION"

PSQL_ARRAY=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
PROD="${PROD_BASE:-https://api.tokenkey.dev}"
RUN_ID="${RUN_ID:-media-parity-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
REQ_SLEEP="${REQ_SLEEP:-2}"

VEO_GROUP_ID="${VEO_GROUP_ID:-16}"
GROK_GROUP_ID="${GROK_GROUP_ID:-25}"
VEO_MODEL="${VEO_MODEL:-veo-3.1-generate-001}"
GROK_IMAGE_MODELS="${GROK_IMAGE_MODELS:-grok-imagine-image grok-imagine-image-quality}"
GROK_VIDEO_MODEL="${GROK_VIDEO_MODEL:-grok-imagine-video}"
GROK_VIDEO_PATHS="${GROK_VIDEO_PATHS:-/v1/video/generations /v1/videos/generations}"

TK_PROBE_PARITY_SCOPES=""

psql_scalar() {
	"${PSQL_ARRAY[@]}" -c "$1" | head -n1 | tr -d '\r'
}

psql_rows() {
	"${PSQL_ARRAY[@]}" -c "$1" 2>&1
}

cleanup() {
	local scope
	for scope in $TK_PROBE_PARITY_SCOPES; do
		[ -n "$scope" ] || continue
		tk_probe_clear_bindings "$scope" >/dev/null 2>&1 || true # preflight-allow: probe cleanup best-effort
	done
	tk_probe_release_reuse_locks
}
trap cleanup EXIT

copy_source_group_policy() { # $1=source_group_id
	local source_group_id="$1"
	[[ "$source_group_id" =~ ^[0-9]+$ ]] || return 0
	if [[ ! "$TK_PROBE_GROUP_ID" =~ ^[0-9]+$ ]]; then
		return 0
	fi
	tk_probe_psql -c "
UPDATE groups dst
SET
  allow_messages_dispatch = src.allow_messages_dispatch,
  messages_dispatch_model_config = src.messages_dispatch_model_config,
  allow_image_generation = src.allow_image_generation,
  updated_at = NOW()
FROM groups src
WHERE dst.id = ${TK_PROBE_GROUP_ID}
  AND src.id = ${source_group_id}
  AND src.deleted_at IS NULL;
" >/dev/null 2>&1 || true # preflight-allow: policy copy is diagnostic-only
	tk_probe_psql -c "
UPDATE api_keys
SET updated_at = NOW()
WHERE group_id = ${TK_PROBE_GROUP_ID}
  AND deleted_at IS NULL;
" >/dev/null 2>&1 || true # preflight-allow: policy copy is diagnostic-only
}

prepare_direct_key() { # $1=label $2=platform $3=source_group_id
	local label="$1" platform="$2" source_group_id="$3" requested_scope
	requested_scope="${platform}_srcgrp_${source_group_id}"
	if ! tk_probe_prepare_catalog "$requested_scope" "$platform" source_group_id "$source_group_id"; then
		printf 'PREP\t%s\tplatform=%s\tsource_group_id=%s\tstatus=direct_pool_empty_or_config_error\n' "$label" "$platform" "$source_group_id"
		return 1
	fi
	copy_source_group_policy "$source_group_id"
	TK_PROBE_PARITY_SCOPES="${TK_PROBE_PARITY_SCOPES} ${TK_PROBE_SCOPE:-$requested_scope}"
	printf 'PREP\t%s\tplatform=%s\tsource_group_id=%s\tprobe_scope=%s\tprobe_group_id=%s\tprobe_key_id=%s\tstatus=ready\n' \
		"$label" "$platform" "$source_group_id" "${TK_PROBE_SCOPE:-$requested_scope}" "$TK_PROBE_GROUP_ID" "$TK_PROBE_KEY_ID"
	return 0
}

select_universal_key() {
	local wanted_id="${UNIVERSAL_KEY_ID:-}"
	if [ -n "$wanted_id" ]; then
		if [[ ! "$wanted_id" =~ ^[0-9]+$ ]]; then
			echo "probe-media-parity: UNIVERSAL_KEY_ID must be numeric" >&2
			return 1
		fi
		UNIVERSAL_KEY_ID="$wanted_id"
	else
		UNIVERSAL_KEY_ID="$(psql_scalar "
SELECT COALESCE((
  SELECT k.id::text
  FROM api_keys k
  WHERE k.routing_mode = 'universal'
    AND k.status = 'active'
    AND k.deleted_at IS NULL
    AND EXISTS (
      SELECT 1 FROM user_allowed_groups u
      WHERE u.user_id = k.user_id AND u.group_id = ${VEO_GROUP_ID}
    )
    AND EXISTS (
      SELECT 1 FROM user_allowed_groups u
      WHERE u.user_id = k.user_id AND u.group_id = ${GROK_GROUP_ID}
    )
  ORDER BY CASE WHEN k.id = 5 THEN 0 ELSE 1 END, k.id
  LIMIT 1
), '');
")"
	fi
	if [[ ! "${UNIVERSAL_KEY_ID:-}" =~ ^[0-9]+$ ]]; then
		echo "probe-media-parity: no active universal key can access both source groups ${VEO_GROUP_ID}/${GROK_GROUP_ID}" >&2
		return 1
	fi
	UNIVERSAL_KEY="$(psql_scalar "
SELECT COALESCE((
  SELECT key FROM api_keys
  WHERE id = ${UNIVERSAL_KEY_ID}
    AND routing_mode = 'universal'
    AND status = 'active'
    AND deleted_at IS NULL
  LIMIT 1
), '');
")"
	if [ -z "$UNIVERSAL_KEY" ]; then
		echo "probe-media-parity: selected universal key id=${UNIVERSAL_KEY_ID} is not usable" >&2
		return 1
	fi
	printf 'UNIVERSAL\tkey_id=%s\tstatus=ready\n' "$UNIVERSAL_KEY_ID"
	psql_rows "
SELECT row_to_json(t)
FROM (
  SELECT k.id AS key_id, k.user_id, k.name,
         bool_or(u.group_id = ${VEO_GROUP_ID}) AS has_veo_group,
         bool_or(u.group_id = ${GROK_GROUP_ID}) AS has_grok_group
  FROM api_keys k
  LEFT JOIN user_allowed_groups u ON u.user_id = k.user_id
  WHERE k.id = ${UNIVERSAL_KEY_ID}
    AND k.deleted_at IS NULL
  GROUP BY k.id, k.user_id, k.name
) t;
"
}

body_image() {
	local model="$1"
	printf '{"model":"%s","prompt":"a small red circle on white","n":1,"size":"1024x1024"}' "$model"
}

body_video() {
	local model="$1"
	printf '{"model":"%s","prompt":"a small red ball rolling on a table","seconds":"4"}' "$model"
}

snippet() {
	head -c 260 "$1" | tr '\r\n\t' '   ' | sed 's/[[:space:]]\+/ /g'
}

path_label() {
	local path="${1#/}"
	path="${path//\//_}"
	path="${path//-/_}"
	printf '%s' "$path"
}

shape_for() { # $1=modality $2=http_code $3=bodyfile
	local modality="$1" code="$2" bodyfile="$3"
	if [ "$code" != "200" ]; then
		echo "error"
		return
	fi
	case "$modality" in
	image)
		if grep -q '"data"' "$bodyfile"; then echo "ok"; else echo "mismatch"; fi
		;;
	video)
		if grep -Eq '"(id|task_id)"[[:space:]]*:' "$bodyfile"; then echo "ok"; else echo "mismatch"; fi
		;;
	*)
		echo "unknown"
		;;
	esac
}

query_attribution() { # $1=label $2=key_id $3=model $4=path $5=started_at
	local label="$1" key_id="$2" model="$3" path="$4" started_at="$5"
	local emodel epath estart
	emodel="$(tk_probe_sql_escape "$model")"
	epath="$(tk_probe_sql_escape "$path")"
	estart="$(tk_probe_sql_escape "$started_at")"
	printf 'ATTR_BEGIN\t%s\n' "$label"
	psql_rows "
WITH recent AS (
  SELECT 'usage'::text AS src, id, created_at, request_id, api_key_id, account_id, group_id,
         model, requested_model, inbound_endpoint, upstream_endpoint,
         NULL::text AS error_phase, NULL::text AS error_type,
         NULL::int AS status_code, NULL::int AS upstream_status_code,
         NULL::text AS error_owner, ''::text AS msg
  FROM usage_logs
  WHERE created_at >= '${estart}'::timestamptz - interval '5 seconds'
    AND created_at <= clock_timestamp() + interval '5 seconds'
    AND api_key_id = ${key_id}
    AND (model = '${emodel}' OR requested_model = '${emodel}' OR upstream_model = '${emodel}')
    AND (inbound_endpoint = '${epath}' OR inbound_endpoint ILIKE '%video%' OR inbound_endpoint ILIKE '%image%')
  UNION ALL
  SELECT 'error'::text AS src, id, created_at, COALESCE(NULLIF(request_id,''), NULLIF(client_request_id,''), '') AS request_id,
         api_key_id, account_id, group_id, model, NULL::text AS requested_model,
         inbound_endpoint, request_path AS upstream_endpoint, error_phase, error_type,
         status_code, upstream_status_code, error_owner,
         left(COALESCE(NULLIF(upstream_error_message,''), NULLIF(error_message,''), ''), 220) AS msg
  FROM ops_error_logs
  WHERE created_at >= '${estart}'::timestamptz - interval '5 seconds'
    AND created_at <= clock_timestamp() + interval '5 seconds'
    AND api_key_id = ${key_id}
    AND model = '${emodel}'
    AND (inbound_endpoint = '${epath}' OR request_path = '${epath}' OR inbound_endpoint ILIKE '%video%' OR request_path ILIKE '%video%' OR inbound_endpoint ILIKE '%image%' OR request_path ILIKE '%image%')
)
SELECT row_to_json(t)
FROM (
  SELECT src, id, created_at AT TIME ZONE 'UTC' AS ts_utc, request_id, api_key_id,
         account_id, group_id, model, requested_model, inbound_endpoint, upstream_endpoint,
         error_phase, error_type, status_code, upstream_status_code, error_owner, msg
  FROM recent
  ORDER BY created_at DESC, id DESC
  LIMIT 4
) t;
"
	printf 'ATTR_END\t%s\n' "$label"
}

attr_only() {
	local since="$1" esince key_ids models_sql
	esince="$(tk_probe_sql_escape "$since")"
	if ! select_universal_key >/dev/null; then
		exit 1
	fi
	key_ids="$(psql_scalar "
SELECT COALESCE(string_agg(id::text, ',' ORDER BY id), '')
FROM api_keys
WHERE deleted_at IS NULL
  AND (
    id = ${UNIVERSAL_KEY_ID}
    OR name IN ('__tk_probe_newapi_srcgrp_16_key', '__tk_probe_grok_srcgrp_25_key')
  );
")"
	if [ -z "$key_ids" ]; then
		echo "ATTR_ONLY no key ids found" >&2
		exit 1
	fi
	models_sql="'$(tk_probe_sql_escape "$VEO_MODEL")','$(tk_probe_sql_escape "$GROK_VIDEO_MODEL")'"
	local model
	for model in $GROK_IMAGE_MODELS; do
		models_sql="${models_sql},'$(tk_probe_sql_escape "$model")'"
	done
	printf 'ATTR_ONLY\tsince=%s\tkey_ids=%s\n' "$since" "$key_ids"
	psql_rows "
WITH recent AS (
  SELECT 'usage'::text AS src, id, created_at, request_id, api_key_id, account_id, group_id,
         model, requested_model, inbound_endpoint, upstream_endpoint,
         NULL::text AS error_phase, NULL::text AS error_type,
         NULL::int AS status_code, NULL::int AS upstream_status_code,
         NULL::text AS error_owner, ''::text AS msg
  FROM usage_logs
  WHERE created_at >= '${esince}'::timestamptz - interval '10 seconds'
    AND api_key_id IN (${key_ids})
    AND (model IN (${models_sql}) OR requested_model IN (${models_sql}) OR upstream_model IN (${models_sql}))
    AND (inbound_endpoint ILIKE '%video%' OR inbound_endpoint ILIKE '%image%')
  UNION ALL
  SELECT 'error'::text AS src, id, created_at, COALESCE(NULLIF(request_id,''), NULLIF(client_request_id,''), '') AS request_id,
         api_key_id, account_id, group_id, model, NULL::text AS requested_model,
         inbound_endpoint, request_path AS upstream_endpoint, error_phase, error_type,
         status_code, upstream_status_code, error_owner,
         left(COALESCE(NULLIF(upstream_error_message,''), NULLIF(error_message,''), ''), 260) AS msg
  FROM ops_error_logs
  WHERE created_at >= '${esince}'::timestamptz - interval '10 seconds'
    AND api_key_id IN (${key_ids})
    AND model IN (${models_sql})
    AND (inbound_endpoint ILIKE '%video%' OR request_path ILIKE '%video%' OR inbound_endpoint ILIKE '%image%' OR request_path ILIKE '%image%')
)
SELECT row_to_json(t)
FROM (
  SELECT src, id, created_at AT TIME ZONE 'UTC' AS ts_utc, request_id, api_key_id,
         account_id, group_id, model, requested_model, inbound_endpoint, upstream_endpoint,
         error_phase, error_type, status_code, upstream_status_code, error_owner, msg
  FROM recent
  ORDER BY created_at DESC, id DESC
  LIMIT 40
) t;
"
}

post_probe() { # $1=label $2=kind $3=key_id $4=key $5=modality $6=model $7=path $8=body
	local label="$1" kind="$2" key_id="$3" key="$4" modality="$5" model="$6" path="$7" body="$8"
	local started_at bodyfile code shape task_id
	started_at="$(psql_scalar "SELECT clock_timestamp();")"
	bodyfile="$(mktemp)"
	code="$(curl -sS -o "$bodyfile" -w '%{http_code}' -m 120 -X POST "$PROD$path" \
		-H "Authorization: Bearer $key" \
		-H 'content-type: application/json' \
		-H "x-tokenkey-probe-run: $RUN_ID" \
		--data-binary "$body" 2>/dev/null || printf '000')"
	shape="$(shape_for "$modality" "$code" "$bodyfile")"
	task_id="$(sed -n 's/.*"\(task_id\|id\)"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\2/p' "$bodyfile" | head -n1)"
	printf 'RESULT\t%s\tkind=%s\tkey_id=%s\tmodality=%s\tmodel=%s\tpath=%s\thttp=%s\tshape=%s\ttask_id=%s\tsnippet=%s\n' \
		"$label" "$kind" "$key_id" "$modality" "$model" "$path" "$code" "$shape" "${task_id:-}" "$(snippet "$bodyfile")"
	rm -f "$bodyfile"
	sleep "$REQ_SLEEP"
	query_attribution "$label" "$key_id" "$model" "$path" "$started_at"
}

main() {
	if [ -n "${ATTR_ONLY_SINCE:-}" ]; then
		attr_only "$ATTR_ONLY_SINCE"
		return 0
	fi

	echo "RUN	run_id=${RUN_ID}	base=${PROD}"
	if ! select_universal_key; then
		exit 1
	fi

	if prepare_direct_key veo newapi "$VEO_GROUP_ID"; then
		VEO_DIRECT_KEY="$TK_PROBE_KEY"
		VEO_DIRECT_KEY_ID="$TK_PROBE_KEY_ID"
		post_probe direct_veo direct "$VEO_DIRECT_KEY_ID" "$VEO_DIRECT_KEY" video "$VEO_MODEL" /v1/video/generations "$(body_video "$VEO_MODEL")"
		post_probe universal_veo universal "$UNIVERSAL_KEY_ID" "$UNIVERSAL_KEY" video "$VEO_MODEL" /v1/video/generations "$(body_video "$VEO_MODEL")"
	fi

	if prepare_direct_key grok grok "$GROK_GROUP_ID"; then
		GROK_DIRECT_KEY="$TK_PROBE_KEY"
		GROK_DIRECT_KEY_ID="$TK_PROBE_KEY_ID"
		local model
		for model in $GROK_IMAGE_MODELS; do
			post_probe "direct_${model}" direct "$GROK_DIRECT_KEY_ID" "$GROK_DIRECT_KEY" image "$model" /v1/images/generations "$(body_image "$model")"
			post_probe "universal_${model}" universal "$UNIVERSAL_KEY_ID" "$UNIVERSAL_KEY" image "$model" /v1/images/generations "$(body_image "$model")"
		done
		local path suffix
		for path in $GROK_VIDEO_PATHS; do
			suffix="$(path_label "$path")"
			post_probe "direct_grok_video_${suffix}" direct "$GROK_DIRECT_KEY_ID" "$GROK_DIRECT_KEY" video "$GROK_VIDEO_MODEL" "$path" "$(body_video "$GROK_VIDEO_MODEL")"
			post_probe "universal_grok_video_${suffix}" universal "$UNIVERSAL_KEY_ID" "$UNIVERSAL_KEY" video "$GROK_VIDEO_MODEL" "$path" "$(body_video "$GROK_VIDEO_MODEL")"
		done
	fi
}

main
