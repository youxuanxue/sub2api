#!/usr/bin/env bash
# release-rollout-summary.sh — Shared post-rollout / post-merge / post-local
# summary helper. Replaces the bash chains that were duplicated across three
# SKILL.md files:
#
#   tokenkey-stage0-release-rollout §"完成后：rollout 摘要"
#   tokenkey-stage0-local-deploy §"完成后：当前代码与上一 tag 的变更摘要"
#   tokenkey-upstream-merge §6 "完成后：本次 upstream merge 变更摘要"
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - Same git state + same args → bit-identical markdown on stdout
#   - All git invocations are read-only; no fetch by default (--fetch opts in)
#
# Modes (--mode is required):
#   --mode release      base = previous v* tag, head = latest v* tag.
#                       Skip commits whose subject matches /chore: bump VERSION/
#                       or contain literal '[skip ci]' / '[ci skip]'.
#   --mode local        base = previous v* tag, head = HEAD (working tree
#                       reference). Same commit filters as release mode.
#   --mode upstream     base = merge-base(HEAD, upstream/main), head = HEAD.
#                       Adds an "upstream brought in" section comparing
#                       merge_base..upstream/main. Filters drop nothing.
#
# Overrides (rare): --base REF --head REF override the mode-derived range
# (e.g. release mode with a non-tag base). Useful for local testing.
#
# Output: a markdown report on stdout. Sections:
#   1. **Range** — base/head SHAs + commit count
#   2. **Commits** — `git log --oneline --no-merges` filtered per mode
#   3. **Top changed files** — `git diff --stat | head -10` for backend/ and frontend/src/
#   4. **Sentinel changes** — list `scripts/sentinels/*.json` paths changed
#   5. **Upstream file deletions** — `git diff --diff-filter=D` against base
#   6. **Upstream brought in** (upstream mode only)
#   7. **TK ahead count** (upstream mode only) — for PR body §5.y audit cadence
#
# Exit codes:
#   0 — summary written
#   1 — usage failure (missing/invalid mode, base/head unresolvable)
#   2 — git transport failure (only when --fetch used)
set -euo pipefail

MODE=""
BASE_OVERRIDE=""
HEAD_OVERRIDE=""
FETCH=0
SHOW_HELP=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode) MODE="${2:-}"; shift 2 ;;
    --base) BASE_OVERRIDE="${2:-}"; shift 2 ;;
    --head) HEAD_OVERRIDE="${2:-}"; shift 2 ;;
    --fetch) FETCH=1; shift ;;
    -h|--help) SHOW_HELP=1; shift ;;
    *) echo "[release-rollout-summary] ERROR: unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [ "$SHOW_HELP" -eq 1 ] || [ -z "$MODE" ]; then
  sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
  if [ -z "$MODE" ]; then exit 1; else exit 0; fi
fi

case "$MODE" in
  release|local|upstream) ;;
  *) echo "[release-rollout-summary] ERROR: --mode must be release|local|upstream" >&2; exit 1 ;;
esac

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if [ "$FETCH" -eq 1 ]; then
  case "$MODE" in
    upstream) git fetch upstream --quiet 2>/dev/null || { echo "[release-rollout-summary] ERROR: git fetch upstream failed" >&2; exit 2; } ;;
    *)        git fetch origin --tags --quiet 2>/dev/null || { echo "[release-rollout-summary] ERROR: git fetch origin failed" >&2; exit 2; } ;;
  esac
fi

# Resolve BASE / HEAD per mode (with overrides)
if [ -n "$BASE_OVERRIDE" ]; then
  BASE="$BASE_OVERRIDE"
elif [ "$MODE" = "upstream" ]; then
  if ! git rev-parse upstream/main >/dev/null 2>&1; then
    echo "[release-rollout-summary] ERROR: upstream/main not present locally; --fetch first or add upstream remote" >&2
    exit 1
  fi
  BASE=$(git merge-base HEAD upstream/main)
else
  # release / local: previous v* tag
  BASE=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)
  if [ -z "$BASE" ]; then
    echo "[release-rollout-summary] ERROR: no v* tag found; pass --base explicitly" >&2
    exit 1
  fi
