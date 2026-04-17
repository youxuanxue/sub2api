#!/usr/bin/env bash
# sync-new-api.sh — keep the QuantumNous/new-api sibling clone in sync with
# the commit pinned in .new-api-ref (the single source of truth).
#
# Project layout assumption (CLAUDE.md §4 "Cross-Repo Dependency: New API"):
#
#   <parent>/
#     ├── sub2api/   <- this repo
#     └── new-api/   <- QuantumNous/new-api clone (resolved via go.mod replace)
#
# Both local dev and CI (.github/workflows/{release,backend-ci,security-scan}.yml)
# read .new-api-ref so the sibling clone, the release Docker image, and the
# CI test/lint runs all use the *exact same* new-api commit.
#
# Usage:
#   bash scripts/sync-new-api.sh                # default: pin from .new-api-ref
#   bash scripts/sync-new-api.sh --check        # exit 1 if local sibling != pin
#   bash scripts/sync-new-api.sh --bump <sha>   # update .new-api-ref + sync
#
# After --bump you should: go test -tags=unit ./... && git add .new-api-ref
# && git commit -m "chore(deps): bump new-api to <sha>".

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
SUB2API_ROOT="$(cd -- "${SCRIPT_DIR}/.." &>/dev/null && pwd)"
PIN_FILE="${SUB2API_ROOT}/.new-api-ref"
SIBLING_DIR="$(cd -- "${SUB2API_ROOT}/.." &>/dev/null && pwd)/new-api"
REMOTE_URL="https://github.com/QuantumNous/new-api.git"

mode="sync"
new_pin=""
case "${1:-}" in
  --check) mode="check" ;;
  --bump)
    mode="bump"
    new_pin="${2:-}"
    [ -n "${new_pin}" ] || { echo "ERROR: --bump requires <sha>" >&2; exit 2; }
    ;;
  "") ;;
  *) echo "ERROR: unknown arg '$1'" >&2; exit 2 ;;
esac

if [ "${mode}" = "bump" ]; then
  printf '%s\n' "${new_pin}" > "${PIN_FILE}"
  echo "Updated ${PIN_FILE} -> ${new_pin}"
fi

[ -f "${PIN_FILE}" ] || { echo "ERROR: ${PIN_FILE} missing" >&2; exit 1; }
PIN="$(tr -d '[:space:]' < "${PIN_FILE}")"
[ -n "${PIN}" ] || { echo "ERROR: ${PIN_FILE} is empty" >&2; exit 1; }

if [ ! -d "${SIBLING_DIR}/.git" ]; then
  if [ "${mode}" = "check" ]; then
    echo "ERROR: sibling clone missing at ${SIBLING_DIR}" >&2
    echo "       run: bash scripts/sync-new-api.sh" >&2
    exit 1
  fi
  echo "Cloning new-api into ${SIBLING_DIR} ..."
  git clone --filter=blob:none "${REMOTE_URL}" "${SIBLING_DIR}"
fi

current="$(git -C "${SIBLING_DIR}" rev-parse HEAD)"

if [ "${mode}" = "check" ]; then
  if [ "${current}" != "${PIN}" ]; then
    echo "DRIFT: sibling new-api is at ${current}, pin is ${PIN}" >&2
    echo "       run: bash scripts/sync-new-api.sh" >&2
    exit 1
  fi
  echo "OK: new-api sibling at pinned ${PIN}"
  exit 0
fi

if [ "${current}" != "${PIN}" ]; then
  echo "Fetching ${PIN} ..."
  git -C "${SIBLING_DIR}" fetch --filter=blob:none origin "${PIN}" || git -C "${SIBLING_DIR}" fetch
  git -C "${SIBLING_DIR}" checkout "${PIN}"
fi

echo "OK: new-api sibling at ${PIN}"
