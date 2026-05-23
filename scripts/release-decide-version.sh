#!/usr/bin/env bash
# release-decide-version.sh — Mechanical 3-state version/tag decision for the
# tokenkey-stage0-release-rollout skill §"决策：要不要升 patch 版本".
#
# Reads backend/cmd/server/VERSION (after main is synced with origin) and the
# remote tag list, then emits one of three actions:
#
#   action=tag-only           VERSION already on main + tag does NOT exist on origin.
#                             Operator should run scripts/release-tag.sh v<VERSION>.
#   action=bump-and-tag       Tag v<VERSION> exists on origin AND origin/main has
#                             newer commits than that tag. Operator must bump
#                             VERSION (patch+1), commit "chore: bump VERSION...",
#                             push, then re-run this script.
#   action=skip-bump-skip-tag origin/main == tag v<VERSION> commit. Image already
#                             built; no new release needed. Caller may proceed
#                             directly to deploy/dispatch with this tag.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - Input = local file (VERSION) + remote git state (read-only).
#   - Output = stable key=value lines on stdout; nothing on stderr unless error.
#   - No side effects with the default mode. --emit-suggested-bump prints the
#     would-be next patch tag for action=bump-and-tag (still no writes).
#
# Output (always on stdout, parseable with grep+cut):
#   action=tag-only|bump-and-tag|skip-bump-skip-tag
#   current_version=<X.Y.Z>           # from backend/cmd/server/VERSION
#   current_tag=v<X.Y.Z>
#   tag_on_origin=<true|false>
#   main_synced_with_tag=<true|false> # only meaningful when tag_on_origin=true
#   suggested_next_version=<X.Y.Z>    # only emitted with --emit-suggested-bump
#   reason=<one-line free text>
#
# Stderr is reserved for fatal errors (network, missing files, etc.).
#
# Exit codes:
#   0 — decision printed (any of the 3 actions)
#   1 — local invariant failure (missing file, malformed VERSION)
#   2 — git/network failure
set -euo pipefail

EMIT_NEXT=0
FETCH_QUIET=1
while [ "$#" -gt 0 ]; do
  case "$1" in
    --emit-suggested-bump) EMIT_NEXT=1; shift ;;
    --verbose-fetch) FETCH_QUIET=0; shift ;;
    -h|--help)
      sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "[release-decide-version] ERROR: unknown arg: $1" >&2; exit 1 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION_FILE="$REPO_ROOT/backend/cmd/server/VERSION"

if [ ! -f "$VERSION_FILE" ]; then
  echo "[release-decide-version] ERROR: VERSION file not found: $VERSION_FILE" >&2
  exit 1
fi

CURRENT_VERSION=$(tr -d '[:space:]' < "$VERSION_FILE")
if ! [[ "$CURRENT_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "[release-decide-version] ERROR: VERSION not X.Y.Z: '$CURRENT_VERSION'" >&2
  exit 1
fi
CURRENT_TAG="v$CURRENT_VERSION"

# Fetch tags + origin/main quietly; surface failure as exit 2
if [ "$FETCH_QUIET" -eq 1 ]; then
  git -C "$REPO_ROOT" fetch origin main --tags --quiet 2>/dev/null || {
    echo "[release-decide-version] ERROR: git fetch origin main --tags failed" >&2
    exit 2
  }
else
  git -C "$REPO_ROOT" fetch origin main --tags || {
    echo "[release-decide-version] ERROR: git fetch failed" >&2
    exit 2
  }
fi

# Does the tag exist on origin?
TAG_ON_ORIGIN="false"
if git -C "$REPO_ROOT" ls-remote --tags origin "refs/tags/$CURRENT_TAG" \
     2>/dev/null | grep -q "refs/tags/$CURRENT_TAG"; then
  TAG_ON_ORIGIN="true"
fi

ACTION=""
MAIN_SYNCED="false"
REASON=""

if [ "$TAG_ON_ORIGIN" = "false" ]; then
  ACTION="tag-only"
  REASON="origin lacks $CURRENT_TAG; VERSION file already at $CURRENT_VERSION on main; run scripts/release-tag.sh"
else
  # Tag exists. Compare its commit SHA with origin/main.
  TAG_SHA=$(git -C "$REPO_ROOT" rev-parse "refs/tags/$CURRENT_TAG^{commit}" 2>/dev/null || true)
  MAIN_SHA=$(git -C "$REPO_ROOT" rev-parse origin/main 2>/dev/null || true)
  if [ -z "$TAG_SHA" ] || [ -z "$MAIN_SHA" ]; then
    echo "[release-decide-version] ERROR: could not resolve tag/main SHAs" >&2
    exit 2
  fi
  if [ "$TAG_SHA" = "$MAIN_SHA" ]; then
    ACTION="skip-bump-skip-tag"
    MAIN_SYNCED="true"
    REASON="origin/main == $CURRENT_TAG ($TAG_SHA); image $CURRENT_VERSION already released"
  else
    ACTION="bump-and-tag"
    MAIN_SYNCED="false"
    REASON="origin has $CURRENT_TAG but origin/main ($MAIN_SHA) != tag ($TAG_SHA); must bump VERSION before tagging"
  fi
fi

printf 'action=%s\n' "$ACTION"
printf 'current_version=%s\n' "$CURRENT_VERSION"
printf 'current_tag=%s\n' "$CURRENT_TAG"
printf 'tag_on_origin=%s\n' "$TAG_ON_ORIGIN"
printf 'main_synced_with_tag=%s\n' "$MAIN_SYNCED"

if [ "$EMIT_NEXT" -eq 1 ] && [ "$ACTION" = "bump-and-tag" ]; then
  MAJOR=$(printf '%s\n' "$CURRENT_VERSION" | cut -d. -f1)
  MINOR=$(printf '%s\n' "$CURRENT_VERSION" | cut -d. -f2)
  PATCH=$(printf '%s\n' "$CURRENT_VERSION" | cut -d. -f3)
  NEXT_PATCH=$((PATCH + 1))
  printf 'suggested_next_version=%s.%s.%s\n' "$MAJOR" "$MINOR" "$NEXT_PATCH"
fi

printf 'reason=%s\n' "$REASON"