fi

if [ -n "$HEAD_OVERRIDE" ]; then
  HEAD_REF="$HEAD_OVERRIDE"
elif [ "$MODE" = "release" ]; then
  # latest v* tag is the new tag; second-latest is base
  # If --base override unset, base above already took head=tag(latest). Recompute:
  if [ -z "$BASE_OVERRIDE" ]; then
    LATEST=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)
    PREVIOUS=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sed -n '2p')
    HEAD_REF="$LATEST"
    BASE="${PREVIOUS:-$LATEST}"
  else
    HEAD_REF="HEAD"
  fi
else
  HEAD_REF="HEAD"
fi

BASE_SHA=$(git rev-parse --short=12 "$BASE" 2>/dev/null) || {
  echo "[release-rollout-summary] ERROR: cannot resolve BASE=$BASE" >&2; exit 1; }
HEAD_SHA=$(git rev-parse --short=12 "$HEAD_REF" 2>/dev/null) || {
  echo "[release-rollout-summary] ERROR: cannot resolve HEAD=$HEAD_REF" >&2; exit 1; }

COMMIT_COUNT=$(git log --oneline "${BASE}..${HEAD_REF}" 2>/dev/null | wc -l | tr -d ' ')

# Per-mode commit filter (sed; pipe-safe)
filter_commits() {
  case "$MODE" in
    upstream) cat ;;
    *)        grep -v 'chore: bump VERSION' | grep -vE '\[skip ci\]|\[ci skip\]' ;;
  esac
}

# === Markdown ===
printf '## Summary (mode=%s)\n\n' "$MODE"
printf -- '- **Range**: `%s` → `%s` (%s commits)\n' "$BASE" "$HEAD_REF" "$COMMIT_COUNT"
printf -- '- BASE sha: `%s`  HEAD sha: `%s`\n\n' "$BASE_SHA" "$HEAD_SHA"

printf '### Commits\n\n'
printf '```\n'
git log "${BASE}..${HEAD_REF}" --oneline --no-merges 2>/dev/null | filter_commits || true
printf '```\n\n'

printf '### Top changed files (backend/, frontend/src/)\n\n'
printf '```\n'
git diff --stat "${BASE}..${HEAD_REF}" -- backend/ frontend/src/ 2>/dev/null | tail -11 || true
printf '```\n\n'

printf '### Sentinel changes\n\n'
SENTINELS=$(git diff --name-only "${BASE}..${HEAD_REF}" -- 'scripts/sentinels/*.json' 2>/dev/null || true)
if [ -n "$SENTINELS" ]; then
  printf '```\n%s\n```\n\n' "$SENTINELS"
else
  printf '_(no sentinel changes)_\n\n'
fi

printf '### Upstream file deletions (backend/)\n\n'
DELETIONS=$(git diff --diff-filter=D --name-only "${BASE}..${HEAD_REF}" -- backend/ 2>/dev/null || true)
if [ -n "$DELETIONS" ]; then
  printf '```\n%s\n```\n\n' "$DELETIONS"
else
  printf '_(no upstream-shaped deletions)_\n\n'
fi

if [ "$MODE" = "upstream" ]; then
  printf '### Upstream brought in (merge_base..upstream/main)\n\n'
  printf '```\n'
  git log "${BASE}..upstream/main" --oneline --no-merges 2>/dev/null | head -30 || true
  printf '```\n\n'

  TK_AHEAD=$(git log --oneline upstream/main..HEAD 2>/dev/null | wc -l | tr -d ' ')
  printf '### TK ahead count (PR body §5.y audit cadence)\n\n'
  printf '\`git log --oneline upstream/main..HEAD | wc -l\` = **%s**\n\n' "$TK_AHEAD"

  printf '### Backend diff stat vs upstream/main (PR body §5.y)\n\n'
  printf '```\n'
  git diff --stat upstream/main..HEAD -- backend/ 2>/dev/null | head -5 || true
  printf '```\n'
fi
