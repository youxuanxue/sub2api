#!/usr/bin/env bash
# release-bump-and-tag.sh — Single mechanical entry for the whole
# VERSION/tag release step, isolated from the operator's checkout.
#
# Why worktree isolation is the DEFAULT (not an option): the primary checkout
# is shared with parallel agents that may switch branches or dirty the tree at
# any moment (three recorded incidents). Bumping VERSION "in place" risks
# committing the release bump onto someone else's feature branch, and the
# pre-commit preflight scans the whole working tree, so unrelated WIP blocks a
# clean VERSION-only commit. This script never touches the calling checkout:
# all writes happen in an ephemeral sibling worktree created from origin/main.
#
# Flow (driven by scripts/release-decide-version.sh):
#   action=skip-bump-skip-tag → nothing to do; exit 0 (deploy the existing tag).
#   action=tag-only           → tag origin/main from an ephemeral worktree.
#   action=bump-and-tag       → in the worktree: write VERSION, commit
#                               "chore: bump VERSION to X.Y.Z" (pre-commit
#                               preflight runs against the clean worktree),
#                               push origin HEAD:main, then tag.
#
# Tagging always delegates to scripts/release-tag.sh (the single tag gate:
# skip-ci / VERSION match / HEAD==origin/main / changelog body).
#
# Usage:
#   bash scripts/release-bump-and-tag.sh             # decide + bump if needed + tag
#   bash scripts/release-bump-and-tag.sh --dry-run   # print the decision + plan, no writes
#   bash scripts/release-bump-and-tag.sh --keep-worktree  # keep the worktree for inspection
#
# Exit codes:
#   0 — released (tag pushed) or nothing to do (skip-bump-skip-tag)
#   1 — validation/push failure (worktree kept; path printed for inspection)
#   2 — git/network failure
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN=0
KEEP_WT=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --keep-worktree) KEEP_WT=1; shift ;;
    -h|--help) sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "[release-bump-and-tag] ERROR: unknown arg: $1" >&2; exit 1 ;;
  esac
done

decide() { bash "$REPO_ROOT/scripts/release-decide-version.sh" --emit-suggested-bump; }
field() { printf '%s\n' "$1" | grep "^$2=" | head -1 | cut -d= -f2-; }

DECISION="$(decide)"
ACTION="$(field "$DECISION" action)"
CURRENT_TAG="$(field "$DECISION" current_tag)"
NEXT_VERSION="$(field "$DECISION" suggested_next_version)"

echo "[release-bump-and-tag] $(field "$DECISION" reason)"

if [ "$ACTION" = "skip-bump-skip-tag" ]; then
  echo "[release-bump-and-tag] nothing to release; deploy existing tag $CURRENT_TAG"
  exit 0
fi

case "$ACTION" in
  tag-only) TARGET_TAG="$CURRENT_TAG" ;;
  bump-and-tag)
    if [ -z "$NEXT_VERSION" ]; then
      echo "[release-bump-and-tag] ERROR: decide script gave no suggested_next_version" >&2
      exit 1
    fi
    TARGET_TAG="v$NEXT_VERSION"
    ;;
  *) echo "[release-bump-and-tag] ERROR: unknown action '$ACTION'" >&2; exit 1 ;;
esac

if [ "$DRY_RUN" -eq 1 ]; then
  echo "[release-bump-and-tag] dry-run: action=$ACTION target_tag=$TARGET_TAG"
  echo "[release-bump-and-tag] dry-run: would create ephemeral worktree from origin/main, $( [ "$ACTION" = bump-and-tag ] && echo 'bump VERSION + push origin HEAD:main, ' )then run release-tag.sh $TARGET_TAG"
  exit 0
fi

# Sibling placement keeps backend's `replace ../../new-api` resolvable from the
# worktree, so any preflight/build step behaves exactly like the main checkout.
PARENT_DIR="$(dirname "$REPO_ROOT")"
WT_DIR="$PARENT_DIR/$(basename "$REPO_ROOT")-release-${TARGET_TAG#v}-$$"

cleanup() {
  local rc=$?
  if [ -d "$WT_DIR" ]; then
    if [ "$rc" -eq 0 ] && [ "$KEEP_WT" -ne 1 ]; then
      # --force: the initialized dev-rules submodule blocks a plain remove.
      git -C "$REPO_ROOT" worktree remove --force "$WT_DIR" 2>/dev/null \
        || echo "[release-bump-and-tag] WARN: could not remove worktree $WT_DIR" >&2
    else
      echo "[release-bump-and-tag] worktree kept for inspection: $WT_DIR" >&2
      echo "[release-bump-and-tag] remove later: git worktree remove --force $WT_DIR" >&2
    fi
  fi
  return "$rc"
}
trap cleanup EXIT

echo "[release-bump-and-tag] creating ephemeral worktree: $WT_DIR (detached at origin/main)"
git -C "$REPO_ROOT" fetch origin main --tags --quiet || { echo "[release-bump-and-tag] ERROR: fetch failed" >&2; exit 2; }
git -C "$REPO_ROOT" worktree add --detach "$WT_DIR" origin/main --quiet
# preflight (pre-commit hook) delegates generic checks to the dev-rules submodule.
git -C "$WT_DIR" submodule update --init dev-rules --quiet

if [ "$ACTION" = "bump-and-tag" ]; then
  printf '%s\n' "$NEXT_VERSION" > "$WT_DIR/backend/cmd/server/VERSION"
  git -C "$WT_DIR" add backend/cmd/server/VERSION
  # NOTE: the commit body must never contain the bracketed skip-ci marker
  # (CLAUDE.md §9.2); release-tag.sh re-validates this before tagging.
  git -C "$WT_DIR" commit -m "chore: bump VERSION to $NEXT_VERSION

no-web-impact"
  echo "[release-bump-and-tag] pushing bump commit to origin/main"
  if ! git -C "$WT_DIR" push origin HEAD:main; then
    echo "[release-bump-and-tag] ERROR: push rejected — origin/main moved since the worktree was created." >&2
    echo "                       Re-run this script; it will re-base the bump on the new origin/main." >&2
    exit 1
  fi
fi

echo "[release-bump-and-tag] tagging $TARGET_TAG via release-tag.sh"
( cd "$WT_DIR" && bash scripts/release-tag.sh "$TARGET_TAG" )

echo "[release-bump-and-tag] done: $TARGET_TAG pushed; release.yml should fire within seconds."
