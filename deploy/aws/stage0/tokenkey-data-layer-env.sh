#!/usr/bin/env bash
# Shared data-layer overlay validator/applicator for bootstrap and cutover.
set -euo pipefail

ACTION="${1:-}"
TEMP_FILES=("")

cleanup() {
  local path
  for path in "${TEMP_FILES[@]}"; do
    [[ -n "${path}" ]] && rm -f "${path}"
  done
  return 0
}
trap cleanup EXIT

validate_overlay() {
  local line
  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    if ! [[ "${line}" =~ ^[A-Za-z_][A-Za-z0-9_]*=[A-Za-z0-9_.:/-]*$ ]]; then
      echo "tokenkey-data-layer-env: invalid overlay line ${line%%=*}=...; values must match [A-Za-z0-9_.:/-]" >&2
      return 1
    fi
  done
}

apply_overlay() {
  local env_file="$1" overlay_file line key
  overlay_file="$(mktemp)"
  TEMP_FILES+=("${overlay_file}")
  chmod 0600 "${overlay_file}"
  cat >"${overlay_file}"
  validate_overlay <"${overlay_file}"

  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    key="${line%%=*}"
    if grep -q "^${key}=" "${env_file}"; then
      sed -i.bak "s|^${key}=.*|${line}|" "${env_file}"
      rm -f "${env_file}.bak"
    else
      printf '%s\n' "${line}" >>"${env_file}"
    fi
  done <"${overlay_file}"
  rm -f "${overlay_file}"
}

fetch_and_apply() {
  local parameter="$1" region="$2" env_file="$3" rds_marker="$4"
  local mark_external="${5:-false}" overlay error_file
  error_file="$(mktemp)"
  TEMP_FILES+=("${error_file}")
  chmod 0600 "${error_file}"

  if overlay="$(aws ssm get-parameter \
      --name "${parameter}" --region "${region}" --with-decryption \
      --query Parameter.Value --output text 2>"${error_file}")"; then
    printf '%s\n' "${overlay}" | apply_overlay "${env_file}"
    if [[ "${mark_external}" == "true" ]] \
        && ! grep -q '^COMPOSE_PROFILES=.*localpg' "${env_file}"; then
      touch "${rds_marker}"
      chmod 0600 "${rds_marker}"
    fi
    echo "tokenkey-data-layer-env: applied ${parameter}"
  elif grep -q 'ParameterNotFound' "${error_file}"; then
    if [[ -e "${rds_marker}" ]]; then
      echo "tokenkey-data-layer-env: ${parameter} is missing after RDS start; refusing stale-local fallback" >&2
      return 1
    fi
    echo "tokenkey-data-layer-env: ${parameter} not found; local mode remains active"
  else
    echo "tokenkey-data-layer-env: failed to read ${parameter}; refusing to guess local mode" >&2
    cat "${error_file}" >&2
    return 1
  fi

  rm -f "${error_file}"
}

case "${ACTION}" in
  validate)
    validate_overlay
    ;;
  apply)
    [[ $# -eq 2 ]] || { echo "usage: $0 apply <env-file>" >&2; exit 2; }
    apply_overlay "$2"
    ;;
  fetch-apply)
    [[ $# -eq 5 || $# -eq 6 ]] || {
      echo "usage: $0 fetch-apply <ssm-parameter> <region> <env-file> <rds-marker> [mark-external]" >&2
      exit 2
    }
    fetch_and_apply "$2" "$3" "$4" "$5" "${6:+true}"
    ;;
  *)
    echo "usage: $0 validate|apply|fetch-apply ..." >&2
    exit 2
    ;;
esac
