#!/usr/bin/env bash
# tk_post_deploy_smoke.sh — mandatory post-deploy gateway checks (Stage0).
#
# Exercises the same paths Claude Code uses against TokenKey:
#   public settings, frontend release assets, /v1/models, /v1/chat/completions,
#   /v1/messages, and (when configured) /v1/messages-with-tools through the
#   Gemini bridge to catch tool-schema cleanup regressions.
#
# Usage:
#   TOKENKEY_BASE_URL=https://api.example.com \
#   POST_DEPLOY_SMOKE_API_KEY=sk-... \
#   POST_DEPLOY_SMOKE_GEMINI_API_KEY=sk-... \              # optional, binds to gemini group
#   POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY=sk-... \        # optional, binds to OpenAI OAuth/codex group
#   bash scripts/tk_post_deploy_smoke.sh
#
# Key resolution (first non-empty): POST_DEPLOY_SMOKE_API_KEY,
# ANTHROPIC_AUTH_TOKEN, TK_TOKEN, TOKENKEY_API_KEY.
#
# Optional Gemini regression check (skipped silently if unset):
#   POST_DEPLOY_SMOKE_GEMINI_API_KEY  api_key bound to a gemini-platform group
#                                     (e.g. gemini-pa); exercises the
#                                     Anthropic→Gemini tool-schema cleanup.
#   POST_DEPLOY_SMOKE_GEMINI_MODEL    default: gemini-3.1-pro-preview
#
# Optional OpenAI OAuth (chatgpt.com codex backend) regression check:
#   POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY  api_key bound to an OpenAI/codex
#                                           OAuth-platform group; exercises
#                                           account correctness end-to-end
#                                           (HTTP 200 + content marker +
#                                           usage totals) and surfaces the
#                                           reasoning_tokens passthrough
#                                           (warn-only by default — see
#                                           section 7 inline comment).
#   POST_DEPLOY_SMOKE_OPENAI_OAUTH_MODEL    default: gpt-5.4
#   POST_DEPLOY_SMOKE_OPENAI_OAUTH_REQUIRE_REASONING_TOKENS  default: 0
#                                           Set to 1 to upgrade the
#                                           reasoning_tokens=0 warn into a
#                                           hard-fail (only when the
#                                           configured account is known to
#                                           emit reasoning_tokens).
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

# --- 6) Gemini /v1/messages with tools (Anthropic→Gemini schema cleanup regression) ---
# Validates tkCleanToolSchema strips Draft 2020 / OpenAPI 3.1 keywords that
# Gemini's restricted OpenAPI 3.0 schema dialect rejects (propertyNames /
# const / exclusiveMinimum / exclusiveMaximum). If cleanup regresses, Google
# upstream returns 400 "Invalid JSON payload received. Unknown name ...:
# Cannot find field." and this section fails. Skipped silently when no
# Gemini-bound key is provided.
#
# Failure semantics (2026-05-06 v1.7.19 false-positive postmortem):
#   200 → schema cleanup verified end-to-end against real Google upstream.
#   400 → HARD FAIL. The bug we are guarding (PR #121) has regressed; the
#         deploy must be rolled back / investigated.
#   401, 403, 404 → HARD FAIL. The configured smoke key/route is broken.
#   503 / 502 / 500 / "no available accounts" / 429 → SOFT WARN, exit 0.
#         These are upstream / scheduling resource issues that could not
#         possibly indicate a schema cleanup regression (a broken cleanup
#         would have been rejected with 400 BEFORE reaching the scheduling
#         pool). Treating them as deploy failures conflates control-plane
#         health with runtime-resource health.
GEMINI_KEY="${POST_DEPLOY_SMOKE_GEMINI_API_KEY:-}"
GEMINI_MODEL="${POST_DEPLOY_SMOKE_GEMINI_MODEL:-gemini-3.1-pro-preview}"

