#!/usr/bin/env bash
#
# Self-test for scripts/checks/script-ref-existence.py.
#
# Why this file exists:
# v1 of script-ref-existence.py (commit 5d6b378d) shipped with the prose
# claim "Self-smoke verified" but the only synthetic test exercised the
# bare 'scripts/X' shape. The lookbehind silently rejected '.' and '/' as
# preceding chars, so the check was blind to the exact PR #307 patterns:
#   ../scripts/foo.sh        (frontend/package.json shape)
#   sub2api/scripts/foo.sh   (Dockerfile COPY shape)
# Fix commit 5169d924 expanded the smoke matrix to 9 patterns — but only
# in the commit body. This file makes that matrix re-runnable so the same
# regression cannot be re-introduced silently.
#
# Each test creates an isolated temp repo, plants a single-line fixture,
# runs the check, and asserts that the check reports the fixture as
# stale (fail-expected) or quietly ignores it (skip-expected). The
# fixture filename is grep-anchored so the check's own self-references
# (which look like script paths to itself) don't pollute the test.

set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/script-ref-existence.py"

if [ ! -f "$SCRIPT" ]; then
  echo "FAIL: $SCRIPT missing" >&2
  exit 1
fi

pass=0
fail=0

# run <label> <fixture-line> <expect: fail|skip>
run() {
  local label="$1" content="$2" expect="$3"
  local dir
  dir="$(mktemp -d)"
  (
    cd "$dir"
    git init -q
    mkdir -p scripts/checks
    cp "$SCRIPT" scripts/checks/
    printf '%s\n' "$content" > fixture.md
    git add fixture.md
  )
  local out hit
  out="$(python3 "$dir/scripts/checks/script-ref-existence.py" 2>&1 || true)"
  hit=0
  if grep -q 'file=fixture.md' <<<"$out"; then
    hit=1
  fi
  case "$expect" in
    fail)
      if [ "$hit" -eq 1 ]; then
        pass=$((pass + 1))
      else
        fail=$((fail + 1))
        echo "  FAIL  $label — expected to catch, but check ignored it" >&2
      fi
      ;;
    skip)
      if [ "$hit" -eq 0 ]; then
        pass=$((pass + 1))
      else
        fail=$((fail + 1))
        echo "  FAIL  $label — expected to skip, but check flagged it" >&2
      fi
      ;;
    *)
      fail=$((fail + 1))
      echo "  FAIL  $label — bad expect '$expect'" >&2
      ;;
  esac
  rm -rf "$dir"
}

# ---- the 9-pattern matrix ----------------------------------------------------

run "P1 bare scripts/X.sh"                "bash scripts/missing-bare.sh"             fail
run "P2 ../scripts/X.py (relative)"       "python3 ../scripts/missing-rel.py"        fail
run "P3 sub2api/scripts/X (Docker)"       "COPY sub2api/scripts/missing-docker.py /" fail
run "P4 /app/scripts/X (container, skip)" "bash /app/scripts/in-container.sh"        skip
run "P5 myscripts/X (false-pos guard)"    "echo myscripts/foo.sh"                    skip
run "P6 backtick scripts/X.sh in md"      'See `scripts/missing-bt.sh` for help.'    fail
run "P7 .json not short-matched to .js"   "ref scripts/sentinels/missing.json"       fail
run "P8 ops/X.vue (Vue path, skip)"       "import '@/views/admin/ops/D.vue'"         skip
run "P9 dev-rules/scripts/X (subm-nested, fixture missing → expect fail)" \
    "see dev-rules/scripts/never-existed.py for ..."                                   fail

# ---- result ------------------------------------------------------------------

total=$((pass + fail))
if [ "$fail" -eq 0 ]; then
  echo "ok: script-ref-existence self-test (${pass}/${total} cases passed)"
  exit 0
else
  echo "FAIL: script-ref-existence self-test (${fail}/${total} cases failed)" >&2
  exit 1
fi
