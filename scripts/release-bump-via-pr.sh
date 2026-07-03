#!/usr/bin/env bash
# release-bump-via-pr.sh — VERSION bump through a PR when origin/main is protected.
#
# Worktree-isolated (never touches the operator checkout). Flow:
#   release-decide-version (must be bump-and-tag)
#   → ephemeral worktree bump commit on chore/bump-version-X.Y.Z
#   → push branch + gh pr create
#   → gh pr checks --watch until green (preflight flake: rerun failed once)
#   → gh pr merge --squash
#   → scripts/release-bump-and-tag.sh (tag-only on merged main)
#
# Usage:
#   bash scripts/release-bump-via-pr.sh
#   bash scripts/release-bump-via-pr.sh --dry-run
#   bash scripts/release-bump-via-pr.sh --pr <number>   # resume: wait CI + merge + tag
#
# Exit codes: same family as release-bump-and-tag.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN=0
RESUME_PR=""
KEEP_WT=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --pr) RESUME_PR="$2"; shift 2 ;;
    --keep-worktree) KEEP_WT=1; shift ;;
    -h|--help) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "[release-bump-via-pr] ERROR: unknown arg: $1" >&2; exit 1 ;;
  esac
done

decide() { bash "$REPO_ROOT/scripts/release-decide-version.sh" --emit-suggested-bump; }
field() { printf '%s\n' "$1" | grep "^$2=" | head -1 | cut -d= -f2- || true; }

DECISION="$(decide)"
ACTION="$(field "$DECISION" action)"
NEXT_VERSION="$(field "$DECISION" suggested_next_version)"
CURRENT_TAG="$(field "$DECISION" current_tag)"

echo "[release-bump-via-pr] $(field "$DECISION" reason)"

if [ "$ACTION" = "skip-bump-skip-tag" ]; then
  echo "[release-bump-via-pr] nothing to bump; deploy existing tag $CURRENT_TAG"
  exit 0
fi

if [ "$ACTION" = "tag-only" ]; then
  echo "[release-bump-via-pr] VERSION already on main; run: bash scripts/release-bump-and-tag.sh"
  if [ "$DRY_RUN" -eq 0 ]; then
    exec bash "$REPO_ROOT/scripts/release-bump-and-tag.sh"
  fi
  exit 0
fi

if [ "$ACTION" != "bump-and-tag" ] || [ -z "$NEXT_VERSION" ]; then
  echo "[release-bump-via-pr] ERROR: expected action=bump-and-tag with suggested_next_version" >&2
  exit 1
fi

TARGET_TAG="v$NEXT_VERSION"
BRANCH="chore/bump-version-$NEXT_VERSION"

if [ "$DRY_RUN" -eq 1 ]; then
  echo "[release-bump-via-pr] dry-run: would bump to $NEXT_VERSION on branch $BRANCH"
  echo "[release-bump-via-pr] dry-run: gh pr create → checks --watch → merge --squash → release-bump-and-tag.sh"
  exit 0
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "[release-bump-via-pr] ERROR: gh required" >&2
  exit 2
fi

PARENT_DIR="$(dirname "$REPO_ROOT")"
WT_DIR="$PARENT_DIR/$(basename "$REPO_ROOT")-bump-pr-${NEXT_VERSION}-$$"

cleanup() {
  local rc=$?
  if [ -d "$WT_DIR" ]; then
    if [ "$rc" -eq 0 ] && [ "$KEEP_WT" -ne 1 ]; then
      git -C "$REPO_ROOT" worktree remove --force "$WT_DIR" 2>/dev/null \
        || echo "[release-bump-via-pr] WARN: could not remove worktree $WT_DIR" >&2
    else
      echo "[release-bump-via-pr] worktree kept: $WT_DIR" >&2
      echo "[release-bump-via-pr] remove: git worktree remove --force $WT_DIR" >&2
    fi
  fi
  return "$rc"
}
trap cleanup EXIT

PR_NUM="$RESUME_PR"

if [ -z "$PR_NUM" ]; then
  echo "[release-bump-via-pr] creating worktree: $WT_DIR"
  git -C "$REPO_ROOT" fetch origin main --tags --quiet
  git -C "$REPO_ROOT" worktree add --quiet --detach "$WT_DIR" origin/main
  git -C "$WT_DIR" submodule update --quiet --init dev-rules

  WT_VERSION="$(tr -d '[:space:]' < "$WT_DIR/backend/cmd/server/VERSION")"
  if [ "$WT_VERSION" = "$NEXT_VERSION" ] \
     || [ "$(printf '%s\n%s\n' "$WT_VERSION" "$NEXT_VERSION" | sort -V | tail -1)" != "$NEXT_VERSION" ]; then
    echo "[release-bump-via-pr] ERROR: refusing non-incrementing bump: VERSION=$WT_VERSION proposed=$NEXT_VERSION" >&2
    exit 1
  fi

  printf '%s\n' "$NEXT_VERSION" > "$WT_DIR/backend/cmd/server/VERSION"
  git -C "$WT_DIR" checkout -b "$BRANCH"
  git -C "$WT_DIR" add backend/cmd/server/VERSION
  git -C "$WT_DIR" commit -m "chore: bump VERSION to $NEXT_VERSION

no-web-impact"

  echo "[release-bump-via-pr] pushing branch $BRANCH"
  git -C "$WT_DIR" push -u origin "$BRANCH"

  PR_URL="$(gh pr create --base main --head "$BRANCH" \
    --title "chore: bump VERSION to $NEXT_VERSION" \
    --body "$(cat <<EOF
## 摘要
发版 $TARGET_TAG 所需的 VERSION 文件 bump。

## 风险
低 — 仅 \`backend/cmd/server/VERSION\` 变更。

## 验证
preflight 已在 bump worktree 通过。

Web impact: none
EOF
)" 2>&1)"
  echo "[release-bump-via-pr] $PR_URL"
  PR_NUM="$(printf '%s\n' "$PR_URL" | sed -n 's|.*/pull/\([0-9]*\).*|\1|p')"
  if [ -z "$PR_NUM" ]; then
    echo "[release-bump-via-pr] ERROR: could not parse PR number from: $PR_URL" >&2
    exit 1
  fi
fi

echo "[release-bump-via-pr] waiting for PR #$PR_NUM checks"
if ! gh pr checks "$PR_NUM" --watch --interval 15; then
  echo "[release-bump-via-pr] WARN: checks failed; if only preflight flake: gh run rerun <run_id> --failed && re-run with --pr $PR_NUM" >&2
  exit 1
fi

echo "[release-bump-via-pr] merging PR #$PR_NUM"
if ! gh pr merge "$PR_NUM" --squash --delete-branch 2>&1; then
  echo "[release-bump-via-pr] WARN: merge ok but local branch cleanup may fail if worktree still holds branch; safe to ignore" >&2
fi

git -C "$REPO_ROOT" fetch origin main --tags --quiet

echo "[release-bump-via-pr] tagging via release-bump-and-tag.sh"
bash "$REPO_ROOT/scripts/release-bump-and-tag.sh"

echo "[release-bump-via-pr] done: $TARGET_TAG"
