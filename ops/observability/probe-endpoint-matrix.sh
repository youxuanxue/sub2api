#!/usr/bin/env bash
# probe-endpoint-matrix.sh — prod endpoint ROUTE-GATE matrix (group.platform × path).
# Runs ON prod via ops/observability/run-probe.sh. Emits TSV:
#   platform\tendpoint\thttp_code\troute_verdict\tsnippet
# route_verdict:
#   open    — request passed the platform route gate (not a local 404 feature gate)
#   closed  — route layer rejected (404 + known gate message)
#   ws_skip — GET /v1/responses WebSocket (not curl-probed; see note)
#   config_error — could not prepare probe key for platform
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPANION="$SCRIPT_DIR/probe_reserved_resources.sh"
if [ ! -f "$COMPANION" ]; then
	COMPANION="$SCRIPT_DIR/../pricing/probe_reserved_resources.sh"
fi
if [ ! -f "$COMPANION" ]; then
	echo "probe-endpoint-matrix: missing probe_reserved_resources.sh companion" >&2
	exit 2
fi
# shellcheck source=probe_reserved_resources.sh
. "$COMPANION"

PSQL_ARRAY=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'
PROD="${PROD_BASE:-https://api.tokenkey.dev}"

# Canonical prod source group ids. Display names are operator-editable and have
# drifted often enough to produce false config_error rows; legacy name overrides
# remain available only for explicit diagnostics.
probe_source_group_id() { # $1=logical source pool
	case "$1" in
	openai) echo 2 ;;
	anthropic_mirror) echo 1 ;;
	gemini_vertex) echo 16 ;;
	dashscope) echo 18 ;;
	grok_prod) echo 25 ;;
	antigravity) echo 21 ;;
	*)
		echo "probe-endpoint-matrix: unknown source group key '$1'" >&2
		return 1
		;;
	esac
}

declare -A SOURCE_GROUP_ID=(
	[anthropic]="${PROBE_ANTHROPIC_MIRROR_GROUP_ID:-$(probe_source_group_id anthropic_mirror)}"
	[openai]="${PROBE_OPENAI_SOURCE_GROUP_ID:-$(probe_source_group_id openai)}"
	[gemini]="${PROBE_GEMINI_SOURCE_GROUP_ID:-$(probe_source_group_id gemini_vertex)}"
	[antigravity]="${PROBE_ANTIGRAVITY_SOURCE_GROUP_ID:-$(probe_source_group_id antigravity)}"
	[newapi]="${PROBE_NEWAPI_SOURCE_GROUP_ID:-${PROBE_DASHSCOPE_SOURCE_GROUP_ID:-$(probe_source_group_id dashscope)}}"
	[kiro]="${PROBE_KIRO_SOURCE_GROUP_ID:-$(probe_source_group_id anthropic_mirror)}"
	[grok]="${PROBE_GROK_SOURCE_GROUP_ID:-$(probe_source_group_id grok_prod)}"
)

declare -A SOURCE_GROUP_NAME=(
	[anthropic]="${PROBE_ANTHROPIC_MIRROR_GROUP:-}"
	[openai]="${PROBE_OPENAI_SOURCE_GROUP:-}"
	[gemini]="${PROBE_GEMINI_PLATFORM_SOURCE_GROUP:-${PROBE_GEMINI_SOURCE_GROUP:-}}"
	[antigravity]="${PROBE_ANTIGRAVITY_SOURCE_GROUP:-}"
	[newapi]="${PROBE_NEWAPI_SOURCE_GROUP:-${PROBE_DASHSCOPE_SOURCE_GROUP:-}}"
	[kiro]="${PROBE_KIRO_SOURCE_GROUP:-}"
	[grok]="${PROBE_GROK_SOURCE_GROUP:-}"
)

MODEL_claude='claude-sonnet-4-6'
MODEL_openai='gpt-5.4-mini'
MODEL_gemini='gemini-2.5-flash'
MODEL_antigravity='gemini-2.5-flash'
MODEL_newapi='qwen3.7-max'
MODEL_kiro='claude-sonnet-4-6'
MODEL_grok='grok-4.3'

emit() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5"; }

is_route_closed() { # $1=code $2=bodyfile
	local code="$1" f="$2"
	[ "$code" != "404" ] && return 1
	grep -qiE 'not supported for|not supported for this platform|only available for OpenAI-compatible|only available for OpenAI platform|Token counting is not supported' "$f"
}

classify_route() { # $1=code $2=bodyfile -> open|closed|other
	local code="$1" f="$2"
	if is_route_closed "$code" "$f"; then
		echo closed
	elif [ "$code" = "404" ]; then
		echo open
	else
		echo open
	fi
}

snippet() { head -c 180 "$1" | tr '\n' ' ' | sed 's/[[:space:]]+/ /g'; }

