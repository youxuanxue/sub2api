#!/usr/bin/env bash
# probe-servable-models.sh — runs ON the prod host (delivered via
# ops/observability/run-probe.sh) and classifies whether each candidate model
# is currently SERVABLE through TokenKey, by sending one minimal real request
# per model and reading the HTTP status.
#
# Inputs (env, space-separated model-id lists; any may be empty):
#   ANTHROPIC_MODELS         -> POST <edge>/v1/messages  (Claude-Code shape)
#   OPENAI_CHAT_MODELS       -> POST <prod>/v1/chat/completions
#   OPENAI_RESPONSES_MODELS  -> POST <prod>/v1/responses   (codex family)
#   OPENAI_IMAGE_MODELS      -> POST <prod>/v1/images/generations (best-effort)
# Optional env:
#   ANTHROPIC_EDGE_BASE      default https://api-us7.tokenkey.dev
#   ANTHROPIC_KEY_ACCOUNT_ID default 54  (its credentials.api_key relays to the edge)
#   PROD_BASE                default https://api.tokenkey.dev
#   OPENAI_KEY_NAME          default TK_SMOKE_PROD_OPENAI_OAUTH_KEY (api_keys.user_id=1)
#   REQ_SLEEP                default 2  (seconds between requests; avoids pool exhaustion)
#
# Output: one TSV line per model on stdout (keys never printed):
#   <platform>\t<model>\t<http_code>\t<verdict>
# verdict in: servable | unsupported | inconclusive | auth_error
#
# Classification (a model is "servable" iff a real 200 came back):
#   200                                   -> servable
#   400/404 + retired/not-found/invalid   -> unsupported (deprecated gate / upstream reject)
#   400 "not supported when using Codex"  -> unsupported (this account does not serve it)
#   502/503/429                           -> inconclusive (capacity / wrong protocol / no account)
#   401/403                               -> auth_error (probe setup wrong, not a model signal)
#
# Determinism / safety: keys are pulled from the local DB into shell vars and
# never echoed; only model + status are emitted. No unquoted parens in echo
# (host-shell parse trap, see check-stage0-ssm-host-parse.sh).
set -uo pipefail

PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'
AEDGE="${ANTHROPIC_EDGE_BASE:-https://api-us7.tokenkey.dev}"
AACCT="${ANTHROPIC_KEY_ACCOUNT_ID:-54}"
PROD="${PROD_BASE:-https://api.tokenkey.dev}"
OKEY_NAME="${OPENAI_KEY_NAME:-TK_SMOKE_PROD_OPENAI_OAUTH_KEY}"
REQ_SLEEP="${REQ_SLEEP:-2}"
UA='claude-cli/2.1.165 (external, sdk-cli)'
SYS='You are Claude Code, the official CLI for Claude.'

emit() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4"; }

verdict() { # $1=code $2=bodyfile -> echoes verdict
	local code="$1" f="$2"
	case "$code" in
	200) echo "servable" ;;
	400 | 404)
		if grep -qiE 'retired|sunset|not_found|does not exist|invalid model|unknown model|model_not_found|not supported when using|removed from this|not a valid' "$f"; then
			echo "unsupported"
		else
			echo "inconclusive"
		fi
		;;
	429 | 502 | 503) echo "inconclusive" ;;
	401 | 403) echo "auth_error" ;;
	*) echo "inconclusive" ;;
	esac
}

probe_anthropic() {
	local key="$1" m f code
	for m in $ANTHROPIC_MODELS; do
		f="$(mktemp)"
		code="$(curl -s -o "$f" -w '%{http_code}' -m 45 -X POST "$AEDGE/v1/messages" \
			-H "x-api-key: $key" -H 'anthropic-version: 2023-06-01' \
			-H 'anthropic-beta: claude-code-20250219' -H 'X-App: cli' \
			-H "User-Agent: $UA" -H 'content-type: application/json' \
			--data-binary "{\"model\":\"$m\",\"max_tokens\":32,\"system\":[{\"type\":\"text\",\"text\":\"$SYS\"}],\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: OK\"}],\"metadata\":{\"user_id\":\"servable-probe\"}}")"
		emit anthropic "$m" "$code" "$(verdict "$code" "$f")"
		rm -f "$f"
		sleep "$REQ_SLEEP"
	done
}

probe_openai_endpoint() { # $1=key $2=endpoint $3=models $4=jsonbody-template-fn
	local key="$1" path="$2" models="$3" buildfn="$4" m f code
	for m in $models; do
		f="$(mktemp)"
		code="$(curl -s -o "$f" -w '%{http_code}' -m 75 -X POST "$PROD$path" \
			-H "Authorization: Bearer $key" -H 'content-type: application/json' \
			--data-binary "$($buildfn "$m")")"
		emit openai "$m" "$code" "$(verdict "$code" "$f")"
		rm -f "$f"
		sleep "$REQ_SLEEP"
	done
}

body_chat() { printf '{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}' "$1"; }
body_resp() { printf '{"model":"%s","instructions":"You are a helpful assistant.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say OK"}]}],"stream":false}' "$1"; }
body_img() { printf '{"model":"%s","prompt":"a small red circle on white","n":1,"size":"1024x1024"}' "$1"; }

main() {
	local akey okey
	if [ -n "${ANTHROPIC_MODELS:-}" ]; then
		akey="$($PSQL -c "SELECT credentials->>'api_key' FROM accounts WHERE id=$AACCT" | tr -d '[:space:]')"
		if [ -z "$akey" ]; then
			emit anthropic "*" 000 "auth_error"
		else
			probe_anthropic "$akey"
		fi
	fi
	if [ -n "${OPENAI_CHAT_MODELS:-}${OPENAI_RESPONSES_MODELS:-}${OPENAI_IMAGE_MODELS:-}" ]; then
		okey="$($PSQL -c "SELECT key FROM api_keys WHERE user_id=1 AND name='$OKEY_NAME' LIMIT 1" | tr -d '[:space:]')"
		if [ -z "$okey" ]; then
			emit openai "*" 000 "auth_error"
		else
			[ -n "${OPENAI_CHAT_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/chat/completions "$OPENAI_CHAT_MODELS" body_chat
			[ -n "${OPENAI_RESPONSES_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/responses "$OPENAI_RESPONSES_MODELS" body_resp
			[ -n "${OPENAI_IMAGE_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/images/generations "$OPENAI_IMAGE_MODELS" body_img
		fi
	fi
}

main
