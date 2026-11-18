#!/usr/bin/env bash
set -euo pipefail

# Restore the three Stage0 bootstrap secrets onto a host.
#
# Closeout C2/C4 SSOT (docs/approved/design-tencent-ops-closeout.md):
#   1) Host root-only file already present → reuse (live SSOT)
#   2) --from-file PATH → copy validated material (COS/operator export)
#   3) --allow-generate → first provision only
# SecureString reads are refused unless TOKENKEY_ALLOW_SECURESTRING_READ=1
# (break-glass until C5 deletes the parameters).

PARAMETER=""
OUTPUT=""
FROM_FILE=""
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
ALLOW_GENERATE=false

while [ "$#" -gt 0 ]; do
  case "$1" in
    --parameter) PARAMETER="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    --from-file) FROM_FILE="${2:-}"; shift 2 ;;
    --allow-generate) ALLOW_GENERATE=true; shift ;;
    *) echo "restore-prod-env-secrets: unknown argument $1" >&2; exit 40 ;;
  esac
done

[[ "${OUTPUT}" = /* ]] || { echo "restore-prod-env-secrets: absolute output path required" >&2; exit 40; }

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
    echo "restore-prod-env-secrets: existing secret file is invalid" >&2
    exit 41
  }
  chmod 0600 "${OUTPUT}"
  echo "prod env secrets already present on host"
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

install_from_path() {
  local src="$1"
  local label="$2"
  validate_secret_file "${src}" || {
    echo "restore-prod-env-secrets: ${label} is malformed" >&2
    exit 42
  }
  cp -f "${src}" "${temp}"
  chmod 0600 "${temp}"
  mv -f "${temp}" "${OUTPUT}"
  chmod 0600 "${OUTPUT}"
  echo "prod env secrets restored from ${label}"
}

if [ -n "${FROM_FILE}" ]; then
  install_from_path "${FROM_FILE}" "--from-file"
  exit 0
fi

if [ "${TOKENKEY_ALLOW_SECURESTRING_READ:-}" = "1" ]; then
  [[ "${PARAMETER}" =~ ^/tokenkey/(hetzner|prod|edge|prod-tokyo-precutover)/[a-z0-9-]+/stage0/env-secrets-backup$ ]] \
    || [[ "${PARAMETER}" =~ ^/tokenkey/prod/stage0/env-secrets-backup$ ]] || {
    echo "restore-prod-env-secrets: bad parameter" >&2
    exit 40
  }
  [[ -n "${REGION}" ]] || { echo "restore-prod-env-secrets: AWS region required for SecureString break-glass" >&2; exit 40; }
  if aws --region "${REGION}" ssm get-parameter \
    --name "${PARAMETER}" --with-decryption \
    --query Parameter.Value --output text >"${temp}" 2>"${error_file}"; then
    validate_secret_file "${temp}" || {
      echo "restore-prod-env-secrets: SSM value is malformed" >&2
      exit 42
    }
    mv -f "${temp}" "${OUTPUT}"
    chmod 0600 "${OUTPUT}"
    echo "prod env secrets restored from SSM (TOKENKEY_ALLOW_SECURESTRING_READ=1)"
    exit 0
  fi
  if ! grep -Fq 'ParameterNotFound' "${error_file}"; then
    echo "restore-prod-env-secrets: SSM read failed; refusing secret rotation" >&2
    tail -c 1024 "${error_file}" >&2
    exit 44
  fi
fi

if [ "${ALLOW_GENERATE}" = true ]; then
  : >"${temp}"
  printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)" >>"${temp}"
  printf 'JWT_SECRET=%s\n' "$(openssl rand -hex 32)" >>"${temp}"
  printf 'TOTP_ENCRYPTION_KEY=%s\n' "$(openssl rand -hex 32)" >>"${temp}"
  validate_secret_file "${temp}" || { echo "restore-prod-env-secrets: generated secrets are invalid" >&2; exit 43; }
  mv -f "${temp}" "${OUTPUT}"
  chmod 0600 "${OUTPUT}"
  echo "prod env secrets generated for first provision"
  exit 0
fi

echo "restore-prod-env-secrets: no host file; supply --from-file (COS/operator export) or --allow-generate" >&2
echo "SecureString is not the live SSOT (closeout C4). Break-glass: TOKENKEY_ALLOW_SECURESTRING_READ=1" >&2
exit 45
