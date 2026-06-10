#!/usr/bin/env bash
# worktree-bootstrap-hook.sh — sub2api project hook for the generic
# dev-rules/templates/worktree-bootstrap.sh.
#
# sub2api's backend/go.mod pins the cross-repo dependency with
#   replace github.com/QuantumNous/new-api => ../../new-api
# resolved relative to backend/, i.e. it expects a sibling `new-api` clone next
# to the repo root. A worktree created at a DEEP path (Claude Code
# EnterWorktree puts them under `.claude/worktrees/<name>/`) makes
# `../../new-api` resolve to `<repo>/.claude/worktrees/new-api` instead of the
# real sibling clone — so `go build` / preflight fail with a missing module.
#
# This hook creates a symlink at that resolved location pointing at the real
# sibling `new-api`, making deep-path worktrees turnkey. Sibling-placed
# worktrees (e.g. twin's `<parent>/<repo>-twin-*`) already resolve natively, so
# the hook is a no-op there. Idempotent. Receives the worktree dir as $1.
set -euo pipefail

WT="${1:?usage: worktree-bootstrap-hook.sh <worktree_dir>}"
WT="$(cd "$WT" && pwd)"

# Where go.mod's `../../new-api` (relative to <WT>/backend) resolves:
#   <WT>/backend/../../new-api == $(dirname "$WT")/new-api
NEEDED="$(dirname "$WT")/new-api"

# Already a usable clone at that path (sibling worktree, or symlink already
# made)? Nothing to do.
if [ -e "$NEEDED/go.mod" ]; then
  echo "[worktree-bootstrap-hook] new-api already resolves at $NEEDED"
  exit 0
fi

# Locate the real sibling new-api via the MAIN repo root (a worktree's
# --show-toplevel is the worktree itself; --git-common-dir points at the main
# .git regardless of which worktree we are in).
GIT_COMMON="$(cd "$WT" && git rev-parse --git-common-dir)"
case "$GIT_COMMON" in
  /*) ;;
  *) GIT_COMMON="$WT/$GIT_COMMON" ;;
esac
GIT_COMMON="$(cd "$GIT_COMMON" && pwd)"
MAIN_ROOT="$(dirname "$GIT_COMMON")"           # <main>/.git -> <main>
REAL="$(dirname "$MAIN_ROOT")/new-api"         # sibling of the main repo root

if [ ! -d "$REAL" ]; then
  echo "[worktree-bootstrap-hook] WARN: real new-api not found at $REAL" >&2
  echo "[worktree-bootstrap-hook]       run scripts/upstream/sync-new-api.sh first." >&2
  exit 0
fi

ln -sfn "$REAL" "$NEEDED"
echo "[worktree-bootstrap-hook] linked $NEEDED -> $REAL"
