#!/usr/bin/env bash
# release-tag.sh — Mechanical enforcement of CLAUDE.md §9.2.
#
# Wraps `git tag` for release tags so the [skip ci] failure mode that
# silently broke v1.3.0 cannot recur. Validates:
#
#   1. Argument is a valid release tag (vX.Y.Z, optional -rc.N / -beta.N).
#   2. Current HEAD commit message does NOT contain literal `[skip ci]`
#      or `[ci skip]` anywhere — otherwise GitHub will skip ALL workflows
#      responding to the resulting tag-push event, including release.yml,
#      and the only recovery is a manual `gh workflow run` dispatch.
#   3. backend/cmd/server/VERSION matches the tag (without the leading 'v').
#   4. Tag does not already exist locally or on origin.
#   5. We're on `main` and up to date with `origin/main`.
#
# Usage:
#   bash scripts/release-tag.sh v1.3.0
#   bash scripts/release-tag.sh v1.3.0 --message "Custom annotated tag message"
#   bash scripts/release-tag.sh v1.3.0 --no-push      # create tag locally only
#
# Exit codes:
#   0 — tag created (and pushed unless --no-push); release.yml will fire
#   1 — validation failure (commit / VERSION / tag conflict)
#   2 — git/network failure

set -euo pipefail

if [ "$#" -lt 1 ]; then
  sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
fi

TAG=""
MESSAGE=""
PUSH=1
while [ "$#" -gt 0 ]; do
  case "$1" in
    --message) MESSAGE="$2"; shift 2 ;;
    --no-push) PUSH=0; shift ;;
    -h|--help) sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    v*) TAG="$1"; shift ;;
    *) echo "ERROR: unknown arg or invalid tag (must start with v): $1" >&2; exit 1 ;;
  esac
done

if [ -z "$TAG" ]; then
  echo "ERROR: missing tag argument (e.g., v1.3.0)" >&2
  exit 1
fi

# 1. Tag format
if ! [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$ ]]; then
  echo "ERROR: tag '$TAG' is not vX.Y.Z (optionally -rc.N or -beta.N)" >&2
  exit 1
fi

VERSION_NUM="${TAG#v}"

# 2. [skip ci] check — the v1.3.0 incident
HEAD_MSG=$(git log -1 --format='%B')
if echo "$HEAD_MSG" | grep -qE '\[skip ci\]|\[ci skip\]'; then
  echo "ERROR: HEAD commit message contains literal '[skip ci]' / '[ci skip]'." >&2
  echo "" >&2
  echo "GitHub's [skip ci] detector matches this substring anywhere in the" >&2
  echo "commit message regardless of context. Tagging this commit and pushing" >&2
  echo "the tag will silently skip release.yml — no image will be built and" >&2
  echo "prod/test stacks will go stale." >&2
  echo "" >&2
  echo "Per §9.2, only release.yml's own sync-version-file writeback may" >&2
  echo "carry [skip ci]. Fix options:" >&2
  echo "" >&2
  echo "  a) Reword the commit message (NEW commit; do not amend if pushed)." >&2
  echo "     Rephrase '[skip ci]' as 'skip-ci' or 'ci-skip' or remove entirely." >&2
  echo "" >&2
  echo "  b) If the commit is already on origin/main and you must release it" >&2
  echo "     unchanged, push the tag anyway and immediately recover via:" >&2
  echo "       gh workflow run release.yml -f tag=$TAG -f simple_release=false" >&2
  echo "     (this bypasses [skip ci] because workflow_dispatch is exempt)." >&2
  echo "" >&2
  echo "Offending lines:" >&2
  echo "$HEAD_MSG" | grep -nE '\[skip ci\]|\[ci skip\]' | sed 's/^/  /' >&2
  exit 1
fi

# 3. VERSION file must match
VERSION_FILE="backend/cmd/server/VERSION"
if [ ! -f "$VERSION_FILE" ]; then
  echo "ERROR: $VERSION_FILE not found" >&2
  exit 1
fi
FILE_VER=$(tr -d '[:space:]' < "$VERSION_FILE")
if [ "$FILE_VER" != "$VERSION_NUM" ]; then
  echo "ERROR: $VERSION_FILE = '$FILE_VER' but tag = '$TAG' (expected '$VERSION_NUM')" >&2
  echo "       Bump VERSION via PR + squash merge first, then re-run this script." >&2
  exit 1
fi

# 4. Tag must not already exist
if git rev-parse "$TAG" >/dev/null 2>&1; then
  echo "ERROR: tag $TAG already exists locally." >&2
  exit 1
fi
if git ls-remote --tags origin "$TAG" 2>/dev/null | grep -q "refs/tags/$TAG"; then
  echo "ERROR: tag $TAG already exists on origin." >&2
  exit 1
fi

# 5. Branch + sync check
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
if [ "$CURRENT_BRANCH" != "main" ]; then
  echo "ERROR: must tag from 'main' (current: $CURRENT_BRANCH)" >&2
  exit 1
fi
git fetch origin main --quiet 2>/dev/null || { echo "ERROR: cannot fetch origin" >&2; exit 2; }
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git rev-parse origin/main)
if [ "$LOCAL" != "$REMOTE" ]; then
  echo "ERROR: local main ($LOCAL) is not in sync with origin/main ($REMOTE)" >&2
  echo "       Pull or push first." >&2
  exit 1
fi

# All checks passed. Create the tag.
if [ -z "$MESSAGE" ]; then
  MESSAGE="Release $TAG"
fi

echo "Creating annotated tag $TAG -> $LOCAL"
git tag -a "$TAG" -m "$MESSAGE"

if [ "$PUSH" -eq 1 ]; then
  echo "Pushing tag to origin"
  git push origin "$TAG"
  echo ""
  echo "Tag pushed. release.yml should fire within seconds."
  echo "Monitor: gh run list --workflow=release.yml --limit 1"
  echo "         gh run watch \$(gh run list --workflow=release.yml --limit 1 --json databaseId -q '.[0].databaseId')"
else
  echo ""
  echo "Tag created locally (not pushed). To trigger release:"
  echo "  git push origin $TAG"
fi
