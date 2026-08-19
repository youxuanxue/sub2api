#!/usr/bin/env bash
set -euo pipefail

# Collect the Feishu prod-rollout changelog for v$PREV..v$NEW.
#
# Why this exists:
#   deploy-stage0.yml checks out with fetch-depth: 1, then #1740 switched notes
#   from the annotated tag body to `git log A..B`. Fetching only the two tag
#   tips does not unshallow the clone, so merge-base fails and the log collapses
#   to the VERSION bump (then filtered to empty). The card then omits「本次更新」.
#
#   This script is the single collection path: deepen until the range is
#   walkable, take first-parent subjects, fall back to the annotated tag body
#   if the log is still empty, and warn when both are empty.
#
# Usage:
#   bash ops/stage0/collect-feishu-release-notes.sh <previous-tag> <new-tag>
#
#   Tags may include or omit the leading v. stdout is the notes (possibly
#   empty). Diagnostics go to stderr. Exit 2 on usage errors; exit 0 otherwise
#   so a missing changelog never reddens the best-effort notify step.
#
# Requires: git. Network only when the current clone cannot walk the range.

usage() {
  cat <<'EOF'
Usage:
  bash ops/stage0/collect-feishu-release-notes.sh <previous-tag> <new-tag>
EOF
}

PREV="${1:-}"
NEW="${2:-}"
if [ -z "$PREV" ] || [ -z "$NEW" ]; then
  echo "[error] <previous-tag> and <new-tag> are required" >&2
  usage >&2
  exit 2
fi
if [ "${3:-}" != "" ]; then
  echo "[error] unexpected extra arg: $3" >&2
  usage >&2
  exit 2
fi

PREV="${PREV#v}"
NEW="${NEW#v}"
if [[ ! "$PREV" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$ ]]; then
  echo "[error] previous-tag must be a Stage0 release tag" >&2
  exit 2
fi
if [[ ! "$NEW" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$ ]]; then
  echo "[error] new-tag must be a Stage0 release tag" >&2
  exit 2
fi

PREV_REF="v$PREV"
NEW_REF="v$NEW"
FILTER='bump VERSION|sync.version.file'

filter_noise() {
  grep -viE "$FILTER" || true  # preflight-allow: swallow -- no matching noise commits is valid
}

notes_from_log() {
  # preflight-allow: swallow -- unwalkable range falls through to tag body
  { git log --first-parent --pretty=format:'- %s' "$PREV_REF..$NEW_REF" || true; } \
    | filter_noise
}

notes_from_tag_body() {
  # Annotated tag body is the pre-#1740 source. Strip the "Release X.Y.Z"
  # subject that release-tag.sh prepends so the card only sees changelog lines.
  git tag -l --format='%(contents:body)' "$NEW_REF" 2>/dev/null \
    | sed '/^[[:space:]]*$/d' \
    | filter_noise
}

range_walkable() {
  git rev-parse --verify --quiet "$PREV_REF^{commit}" >/dev/null \
    && git rev-parse --verify --quiet "$NEW_REF^{commit}" >/dev/null \
    && git merge-base "$PREV_REF" "$NEW_REF" >/dev/null 2>&1
}

fetch_tag_tips() {
  if ! git remote get-url origin >/dev/null 2>&1; then
    echo "[collect-feishu-release-notes] no origin remote; skipping fetch" >&2
    return 1
  fi
  if git fetch origin \
    "refs/tags/${NEW_REF}:refs/tags/${NEW_REF}" \
    "refs/tags/${PREV_REF}:refs/tags/${PREV_REF}" \
    --force; then
    return 0
  fi
  echo "::warning::collect-feishu-release-notes: git fetch of $PREV_REF / $NEW_REF failed" >&2
  return 1
}

deepen_until_walkable() {
  if range_walkable; then
    return 0
  fi
  if [ "$(git rev-parse --is-shallow-repository 2>/dev/null || echo false)" != "true" ]; then
    return 1
  fi
  if ! git remote get-url origin >/dev/null 2>&1; then
    return 1
  fi

  echo "[collect-feishu-release-notes] shallow clone; unshallowing so $PREV_REF..$NEW_REF is walkable" >&2
  if git fetch --unshallow origin; then
    range_walkable && return 0
  fi

  local depth=64
  while [ "$depth" -le 1024 ]; do
    echo "[collect-feishu-release-notes] deepen=$depth" >&2
    git fetch --deepen="$depth" origin || true  # preflight-allow: swallow -- next depth or tag-body fallback remains
    if range_walkable; then
      return 0
    fi
    depth=$((depth * 2))
  done
  return 1
}

fetch_tag_tips || true  # preflight-allow: swallow -- notification remains best-effort
if ! range_walkable; then
  deepen_until_walkable || true  # preflight-allow: swallow -- empty notes omit 本次更新
fi

NOTES="$(notes_from_log | sed '/^[[:space:]]*$/d')"
SOURCE="git-log"
if [ -z "$NOTES" ]; then
  NOTES="$(notes_from_tag_body)"
  if [ -n "$NOTES" ]; then
    SOURCE="tag-body"
    echo "[collect-feishu-release-notes] git log empty after filter; falling back to annotated tag body" >&2
  fi
fi

LINE_COUNT=0
if [ -n "$NOTES" ]; then
  LINE_COUNT="$(printf '%s\n' "$NOTES" | grep -c . || true)"  # preflight-allow: swallow -- zero lines is a valid count
fi
echo "[collect-feishu-release-notes] source=$SOURCE lines=$LINE_COUNT range=$PREV_REF..$NEW_REF" >&2

if [ -z "$NOTES" ]; then
  echo "::warning::collect-feishu-release-notes: empty notes for $PREV_REF..$NEW_REF" >&2
fi

printf '%s' "$NOTES"
if [ -n "$NOTES" ]; then
  printf '\n'
fi
