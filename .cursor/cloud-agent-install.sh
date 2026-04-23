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

warn() {
  echo "[cloud-agent][warn] $*" >&2
}

echo "[cloud-agent] initializing dev-rules submodule"
git submodule update --init --recursive

# scripts/sync-new-api.sh expects to clone QuantumNous/new-api as a *sibling*
# of this repo (per CLAUDE.md §4 + backend/go.mod's `replace ../../new-api`).
# On the Cursor cloud-agent VM the workspace lives at /workspace, so the
# sibling target becomes /new-api — root-owned and not writable by the
# `ubuntu` agent user. Without preparing it here, sync-new-api.sh fails with
# `fatal: could not create work tree dir '/new-api': Permission denied` and
# every later step that needs the new-api code (Go build, preflight § 9/§ 10,
# `make test`) silently degrades.
#
# We don't push this workaround into sync-new-api.sh itself: local developer
# machines arrange their own sibling layout (typically under ~/Codes/tk/),
# and giving the sync script implicit `sudo mkdir` powers there would be
# surprising. The cloud-agent install script is the right layer.
SIBLING_PARENT="$(dirname -- "$REPO_ROOT")"
if [ "$SIBLING_PARENT" = "/" ]; then
  SIBLING_DIR="/new-api"
else
  SIBLING_DIR="$SIBLING_PARENT/new-api"
fi
if [ ! -d "$SIBLING_DIR/.git" ] && [ ! -w "$SIBLING_PARENT" ]; then
  echo "[cloud-agent] preparing sibling new-api dir at $SIBLING_DIR (parent $SIBLING_PARENT not writable by $(id -un))"
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    sudo install -d -o "$(id -un)" -g "$(id -gn)" -m 0755 "$SIBLING_DIR"
  else
    warn "no passwordless sudo available; sync-new-api.sh will likely fail at git clone"
  fi
fi

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

# Install GitHub CLI so the agent can run scripts/fetch-prod-{logs,
# error-clusters}.sh when GH_TOKEN is configured. We try in order:
#   1. already installed → no-op (Cursor's image may bundle gh).
#   2. simple `apt-get install gh` (works on Ubuntu 22.04+ where gh is
#      in the universe repo).
#   3. official GitHub CLI APT source (works on older Ubuntu/Debian
#      images that don't ship gh in their default repos — without this,
#      stock Ubuntu 20.04 silently fails the simple install and the
#      WARNING below would fire on every boot, training operators to
#      ignore it).
#   4. brew (macOS / Linuxbrew dev sandboxes).
# A real failure prints once with the underlying error; we never let an
# install error abort the whole bootstrap (the self-test below will fire
# a louder warning if gh is still missing at the end).
install_gh() {
  if command -v gh >/dev/null 2>&1; then return 0; fi

  echo "[cloud-agent] installing GitHub CLI"
  if command -v apt-get >/dev/null 2>&1; then
    if sudo apt-get update -qq 2>/dev/null && sudo apt-get install -y -qq gh 2>/dev/null; then
      return 0
    fi
    echo "[cloud-agent]   simple apt install failed; adding official GitHub CLI APT source"
    if command -v curl >/dev/null 2>&1 || sudo apt-get install -y -qq curl 2>/dev/null; then
      sudo mkdir -p -m 755 /etc/apt/keyrings
      curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | sudo tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
        && sudo chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
        && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
            | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
        && sudo apt-get update -qq \
        && sudo apt-get install -y -qq gh \
        && return 0
    fi
    echo "[cloud-agent] gh install via apt failed; install manually if needed" >&2
    return 1
  elif command -v brew >/dev/null 2>&1; then
    brew install gh || { echo "[cloud-agent] gh install via brew failed" >&2; return 1; }
    return 0
  else
    echo "[cloud-agent] no apt-get/brew available; skipping gh install" >&2
    return 1
  fi
}
install_gh || true

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
