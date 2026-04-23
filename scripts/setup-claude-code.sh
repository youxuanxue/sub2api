#!/usr/bin/env bash
#
# setup-claude-code.sh — install Claude Code CLI and verify auth is wired.
#
# Auth model (TokenKey):
#   We do NOT use Anthropic's SaaS API key path. The CLI talks to the
#   self-hosted gateway (https://api.tokenkey.dev) via:
#     ANTHROPIC_BASE_URL  + ANTHROPIC_AUTH_TOKEN
#   These are normally set in ~/.claude/settings.json (see
#   .cursor/cloud-agent-install.sh for the canonical layout). CI workflows
#   pass them as environment variables instead, so this script accepts
#   either path.
#
# Exit non-zero only when the CLI cannot authenticate at all.
set -euo pipefail

if ! command -v claude >/dev/null 2>&1; then
  npm install -g @anthropic-ai/claude-code
fi

if [ -z "${ANTHROPIC_AUTH_TOKEN:-}" ] && [ ! -s "${HOME}/.claude/settings.json" ]; then
  echo "Claude Code CLI auth missing." >&2
  echo "  Set ANTHROPIC_AUTH_TOKEN in the environment, OR provide" >&2
  echo "  ~/.claude/settings.json (see .cursor/cloud-agent-install.sh)." >&2
  exit 1
fi

claude --version >/dev/null 2>&1 || {
  echo "Claude Code CLI install check failed" >&2
  exit 1
}
