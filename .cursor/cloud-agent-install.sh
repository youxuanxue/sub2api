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
# prod data via gh-workflow-dispatch wrappers):
#   GH_TOKEN               GitHub PAT (fine-grained, scoped to
#                          youxuanxue/sub2api with actions:read/write +
#                          contents:read). Used by `gh` CLI to dispatch
#                          ops workflows and download their artifacts.
#                          One token unlocks both:
#                            - scripts/fetch-prod-error-clusters.sh
#                                → aggregate clustering reports
#                            - scripts/fetch-prod-logs.sh
#                                → raw container logs (tokenkey,
#                                  postgres, caddy, redis) on demand
#                          Both workflows handle the AWS OIDC → SSM
#                          chain, so the agent never needs AWS credentials.
#                          See deploy/aws/README.md § "Cloud Agent 拉取
#                          error-clustering 报告" + § "Cloud Agent 按需
#                          拉取 prod 容器日志" for setup details.
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

# Self-test for the prod-log fetch path.
#   - GH_TOKEN unset      → skip silently (the secret is OPTIONAL; not every
#                           session needs to pull error-clustering reports).
#   - GH_TOKEN set + OK   → one-line confirmation, install continues.
#   - GH_TOKEN set + FAIL → loud WARNING with the underlying error, but
#                           install still exits 0. Rationale: a stale/wrong
#                           token must not block Claude Code or other agent
#                           capabilities — operator fixes the token next time
#                           they look at the bootstrap log.
if [ -n "${GH_TOKEN:-}" ]; then
  echo "[cloud-agent] verifying prod-data fetch env (GH_TOKEN is set)"
  # Both scripts share the same env (gh + jq + GH_TOKEN + same workflow
  # repo + same OIDC chain). Running --check on one is sufficient to
  # validate the other; we still call both so each script's argument-
  # validation matrix is exercised at bootstrap (e.g. CONTAINER enum,
  # SINCE regex). Either failure → loud WARNING, install still exits 0.
  CLUSTER_OK=0; LOGS_OK=0
  bash scripts/fetch-prod-error-clusters.sh --check && CLUSTER_OK=1
  bash scripts/fetch-prod-logs.sh             --check && LOGS_OK=1
  if [ "$CLUSTER_OK" = "1" ] && [ "$LOGS_OK" = "1" ]; then
    echo "[cloud-agent] prod-data fetch env OK — fetch-prod-error-clusters.sh + fetch-prod-logs.sh are ready"
  else
    echo "[cloud-agent] WARNING: prod-data fetch self-test FAILED (clusters=$CLUSTER_OK logs=$LOGS_OK). Fix GH_TOKEN scopes or gh install before relying on the scripts." >&2
  fi
else
  echo "[cloud-agent] GH_TOKEN unset; skipping prod-data fetch self-test (this is fine if this session does not need prod logs / clustering reports)"
fi

echo "[cloud-agent] install complete"
