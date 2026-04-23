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
#   4. Run the local/cloud consistency bootstrap template automatically:
#      - git pull origin main
#      - git submodule update --init --recursive
#      - bash scripts/sync-new-api.sh --check || bash scripts/sync-new-api.sh
#      - pnpm --dir frontend install
#      - bash scripts/preflight.sh (non-blocking check)
#      - make test (non-blocking check)
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

warn() {
  echo "[cloud-agent][warn] $*" >&2
}

echo "[cloud-agent] initializing dev-rules submodule"
git submodule update --init --recursive

echo "[cloud-agent] syncing sibling new-api to pinned SHA"
bash scripts/sync-new-api.sh --check || bash scripts/sync-new-api.sh

echo "[cloud-agent] syncing local branch with origin/main"
# Cloud agents normally start from a fresh clone, but pulling here keeps
# behavior aligned with local "start-of-session" workflows.
git pull --ff-only origin main || warn "git pull origin main failed; continuing with checked-out revision"

echo "[cloud-agent] installing frontend dependencies via pnpm"
pnpm --dir frontend install --frozen-lockfile

echo "[cloud-agent] running consistency checks (non-blocking)"
bash scripts/preflight.sh || warn "preflight failed; see logs above"
make test || warn "make test failed; investigate before merge"

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
