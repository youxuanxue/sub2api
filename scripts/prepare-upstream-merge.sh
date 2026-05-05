#!/usr/bin/env bash
# prepare-upstream-merge.sh — thin wrapper for the repo's upstream merge ritual.
#
# Usage:
#   bash scripts/prepare-upstream-merge.sh
#   bash scripts/prepare-upstream-merge.sh --dry-run
#   bash scripts/prepare-upstream-merge.sh --branch merge/upstream-2026-04-26
#
# Default mode:
#   - fetches origin/main and upstream/main
#   - previews whether merging upstream/main into origin/main would conflict
#   - writes a PR body draft under .git/ with the required §5.y audit cadence
#   - if clean, creates a fresh merge/upstream-YYYY-MM-DD branch from main
#
# --dry-run:
#   - performs the same fetch + merge preview + PR body draft generation
#   - exits non-zero only when the preview indicates conflicts or a precondition fails

set -euo pipefail

MODE="prepare"
BRANCH="merge/upstream-$(date -u +%Y-%m-%d)"
PR_TITLE=""

usage() {
  sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run)
      MODE="dry-run"
      shift
      ;;
    --branch)
      BRANCH="${2:-}"
      [ -n "$BRANCH" ] || { echo "ERROR: --branch requires a name" >&2; exit 1; }
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERROR: unknown arg '$1'" >&2
      exit 1
      ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/upstream-drift.sh
source "$SCRIPT_DIR/lib/upstream-drift.sh"

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "ERROR: must run inside the git repository" >&2
  exit 2
fi

if ! fetch_and_load_upstream_drift_snapshot; then
  exit 2
fi

BEHIND="$TK_BEHIND"
AHEAD="$TK_AHEAD"
PR_TITLE="chore: merge upstream/main (${BEHIND} commits) into TK fork"

printf 'Upstream:  Wei-Shaw/sub2api@%s\n' "$UPSTREAM_HEAD"
printf 'TK fork:   origin/main@%s\n' "$ORIGIN_HEAD"
printf 'TK ahead:  %s commits\n' "$AHEAD"
printf 'TK behind: %s commits\n' "$BEHIND"

if [ "$BEHIND" -eq 0 ]; then
  echo ""
  echo "TK fork is already in sync with upstream/main."
  exit 0
fi

BASE=$(git merge-base origin/main upstream/main)
MERGE_PREVIEW=$(git merge-tree "$BASE" origin/main upstream/main || true)
HAS_CONFLICTS=0
if printf '%s\n' "$MERGE_PREVIEW" | grep -q '<<<<<<<'; then
  HAS_CONFLICTS=1
fi

CONFLICT_HOTSPOTS=$(printf '%s\n' "$MERGE_PREVIEW" | awk '/^changed in both$/{getline; if ($0 != "") print $0}' | sed 's/^/  - /' || true)
if [ -z "$CONFLICT_HOTSPOTS" ]; then
  CONFLICT_HOTSPOTS="  - none in merge-tree preview"
fi

PR_BODY_FILE="$(git rev-parse --git-path "upstream-merge-pr-body-$(date -u +%Y%m%d).md")"
COMMITS_SAMPLE=$(git log --oneline origin/main..upstream/main | head -20 || true)
if [ -z "$COMMITS_SAMPLE" ]; then
  COMMITS_SAMPLE="(none)"
fi
BACKEND_DIFF_STAT=$(git diff --stat upstream/main..HEAD -- backend/ | head -5 || true)
if [ -z "$BACKEND_DIFF_STAT" ]; then
  BACKEND_DIFF_STAT="(no backend diff against upstream/main)"
fi
cat > "$PR_BODY_FILE" <<EOF
## Summary
- Merge upstream/main into TokenKey mainline on branch ${BRANCH}
- Preserve TK-specific guards: upstream merge shape, newapi sentinels, and brand sentinels
- Resolve any conflicts on this branch before opening the PR

## Audit cadence

    git log --oneline upstream/main..HEAD | wc -l
    git diff --stat upstream/main..HEAD -- backend/ | head -5

## Current drift snapshot
- upstream/main: ${UPSTREAM_HEAD}
- origin/main: ${ORIGIN_HEAD}
- TK ahead: ${AHEAD}
- TK behind: ${BEHIND}

## New upstream commits (sample)

    ${COMMITS_SAMPLE}

## Backend diff hot files

    ${BACKEND_DIFF_STAT}

## Conflict hotspots from merge-tree
${CONFLICT_HOTSPOTS}

## Validation
- [ ] git merge --no-ff upstream/main
- [ ] make test
- [ ] ./scripts/preflight.sh
- [ ] newapi sentinels intact
- [ ] brand sentinels intact
- [ ] Reviewer will pick GitHub Create a merge commit
EOF

echo ""
echo "Previewing merge of upstream/main into origin/main ..."
if [ "$HAS_CONFLICTS" -eq 1 ]; then
  echo "Potential conflicts detected in merge preview."
  echo ""
  printf '%s\n' "$MERGE_PREVIEW" | grep -nE '<<<<<<<|=======|>>>>>>>' || true
else
  echo "Preview is clean: no textual conflicts detected."
fi

echo "PR body draft: ${PR_BODY_FILE}"
echo "Suggested PR title: ${PR_TITLE}"

if [ "$MODE" = "dry-run" ]; then
  if [ "$HAS_CONFLICTS" -eq 1 ]; then
    exit 1
  fi
  exit 0
fi

CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
if [ "$CURRENT_BRANCH" != "main" ]; then
  echo "ERROR: switch to local main before preparing the upstream merge branch (current: $CURRENT_BRANCH)" >&2
  exit 1
fi

if ! git diff --quiet || ! git diff --cached --quiet || [ -n "$(git ls-files --others --exclude-standard)" ]; then
  echo "ERROR: working tree must be clean before preparing the upstream merge branch" >&2
  exit 1
fi

LOCAL_MAIN=$(git rev-parse main)
if [ "$LOCAL_MAIN" != "$(git rev-parse origin/main)" ]; then
  echo "ERROR: local main is not in sync with origin/main" >&2
  echo "       run: git pull --ff-only origin main" >&2
  exit 1
fi

if git show-ref --verify --quiet "refs/heads/${BRANCH}"; then
  echo "ERROR: branch already exists: ${BRANCH}" >&2
  echo "       choose a different --branch name or delete it manually after review" >&2
  exit 1
fi

if [ "$HAS_CONFLICTS" -eq 1 ]; then
  echo ""
  echo "Dry-run found conflicts, so no branch was created automatically."
  echo "If you want to resolve them now, create the branch manually and run:"
  echo "  git switch -c ${BRANCH} main"
  echo "  git merge --no-ff upstream/main"
  exit 1
fi

git switch -c "$BRANCH" main >/dev/null

echo ""
echo "Created branch: ${BRANCH}"
echo "Next steps:"
echo "  git merge --no-ff upstream/main"
echo "  make test"
echo "  ./scripts/preflight.sh"
echo "  git push -u origin ${BRANCH}"
echo "  gh pr create --base main --title '${PR_TITLE}' --body-file '${PR_BODY_FILE}'"
