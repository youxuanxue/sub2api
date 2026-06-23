#!/usr/bin/env bash
# Load TK_SMOKE_* GitHub Environment config into the current shell.
#
# Variables (plaintext) are fetched via gh API. Secrets are write-only on GitHub —
# this script verifies they exist on the Environment but cannot read their values.
# Export secret values yourself (same TK_SMOKE_* names) before sourcing, or run
# smoke via deploy-stage0 / deploy-edge-lightsail-stage0 workflows.
#
# Usage:
#   eval "$(bash ops/stage0/load_smoke_github_env.sh prod)"
#   TK_SMOKE_GITHUB_ENV=prod bash ops/stage0/post_deploy_smoke.sh   # auto via smoke_env.sh
#
# Flags:
#   --check   Verify Environment has expected TK_SMOKE secrets/vars; no export output.
set -euo pipefail

CHECK_ONLY=0
ENV_NAME=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check) CHECK_ONLY=1; shift ;;
    -*) echo "tk_load_smoke_github_env: unknown flag $1" >&2; exit 1 ;;
    *)
      if [[ -n "${ENV_NAME}" ]]; then
        echo "tk_load_smoke_github_env: unexpected argument $1" >&2
        exit 1
      fi
      ENV_NAME="$1"
      shift
      ;;
  esac
done

if [[ -z "${ENV_NAME}" ]]; then
  echo "usage: load_smoke_github_env.sh [--check] <github-environment>" >&2
  echo "  e.g. prod | edge-uk1 | edge-us1" >&2
  exit 1
fi

command -v gh >/dev/null 2>&1 || {
  echo "tk_load_smoke_github_env: gh CLI required (gh auth login)" >&2
  exit 1
}
command -v jq >/dev/null 2>&1 || {
  echo "tk_load_smoke_github_env: jq required" >&2
  exit 1
}

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REPO="$(cd "${REPO_ROOT}" && gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null)" || {
  echo "tk_load_smoke_github_env: gh cannot resolve GitHub repo (gh repo set-default?)" >&2
  exit 1
}
gh auth status >/dev/null 2>&1 || {
  echo "tk_load_smoke_github_env: gh not authenticated (gh auth login or GH_TOKEN)" >&2
  exit 1
}

required_secrets_for_env() {
  case "$1" in
    prod|edge-*)
      printf '%s\n' TK_SMOKE_API_KEY
      ;;
    *)
      echo "tk_load_smoke_github_env: unsupported environment '${1}' (want prod or edge-<id>)" >&2
      return 1
      ;;
  esac
}

fetch_variables_json() {
  gh api "repos/${REPO}/environments/${ENV_NAME}/variables?per_page=100" --paginate 2>/dev/null \
    | jq -s '[.[].variables[] | select(.name | startswith("TK_SMOKE_"))]'
}

secret_configured() {
  local name="$1"
  gh api "repos/${REPO}/environments/${ENV_NAME}/secrets/${name}" >/dev/null 2>&1
}

vars_json="$(fetch_variables_json)" || {
  echo "tk_load_smoke_github_env: failed to list variables for environment ${ENV_NAME} on ${REPO}" >&2
  exit 1
}

missing_secrets=()
while IFS= read -r secret; do
  [[ -z "${secret}" ]] && continue
  if [[ "${CHECK_ONLY}" -eq 0 && -n "${!secret:-}" ]]; then
    continue
  fi
  if secret_configured "${secret}"; then
    if [[ "${CHECK_ONLY}" -eq 0 ]]; then
      missing_secrets+=("${secret}")
    fi
  else
    echo "tk_load_smoke_github_env: secret ${secret} not configured on GitHub Environment ${ENV_NAME}" >&2
    exit 1
  fi
done < <(required_secrets_for_env "${ENV_NAME}")

if [[ "${CHECK_ONLY}" -eq 1 ]]; then
  var_count="$(jq 'length' <<<"${vars_json}")"
  echo "tk_load_smoke_github_env: OK environment=${ENV_NAME} repo=${REPO} tk_smoke_vars=${var_count}" >&2
  exit 0
fi

if [[ ${#missing_secrets[@]} -gt 0 ]]; then
  echo "tk_load_smoke_github_env: GitHub Environment secrets are not readable via gh/API." >&2
  echo "  Environment: ${ENV_NAME} (${REPO})" >&2
  echo "  Configured on GitHub but missing locally:" >&2
  for secret in "${missing_secrets[@]}"; do
    echo "    ${secret}" >&2
  done
  echo "  Export the same TK_SMOKE_* names locally, or run smoke via GitHub Actions workflows." >&2
  exit 1
fi

# shellcheck disable=SC2016
jq -r '.[] | "export \(.name)=\(.value | @sh)"' <<<"${vars_json}"