copy_source_group_policy() { # $1=bind_kind $2=bind_val
	local bind_kind="$1" bind_val="$2" where=""
	if [[ ! "$TK_PROBE_GROUP_ID" =~ ^[0-9]+$ ]]; then
		return 0
	fi
	case "$bind_kind" in
	source_group)
		where="src.name = '$(tk_probe_sql_escape "$bind_val")'"
		;;
	group_like)
		where="src.name = '$(tk_probe_sql_escape "${bind_val%%|*}")'"
		;;
	source_group_id)
		[[ "$bind_val" =~ ^[0-9]+$ ]] || return 0
		where="src.id = ${bind_val}"
		;;
	group_id_like)
		local source_group_id="${bind_val%%|*}"
		[[ "$source_group_id" =~ ^[0-9]+$ ]] || return 0
		where="src.id = ${source_group_id}"
		;;
	*)
		return 0
		;;
	esac
	tk_probe_psql -c "
UPDATE groups dst
SET
  allow_messages_dispatch = src.allow_messages_dispatch,
  messages_dispatch_model_config = src.messages_dispatch_model_config,
  allow_image_generation = src.allow_image_generation,
  updated_at = NOW()
FROM groups src
WHERE dst.id = ${TK_PROBE_GROUP_ID}
  AND ${where}
  AND src.deleted_at IS NULL;
" >/dev/null 2>&1 || true # preflight-allow: swallow
	tk_probe_psql -c "
UPDATE api_keys
SET updated_at = NOW()
WHERE group_id = ${TK_PROBE_GROUP_ID}
  AND deleted_at IS NULL;
" >/dev/null 2>&1 || true # preflight-allow: swallow
}

prepare_route_gate_probe() { # $1=scope $2=platform $3=bind_kind $4=bind_val
	local scope="$1" platform="$2" bind_kind="$3" bind_val="$4"
	if tk_probe_prepare_catalog "$scope" "$platform" "$bind_kind" "$bind_val"; then
		copy_source_group_policy "$bind_kind" "$bind_val"
		return 0
	fi
	scope="${TK_PROBE_SCOPE:-$scope}"

	# Route-gate evidence is still meaningful with an empty probe group: closed
	# endpoints return the local 404 feature gate, while open endpoints proceed
	# to scheduler/account selection and usually report 429 no-available-accounts.
	TK_PROBE_GROUP_ID=""
	TK_PROBE_KEY=""
	TK_PROBE_KEY_ID=""
	if ! tk_probe_acquire_reuse_lock "$scope"; then
		return 1
	fi
	tk_probe_ensure_group "$scope" "$platform" || return 1
	tk_probe_ensure_key "$scope" || return 1
	tk_probe_psql -c "DELETE FROM account_groups WHERE group_id = ${TK_PROBE_GROUP_ID};" >/dev/null 2>&1 || true # preflight-allow: route-gate empty pool fallback
	copy_source_group_policy "$bind_kind" "$bind_val"
	echo "probe-endpoint-matrix: continuing with empty probe group for route-gate scope=$scope platform=$platform source=$bind_kind:$bind_val" >&2
	return 0
}

body_messages() {
	local m="$1"
	printf '{"model":"%s","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}' "$m"
}

body_count_tokens() {
	local m="$1"
	printf '{"model":"%s","messages":[{"role":"user","content":"hi"}]}' "$m"
}

body_chat() {
	local m="$1"
	printf '{"model":"%s","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}' "$m"
}

body_responses() {
	local m="$1"
	printf '{"model":"%s","instructions":"ok","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}' "$m"
}

probe_post() { # $1=platform $2=endpoint_label $3=path $4=key $5=body
	local platform="$1" label="$2" path="$3" key="$4" body="$5" f code rv
	f="$(mktemp)"
	code="$(curl -s -o "$f" -w '%{http_code}' -m 60 -X POST "$PROD$path" \
		-H "Authorization: Bearer $key" \
		-H 'content-type: application/json' \
		--data-binary "$body")"
	rv="$(classify_route "$code" "$f")"
	emit "$platform" "$label" "$code" "$rv" "$(snippet "$f")"
	rm -f "$f"
}

probe_post_messages_anthropic_shape() { # anthropic uses x-api-key
	local platform="$1" label="$2" path="$3" key="$4" body="$5" f code rv
	f="$(mktemp)"
	code="$(curl -s -o "$f" -w '%{http_code}' -m 60 -X POST "$PROD$path" \
		-H "x-api-key: $key" \
		-H 'anthropic-version: 2023-06-01' \
		-H 'content-type: application/json' \
		--data-binary "$body")"
	rv="$(classify_route "$code" "$f")"
	emit "$platform" "$label" "$code" "$rv" "$(snippet "$f")"
	rm -f "$f"
}

