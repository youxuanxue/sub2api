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

echo "[cloud-agent] install complete"
