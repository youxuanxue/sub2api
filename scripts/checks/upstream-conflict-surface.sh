#!/usr/bin/env bash
# upstream-conflict-surface.sh — conflict-surface DELTA gate against upstream.
#
# Mechanizes the missing axis of CLAUDE.md §5 upstream discipline: the existing
# gates protect WHAT a PR deletes (upstream-override-marker.py), the SHAPE of
# the upstream-merge PR itself (upstream-merge-pr-shape.yml) and main's history
# (main-ancestry-guard.yml) — but none of them answers "does THIS PR make the
# next `git merge upstream/main` more painful?". Historically that pain was
# discovered reactively, as post-merge compile-fix commits.
#
# Semantics
#   conflicts(X) = set of file paths reported conflicted by
#       git merge-tree --write-tree --name-only --no-messages <upstream-ref> <X>
#   NEW      = conflicts(head) − conflicts(base)   ← the gated delta
#   RESOLVED = conflicts(base) − conflicts(head)   ← informational credit
#
# A PR whose NEW set is non-empty is buying conflicts for the next upstream
# merge. Either restructure it (companion *_tk_*.go / *.tk.ts file, pure
# append — see CLAUDE.md §5 minimal-invasion patterns) or accept the cost
# explicitly via the PR-body marker `upstream-conflict-surface-accepted`
# (marker handling lives in .github/workflows/upstream-conflict-surface.yml;
# this script stays a pure set computation).
#
# git version note: requires `git merge-tree --write-tree` (git >= 2.38).
# Output contract verified on git 2.52: first line is the merged tree OID,
# subsequent lines are the deduplicated conflicted paths; exit 0 = clean,
# exit 1 = conflicts. CAUTION: merge-tree ALSO exits 1 on "not something we
# can merge", so refs are resolved up front and the first output line is
# validated to be a tree OID — anything else fails loud with exit 2.
#
# Usage:
#   scripts/checks/upstream-conflict-surface.sh \
#     [--upstream-ref upstream/main] [--base origin/main] [--head HEAD] \
#     [--root <repo-dir>] [--summary-file <markdown-file-to-append>]
#
# Exit codes:
#   0 — no new conflict files introduced by head relative to base
#   1 — at least one NEW conflict file (head conflicts that base does not have)
#   2 — usage error, unresolvable ref, or git failure

set -euo pipefail

DEFAULT_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ROOT="$DEFAULT_ROOT"
UPSTREAM_REF="upstream/main"
BASE_REF="origin/main"
HEAD_REF="HEAD"
SUMMARY_FILE=""

usage() {
  sed -n '2,40p' "$0" | sed -n '/^# /s/^# //p'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --root|--upstream-ref|--base|--head|--summary-file)
      if [ $# -lt 2 ]; then
        echo "FAIL: $1 requires a value" >&2
        exit 2
      fi
      case "$1" in
        --root) ROOT="$2" ;;
        --upstream-ref) UPSTREAM_REF="$2" ;;
        --base) BASE_REF="$2" ;;
        --head) HEAD_REF="$2" ;;
        --summary-file) SUMMARY_FILE="$2" ;;
      esac
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "FAIL: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [ ! -d "$ROOT" ]; then
  echo "FAIL: --root '$ROOT' is not a directory" >&2
  exit 2
fi

resolve_commit() {
  # Resolve a ref to a commit SHA up front. merge-tree exits 1 (same as
  # "conflicts") on unresolvable refs, so this MUST happen before merge-tree.
  local ref="$1" sha
  if ! sha=$(git -C "$ROOT" rev-parse --verify --quiet "${ref}^{commit}"); then
    echo "FAIL: cannot resolve '${ref}' to a commit in ${ROOT}" >&2
    echo "FAIL: (for upstream refs: run 'git fetch upstream main' first — never skip silently)" >&2
    exit 2
  fi
  printf '%s\n' "$sha"
}

UPSTREAM_SHA="$(resolve_commit "$UPSTREAM_REF")"
BASE_SHA="$(resolve_commit "$BASE_REF")"
HEAD_SHA="$(resolve_commit "$HEAD_REF")"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/upstream-conflict-surface.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

conflict_files() {
  # $1 = commit SHA to merge with upstream; $2 = output file (sorted, unique
  # conflicted paths, one per line). --name-only + --no-messages keeps the
  # output locale-independent: first line tree OID, rest conflicted paths.
  local sha="$1" out="$2" raw rc first
  set +e
  raw=$(git -C "$ROOT" merge-tree --write-tree --name-only --no-messages \
        "$UPSTREAM_SHA" "$sha" 2>"$TMP/merge-tree.err")
  rc=$?
  set -e
  if [ "$rc" -gt 1 ]; then
    echo "FAIL: git merge-tree failed (exit $rc) for $UPSTREAM_SHA x $sha" >&2
    sed 's/^/  merge-tree: /' "$TMP/merge-tree.err" >&2 || true
    exit 2
  fi
  first=$(printf '%s\n' "$raw" | head -n 1)
  if ! printf '%s' "$first" | grep -qE '^[0-9a-f]{40,64}$'; then
    # merge-tree exits 1 both for conflicts AND for unmergeable input; only a
    # leading tree OID proves a real merge computation happened.
    echo "FAIL: unexpected merge-tree output (no tree OID on first line) for $UPSTREAM_SHA x $sha" >&2
    sed 's/^/  merge-tree: /' "$TMP/merge-tree.err" >&2 || true
    exit 2
  fi
  printf '%s\n' "$raw" | tail -n +2 | sed '/^$/d' | LC_ALL=C sort -u > "$out"
}

