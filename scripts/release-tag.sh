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
#   5. HEAD is exactly origin/main (branch `main` in sync, or a detached
#      worktree HEAD created by release-bump-and-tag.sh).
#
# Changelog: when --message is NOT given, the annotated tag BODY is filled
# deterministically with the conventional-commit subjects since the previous
# release tag (first-parent = squash-merged PR titles). That body is the single
# source the whole release-notes pipeline reads:
#   release.yml  -> git tag '%(contents:body)' -> goreleaser header -> GitHub Release
#   deploy-stage0 -> same tag body -> Feishu rollout card
# An empty changelog (nothing but a VERSION bump in range) is REFUSED unless
# --allow-empty-changelog is passed, so a version can never ship with blank notes.
#
# Usage:
#   bash scripts/release-tag.sh v1.3.0
#   bash scripts/release-tag.sh v1.3.0 --message "Custom annotated tag message"
#   bash scripts/release-tag.sh v1.3.0 --no-push                 # create tag locally only
#   bash scripts/release-tag.sh v1.3.0 --dry-run                 # print the tag message, do NOT tag/push
#   bash scripts/release-tag.sh v1.3.0 --allow-empty-changelog   # permit a no-changelog release
#
# Exit codes:
#   0 — tag created (and pushed unless --no-push); release.yml will fire
#   1 — validation failure (commit / VERSION / tag conflict / empty changelog)
#   2 — git/network failure

set -euo pipefail

# print_usage — render the comment banner (lines 2-35) as help text. Single
# source for both the no-args error and -h/--help so the line range never drifts.
print_usage() { sed -n '2,35p' "$0" | sed 's/^# \{0,1\}//'; }

if [ "$#" -lt 1 ]; then
  print_usage
  exit 1
fi

TAG=""
MESSAGE=""
PUSH=1
DRY_RUN=0
ALLOW_EMPTY=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --message) MESSAGE="$2"; shift 2 ;;
    --no-push) PUSH=0; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --allow-empty-changelog) ALLOW_EMPTY=1; shift ;;
    -h|--help) print_usage; exit 0 ;;
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

# gen_changelog — deterministic changelog body for the annotated tag.
# Emits one `- <subject>` line per first-parent commit since the previous
# release tag (squash-merged PRs carry `type(scope): … (#NNN)` subjects, the
# universal interchange shape: GitHub auto-links #NNN, the Feishu card buckets
# by `type`). VERSION-bump and sync-version-file writebacks are dropped as noise.
gen_changelog() {
  local prev range
  # Merged upstream history carries its own, often closer v0.x tags.
  # Release boundaries belong to TokenKey's first-parent mainline only.
  prev=$(git describe --tags --first-parent --abbrev=0 HEAD 2>/dev/null || true)
  # If HEAD is already tagged (re-run, or --dry-run on a released commit), the
  # describe above returns that same tag; step back one commit for the real prev.
  if [ -n "$prev" ] && [ "$(git rev-parse "${prev}^{commit}")" = "$(git rev-parse HEAD)" ]; then
    prev=$(git describe --tags --first-parent --abbrev=0 HEAD^ 2>/dev/null || true)
  fi
  if [ -n "$prev" ]; then
    range="${prev}..HEAD"
  else
    range="HEAD"  # first release ever: take the whole history
  fi
  git log --first-parent --pretty=format:'- %s' "$range" \
    | grep -viE 'bump VERSION|sync.version.file' || true
}

# build_message — populate MESSAGE when the caller did not pass --message.
# Subject line + blank + changelog body so `git tag '%(contents:body)'` returns
# exactly the changelog. Refuses an empty changelog unless --allow-empty-changelog.
build_message() {
  [ -n "$MESSAGE" ] && return 0
  local body
  body=$(gen_changelog)
  if [ -z "$body" ] && [ "$ALLOW_EMPTY" -ne 1 ]; then
    echo "ERROR: no changelog entries since the previous release tag." >&2
    echo "       The annotated tag body would be empty, so the GitHub Release" >&2
    echo "       page and the Feishu rollout card would show no real changes." >&2
    echo "" >&2
    echo "  - If this release genuinely has no user-facing changes, re-run with" >&2
    echo "      --allow-empty-changelog" >&2
    echo "  - Otherwise pass an explicit body with --message \"…\"." >&2
    exit 1
  fi
  MESSAGE="$(printf 'Release %s\n\n%s\n' "$TAG" "$body")"
}

# --dry-run: print the tag message that WOULD be written, then stop. Skips the
# must-not-exist / branch-sync / network checks so it works on any historical
# commit (also used to regenerate backfill text — same code path = single source).
if [ "$DRY_RUN" -eq 1 ]; then
  build_message
  printf '%s\n' "$MESSAGE"
  exit 0
fi

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

# 5. Sync check: HEAD must be exactly origin/main. This is the real invariant
# behind the old "must be on branch main" rule — it additionally admits a
# detached worktree HEAD (release-bump-and-tag.sh) while still rejecting any
# feature branch that has diverged from origin/main.
git fetch origin main --quiet 2>/dev/null || { echo "ERROR: cannot fetch origin" >&2; exit 2; }
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git rev-parse origin/main)
if [ "$LOCAL" != "$REMOTE" ]; then
  CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
  echo "ERROR: HEAD ($LOCAL, branch: $CURRENT_BRANCH) is not origin/main ($REMOTE)." >&2
  echo "       Tags must point at the pushed origin/main tip. Pull/push first," >&2
  echo "       or use scripts/release-bump-and-tag.sh (worktree-isolated)." >&2
  exit 1
fi

# All checks passed. Build the message (auto-changelog unless --message given).
build_message

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
