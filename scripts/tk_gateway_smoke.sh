#!/usr/bin/env bash
# TokenKey smoke tests against TOKENKEY_BASE_URL (never prints API keys).
#
# For deploy-stage0's mandatory gate (incl. /v1/messages), use
# scripts/tk_post_deploy_smoke.sh instead.
#
# 1) GET /api/v1/settings/public — no auth (validates cold-start public fields).
# 2) GET/POST /v1/* — requires a **user API key** (sk-...): TK_TOKEN or TOKENKEY_API_KEY.
#    Do **not** use ANTHROPIC_AUTH_TOKEN here — it is for Claude CLI / balance auth,
#    not /v1/models scheduling.
#
# Usage:
#   export TOKENKEY_BASE_URL='https://api.example.com'
#   export TK_TOKEN='sk-...'            # optional for step 2
#   bash scripts/tk_gateway_smoke.sh
#
# Legacy: TK_GATEWAY_URL if TOKENKEY_BASE_URL is unset.
set -euo pipefail

BASE="${TOKENKEY_BASE_URL:-${TK_GATEWAY_URL:-}}"
BASE="${BASE%/}"

if [[ -z "${BASE}" ]]; then
  echo "tk_gateway_smoke: set TOKENKEY_BASE_URL (or TK_GATEWAY_URL)." >&2
  exit 1
fi

PUB="${BASE}/api/v1/settings/public"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

pub_http=$(curl -sS -o "$tmpdir/pub.json" -w "%{http_code}" "${PUB}")

echo "GET .../api/v1/settings/public -> HTTP ${pub_http}"

if [[ "${pub_http}" != "200" ]]; then
  echo "tk_gateway_smoke: public settings failed" >&2
  exit 1
fi

pub_code="$(jq -r '.code // empty' "$tmpdir/pub.json")"
if [[ "${pub_code}" != "0" ]]; then
  echo "tk_gateway_smoke: public settings JSON code != 0" >&2
  exit 1
fi

bonus="$(jq -r '.data.signup_bonus_balance_usd // 0' "$tmpdir/pub.json")"
bonus_on="$(jq -r '.data.signup_bonus_enabled // false' "$tmpdir/pub.json")"
echo "public signup_bonus_enabled=${bonus_on} signup_bonus_balance_usd=${bonus}"

API_KEY="${TK_TOKEN:-${TOKENKEY_API_KEY:-}}"
if [[ -z "${API_KEY}" ]]; then
  echo "tk_gateway_smoke: gateway steps skipped (set TK_TOKEN or TOKENKEY_API_KEY for /v1/models + /v1/chat/completions)."
  echo "tk_gateway_smoke: partial OK (public settings only)."
  exit 0
fi

models_http=$(curl -sS -o "$tmpdir/models.json" -w "%{http_code}" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Accept: application/json" \
  "${BASE}/v1/models")

echo "GET .../v1/models -> HTTP ${models_http}"

if [[ "${models_http}" != "200" ]]; then
  echo "tk_gateway_smoke: /v1/models failed (body on stderr)" >&2
  jq . "$tmpdir/models.json" >&2 2>/dev/null || cat "$tmpdir/models.json" >&2
  exit 1
fi

model_id="$(jq -r '.data[0].id // empty' "$tmpdir/models.json")"
if [[ -z "${model_id}" ]]; then
  echo "tk_gateway_smoke: no models in response" >&2
  exit 1
fi

echo "Using model id from list (first entry): ${model_id}"

payload="$(jq -n \
  --arg m "${model_id}" \
  '{model:$m,messages:[{role:"user",content:"Say OK in one word."}],max_tokens:16,temperature:0,stream:false}')"

chat_http=$(curl -sS -o "$tmpdir/chat.json" -w "%{http_code}" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json" \
  -d "${payload}" \
  "${BASE}/v1/chat/completions")

echo "POST .../v1/chat/completions -> HTTP ${chat_http}"

if [[ "${chat_http}" != "200" ]]; then
  echo "tk_gateway_smoke: /v1/chat/completions failed" >&2
  jq . "$tmpdir/chat.json" >&2 || cat "$tmpdir/chat.json" >&2
  exit 1
fi

jq -r '.choices[0].message.content // .error // .' "$tmpdir/chat.json"
echo ""
echo "tk_gateway_smoke: OK (public settings + gateway)"
