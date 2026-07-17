#!/usr/bin/env bash
# TokenKey manual spot-check against TOKENKEY_BASE_URL (never prints API keys).
#
# Canonical runner: ops/stage0/post_deploy_smoke.sh (GATEWAY_SMOKE_SUITE=quick).
# Edge infra SSM: ops/stage0/edge_post_deploy_smoke.sh
#
# Usage against prod (GitHub Environment prod — vars auto-loaded when set):
#   export TOKENKEY_BASE_URL=https://api.tokenkey.dev
#   export TK_SMOKE_GITHUB_ENV=prod
#   export TK_SMOKE_API_KEY=sk-...   # GitHub secret values are not readable via gh
#   bash ops/stage0/gateway_smoke.sh
#
# Public-only (no API key):
#   export TOKENKEY_BASE_URL=https://api.example.com
#   bash ops/stage0/gateway_smoke.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE="${TOKENKEY_BASE_URL:-${TK_GATEWAY_URL:-}}"
BASE="${BASE%/}"

if [[ -z "${BASE}" ]]; then
  echo "tk_gateway_smoke: set TOKENKEY_BASE_URL (or TK_GATEWAY_URL)." >&2
  exit 1
fi

# shellcheck source=smoke_env.sh
source "${SCRIPT_DIR}/smoke_env.sh"

if [[ -n "${TK_SMOKE_API_KEY:-}" ]]; then
  export TOKENKEY_BASE_URL="${BASE}"
  export GATEWAY_SMOKE_SUITE=quick
  export TK_SMOKE_SKIP_FRONTEND=1
  exec bash "${SCRIPT_DIR}/post_deploy_smoke.sh"
fi

command -v curl >/dev/null 2>&1 || { echo "tk_gateway_smoke: curl not on PATH" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "tk_gateway_smoke: jq not on PATH" >&2; exit 1; }

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

echo "tk_gateway_smoke: gateway steps skipped (set TK_SMOKE_API_KEY or TK_SMOKE_GITHUB_ENV=prod with secret exported)."
echo "tk_gateway_smoke: partial OK (public settings only)."
