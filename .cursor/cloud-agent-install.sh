#!/usr/bin/env bash
#
# .cursor/cloud-agent-install.sh — bootstrap script run once per Cursor
# Cloud Agent session (referenced by .cursor/environment.json).
#
# What this script is for:
#   1. Initialize the dev-rules submodule (preflight depends on it).
#   2. Sync the sibling new-api repo to the pinned SHA so backend Go
#      builds resolve the `replace` directive in backend/go.mod.
#   3. Install Claude Code CLI and write ~/.claude/settings.json so the
#      agent's `claude` invocations talk to the TokenKey gateway with
#      the same knobs we use locally.
#
# What this script is NOT for:
#   - Backend runtime config (DB / Redis / payment keys). The cloud agent
#     does not start the backend; it only reads/writes code and runs
#     preflight + tests that don't need a live datastore.
#
# Required Cursor Cloud Agents secret (Dashboard → Cloud Agents → Secrets):
#   ANTHROPIC_AUTH_TOKEN   sk-... TokenKey gateway token
#
# Optional Cursor Cloud Agents secret (only needed if the agent will pull
# prod error-clustering reports via `scripts/fetch-prod-error-clusters.sh`):
#   GH_TOKEN               GitHub PAT (fine-grained, scoped to
#                          youxuanxue/sub2api with actions:read/write +
#                          contents:read). Used by `gh` CLI to dispatch
#                          the existing error-clustering-daily workflow
#                          and download its artifact. The workflow itself
#                          handles the AWS OIDC → SSM chain, so the agent
#                          never needs AWS credentials.
#                          See deploy/aws/README.md § "Cloud Agent 拉取
#                          error-clustering 报告" for setup details.
#
# Non-secret defaults are baked in here on purpose — they are project
# policy, not credentials, and changing them deserves a PR diff.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

echo "[cloud-agent] initializing dev-rules submodule"
git submodule update --init --recursive

echo "[cloud-agent] syncing sibling new-api to pinned SHA"
bash scripts/sync-new-api.sh

echo "[cloud-agent] installing Claude Code CLI + writing ~/.claude/settings.json"
: "${ANTHROPIC_AUTH_TOKEN:?ANTHROPIC_AUTH_TOKEN must be set in Cursor Cloud Agents Secrets}"

mkdir -p "$HOME/.claude"
# 0600 on settings.json — token must not be world-readable on shared hosts.
umask 077
cat > "$HOME/.claude/settings.json" <<EOF
{
  "effortLevel": "high",
  "env": {
    "ANTHROPIC_BASE_URL": "https://api.tokenkey.dev",
    "ANTHROPIC_AUTH_TOKEN": "${ANTHROPIC_AUTH_TOKEN}",
    "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING": "1",
    "MAX_THINKING_TOKENS": "31999",
    "CLAUDE_CODE_DISABLE_1M_CONTEXT": "1",
    "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "200000",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0"
  }
}
EOF

bash scripts/setup-claude-code.sh

# Best-effort install of GitHub CLI so the agent can run
# scripts/fetch-prod-error-clusters.sh when GH_TOKEN is configured.
# Skipped silently if `gh` is already present or if neither apt-get nor
# brew is available — fetch-prod-error-clusters.sh will give a clear
# error at runtime if the binary is still missing.
if ! command -v gh >/dev/null 2>&1; then
  echo "[cloud-agent] installing GitHub CLI (best-effort)"
  if command -v apt-get >/dev/null 2>&1; then
    sudo apt-get update -qq && sudo apt-get install -y -qq gh || \
      echo "[cloud-agent] gh install via apt-get failed; install manually if needed"
  elif command -v brew >/dev/null 2>&1; then
    brew install gh || echo "[cloud-agent] gh install via brew failed; install manually if needed"
  else
    echo "[cloud-agent] no apt-get/brew available; skipping gh install"
  fi
fi

echo "[cloud-agent] install complete"
