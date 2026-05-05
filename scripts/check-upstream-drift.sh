#!/usr/bin/env bash
# check-upstream-drift.sh — Mechanical enforcement of CLAUDE.md §5.y
#
# Reports whether upstream/main has commits not yet merged into the TK fork
# and prints the full §5.y merge procedure when drift is detected.
#
# Usage:
#   bash scripts/check-upstream-drift.sh           # human-readable
#   bash scripts/check-upstream-drift.sh --json    # JSON for CI consumption
#   bash scripts/check-upstream-drift.sh --quiet   # exit code only (no output)
#
# Exit codes:
#   0 — TK fork is in sync (origin/main contains all of upstream/main)
#   1 — upstream is ahead (one or more upstream commits not yet merged)
#   2 — git/network failure (cannot fetch upstream or origin)
#
# Dependencies: git, with the `upstream` remote pointing at
# https://github.com/Wei-Shaw/sub2api.git (set up by README onboarding).
# CI environments without an `upstream` remote will get one auto-added.

set -euo pipefail

MODE="human"

for arg in "$@"; do
  case "$arg" in
    --json)  MODE="json" ;;
    --quiet) MODE="quiet" ;;
    -h|--help)
      sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

log() { [ "$MODE" = "quiet" ] && return; [ "$MODE" = "json" ] && return; echo "$@"; }
err() { echo "$@" >&2; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/upstream-drift.sh
source "$SCRIPT_DIR/lib/upstream-drift.sh"

# Fetch both, fail loudly on network/auth errors.
if ! fetch_and_load_upstream_drift_snapshot; then
  exit 2
fi

BEHIND="$TK_BEHIND"
AHEAD="$TK_AHEAD"

if [ "$MODE" = "json" ]; then
  printf '{"behind":%d,"ahead":%d,"upstream_head":"%s","origin_head":"%s","in_sync":%s}\n' \
    "$BEHIND" "$AHEAD" "$UPSTREAM_HEAD" "$ORIGIN_HEAD" \
    "$([ "$BEHIND" -eq 0 ] && echo true || echo false)"
elif [ "$MODE" = "human" ]; then
  log "Upstream:  Wei-Shaw/sub2api@$UPSTREAM_HEAD"
  log "TK fork:   origin/main@$ORIGIN_HEAD"
  log "TK ahead:  $AHEAD commits"
  log "TK behind: $BEHIND commits"
fi

if [ "$BEHIND" -eq 0 ]; then
  log ""
  log "TK fork is in sync with upstream/main."
  exit 0
fi

if [ "$MODE" = "human" ]; then
  log ""
  log "Upstream has $BEHIND new commits not yet merged into TK fork:"
  log ""
  git log --oneline origin/main..upstream/main | head -20 | sed 's/^/  /'
  if [ "$BEHIND" -gt 20 ]; then
    log "  ... ($((BEHIND - 20)) more)"
  fi
  log ""
  log "Next steps (per CLAUDE.md §5.y):"
  log ""
  log "  bash scripts/prepare-upstream-merge.sh    # fetch + dry-run + branch prep"
  log ""
  log "  # then on the prepared branch:"
  log "  git merge --no-ff upstream/main            # MUST be --no-ff, never --squash"
  log "  make test                                  # backend + frontend gates"
  log "  git push -u origin merge/upstream-\$(date +%Y-%m-%d)"
  log "  gh pr create --base main --title 'chore: merge upstream/main (...)'"
  log ""
  log "  # PR body MUST include the §5.y audit cadence:"
  log "  #   git log --oneline upstream/main..HEAD | wc -l"
  log "  #   git diff --stat upstream/main..HEAD -- backend/ | head -5"
  log ""
  log "  # Reviewer MUST pick GitHub 'Create a merge commit' (not Squash)."
fi

exit 1
