#!/usr/bin/env bash
# Canonical TK_SMOKE_* resolution for Stage0 smoke scripts.
# Source from post_deploy_smoke.sh / edge_post_deploy_smoke.sh / CI wrappers.
#
# GitHub Environment config (see deploy/aws/README.md § Smoke config):
#   prod secrets: TK_SMOKE_PROD_ANTHROPIC_KEY, TK_SMOKE_PROD_GEMINI_KEY, TK_SMOKE_PROD_OPENAI_OAUTH_KEY, TK_SMOKE_PROD_KIRO_KEY
#   prod vars:    TK_SMOKE_PROD_ANTHROPIC_MODEL, TK_SMOKE_PROD_GEMINI_MODEL, TK_SMOKE_PROD_OPENAI_OAUTH_MODEL, TK_SMOKE_PROD_KIRO_MODEL
#   edge secrets: TK_SMOKE_EDGE_CANARY_KEY
#
# Fixed in code (not GitHub-configured per edge):
#   TK_SMOKE_EDGE_CANARY_BASE_URL=https://api.tokenkey.dev
#   TK_SMOKE_EDGE_LOCAL_CHAT_MODEL=claude-sonnet-4-6
#
# Runtime-only: TK_SMOKE_SKIP_FRONTEND, TK_SMOKE_CLAUDE_USER_AGENT, GATEWAY_SMOKE_SUITE
#
# Local manual smoke against prod/edge GitHub config:
#   export TK_SMOKE_GITHUB_ENV=prod   # or edge-uk1, edge-us1, …
#   bash ops/stage0/post_deploy_smoke.sh
# Loads TK_SMOKE_* variables via gh; secrets must already be exported locally
# (GitHub secret values are not readable via API — see load_smoke_github_env.sh).
set -euo pipefail

_SMOKE_ENV_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -n "${TK_SMOKE_GITHUB_ENV:-}" && -z "${_TK_SMOKE_GH_LOADED:-}" ]]; then
  # shellcheck disable=SC1091
  eval "$(bash "${_SMOKE_ENV_DIR}/load_smoke_github_env.sh" "${TK_SMOKE_GITHUB_ENV}")" || {
    echo "tk_smoke_env: failed to load GitHub Environment ${TK_SMOKE_GITHUB_ENV}" >&2
    exit 1
  }
  export _TK_SMOKE_GH_LOADED=1
fi

readonly _TK_SMOKE_DEFAULT_PROD_BASE_URL="https://api.tokenkey.dev"
readonly _TK_SMOKE_DEFAULT_EDGE_LOCAL_ANTHROPIC_MODEL="claude-sonnet-4-6"

export TK_SMOKE_PROD_ANTHROPIC_KEY="${TK_SMOKE_PROD_ANTHROPIC_KEY:-}"
export TK_SMOKE_PROD_GEMINI_KEY="${TK_SMOKE_PROD_GEMINI_KEY:-}"
export TK_SMOKE_PROD_OPENAI_OAUTH_KEY="${TK_SMOKE_PROD_OPENAI_OAUTH_KEY:-}"
export TK_SMOKE_PROD_KIRO_KEY="${TK_SMOKE_PROD_KIRO_KEY:-}"
export TK_SMOKE_PROD_ANTHROPIC_MODEL="${TK_SMOKE_PROD_ANTHROPIC_MODEL:-}"
export TK_SMOKE_PROD_GEMINI_MODEL="${TK_SMOKE_PROD_GEMINI_MODEL:-}"
export TK_SMOKE_PROD_OPENAI_OAUTH_MODEL="${TK_SMOKE_PROD_OPENAI_OAUTH_MODEL:-}"
export TK_SMOKE_PROD_KIRO_MODEL="${TK_SMOKE_PROD_KIRO_MODEL:-}"
export TK_SMOKE_PROD_GEMINI_MAX_TOKENS="${TK_SMOKE_PROD_GEMINI_MAX_TOKENS:-}"
export TK_SMOKE_PROD_OPENAI_OAUTH_REQUIRE_REASONING_TOKENS="${TK_SMOKE_PROD_OPENAI_OAUTH_REQUIRE_REASONING_TOKENS:-}"

export TK_SMOKE_SKIP_FRONTEND="${TK_SMOKE_SKIP_FRONTEND:-}"
export TK_SMOKE_CLAUDE_USER_AGENT="${TK_SMOKE_CLAUDE_USER_AGENT:-}"

export TK_SMOKE_EDGE_CANARY_KEY="${TK_SMOKE_EDGE_CANARY_KEY:-}"
export TK_SMOKE_EDGE_CANARY_BASE_URL="${TK_SMOKE_EDGE_CANARY_BASE_URL:-${_TK_SMOKE_DEFAULT_PROD_BASE_URL}}"
export TK_SMOKE_EDGE_LOCAL_CHAT_MODEL="${TK_SMOKE_EDGE_LOCAL_CHAT_MODEL:-${_TK_SMOKE_DEFAULT_EDGE_LOCAL_ANTHROPIC_MODEL}}"