conflict_files "$BASE_SHA" "$TMP/base.txt"
conflict_files "$HEAD_SHA" "$TMP/head.txt"

LC_ALL=C comm -13 "$TMP/base.txt" "$TMP/head.txt" > "$TMP/new.txt"
LC_ALL=C comm -23 "$TMP/base.txt" "$TMP/head.txt" > "$TMP/resolved.txt"
LC_ALL=C comm -12 "$TMP/base.txt" "$TMP/head.txt" > "$TMP/preexisting.txt"

count() { wc -l < "$1" | tr -d '[:space:]'; }
N_BASE="$(count "$TMP/base.txt")"
N_HEAD="$(count "$TMP/head.txt")"
N_NEW="$(count "$TMP/new.txt")"
N_RESOLVED="$(count "$TMP/resolved.txt")"

short() { printf '%s' "$1" | cut -c1-12; }

echo "[upstream-conflict-surface] upstream=${UPSTREAM_REF}@$(short "$UPSTREAM_SHA") base=$(short "$BASE_SHA") head=$(short "$HEAD_SHA")"
echo "  conflicts(upstream x base): $N_BASE file(s)"
echo "  conflicts(upstream x head): $N_HEAD file(s)"
echo "  new conflict files (head only): $N_NEW"
echo "  resolved conflict files (base only): $N_RESOLVED"

if [ "$N_NEW" -gt 0 ]; then
  echo ""
  echo "  NEW conflict files introduced by this change:"
  sed 's/^/    + /' "$TMP/new.txt"
fi
if [ "$N_RESOLVED" -gt 0 ]; then
  echo ""
  echo "  conflict files resolved by this change (informational):"
  sed 's/^/    - /' "$TMP/resolved.txt"
fi

if [ -n "$SUMMARY_FILE" ]; then
  {
    echo "## Upstream conflict surface"
    echo ""
    echo "| metric | value |"
    echo "|---|---|"
    echo "| upstream | \`${UPSTREAM_REF}\` @ \`$(short "$UPSTREAM_SHA")\` |"
    echo "| base \`$(short "$BASE_SHA")\` conflicts | $N_BASE |"
    echo "| head \`$(short "$HEAD_SHA")\` conflicts | $N_HEAD |"
    echo "| **new conflict files** | **$N_NEW** |"
    echo "| resolved conflict files | $N_RESOLVED |"
    echo ""
    if [ "$N_NEW" -gt 0 ] || [ "$N_RESOLVED" -gt 0 ]; then
      echo "| file | status |"
      echo "|---|---|"
      while IFS= read -r f; do
        echo "| \`$f\` | NEW conflict |"
      done < "$TMP/new.txt"
      while IFS= read -r f; do
        echo "| \`$f\` | resolved |"
      done < "$TMP/resolved.txt"
      # Pre-existing conflicts are background noise for THIS PR; cap the rows.
      head -n 20 "$TMP/preexisting.txt" | while IFS= read -r f; do
        echo "| \`$f\` | pre-existing |"
      done
      N_PRE="$(count "$TMP/preexisting.txt")"
      if [ "$N_PRE" -gt 20 ]; then
        echo "| _... and $((N_PRE - 20)) more pre-existing_ | pre-existing |"
      fi
      echo ""
    fi
    if [ "$N_NEW" -gt 0 ]; then
      echo "> :warning: This PR introduces **$N_NEW** new conflict file(s) against \`${UPSTREAM_REF}\`."
      echo "> Restructure via companion \`*_tk_*.go\` / \`*.tk.ts\` files or pure appends (CLAUDE.md §5),"
      echo "> or accept explicitly with the PR-body marker \`upstream-conflict-surface-accepted\`."
    else
      echo "> :white_check_mark: No new conflict files against \`${UPSTREAM_REF}\`."
    fi
    echo ""
  } >> "$SUMMARY_FILE"
fi

if [ "$N_NEW" -gt 0 ]; then
  echo ""
  echo "FAIL: this change introduces $N_NEW new conflict file(s) against ${UPSTREAM_REF}." >&2
  echo "      The next 'git merge upstream/main' will fight these files. Prefer a" >&2
  echo "      companion *_tk_*.go / *.tk.ts file or a pure append (CLAUDE.md §5);" >&2
  echo "      to accept the cost, add the literal marker" >&2
  echo "        upstream-conflict-surface-accepted" >&2
  echo "      to the PR body (CI downgrades the failure to a warning)." >&2
  exit 1
fi

echo "ok: no new upstream conflict files ($N_HEAD total, all pre-existing at base)"
