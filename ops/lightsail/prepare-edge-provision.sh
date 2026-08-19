#!/usr/bin/env bash
set -euo pipefail

EDGE_ID="${1:-${EDGE_ID:-}}"
INSTANCE_NAME="${2:-${INSTANCE_NAME:-}}"
SSM_PREFIX="${3:-${SSM_PREFIX:-}}"
REGION="${4:-${AWS_REGION:-${AWS_DEFAULT_REGION:-}}}"
ROLE_NAME="${5:-${SSM_HYBRID_ROLE_NAME:-}}"
RECREATE="${RECREATE:-false}"

[[ "${EDGE_ID}" =~ ^[a-z]{2}[0-9]+$ ]] || { echo "prepare_edge_provision: invalid Edge ID" >&2; exit 1; }
[[ -n "${INSTANCE_NAME}" && -n "${SSM_PREFIX}" && -n "${REGION}" ]] || { echo "prepare_edge_provision: target fields required" >&2; exit 1; }
[[ "${ROLE_NAME}" == "tokenkey-lightsail-ssm-hybrid-${EDGE_ID}" ]] || { echo "prepare_edge_provision: invalid per-Edge role" >&2; exit 1; }
[[ "${RECREATE}" == true || "${RECREATE}" == false ]] || { echo "prepare_edge_provision: RECREATE must be true or false" >&2; exit 1; }
[[ -n "${GITHUB_ENV:-}" ]] || { echo "prepare_edge_provision: GITHUB_ENV required" >&2; exit 1; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
error_file="$(mktemp)"
trap 'rm -f "${error_file}"' EXIT

instance_exists=false
if aws lightsail get-instance --region "${REGION}" --instance-name "${INSTANCE_NAME}" >/dev/null 2>"${error_file}"; then
  instance_exists=true
elif ! grep -Fq 'NotFoundException' "${error_file}"; then
  cat "${error_file}" >&2
  echo "prepare_edge_provision: cannot determine Lightsail instance state" >&2
  exit 1
fi

marker_exists=false
if aws ssm get-parameter --region "${REGION}" --name "${SSM_PREFIX}/instance_name" >/dev/null 2>"${error_file}"; then
  marker_exists=true
elif ! grep -Fq 'ParameterNotFound' "${error_file}"; then
  cat "${error_file}" >&2
  echo "prepare_edge_provision: cannot determine persistent Edge identity" >&2
  exit 1
fi

allow_generate=false
if [[ "${instance_exists}" == false && "${marker_exists}" == false ]]; then
  allow_generate=true
fi

backup_verified=false
if [[ "${instance_exists}" == true && "${RECREATE}" == true ]]; then
  managed_id="$(bash "${ROOT}/ops/lightsail/resolve_ssm_instance_id.sh" "${SSM_PREFIX}" "${REGION}")"
  bash "${ROOT}/ops/lightsail/ensure-edge-ssm-role.sh" "${EDGE_ID}" "${managed_id}" "${REGION}"
  AWS_REGION="${REGION}" \
    TK_ENV_SECRETS_SOURCE=/var/lib/tokenkey/.env.secret \
    TK_ENV_SECRETS_PARAM="/tokenkey/edge/${EDGE_ID}/stage0/env-secrets-backup" \
    STAGE0_SSM_OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.edge-env-secrets-pre-recreate}" \
    bash "${ROOT}/ops/stage0/backup-env-secrets-via-ssm.sh" \
      "${managed_id}" "pre-recreate edge=${EDGE_ID}"
  backup_verified=true
fi

{
  echo "ALLOW_SECRET_GENERATE=${allow_generate}"
  echo "RECREATE_BACKUP_VERIFIED=${backup_verified}"
} >>"${GITHUB_ENV}"
echo "provision safety prepared edge=${EDGE_ID} new_identity=${allow_generate} backup_verified=${backup_verified}"
