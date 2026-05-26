#!/usr/bin/env bash
# Ensure QuantumNous/new-api exists at the go.mod replace path (../../new-api from
# backend/) using a workspace-local cache dir that GitHub Actions can save/restore.
set -euo pipefail

workspace="${1:-${GITHUB_WORKSPACE:?set GITHUB_WORKSPACE or pass workspace path}}"
new_api_ref="$(tr -d '[:space:]' < "${workspace}/.new-api-ref")"
if [ -z "${new_api_ref}" ]; then
  echo "ERROR: .new-api-ref is empty" >&2
  exit 1
fi

cache_dir="${workspace}/.cache/new-api"
sibling_link="${workspace}/../new-api"

mkdir -p "${workspace}/.cache"

# Migrate legacy ../new-api real directories into the cache path once.
if [ ! -d "${cache_dir}/.git" ] && [ -d "${sibling_link}/.git" ] && [ ! -L "${sibling_link}" ]; then
  mv "${sibling_link}" "${cache_dir}"
fi

ln -sfn "${cache_dir}" "${sibling_link}"

if [ "${ENSURE_NEW_API_LAYOUT_ONLY:-}" = "1" ]; then
  ls "${cache_dir}/go.mod" 2>/dev/null || touch "${cache_dir}/go.mod"
  exit 0
fi

if [ -d "${cache_dir}/.git" ]; then
  git -C "${cache_dir}" fetch --filter=blob:none origin "${new_api_ref}" 2>/dev/null || \
    git -C "${cache_dir}" fetch origin "${new_api_ref}" 2>/dev/null || true
  if git -C "${cache_dir}" rev-parse "${new_api_ref}^{commit}" >/dev/null 2>&1; then
    git -C "${cache_dir}" checkout -q "${new_api_ref}"
    ls "${cache_dir}/go.mod"
    exit 0
  fi
  echo "WARN: cached new-api repo missing SHA ${new_api_ref}; recloning" >&2
  rm -rf "${cache_dir}"
  ln -sfn "${cache_dir}" "${sibling_link}"
fi

git clone --filter=blob:none https://github.com/QuantumNous/new-api.git "${cache_dir}"
git -C "${cache_dir}" fetch --filter=blob:none origin "${new_api_ref}" || true
git -C "${cache_dir}" checkout "${new_api_ref}"
ls "${cache_dir}/go.mod"
