#!/usr/bin/env bash
# tk_post_deploy_smoke.sh — mandatory post-deploy gateway checks (Stage0).
#
# Exercises the same paths Claude Code uses against TokenKey:
#   public settings, frontend release assets, /v1/models, /v1/chat/completions, /v1/messages.
#
# Usage:
#   TOKENKEY_BASE_URL=https://api.example.com \
#   POST_DEPLOY_SMOKE_API_KEY=sk-... \
#   bash scripts/tk_post_deploy_smoke.sh
#
# Key resolution (first non-empty): POST_DEPLOY_SMOKE_API_KEY,
# ANTHROPIC_AUTH_TOKEN, TK_TOKEN, TOKENKEY_API_KEY.
#
# Never prints the full API key. Requires curl + jq on PATH.
set -euo pipefail

BASE="${TOKENKEY_BASE_URL:-${TK_GATEWAY_URL:-}}"
BASE="${BASE%/}"

API_KEY="${POST_DEPLOY_SMOKE_API_KEY:-${ANTHROPIC_AUTH_TOKEN:-${TK_TOKEN:-${TOKENKEY_API_KEY:-}}}}"

if [[ -z "${BASE}" ]]; then
  echo "tk_post_deploy_smoke: set TOKENKEY_BASE_URL (or TK_GATEWAY_URL)" >&2
  exit 1
fi
if [[ -z "${API_KEY}" ]]; then
  echo "tk_post_deploy_smoke: set POST_DEPLOY_SMOKE_API_KEY (or ANTHROPIC_AUTH_TOKEN / TK_TOKEN / TOKENKEY_API_KEY)" >&2
  exit 1
fi

command -v curl >/dev/null 2>&1 || { echo "tk_post_deploy_smoke: curl not on PATH" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "tk_post_deploy_smoke: jq not on PATH" >&2; exit 1; }

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

prefix="$(printf '%s' "${API_KEY}" | head -c 6)"
suffix="$(printf '%s' "${API_KEY}" | tail -c 4)"
echo "tk_post_deploy_smoke: base_url=${BASE} key_hint=${prefix}…${suffix}"

# --- 1) Public settings (cold path) ---
pub_http=$(curl -sS -o "$tmpdir/pub.json" -w "%{http_code}" "${BASE}/api/v1/settings/public")
echo "tk_post_deploy_smoke: GET .../api/v1/settings/public -> HTTP ${pub_http}"
if [[ "${pub_http}" != "200" ]]; then
  echo "tk_post_deploy_smoke: public settings failed" >&2
  exit 1
fi
pub_code="$(jq -r '.code // empty' "$tmpdir/pub.json")"
if [[ "${pub_code}" != "0" ]]; then
  echo "tk_post_deploy_smoke: public settings JSON code != 0" >&2
  jq . "$tmpdir/pub.json" >&2 || true
  exit 1
fi

# --- 2) Frontend release asset shape ---
if [[ "${POST_DEPLOY_SMOKE_SKIP_FRONTEND:-}" != "1" ]]; then
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if [[ -x "${script_dir}/check-frontend-release-assets.py" || -f "${script_dir}/check-frontend-release-assets.py" ]]; then
    python3 "${script_dir}/check-frontend-release-assets.py" --url "${BASE}"
  else
    echo "tk_post_deploy_smoke: missing check-frontend-release-assets.py" >&2
    exit 1
  fi

  missing_asset_headers="$tmpdir/missing-asset.headers"
  missing_asset_body="$tmpdir/missing-asset.body"
  missing_asset_http=$(curl -sS -o "$missing_asset_body" -D "$missing_asset_headers" -w "%{http_code}" \
    "${BASE}/assets/TokenKeyMissingAsset-smoke.js")
  echo "tk_post_deploy_smoke: GET .../assets/TokenKeyMissingAsset-smoke.js -> HTTP ${missing_asset_http}"
  if [[ "${missing_asset_http}" != "404" ]]; then
    echo "tk_post_deploy_smoke: missing static asset should return HTTP 404" >&2
    exit 1
  fi
  if ! grep -i '^cache-control:.*no-store' "$missing_asset_headers" >/dev/null; then
    echo "tk_post_deploy_smoke: missing static asset should return Cache-Control: no-store" >&2
    cat "$missing_asset_headers" >&2
    exit 1
  fi
  if grep -iq '<!doctype html' "$missing_asset_body"; then
    echo "tk_post_deploy_smoke: missing static asset returned index.html" >&2
    exit 1
  fi
fi

