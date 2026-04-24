#!/usr/bin/env bash
#
# .cursor/cloud-agent-project-hook.sh — sub2api-specific bootstrap steps.
#
# Invoked by `dev-rules/templates/cloud-agent-bootstrap.sh` AFTER the
# generic tool-install + env-check phase succeeds. Generic concerns
# (claude / gh / jq install, ~/.claude/settings.json with gateway URL +
# token, secret presence checks) live in the dev-rules template; this
# file only carries what is actually specific to sub2api:
#
#   1. Ensure /new-api sibling dir exists + sync to the pinned SHA so
#      backend Go builds resolve `replace ../../new-api` (CLAUDE.md §4).
#   2. Install frontend dependencies via pnpm.
#   3. Layer TokenKey's opinionated Claude Code knobs on top of the
#      minimal settings.json the dev-rules template wrote.
#   4. Self-test the prod-data fetch path when GH_TOKEN is set.
#   5. Non-blocking preflight + make test sanity.
#
# Intentionally non-fatal: any non-zero exit becomes a warning in the
# dev-rules bootstrap (so a stale step never locks the agent out of
# everything else).
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

warn() { echo "[project-hook][warn] $*" >&2; }

echo "[project-hook] using prepared dev-rules submodule from .cursor/cloud-agent-install.sh"

# Sibling new-api setup. On Cursor's cloud-agent VM the workspace lives at
# /workspace, so the sibling target becomes /new-api — root-owned and not
# writable by the `ubuntu` agent user. Local dev machines arrange their own
# layout (~/Codes/tk/), so the workaround stays out of sync-new-api.sh.
SIBLING_PARENT="$(dirname -- "$REPO_ROOT")"
if [ "$SIBLING_PARENT" = "/" ]; then
  SIBLING_DIR="/new-api"
else
  SIBLING_DIR="$SIBLING_PARENT/new-api"
fi
if [ ! -d "$SIBLING_DIR/.git" ] && [ ! -w "$SIBLING_PARENT" ]; then
  echo "[project-hook] preparing sibling new-api dir at $SIBLING_DIR (parent $SIBLING_PARENT not writable by $(id -un))"
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    sudo install -d -o "$(id -un)" -g "$(id -gn)" -m 0755 "$SIBLING_DIR"
  else
    warn "no passwordless sudo; sync-new-api.sh will likely fail at git clone"
  fi
fi

echo "[project-hook] syncing sibling new-api to pinned SHA"
bash scripts/sync-new-api.sh --check || bash scripts/sync-new-api.sh || warn "sync-new-api.sh failed"

echo "[project-hook] installing frontend dependencies via pnpm"
if command -v pnpm >/dev/null 2>&1; then
  pnpm --dir frontend install --frozen-lockfile || warn "pnpm install failed"
else
  warn "pnpm not on PATH — install Node.js+pnpm in the cloud agent image, then re-run"
fi

# TokenKey-opinionated Claude Code settings. The dev-rules template already
# wrote a minimal ~/.claude/settings.json with ANTHROPIC_BASE_URL +
# ANTHROPIC_AUTH_TOKEN. Layer on the project's preferred runtime knobs
# (high effort, large thinking budget, no adaptive thinking, no auto-1M
# context, no upstream attribution header). Keeping these here — not in
# dev-rules — means dev-rules doesn't bake any one project's opinions in.
SETTINGS="$HOME/.claude/settings.json"
if [ -s "$SETTINGS" ] && [ -n "${ANTHROPIC_AUTH_TOKEN:-}" ]; then
  echo "[project-hook] writing TokenKey-opinionated $SETTINGS"
  umask 077
  cat > "$SETTINGS" <<EOF
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
fi

# Prod-data fetch self-test.
#   GH_TOKEN unset → skip silently (declared OPTIONAL in cloud-agent.env).
#   GH_TOKEN set + OK → one-line confirmation.
#   GH_TOKEN set + FAIL → loud WARNING with underlying error; install
#                         continues. A stale token must not block other
#                         capabilities; operator fixes it next session.
if [ -n "${GH_TOKEN:-}" ]; then
  echo "[project-hook] verifying prod-data fetch env (GH_TOKEN is set)"
  CLUSTER_OK=0; LOGS_OK=0
  bash scripts/fetch-prod-error-clusters.sh --check && CLUSTER_OK=1
  bash scripts/fetch-prod-logs.sh             --check && LOGS_OK=1
  if [ "$CLUSTER_OK" = "1" ] && [ "$LOGS_OK" = "1" ]; then
    echo "[project-hook] prod-data fetch env OK"
  else
    warn "prod-data fetch self-test FAILED (clusters=$CLUSTER_OK logs=$LOGS_OK). Fix GH_TOKEN scopes or gh install before relying on the scripts."
  fi
fi

echo "[project-hook] running consistency checks (non-blocking)"
bash scripts/preflight.sh || warn "preflight failed; see logs above"
make test || warn "make test failed; investigate before merge"

echo "[project-hook] complete"