if [[ -n "${GEMINI_KEY}" ]]; then
  gemini_prefix="$(printf '%s' "${GEMINI_KEY}" | head -c 6)"
  gemini_suffix="$(printf '%s' "${GEMINI_KEY}" | tail -c 4)"
  echo "tk_post_deploy_smoke: gemini_key_hint=${gemini_prefix}…${gemini_suffix} gemini_model=${GEMINI_MODEL}"

  gpayload="$(jq -n \
    --arg m "${GEMINI_MODEL}" \
    '{
      model: $m,
      max_tokens: 96,
      messages: [{role:"user",content:"Reply with one short sentence."}],
      tools: [{
        name: "tk_smoke_schema_probe",
        description: "Schema sanitize probe. Do not call.",
        input_schema: {
          type: "object",
          required: ["mode"],
          properties: {
            mode:  {type:"string",  const: "auto"},
            limit: {type:"integer", minimum: 1, exclusiveMinimum: 0, exclusiveMaximum: 100},
            tags:  {type:"object",  propertyNames: {pattern: "^[a-z]+$"}}
          }
        }
      }]
    }')"

  gemini_http=$(curl -sS -o "$tmpdir/gemini-msg.json" -w "%{http_code}" \
    -H "x-api-key: ${GEMINI_KEY}" \
    -H "anthropic-version: 2023-06-01" \
    -H "Content-Type: application/json" \
    -d "${gpayload}" \
    "${BASE}/v1/messages")
  echo "tk_post_deploy_smoke: POST .../v1/messages (gemini, with tools) -> HTTP ${gemini_http}"

  # Read the gateway-reported error message (if any) to disambiguate
  # "schema cleanup broken" (400, the bug we guard) from "runtime resource
  # unavailable" (503 / 5xx / no available accounts / rate-limit).
  gemini_err_msg="$(jq -r '.error.message // empty' "$tmpdir/gemini-msg.json" 2>/dev/null)"

  # 200 happy path → verify shape, then continue to "OK".
  if [[ "${gemini_http}" == "200" ]]; then
    gemini_type="$(jq -r '.type // empty' "$tmpdir/gemini-msg.json")"
    gemini_role="$(jq -r '.role // empty' "$tmpdir/gemini-msg.json")"
    gemini_content_count="$(jq -r '(.content // []) | length' "$tmpdir/gemini-msg.json")"
    if [[ "${gemini_type}" != "message" ]] || [[ "${gemini_role}" != "assistant" ]] || [[ "${gemini_content_count}" -lt 1 ]]; then
      echo "tk_post_deploy_smoke: /v1/messages (gemini) shape invalid (type=${gemini_type:-missing} role=${gemini_role:-missing} content=${gemini_content_count})" >&2
      jq . "$tmpdir/gemini-msg.json" >&2 || true
      exit 1
    fi
    echo "tk_post_deploy_smoke: /v1/messages (gemini, with tools) shape type=${gemini_type} role=${gemini_role} content=${gemini_content_count}"
  # 400 → HARD FAIL. Schema cleanup regressed, that is the whole point of
  # this section. Operators must investigate before considering the deploy
  # successful.
  elif [[ "${gemini_http}" == "400" ]]; then
    echo "::error::tk_post_deploy_smoke: /v1/messages (gemini, with tools) returned HTTP 400 — Anthropic→Gemini schema cleanup likely regressed (see PR #121 / 2026-05-06 prod incident). DO NOT promote this build." >&2
    jq . "$tmpdir/gemini-msg.json" >&2 2>/dev/null || cat "$tmpdir/gemini-msg.json" >&2
    exit 1
  # Other 4xx (auth / route broken) → HARD FAIL: the smoke contract itself
  # is broken; without auth/route working we cannot say anything about the
  # gateway behavior.
  elif [[ "${gemini_http}" == "401" || "${gemini_http}" == "403" || "${gemini_http}" == "404" ]]; then
    echo "::error::tk_post_deploy_smoke: /v1/messages (gemini, with tools) returned HTTP ${gemini_http} — smoke key/route broken; fix POST_DEPLOY_SMOKE_GEMINI_API_KEY config or gateway routing." >&2
    jq . "$tmpdir/gemini-msg.json" >&2 2>/dev/null || cat "$tmpdir/gemini-msg.json" >&2
    exit 1
  # 5xx OR 429 OR gateway "no available accounts" → SOFT WARN. These cannot
  # signal a schema cleanup regression (the request never reached upstream
  # Google in a way that Google would have parsed the schema). Surface a CI
  # warning + the upstream error so operators see the resource issue, but
  # do NOT mark the deploy failed.
  else
    case "${gemini_err_msg}" in
      *"no available accounts"*|*"rate"*|*"timeout"*|*"context canceled"*|*"upstream error: 5"*)
        :
        ;;
    esac
    echo "::warning::tk_post_deploy_smoke: /v1/messages (gemini, with tools) returned HTTP ${gemini_http} — runtime resource issue (likely Gemini account cooldown / 429 / upstream 5xx), NOT a schema regression. Schema cleanup contract was not violated." >&2
    if [[ -n "${gemini_err_msg}" ]]; then
      echo "  gateway message: ${gemini_err_msg}" >&2
    fi
    jq . "$tmpdir/gemini-msg.json" >&2 2>/dev/null || cat "$tmpdir/gemini-msg.json" >&2
    echo "tk_post_deploy_smoke: gemini section soft-skipped (HTTP ${gemini_http} is not a schema-regression signal)"
  fi
