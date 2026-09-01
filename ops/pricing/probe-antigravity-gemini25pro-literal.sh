#!/usr/bin/env bash
# probe-antigravity-gemini25pro-literal.sh — focused Antigravity probe for literal
# gemini-2.5-pro on prod Google-Gemini group. Emits TSV: tag\tmodel\tcode\tverdict\thint
#
# Canonical client path: POST /antigravity/v1beta/models/<id>:generateContent
# Also probes /v1/chat/completions (standard servable refresh shape) for comparison.
#
# Usage (on prod host via run-probe.sh):
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/pricing/probe-antigravity-gemini25pro-literal.sh \
#     --with ops/pricing/probe_reserved_resources.sh \
#     --timeout-seconds 180
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ ! -f "$SCRIPT_DIR/probe_reserved_resources.sh" ]; then
	echo "missing probe_reserved_resources.sh companion" >&2
	exit 2
fi
# shellcheck source=probe_reserved_resources.sh
. "$SCRIPT_DIR/probe_reserved_resources.sh"

PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'
PROD="${PROD_BASE:-https://api.tokenkey.dev}"
PROBE_ANTIGRAVITY_SOURCE_GROUP="${PROBE_ANTIGRAVITY_SOURCE_GROUP:-Google-Gemini}"
CURL_TIMEOUT="${CURL_TIMEOUT:-90}"
MODELS="${PROBE_MODELS:-gemini-pro-agent gemini-2.5-pro}"

emit() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5"; }

TK_PROBE_CATALOG_SCOPES=""
tk_probe_catalog_key() { # $1=scope $2=platform $3=bind_kind $4=bind_val -> sets REPLY_KEY
	local scope="$1" platform="$2" bind_kind="$3" bind_val="$4"
	if ! tk_probe_prepare_catalog "$scope" "$platform" "$bind_kind" "$bind_val"; then
		return 1
	fi
	TK_PROBE_CATALOG_SCOPES="${TK_PROBE_CATALOG_SCOPES} ${TK_PROBE_SCOPE:-$scope}"
	REPLY_KEY="$TK_PROBE_KEY"
}

tk_probe_catalog_cleanup() {
	local scope
	for scope in $TK_PROBE_CATALOG_SCOPES; do
		[ -n "$scope" ] || continue
		tk_probe_clear_bindings "$scope" || true # preflight-allow: swallow
	done
	tk_probe_release_reuse_locks
}

trap tk_probe_catalog_cleanup EXIT

verdict() {
	local code="$1" f="$2"
	case "$code" in
	200) echo "servable" ;;
	400 | 404)
		if grep -qiE 'retired|sunset|not_found|does not exist|invalid model|unknown model|model_not_found|not supported|removed from|not a valid' "$f" 2>/dev/null; then
			echo "unsupported"
		else echo "inconclusive"; fi ;;
	429)
		if grep -qiE 'no available accounts' "$f" 2>/dev/null; then echo "not_allowlisted"; else echo "inconclusive"; fi ;;
	502 | 503) echo "relay_or_upstream" ;;
	401 | 403) echo "auth_error" ;;
	000) echo "timeout" ;;
	*) echo "inconclusive" ;;
	esac
}

hint_from_body() {
	local f="$1" code="$2"
	if [ "$code" = "000" ]; then echo "curl timeout or no response"; return; fi
	if grep -qiE 'no available accounts' "$f" 2>/dev/null; then echo "TK scheduling empty pool"; return; fi
	if grep -qiE '502|bad gateway|upstream' "$f" 2>/dev/null; then echo "prod->edge relay failure"; return; fi
	head -c 120 "$f" 2>/dev/null | tr '\n' ' ' | sed 's/[[:space:]]\+/ /g'
}

body_chat() {
	printf '{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"Say OK in one word"}]}' "$1"
}

body_generate() {
	printf '{"contents":[{"role":"user","parts":[{"text":"Say OK in one word"}]}]}'
}

probe_one() { # $1=tag $2=model $3=path $4=body
	local tag="$1" model="$2" path="$3" body="$4" f code v hint
	f="$(mktemp)"
	code="$(curl -s -o "$f" -w '%{http_code}' -m "$CURL_TIMEOUT" -X POST "$PROD$path" \
		-H "Authorization: Bearer $AGKEY" -H 'content-type: application/json' \
		--data-binary "$body")"
	v="$(verdict "$code" "$f")"
	hint="$(hint_from_body "$f" "$code")"
	emit "$tag" "$model" "$code" "$v" "$hint"
	rm -f "$f"
}

# Resolve probe key bound to Google-Gemini schedulable antigravity accounts.
if ! tk_probe_catalog_key antigravity antigravity source_group "$PROBE_ANTIGRAVITY_SOURCE_GROUP"; then
	emit "setup" "*" "000" "config_error" "failed to prepare __tk_probe_antigravity_* (source_group=$PROBE_ANTIGRAVITY_SOURCE_GROUP)"
	exit 1
fi
AGKEY="$REPLY_KEY"

printf 'probe_meta\tgroup=%s\tprod=%s\tmodels=%s\n' "$PROBE_ANTIGRAVITY_SOURCE_GROUP" "$PROD" "$MODELS"

for m in $MODELS; do
	probe_one "antigravity_chat" "$m" "/v1/chat/completions" "$(body_chat "$m")"
	probe_one "antigravity_generate" "$m" "/antigravity/v1beta/models/${m}:generateContent" "$(body_generate)"
done

# Account snapshot for relay diagnosis (no secrets).
tk_probe_psql -c "
SELECT a.id, a.name, a.platform, a.status, a.schedulable,
       COALESCE(a.credentials->>'base_url', '') AS relay_base_url
FROM accounts a
JOIN account_groups ag ON ag.account_id = a.id
JOIN groups g ON g.id = ag.group_id
WHERE g.name = '$(tk_probe_sql_escape "$PROBE_ANTIGRAVITY_SOURCE_GROUP")'
  AND g.deleted_at IS NULL AND a.deleted_at IS NULL
ORDER BY a.id;
" | while IFS='|' read -r id name platform status sched base; do
	printf 'account\t%s\tname=%s\tstatus=%s\tsched=%s\trelay=%s\n' "$id" "$name" "$status" "$sched" "$base"
done
