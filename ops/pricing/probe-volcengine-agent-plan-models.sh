#!/usr/bin/env bash
# probe-volcengine-agent-plan-models.sh — direct Ark Agent Plan activation probe.
# Reads credentials for AGENT_PLAN_ACCOUNT_ID from the DB (via psql).
# AGENT_PLAN_ACCOUNT_ID (default 88) and classifies each candidate model against
# the Agent Plan data plane (/api/plan/v3/*), NOT pay-as-you-go /api/v3.
#
# Usage (prod):
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/pricing/probe-volcengine-agent-plan-models.sh \
#     --env AGENT_PLAN_ACCOUNT_ID=88 --timeout-seconds 600
#
# Output TSV: endpoint<TAB>model<TAB>http_code<TAB>verdict
set -euo pipefail

AGENT_PLAN_ACCOUNT_ID="${AGENT_PLAN_ACCOUNT_ID:-88}"
REQ_SLEEP="${REQ_SLEEP:-1}"
PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'

# Candidate catalog (pi-provider-volcengine-agent-plan + ark-code-latest alias).
CHAT_MODELS="${AGENT_PLAN_CHAT_MODELS:-kimi-k2.6 kimi-k2.7-code ark-code-latest}"
RESPONSES_MODELS="${AGENT_PLAN_RESPONSES_MODELS:-doubao-seed-2.0-mini doubao-seed-2.0-lite deepseek-v4-flash doubao-seed-evolving doubao-seed-2.0-code doubao-seed-2.0-pro minimax-m2.7 minimax-m3 glm-5.2 deepseek-v4-pro kimi-k3 ark-code-latest}"

emit() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4"; }

cfgerr() {
	emit setup_error "account_${AGENT_PLAN_ACCOUNT_ID}" 000 config_error
	printf 'probe-setup: %s\n' "$1" >&2
}

verdict() {
	local code="$1" f="$2"
	case "$code" in
	200) echo "servable" ;;
	400 | 404)
		if grep -qiE 'retired|sunset|not_found|does not exist|invalid model|unknown model|model_not_found|not supported|removed from|not a valid|does not support|no access|not activated|InvalidEndpointOrModel' "$f"; then
			echo "unsupported"
		else
			echo "inconclusive"
		fi
		;;
	401 | 403) echo "auth_error" ;;
	*) echo "inconclusive" ;;
	esac
}

normalize_plan_base() {
	local raw="$1"
	raw="${raw%/}"
	case "$raw" in
		doubao-agent-plan) printf '%s' "https://ark.cn-beijing.volces.com/api/plan/v3" ;;
		*/api/plan/v3) printf '%s' "$raw" ;;
		*/api/plan) printf '%s' "${raw%/api/plan}/api/plan/v3" ;;
		*) printf '%s' "https://ark.cn-beijing.volces.com/api/plan/v3" ;;
	esac
}

body_chat() {
	local m="$1"
	if [ "$m" = "kimi-k2.6" ]; then
		printf '{"model":"%s","max_tokens":16,"stream":false,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}' "$m"
	else
		printf '{"model":"%s","max_tokens":16,"stream":false,"messages":[{"role":"user","content":"hi"}]}' "$m"
	fi
}

body_resp() {
	printf '{"model":"%s","instructions":"Reply with OK only.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false,"max_output_tokens":16}' "$1"
}

probe_one() {
	local endpoint="$1" path="$2" base="$3" key="$4" model="$5" buildfn="$6"
	local f code
	f="$(mktemp)"
	if ! code="$(curl -s -o "$f" -w '%{http_code}' -m 75 -X POST "${base}${path}" \
		-H "Authorization: Bearer $key" -H 'content-type: application/json' \
		--data-binary "$($buildfn "$model")")"; then
		code=000
		emit "$endpoint" "$model" "$code" inconclusive
		rm -f "$f"
		sleep "$REQ_SLEEP"
		return 0
	fi
	emit "$endpoint" "$model" "$code" "$(verdict "$code" "$f")"
	rm -f "$f"
	sleep "$REQ_SLEEP"
}

case "$AGENT_PLAN_ACCOUNT_ID" in
	''|*[!0-9]*)
		cfgerr "account id must be numeric"
		exit 1
		;;
esac
if ! api_key="$($PSQL -c "SELECT credentials->>'api_key' FROM accounts WHERE id=${AGENT_PLAN_ACCOUNT_ID} AND deleted_at IS NULL" | tr -d '[:space:]')"; then
	cfgerr "failed to query api_key"
	exit 1
fi
if ! base_raw="$($PSQL -c "SELECT credentials->>'base_url' FROM accounts WHERE id=${AGENT_PLAN_ACCOUNT_ID} AND deleted_at IS NULL" | tr -d '[:space:]')"; then
	cfgerr "failed to query base_url"
	exit 1
fi
if [ -z "$api_key" ] || [ -z "$base_raw" ]; then
	cfgerr "missing api_key or base_url on account id=$AGENT_PLAN_ACCOUNT_ID"
	exit 1
fi
if ! plan_base="$(normalize_plan_base "$base_raw")"; then
	cfgerr "unsupported Agent Plan base_url: $base_raw"
	exit 1
fi
printf 'probe-meta\taccount_id=%s\tbase=%s\tplan_base=%s\n' "$AGENT_PLAN_ACCOUNT_ID" "$base_raw" "$plan_base" >&2

for m in $CHAT_MODELS; do
	probe_one chat_completions /chat/completions "$plan_base" "$api_key" "$m" body_chat
done
for m in $RESPONSES_MODELS; do
	probe_one responses /responses "$plan_base" "$api_key" "$m" body_resp
done
