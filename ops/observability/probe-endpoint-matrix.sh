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

# Canonical prod source groups (case-sensitive); override via env if needed.
declare -A SOURCE_GROUP=(
	[anthropic]="${PROBE_ANTHROPIC_MIRROR_GROUP:-claude}"
	[openai]="${PROBE_OPENAI_SOURCE_GROUP:-GPT专线}"
	[gemini]="${PROBE_GEMINI_PLATFORM_SOURCE_GROUP:-Gemini-PA}"
	[antigravity]="${PROBE_ANTIGRAVITY_SOURCE_GROUP:-Google-Gemini}"
	[newapi]="${PROBE_NEWAPI_SOURCE_GROUP:-Qwen}"
	[kiro]="${PROBE_KIRO_SOURCE_GROUP:-kiro}"
	[grok]="${PROBE_GROK_SOURCE_GROUP:-grok}"
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
	local platform scope source bind_kind bind_val key

	printf 'platform\tendpoint\thttp_code\troute_verdict\tsnippet\n'

	for platform in anthropic openai gemini antigravity newapi kiro grok; do
		scope="endpoint_matrix_${platform}"
		source="${SOURCE_GROUP[$platform]:-}"
		bind_kind=source_group
		bind_val="$source"

		# anthropic prod matrix uses cc-* mirrors (platform=anthropic on claude group)
		if [ "$platform" = anthropic ]; then
			bind_kind=group_like
			bind_val="${source}|cc-%"
		fi
		# kiro: prod has no native platform=kiro customer group; bind kiro-* prod
		# mirrors (platform=anthropic credentials) while keeping probe group.platform=kiro
		# so route gates match a kiro-platform API key.
		if [ "$platform" = kiro ]; then
			bind_kind=group_like
			bind_val="${SOURCE_GROUP[anthropic]}|kiro-%"
		fi
		# grok on prod: grok group schedules on prod gateway (mirrors may relay to edge)
		if [ "$platform" = grok ]; then
			:
		fi

		if ! tk_probe_prepare_catalog "$scope" "$platform" "$bind_kind" "$bind_val"; then
			emit "$platform" '*' '000' 'config_error' "failed source=$source bind=$bind_kind:$bind_val"
			continue
		fi
		# Mirror dispatch + image flags from the canonical source group so messages
		# probes reflect prod group policy (probe groups default allow_messages_dispatch=false).
		if [ "$bind_kind" = source_group ]; then
			tk_probe_psql -c "
UPDATE groups dst
SET
  allow_messages_dispatch = src.allow_messages_dispatch,
  messages_dispatch_model_config = src.messages_dispatch_model_config,
  allow_image_generation = src.allow_image_generation,
  updated_at = NOW()
FROM groups src
WHERE dst.id = ${TK_PROBE_GROUP_ID}
  AND src.name = '$(tk_probe_sql_escape "$source")'
  AND src.deleted_at IS NULL;
" >/dev/null 2>&1 || true # preflight-allow: swallow
			# Bust in-memory API key cache: group policy is copied above but running
			# replicas may still serve stale AllowMessagesDispatch from Redis until
			# the key row is touched.
			tk_probe_psql -c "
UPDATE api_keys
SET updated_at = NOW()
WHERE group_id = ${TK_PROBE_GROUP_ID}
  AND deleted_at IS NULL;
" >/dev/null 2>&1 || true # preflight-allow: swallow
		fi
		TK_PROBE_CATALOG_SCOPES="${TK_PROBE_CATALOG_SCOPES} ${scope}"
		key="$TK_PROBE_KEY"
		pick_auth_and_probe "$platform" "$key"
		sleep 1
	done
	return 0
}

main