# --- 3) Model list ---
models_http=$(curl -sS -o "$tmpdir/models.json" -w "%{http_code}" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Accept: application/json" \
  "${BASE}/v1/models")
echo "tk_post_deploy_smoke: GET .../v1/models -> HTTP ${models_http}"
if [[ "${models_http}" != "200" ]]; then
  echo "tk_post_deploy_smoke: /v1/models failed" >&2
  jq . "$tmpdir/models.json" >&2 2>/dev/null || cat "$tmpdir/models.json" >&2
  exit 1
fi

models_object="$(jq -r '.object // empty' "$tmpdir/models.json")"
models_count="$(jq -r '(.data // []) | length' "$tmpdir/models.json")"
if [[ "${models_object}" != "list" ]] || [[ "${models_count}" -lt 1 ]]; then
  echo "tk_post_deploy_smoke: /v1/models shape invalid (object=${models_object:-missing} count=${models_count})" >&2
  jq . "$tmpdir/models.json" >&2 || true
  exit 1
fi

echo "tk_post_deploy_smoke: /v1/models shape object=${models_object} count=${models_count}"

model="$(jq -r '(.data // []) as $d | ($d | map(select(.id|test("claude";"i"))) | .[0].id) // $d[0].id // empty' "$tmpdir/models.json")"
if [[ -z "${model}" ]] || [[ "${model}" == "null" ]]; then
  echo "tk_post_deploy_smoke: no model id in /v1/models" >&2
  jq . "$tmpdir/models.json" >&2 || true
  exit 1
fi
echo "tk_post_deploy_smoke: using model=${model}"

# --- 4) OpenAI-compat chat ---
expect_openai="E2E-OPENAI-OK"
payload="$(jq -n \
  --arg m "${model}" \
  --arg x "${expect_openai}" \
  '{model:$m,messages:[{role:"user",content:("Reply with exactly: " + $x)}],max_tokens:48,temperature:0,stream:false}')"

chat_http=$(curl -sS -o "$tmpdir/chat.json" -w "%{http_code}" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json" \
  -d "${payload}" \
  "${BASE}/v1/chat/completions")
echo "tk_post_deploy_smoke: POST .../v1/chat/completions -> HTTP ${chat_http}"
if [[ "${chat_http}" != "200" ]]; then
  echo "tk_post_deploy_smoke: /v1/chat/completions failed" >&2
  jq . "$tmpdir/chat.json" >&2 2>/dev/null || cat "$tmpdir/chat.json" >&2
  exit 1
fi
chat_object="$(jq -r '.object // empty' "$tmpdir/chat.json")"
chat_choices="$(jq -r '(.choices // []) | length' "$tmpdir/chat.json")"
chat_finish="$(jq -r '.choices[0].finish_reason // empty' "$tmpdir/chat.json")"
chat_usage_keys="$(jq -r 'if (.usage? | type) == "object" then (.usage | keys | join(",")) else "missing" end' "$tmpdir/chat.json")"
if [[ "${chat_object}" != "chat.completion" ]] || [[ "${chat_choices}" -lt 1 ]] || [[ -z "${chat_finish}" ]] || [[ "${chat_usage_keys}" == "missing" ]]; then
  echo "tk_post_deploy_smoke: /v1/chat/completions shape invalid (object=${chat_object:-missing} choices=${chat_choices} finish_reason=${chat_finish:-missing} usage=${chat_usage_keys})" >&2
  jq . "$tmpdir/chat.json" >&2 || true
  exit 1
fi

echo "tk_post_deploy_smoke: /v1/chat/completions shape object=${chat_object} choices=${chat_choices} finish_reason=${chat_finish} usage_keys=${chat_usage_keys}"

chat_body="$(jq -r '.choices[0].message.content // empty' "$tmpdir/chat.json")"
if ! printf '%s' "${chat_body}" | grep -Fq "${expect_openai}"; then
  echo "tk_post_deploy_smoke: chat response missing expected marker '${expect_openai}' (body below)" >&2
  printf '%s\n' "${chat_body}" >&2
  exit 1
fi

# --- 5) Anthropic Messages shape (Claude Code / x-api-key path) ---
expect_anthropic="E2E-ANTHROPIC-OK"
apayload="$(jq -n \
  --arg m "${model}" \
  --arg x "${expect_anthropic}" \
  '{model:$m,max_tokens:96,messages:[{role:"user",content:("Reply with exactly: " + $x)}]}')"

msg_http=$(curl -sS -o "$tmpdir/msg.json" -w "%{http_code}" \
  -H "x-api-key: ${API_KEY}" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d "${apayload}" \
  "${BASE}/v1/messages")
echo "tk_post_deploy_smoke: POST .../v1/messages -> HTTP ${msg_http}"
if [[ "${msg_http}" != "200" ]]; then
  echo "tk_post_deploy_smoke: /v1/messages failed" >&2
  jq . "$tmpdir/msg.json" >&2 2>/dev/null || cat "$tmpdir/msg.json" >&2
  exit 1
fi
msg_type="$(jq -r '.type // empty' "$tmpdir/msg.json")"
msg_role="$(jq -r '.role // empty' "$tmpdir/msg.json")"
msg_content_count="$(jq -r '(.content // []) | length' "$tmpdir/msg.json")"
msg_stop="$(jq -r '.stop_reason // empty' "$tmpdir/msg.json")"
msg_usage_keys="$(jq -r 'if (.usage? | type) == "object" then (.usage | keys | join(",")) else "missing" end' "$tmpdir/msg.json")"
if [[ "${msg_type}" != "message" ]] || [[ "${msg_role}" != "assistant" ]] || [[ "${msg_content_count}" -lt 1 ]] || [[ -z "${msg_stop}" ]] || [[ "${msg_usage_keys}" == "missing" ]]; then
  echo "tk_post_deploy_smoke: /v1/messages shape invalid (type=${msg_type:-missing} role=${msg_role:-missing} content=${msg_content_count} stop_reason=${msg_stop:-missing} usage=${msg_usage_keys})" >&2
  jq . "$tmpdir/msg.json" >&2 || true
  exit 1
fi

echo "tk_post_deploy_smoke: /v1/messages shape type=${msg_type} role=${msg_role} content=${msg_content_count} stop_reason=${msg_stop} usage_keys=${msg_usage_keys}"

msg_text="$(jq -r '[.content[]? | select(.type == "text") | .text] | add // empty' "$tmpdir/msg.json")"
if ! printf '%s' "${msg_text}" | grep -Fq "${expect_anthropic}"; then
  echo "tk_post_deploy_smoke: messages response missing expected marker '${expect_anthropic}' (text below)" >&2
  printf '%s\n' "${msg_text}" >&2
  exit 1
fi

echo "tk_post_deploy_smoke: OK"
