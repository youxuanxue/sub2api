#!/usr/bin/env bash
# resolve-gh-repo.sh — Resolve the GitHub OWNER/REPO slug for this worktree.
#
# Resolution order:
#   1. Parse git remote `origin` when it points at github.com.
#   2. Fallback to `gh repo view` for non-standard layouts.
#
# Usage:
#   bash scripts/lib/resolve-gh-repo.sh [repo-root]
#
# Exit codes:
#   0 — resolved and printed OWNER/REPO
#   1 — could not resolve

set -euo pipefail

REPO_ROOT="${1:-$(pwd)}"

origin_url="$(git -C "$REPO_ROOT" remote get-url origin 2>/dev/null || true)"
if [[ -n "$origin_url" ]]; then
  case "$origin_url" in
    git@github.com:*.git)
      repo="${origin_url#git@github.com:}"
      repo="${repo%.git}"
      ;;
    git@github.com:*)
      repo="${origin_url#git@github.com:}"
      ;;
    https://github.com/*/*.git)
      repo="${origin_url#https://github.com/}"
      repo="${repo%.git}"
      ;;
    https://github.com/*/*)
      repo="${origin_url#https://github.com/}"
      ;;
    ssh://git@github.com/*/*.git)
      repo="${origin_url#ssh://git@github.com/}"
      repo="${repo%.git}"
      ;;
    ssh://git@github.com/*/*)
      repo="${origin_url#ssh://git@github.com/}"
      ;;
    *)
      repo=""
      ;;
  esac
  if [[ -n "${repo:-}" && "$repo" != */*/* ]]; then
    printf '%s\n' "$repo"
    exit 0
  fi
fi

if command -v gh >/dev/null 2>&1; then
  repo="$(cd "$REPO_ROOT" && gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || true)"
  if [[ -n "$repo" ]]; then
    printf '%s\n' "$repo"
    exit 0
  fi
fi

exit 1
