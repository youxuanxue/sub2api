#!/usr/bin/env bash
#
# .claude/hooks/session-start.sh — Claude Code on the web SessionStart hook.
#
# Why this exists and what it does:
#   Claude Code on the web sessions boot from a fresh container that has none
#   of this repo's runtime tools (claude/gh/jq/aws CLIs), no synced sibling
#   new-api repo, and no frontend node_modules. This hook provisions all of
#   that before the agent starts taking instructions, so the very first
#   request can run `make test`, `pnpm typecheck`, or `aws ssm send-command`
#   without race conditions.
#
#   Skills and rules need NO action here: `.claude/skills` is a tracked
#   symlink to `.cursor/skills/` (CLAUDE.md § Agent skills) so Claude Code's
#   harness discovers all four TokenKey skills automatically; rules live at
#   `.cursor/rules/*.mdc` and are referenced from CLAUDE.md (§10), which
#   Claude Code loads into context on its own.
#
#   This hook is a thin wrapper: it delegates the actual install work to
#   `.cursor/cloud-agent-install.sh` (the same entrypoint Cursor's cloud
#   agent uses), which in turn runs `dev-rules/templates/cloud-agent-bootstrap.sh`
#   + `.cursor/cloud-agent-project-hook.sh`. Keeping the install logic in one
#   place means Cursor and Claude Code on the web stay in lock-step on tool
#   list, gateway settings, sibling new-api pin, and pnpm install.
#
# Hook contract (Claude Code SessionStart):
#   - stdin:   JSON event {session_id, source, transcript_path, ...}
#   - stdout:  optional control JSON (we run synchronous, so no `{"async":true}`)
#   - env:     $CLAUDE_PROJECT_DIR points at repo root,
#              $CLAUDE_CODE_REMOTE=true on Claude Code on the web only
#   - exit 0:  always — never block session start on optional install steps
#
# Synchronous mode: the agent waits for this script to finish before the first
# turn. Trade-off accepted per .cursor/skills/session-start-hook setup choice
# (sync = guarantees deps installed; async = faster session start but races
# possible). Switch by replacing the `set -euo pipefail` block with
# `echo '{"async":true,"asyncTimeout":300000}'` and removing `exec` below.
set -euo pipefail

# Gate to the web. Local Claude Code sessions inherit the developer's normal
# shell environment with tools and ~/Codes/tk/new-api already in place; we
# don't want this hook running heavy installs on every local resume.
if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

REPO_ROOT="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$REPO_ROOT"

# Delegate to the shared cloud-agent installer. The wrapper itself swallows
# non-zero exits from the dev-rules bootstrap (logs a warning, returns 0) so
# that a missing optional secret like GH_TOKEN never blocks session startup.
exec bash .cursor/cloud-agent-install.sh