pick_auth_and_probe() {
	local platform="$1" key="$2"
	local model_var="MODEL_${platform}"
	local model="${!model_var:-claude-sonnet-4-6}"
	local body_m body_c body_chat body_r

	body_m="$(body_messages "$model")"
	body_c="$(body_count_tokens "$model")"
	body_chat="$(body_chat "$model")"
	body_r="$(body_responses "$model")"

	case "$platform" in
	anthropic|gemini|antigravity|kiro)
		probe_post_messages_anthropic_shape "$platform" 'POST /v1/messages' '/v1/messages' "$key" "$body_m"
		probe_post_messages_anthropic_shape "$platform" 'POST /v1/messages/count_tokens' '/v1/messages/count_tokens' "$key" "$body_c"
		probe_post "$platform" 'POST /v1/chat/completions' '/v1/chat/completions' "$key" "$body_chat"
		probe_post "$platform" 'POST /v1/responses' '/v1/responses' "$key" "$body_r"
		;;
	openai|newapi|grok)
		probe_post "$platform" 'POST /v1/messages' '/v1/messages' "$key" "$body_m"
		probe_post "$platform" 'POST /v1/messages/count_tokens' '/v1/messages/count_tokens' "$key" "$body_c"
		probe_post "$platform" 'POST /v1/chat/completions' '/v1/chat/completions' "$key" "$body_chat"
		probe_post "$platform" 'POST /v1/responses' '/v1/responses' "$key" "$body_r"
		;;
	esac

	# WebSocket GET — curl cannot complete WS handshake meaningfully; record HTTP prelude only.
	local hf bf code rv
	hf="$(mktemp)"; bf="$(mktemp)"
	code="$(curl -s -o "$bf" -w '%{http_code}' -m 15 -X GET "$PROD/v1/responses" \
		-H "Authorization: Bearer $key" \
		-H 'Connection: Upgrade' \
		-H 'Upgrade: websocket' \
		-H 'Sec-WebSocket-Version: 13' \
		-H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' 2>"$hf" || true)"
	rv="$(classify_route "$code" "$bf")"
	if [ "$code" = "000" ]; then
		rv="ws_skip"
		code="000"
	fi
	emit "$platform" 'GET /v1/responses (WS prelude)' "$code" "$rv" "$(snippet "$bf")"
	rm -f "$hf" "$bf"
}

TK_PROBE_CATALOG_SCOPES=""
tk_probe_catalog_cleanup() {
	local scope
	for scope in $TK_PROBE_CATALOG_SCOPES; do
		[ -n "$scope" ] || continue
		tk_probe_clear_bindings "$scope" || true # preflight-allow: swallow
	done
	tk_probe_release_reuse_locks
}

main() {
	trap tk_probe_catalog_cleanup EXIT
	local platform scope source_name source_id bind_kind bind_val key

	printf 'platform\tendpoint\thttp_code\troute_verdict\tsnippet\n'

	for platform in anthropic openai gemini antigravity newapi kiro grok; do
		scope="endpoint_matrix_${platform}"
		source_name="${SOURCE_GROUP_NAME[$platform]:-}"
		source_id="${SOURCE_GROUP_ID[$platform]:-}"
		if [ -n "$source_name" ]; then
			bind_kind=source_group
			bind_val="$source_name"
		else
			bind_kind=source_group_id
			bind_val="$source_id"
		fi

		# anthropic prod matrix uses cc-* mirrors (platform=anthropic on claude group)
		if [ "$platform" = anthropic ]; then
			if [ -n "$source_name" ]; then
				bind_kind=group_like
				bind_val="${source_name}|cc-%"
			else
				bind_kind=group_id_like
				bind_val="${source_id}|cc-%"
			fi
		fi
		# kiro: prod has no native platform=kiro customer group; bind kiro-* prod
		# mirrors (platform=anthropic credentials) while keeping probe group.platform=kiro
		# so route gates match a kiro-platform API key.
		if [ "$platform" = kiro ]; then
			if [ -n "$source_name" ]; then
				bind_kind=group_like
				bind_val="${source_name}|kiro-%"
			else
				bind_kind=group_id_like
				bind_val="${source_id}|kiro-%"
			fi
		fi
		# grok on prod: grok group schedules on prod gateway (mirrors may relay to edge)
		if [ "$platform" = grok ]; then
			:
		fi

		if ! prepare_route_gate_probe "$scope" "$platform" "$bind_kind" "$bind_val"; then
			emit "$platform" '*' '000' 'config_error' "failed source_id=$source_id source_name=$source_name bind=$bind_kind:$bind_val"
			continue
		fi
		TK_PROBE_CATALOG_SCOPES="${TK_PROBE_CATALOG_SCOPES} ${TK_PROBE_SCOPE:-$scope}"
		key="$TK_PROBE_KEY"
		pick_auth_and_probe "$platform" "$key"
		sleep 1
	done
	return 0
}

main