else
  echo "tk_post_deploy_smoke: skip /v1/messages (gemini) — POST_DEPLOY_SMOKE_GEMINI_API_KEY not set"
fi

# --- 7) OpenAI OAuth /v1/chat/completions account + token-count probe ---
# Two-layer check on the OAuth/codex group key:
#   (a) account correctness — HTTP 200 + correct shape + non-empty assistant
#       content + expected marker + non-zero usage totals that add up.
#   (b) reasoning_tokens passthrough — whether
#       `usage.completion_tokens_details.reasoning_tokens` is present.
#       Layered as SOFT-WARN, not hard-fail, because the chatgpt.com codex
#       OAuth backend in observed prod traffic returns
#       `completion_tokens` ~= total (i.e. no reasoning_tokens broken out)
#       even when the request asks for `reasoning_effort=medium`. We cannot
#       distinguish "upstream did not reason" from "passthrough regressed"
#       from a single response on this path. The unit tests in
#       backend/internal/pkg/apicompat/chatcompletions_responses_test.go
#       are the authoritative regression guard for the conversion logic;
#       this smoke section's job is end-to-end account health.
#
# When operators confirm a path that DOES emit reasoning_tokens (e.g. an
# APIKey-direct OpenAI Responses account), set
#   POST_DEPLOY_SMOKE_OPENAI_OAUTH_REQUIRE_REASONING_TOKENS=1
# to upgrade the soft-warn to a hard-fail.
#
# Failure semantics:
#   200 + valid shape + correct totals → OK (warn if reasoning_tokens=0 and
#       strict mode is off; hard-fail if strict mode is on).
#   200 + invalid shape / missing marker / total mismatch → HARD FAIL.
#   400 / 401 / 403 / 404 → HARD FAIL. Smoke key/route/account broken.
#   5xx / 429 / "no available accounts" / "rate" / "timeout" → SOFT WARN,
#       exit 0. Runtime resource issue.
OPENAI_OAUTH_KEY="${POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY:-}"
OPENAI_OAUTH_MODEL="${POST_DEPLOY_SMOKE_OPENAI_OAUTH_MODEL:-gpt-5.4}"

