#!/usr/bin/env bash
# Unit-style tests for scripts/ci/ensure-new-api-sibling.sh (symlink + cache layout).
set -euo pipefail

# When invoked from a git hook (pre-commit) or from inside a worktree, git
# exports GIT_DIR / GIT_INDEX_FILE / GIT_WORK_TREE / GIT_OBJECT_DIRECTORY /
# GIT_NAMESPACE pointing at the outer repository. The test workspace is a
# temporary directory that runs `git init` of its own; if any of those env
# vars leak through, the inner git refuses with "must be run in a work tree"
# and test_migrates_legacy_real_sibling_dir regresses. Mirrors the scrub
# pattern from scripts/checks/script-ref-existence_test.sh.
unset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_NAMESPACE

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/scripts/ci/ensure-new-api-sibling.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

setup_workspace() {
  local ws="${TMP}/sub2api/sub2api"
  mkdir -p "${ws}/backend"
  printf '%s\n' "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" > "${ws}/.new-api-ref"
  printf '%s\n' "${ws}"
}

init_fixture_repo() {
  local repo="$1"
  git -C "${repo}" init -q
  git -C "${repo}" config user.email "test@example.com"
  git -C "${repo}" config user.name "Test"
}

test_symlink_points_at_cache_dir() {
  local ws
  ws="$(setup_workspace)"
  mkdir -p "${ws}/.cache/new-api"
  init_fixture_repo "${ws}/.cache/new-api"
  git -C "${ws}/.cache/new-api" commit --allow-empty -q -m "seed"

  ENSURE_NEW_API_LAYOUT_ONLY=1 bash "${SCRIPT}" "${ws}"

  [ -L "${ws}/../new-api" ] || { echo "expected sibling symlink"; return 1; }
  [ "$(readlink "${ws}/../new-api")" = "${ws}/.cache/new-api" ] || {
    echo "symlink target mismatch: $(readlink "${ws}/../new-api")"
    return 1
  }
}

test_migrates_legacy_real_sibling_dir() {
  local ws
  ws="$(setup_workspace)"
  mkdir -p "${ws}/../new-api"
  init_fixture_repo "${ws}/../new-api"
  git -C "${ws}/../new-api" commit --allow-empty -q -m "legacy"

  ENSURE_NEW_API_LAYOUT_ONLY=1 bash "${SCRIPT}" "${ws}"

  [ -d "${ws}/.cache/new-api/.git" ] || { echo "expected migrated cache git dir"; return 1; }
  [ -L "${ws}/../new-api" ] || { echo "expected sibling symlink after migration"; return 1; }
}

test_rejects_empty_pin() {
  local ws="${TMP}/empty/sub2api"
  mkdir -p "${ws}"
  : > "${ws}/.new-api-ref"
  if ENSURE_NEW_API_LAYOUT_ONLY=1 bash "${SCRIPT}" "${ws}" >/dev/null 2>&1; then
    echo "expected failure for empty .new-api-ref"
    return 1
  fi
}

fail=0
for name in test_symlink_points_at_cache_dir test_migrates_legacy_real_sibling_dir test_rejects_empty_pin; do
  if "${name}"; then
    echo "PASS ${name}"
  else
    echo "FAIL ${name}"
    fail=1
  fi
done
exit "${fail}"
