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
NEW_API_UPSTREAM_URL="${NEW_API_UPSTREAM_URL:-https://github.com/QuantumNous/new-api.git}"
NEW_API_FORK_URL="${NEW_API_FORK_URL:-https://github.com/youxuanxue/new-api.git}"

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

fetch_pin_from_remote() {
  local remote_name="$1"
  local remote_url="$2"
  if ! git -C "${cache_dir}" remote get-url "${remote_name}" >/dev/null 2>&1; then
    git -C "${cache_dir}" remote add "${remote_name}" "${remote_url}"
  else
    git -C "${cache_dir}" remote set-url "${remote_name}" "${remote_url}"
  fi
  git -C "${cache_dir}" fetch --filter=blob:none "${remote_name}" "${new_api_ref}" 2>/dev/null || \
    git -C "${cache_dir}" fetch "${remote_name}" "${new_api_ref}" 2>/dev/null || return 1
  git -C "${cache_dir}" rev-parse "${new_api_ref}^{commit}" >/dev/null 2>&1
}

checkout_pinned_new_api() {
  git -C "${cache_dir}" checkout -q "${new_api_ref}"
  ls "${cache_dir}/go.mod"
}

if [ -d "${cache_dir}/.git" ]; then
  if fetch_pin_from_remote origin "${NEW_API_UPSTREAM_URL}"; then
    checkout_pinned_new_api
    exit 0
  fi
  if fetch_pin_from_remote fork "${NEW_API_FORK_URL}"; then
    echo "INFO: .new-api-ref ${new_api_ref} resolved from fork ${NEW_API_FORK_URL}" >&2
    checkout_pinned_new_api
    exit 0
  fi
  echo "WARN: cached new-api repo missing SHA ${new_api_ref}; recloning" >&2
  rm -rf "${cache_dir}"
  ln -sfn "${cache_dir}" "${sibling_link}"
fi

git clone --filter=blob:none "${NEW_API_UPSTREAM_URL}" "${cache_dir}"
if fetch_pin_from_remote origin "${NEW_API_UPSTREAM_URL}"; then
  checkout_pinned_new_api
  exit 0
fi
if fetch_pin_from_remote fork "${NEW_API_FORK_URL}"; then
  echo "INFO: .new-api-ref ${new_api_ref} resolved from fork ${NEW_API_FORK_URL}" >&2
  checkout_pinned_new_api
  exit 0
fi

echo "ERROR: .new-api-ref ${new_api_ref} not found on ${NEW_API_UPSTREAM_URL} or ${NEW_API_FORK_URL}" >&2
exit 1
