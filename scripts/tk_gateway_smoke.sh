#!/usr/bin/env bash
# Smoke-test a TokenKey-compatible gateway with an API key (never prints the key).
# Usage:
#   export TK_TOKEN='sk-...'
#   export TK_GATEWAY_URL='https://your-api-host'   # required; no default in-repo
#   bash scripts/tk_gateway_smoke.sh
set -euo pipefail

if [[ -z "${TK_GATEWAY_URL:-}" ]]; then
  echo "tk_gateway_smoke: set TK_GATEWAY_URL to the gateway origin (scheme + host, no path)." >&2
  exit 1
fi

BASE="${TK_GATEWAY_URL}"
BASE="${BASE%/}"

if [[ -z "${TK_TOKEN:-}" ]]; then
  echo "tk_gateway_smoke: set TK_TOKEN to your API key (not printed)." >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

models_http=$(curl -sS -o "$tmpdir/models.json" -w "%{http_code}" \
  -H "Authorization: Bearer ${TK_TOKEN}" \
  -H "Accept: application/json" \
  "${BASE}/v1/models")

echo "GET (origin)/v1/models -> HTTP ${models_http}"

if [[ "${models_http}" != "200" ]]; then
  echo "tk_gateway_smoke: unexpected status for /v1/models" >&2
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
  -H "Authorization: Bearer ${TK_TOKEN}" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json" \
  -d "${payload}" \
  "${BASE}/v1/chat/completions")

echo "POST (origin)/v1/chat/completions -> HTTP ${chat_http}"

if [[ "${chat_http}" != "200" ]]; then
  echo "tk_gateway_smoke: unexpected status for /v1/chat/completions" >&2
  jq . "$tmpdir/chat.json" >&2 || cat "$tmpdir/chat.json" >&2
  exit 1
fi

jq -r '.choices[0].message.content // .error // .' "$tmpdir/chat.json"
echo ""
echo "tk_gateway_smoke: OK"