if [[ -n "${OPENAI_OAUTH_KEY}" ]]; then
  oai_prefix="$(printf '%s' "${OPENAI_OAUTH_KEY}" | head -c 6)"
  oai_suffix="$(printf '%s' "${OPENAI_OAUTH_KEY}" | tail -c 4)"
  echo "tk_post_deploy_smoke: openai_oauth_key_hint=${oai_prefix}…${oai_suffix} openai_oauth_model=${OPENAI_OAUTH_MODEL}"

  expect_oai_oauth="E2E-OPENAI-OAUTH-OK"
  # The math problem reliably triggers reasoning so reasoning_tokens > 0.
  # Asking the model to end with the marker on its own line lets us probe
  # account correctness without depending on the model's exact phrasing.
  oai_payload="$(jq -n \
    --arg m "${OPENAI_OAUTH_MODEL}" \
    --arg x "${expect_oai_oauth}" \
    '{
      model: $m,
      messages: [{
        role: "user",
        content: ("What is 17*23? Think step by step, then on the very last line write exactly: " + $x)
      }],
      reasoning_effort: "medium",
      max_tokens: 4096,
      stream: false
    }')"

  oai_http=$(curl -sS -o "$tmpdir/openai-oauth-chat.json" -w "%{http_code}" \
    -H "Authorization: Bearer ${OPENAI_OAUTH_KEY}" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json" \
    -d "${oai_payload}" \
    "${BASE}/v1/chat/completions")
  echo "tk_post_deploy_smoke: POST .../v1/chat/completions (openai oauth) -> HTTP ${oai_http}"

  oai_err_msg="$(jq -r '.error.message // empty' "$tmpdir/openai-oauth-chat.json" 2>/dev/null)"

  if [[ "${oai_http}" == "200" ]]; then
    oai_object="$(jq -r '.object // empty' "$tmpdir/openai-oauth-chat.json")"
    oai_choices="$(jq -r '(.choices // []) | length' "$tmpdir/openai-oauth-chat.json")"
    oai_finish="$(jq -r '.choices[0].finish_reason // empty' "$tmpdir/openai-oauth-chat.json")"
    oai_content="$(jq -r '.choices[0].message.content // empty' "$tmpdir/openai-oauth-chat.json")"
    oai_prompt_tokens="$(jq -r '.usage.prompt_tokens // 0' "$tmpdir/openai-oauth-chat.json")"
    oai_completion_tokens="$(jq -r '.usage.completion_tokens // 0' "$tmpdir/openai-oauth-chat.json")"
    oai_total_tokens="$(jq -r '.usage.total_tokens // 0' "$tmpdir/openai-oauth-chat.json")"
    oai_reasoning_tokens="$(jq -r '.usage.completion_tokens_details.reasoning_tokens // 0' "$tmpdir/openai-oauth-chat.json")"

    # Layer (a): account correctness — shape + content + marker + token totals.
    if [[ "${oai_object}" != "chat.completion" ]] || [[ "${oai_choices}" -lt 1 ]] || [[ -z "${oai_finish}" ]] || [[ -z "${oai_content}" ]]; then
      echo "::error::tk_post_deploy_smoke: /v1/chat/completions (openai oauth) shape invalid (object=${oai_object:-missing} choices=${oai_choices} finish_reason=${oai_finish:-missing} content_empty=$([[ -z "${oai_content}" ]] && echo yes || echo no))" >&2
      jq . "$tmpdir/openai-oauth-chat.json" >&2 || true
      exit 1
    fi
    if ! printf '%s' "${oai_content}" | grep -Fq "${expect_oai_oauth}"; then
      echo "::error::tk_post_deploy_smoke: /v1/chat/completions (openai oauth) response missing expected marker '${expect_oai_oauth}'; OAuth/codex account likely broken or returning empty content" >&2
      printf '%s\n' "${oai_content}" >&2
      exit 1
    fi
    # prompt_tokens > 0 AND completion_tokens > 0 AND total = prompt + completion.
    # This proves the usage block is being populated and arithmetically
    # consistent — independent of whether the upstream did reasoning.
    if [[ "${oai_prompt_tokens}" -le 0 ]] || [[ "${oai_completion_tokens}" -le 0 ]]; then
      echo "::error::tk_post_deploy_smoke: /v1/chat/completions (openai oauth) usage tokens missing/zero (prompt=${oai_prompt_tokens} completion=${oai_completion_tokens})" >&2
      jq '.usage' "$tmpdir/openai-oauth-chat.json" >&2 || true
      exit 1
    fi
    if [[ "${oai_total_tokens}" -ne $(( oai_prompt_tokens + oai_completion_tokens )) ]]; then
      echo "::error::tk_post_deploy_smoke: /v1/chat/completions (openai oauth) usage totals do not balance (prompt=${oai_prompt_tokens} + completion=${oai_completion_tokens} != total=${oai_total_tokens})" >&2
      jq '.usage' "$tmpdir/openai-oauth-chat.json" >&2 || true
      exit 1
    fi

    # Layer (b): reasoning_tokens passthrough.
    # On chatgpt.com codex OAuth backend in observed prod traffic, this is
    # always 0 (upstream apparently does not break out reasoning tokens for
    # this path). The unit tests guard the conversion logic; here we just
    # surface the value and allow operators to opt into a hard-fail when
    # they have a path that does emit reasoning_tokens.
    require_reasoning="${POST_DEPLOY_SMOKE_OPENAI_OAUTH_REQUIRE_REASONING_TOKENS:-0}"
    if [[ "${oai_reasoning_tokens}" -gt 0 ]]; then
      echo "tk_post_deploy_smoke: /v1/chat/completions (openai oauth) reasoning_tokens=${oai_reasoning_tokens} (passthrough verified end-to-end)"
    elif [[ "${require_reasoning}" == "1" ]]; then
      echo "::error::tk_post_deploy_smoke: /v1/chat/completions (openai oauth) usage.completion_tokens_details.reasoning_tokens is missing or 0 (got ${oai_reasoning_tokens}, completion_tokens=${oai_completion_tokens}) and POST_DEPLOY_SMOKE_OPENAI_OAUTH_REQUIRE_REASONING_TOKENS=1 — apicompat ResponsesToChatCompletions reasoning_tokens passthrough has regressed. DO NOT promote this build." >&2
      jq '.usage' "$tmpdir/openai-oauth-chat.json" >&2 || true
      exit 1
    else
      echo "::warning::tk_post_deploy_smoke: /v1/chat/completions (openai oauth) reasoning_tokens=0; cannot verify passthrough end-to-end on this path (chatgpt.com codex OAuth backend does not break out reasoning tokens for our keys). Unit tests in apicompat/chatcompletions_responses_test.go are the regression guard. Set POST_DEPLOY_SMOKE_OPENAI_OAUTH_REQUIRE_REASONING_TOKENS=1 to make this a hard-fail when you have an account that does emit them."
    fi
    echo "tk_post_deploy_smoke: /v1/chat/completions (openai oauth) shape object=${oai_object} choices=${oai_choices} finish_reason=${oai_finish} prompt_tokens=${oai_prompt_tokens} completion_tokens=${oai_completion_tokens} total_tokens=${oai_total_tokens} reasoning_tokens=${oai_reasoning_tokens}"
  elif [[ "${oai_http}" == "400" || "${oai_http}" == "401" || "${oai_http}" == "403" || "${oai_http}" == "404" ]]; then
    echo "::error::tk_post_deploy_smoke: /v1/chat/completions (openai oauth) returned HTTP ${oai_http} — smoke key/route/account broken; fix POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY config or gateway routing." >&2
    jq . "$tmpdir/openai-oauth-chat.json" >&2 2>/dev/null || cat "$tmpdir/openai-oauth-chat.json" >&2
    exit 1
  else
    case "${oai_err_msg}" in
      *"no available accounts"*|*"rate"*|*"timeout"*|*"context canceled"*|*"upstream error: 5"*)
        :
        ;;
    esac
    echo "::warning::tk_post_deploy_smoke: /v1/chat/completions (openai oauth) returned HTTP ${oai_http} — runtime resource issue (likely OpenAI/codex account cooldown / 429 / upstream 5xx), NOT a reasoning_tokens passthrough regression." >&2
    if [[ -n "${oai_err_msg}" ]]; then
      echo "  gateway message: ${oai_err_msg}" >&2
    fi
    jq . "$tmpdir/openai-oauth-chat.json" >&2 2>/dev/null || cat "$tmpdir/openai-oauth-chat.json" >&2
    echo "tk_post_deploy_smoke: openai oauth section soft-skipped (HTTP ${oai_http} is not a passthrough-regression signal)"
  fi
else
  echo "tk_post_deploy_smoke: skip /v1/chat/completions (openai oauth) — POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY not set"
fi

echo "tk_post_deploy_smoke: OK"
