#!/usr/bin/env bash
#
# .cursor/cloud-agent-update.sh — lightweight refresh for Cursor Cloud Agent sessions.
#
# Cursor may invoke this separately from `install` when reconnecting or updating
# the workspace. Keep it fast and non-fatal: submodule sync + frontend deps only.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

echo "[cloud-agent-update] refreshing dev-rules submodule"
git submodule update --init --recursive dev-rules || true

echo "[cloud-agent-update] checking sibling new-api pin"
bash scripts/sync-new-api.sh --check 2>/dev/null || bash scripts/sync-new-api.sh || true

if command -v pnpm >/dev/null 2>&1; then
  echo "[cloud-agent-update] frontend dependencies"
  pnpm --dir frontend install --frozen-lockfile || true
fi

echo "[cloud-agent-update] done"
exit 0
