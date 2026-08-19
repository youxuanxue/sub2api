#!/usr/bin/env bash
set -euo pipefail

PARAMETER=""
OUTPUT=""
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
ALLOW_GENERATE=false

while [ "$#" -gt 0 ]; do
  case "$1" in
    --parameter) PARAMETER="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    --allow-generate) ALLOW_GENERATE=true; shift ;;
    *) echo "restore-edge-env-secrets: unknown argument $1" >&2; exit 40 ;;
  esac
done

[[ "${PARAMETER}" =~ ^/tokenkey/edge/[a-z0-9-]+/stage0/env-secrets-backup$ ]] || {
  echo "restore-edge-env-secrets: invalid edge parameter" >&2
  exit 40
}
[[ "${OUTPUT}" = /* ]] || { echo "restore-edge-env-secrets: absolute output path required" >&2; exit 40; }
[[ -n "${REGION}" ]] || { echo "restore-edge-env-secrets: AWS region required" >&2; exit 40; }

validate_secret_file() {
  local path="$1"
  [ -f "${path}" ] && [ ! -L "${path}" ] || return 1
  [ "$(awk 'END { print NR + 0 }' "${path}")" -eq 3 ] || return 1
  local key count value expected_length
  for key in POSTGRES_PASSWORD JWT_SECRET TOTP_ENCRYPTION_KEY; do
    count="$(awk -F= -v key="${key}" '$1 == key && length($0) > length(key) + 1 { count++ } END { print count + 0 }' "${path}")"
    [ "${count}" -eq 1 ] || return 1
    value="$(awk -F= -v key="${key}" '$1 == key { print substr($0, length(key) + 2) }' "${path}")"
    if [ "${key}" = POSTGRES_PASSWORD ]; then expected_length=48; else expected_length=64; fi
    [[ "${value}" =~ ^[0-9a-f]+$ ]] && [ "${#value}" -eq "${expected_length}" ] || return 1
  done
  ! awk -F= '$1 !~ /^(POSTGRES_PASSWORD|JWT_SECRET|TOTP_ENCRYPTION_KEY)$/ || length($0) <= length($1) + 1 { bad=1 } END { exit bad ? 0 : 1 }' "${path}"
}

if [ -e "${OUTPUT}" ] || [ -L "${OUTPUT}" ]; then
  validate_secret_file "${OUTPUT}" || {
    echo "restore-edge-env-secrets: existing secret file is invalid" >&2
    exit 41
  }
  chmod 0600 "${OUTPUT}"
  echo "edge env secrets already present on host"
  exit 0
fi

parent="$(dirname "${OUTPUT}")"
install -d -m 0700 "${parent}"
temp="$(mktemp "${parent}/.${OUTPUT##*/}.XXXXXX")"
error_file="$(mktemp "${parent}/.${OUTPUT##*/}.error.XXXXXX")"
chmod 0600 "${temp}" "${error_file}"
cleanup() {
  if command -v shred >/dev/null 2>&1; then
    shred -u "${temp}" "${error_file}" 2>/dev/null || rm -f "${temp}" "${error_file}"
  else
    rm -f "${temp}" "${error_file}"
  fi
}
trap cleanup EXIT

if aws --region "${REGION}" ssm get-parameter \
  --name "${PARAMETER}" --with-decryption \
  --query Parameter.Value --output text >"${temp}" 2>"${error_file}"; then
  validate_secret_file "${temp}" || {
    echo "restore-edge-env-secrets: SSM value is malformed" >&2
    exit 42
  }
  mv -f "${temp}" "${OUTPUT}"
  chmod 0600 "${OUTPUT}"
  echo "edge env secrets restored from SSM"
elif grep -Fq 'ParameterNotFound' "${error_file}"; then
  if [ "${ALLOW_GENERATE}" != true ]; then
    echo "restore-edge-env-secrets: parameter is missing and generation is not authorized" >&2
    exit 45
  fi
  : >"${temp}"
  printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)" >>"${temp}"
  printf 'JWT_SECRET=%s\n' "$(openssl rand -hex 32)" >>"${temp}"
  printf 'TOTP_ENCRYPTION_KEY=%s\n' "$(openssl rand -hex 32)" >>"${temp}"
  validate_secret_file "${temp}" || { echo "restore-edge-env-secrets: generated secrets are invalid" >&2; exit 43; }
  mv -f "${temp}" "${OUTPUT}"
  chmod 0600 "${OUTPUT}"
  echo "edge env secrets generated for first provision"
else
  echo "restore-edge-env-secrets: SSM read failed; refusing secret rotation" >&2
  tail -c 1024 "${error_file}" >&2
  exit 44
fi
