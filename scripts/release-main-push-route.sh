#!/usr/bin/env bash
# release-main-push-route.sh — How to land a VERSION bump on origin/main.
#
# Prints one line to stdout (parseable):
#   direct-push  — push origin HEAD:main is expected to work
#   bump-via-pr    — main has branch protection; use scripts/release-bump-via-pr.sh
#
# Exit 0 always when route is determined; exit 2 on gh/network failure.
set -euo pipefail

if ! command -v gh >/dev/null 2>&1; then
  echo direct-push
  exit 0
fi

REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null)" || {
  echo "[release-main-push-route] ERROR: gh repo view failed" >&2
  exit 2
}

if gh api "repos/$REPO/branches/main/protection" >/dev/null 2>&1; then
  echo bump-via-pr
else
  echo direct-push
fi
