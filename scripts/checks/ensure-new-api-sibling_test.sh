#!/usr/bin/env bash
# Unit-style tests for scripts/ci/ensure-new-api-sibling.sh (symlink + cache layout).
set -euo pipefail

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

test_symlink_points_at_cache_dir() {
  local ws
  ws="$(setup_workspace)"
  mkdir -p "${ws}/.cache/new-api"
  git -C "${ws}/.cache/new-api" init -q
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
  git -C "${ws}/../new-api" init -q
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
