#!/usr/bin/env bash
set -euo pipefail

MODE="${1:---dry-run}"
case "${MODE}" in
  --dry-run|--check|--apply) ;;
  *) echo "usage: $0 [--dry-run|--check|--apply]" >&2; exit 1 ;;
esac

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MATRIX="${EDGE_LIGHTSAIL_MATRIX:-${ROOT}/deploy/aws/lightsail/edge-targets-lightsail.json}"
SHARED_ROLE=tokenkey-lightsail-ssm-hybrid
LEGACY_POLICY=EdgePgdumpPutOnly
CORE_POLICY_ARN=arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
BACKUP_ENV_SCRIPT="${EDGE_BACKUP_ENV_SCRIPT:-${ROOT}/ops/stage0/backup-env-secrets-via-ssm.sh}"
RUN_PROBE_SCRIPT="${EDGE_RUN_PROBE_SCRIPT:-${ROOT}/ops/observability/run-probe.sh}"
RECOVERY_OUTPUT_ROOT="${EDGE_RECOVERY_OUTPUT_ROOT:-}"
RECOVERY_TIMEOUT_SECONDS="${EDGE_RECOVERY_TIMEOUT_SECONDS:-3600}"
[[ "${RECOVERY_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || {
  echo "migrate_edge_ssm_roles: invalid recovery timeout" >&2
  exit 1
}

targets="$(python3 - "${MATRIX}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as fh:
    data = json.load(fh)
for edge_id, target in sorted(data.get("targets", {}).items()):
    if target.get("deployable") is True:
        print(edge_id, target["lightsail_region"], target["ssm_prefix"], sep="\t")
PY
)"
[[ -n "${targets}" ]] || { echo "migrate_edge_ssm_roles: no deployable Lightsail targets" >&2; exit 1; }

if [[ "${MODE}" == --apply && -z "${RECOVERY_OUTPUT_ROOT}" ]]; then
  RECOVERY_OUTPUT_ROOT="$(mktemp -d)"
  trap 'rm -rf -- "${RECOVERY_OUTPUT_ROOT}"' EXIT
fi

while IFS=$'\t' read -r edge_id region ssm_prefix; do
  managed_id="$(bash "${ROOT}/ops/lightsail/resolve_ssm_instance_id.sh" "${ssm_prefix}" "${region}")"
  if [[ "${MODE}" == --dry-run ]]; then
    echo "would reconcile edge=${edge_id} instance=${managed_id} role=tokenkey-lightsail-ssm-hybrid-${edge_id} region=${region}"
  elif [[ "${MODE}" == --check ]]; then
    bash "${ROOT}/ops/lightsail/ensure-edge-ssm-role.sh" "${edge_id}" "${managed_id}" "${region}" --check
  else
    bash "${ROOT}/ops/lightsail/ensure-edge-ssm-role.sh" "${edge_id}" "${managed_id}" "${region}"
    output_dir="${RECOVERY_OUTPUT_ROOT}/${edge_id}"
    mkdir -p "${output_dir}"
    TK_ENV_SECRETS_PARAM="/tokenkey/edge/${edge_id}/stage0/env-secrets-backup" \
      AWS_REGION="${region}" \
      STAGE0_SSM_OUTPUT_DIR="${output_dir}/env-secrets" \
      bash "${BACKUP_ENV_SCRIPT}" "${managed_id}" "migrate-${edge_id}-env-secrets"
    receipt="$(bash "${RUN_PROBE_SCRIPT}" \
      --target "edge:${edge_id}" \
      --expected-instance-id "${managed_id}" \
      --script "${ROOT}/ops/stage0/pgdump_restore_canary_remote.sh" \
      --with "${ROOT}/ops/stage0/pgdump_restore_canary.py" \
      --with "${ROOT}/ops/stage0/pgdump_restore_canary_contract.py" \
      --env "CANARY_TARGET=edge:${edge_id}" \
      --env CANARY_CREATE_DUMP=1 \
      --comment "migrate-${edge_id}-recovery-gate" \
      --timeout-seconds "${RECOVERY_TIMEOUT_SECONDS}")"
    python3 - "${edge_id}" "${receipt}" <<'PY'
import json
import re
import sys

edge_id, raw = sys.argv[1:]
try:
    receipt = json.loads(raw)
except json.JSONDecodeError as exc:
    raise SystemExit(f"migrate_edge_ssm_roles: invalid recovery receipt for {edge_id}: {exc}")
digest = receipt.get("artifact_sha256")
valid = (
    receipt.get("target") == f"edge:{edge_id}"
    and receipt.get("s3_round_trip_verified") is True
    and isinstance(digest, str)
    and re.fullmatch(r"[0-9a-f]{64}", digest) is not None
    and receipt.get("source_local_sha256") == digest
)
if not valid:
    raise SystemExit(f"migrate_edge_ssm_roles: recovery receipt failed validation for {edge_id}")
PY
    echo "recovery path verified: edge=${edge_id} instance=${managed_id}"
  fi
done <<<"${targets}"

if [[ "${MODE}" == --dry-run ]]; then
  echo "dry-run complete; no managed-instance role or IAM policy changed"
  exit 0
fi

inline_policies="$(aws iam list-role-policies \
  --role-name "${SHARED_ROLE}" \
  --query 'PolicyNames' --output text)"
attached_policies="$(aws iam list-attached-role-policies \
  --role-name "${SHARED_ROLE}" \
  --query 'AttachedPolicies[].PolicyArn' --output text)"

for policy in ${inline_policies}; do
  [[ "${policy}" == None || "${policy}" == "${LEGACY_POLICY}" ]] || {
    echo "migrate_edge_ssm_roles: unexpected shared-role inline policy ${policy}" >&2
    exit 1
  }
done
for arn in ${attached_policies}; do
  [[ "${arn}" == None || "${arn}" == "${CORE_POLICY_ARN}" ]] || {
    echo "migrate_edge_ssm_roles: unexpected shared-role attached policy ${arn}" >&2
    exit 1
  }
done
[[ " ${attached_policies} " == *" ${CORE_POLICY_ARN} "* ]] || {
  echo "migrate_edge_ssm_roles: shared role is missing AmazonSSMManagedInstanceCore" >&2
  exit 1
}

if [[ "${MODE}" == --check ]]; then
  echo "check complete; Edge roles and shared-role policy shape verified; --apply runs recovery gates before deletion"
  exit 0
fi

if [[ " ${inline_policies} " == *" ${LEGACY_POLICY} "* ]]; then
  aws iam delete-role-policy --role-name "${SHARED_ROLE}" --policy-name "${LEGACY_POLICY}"
fi

remaining_inline="$(aws iam list-role-policies \
  --role-name "${SHARED_ROLE}" \
  --query 'PolicyNames' --output text)"
remaining_attached="$(aws iam list-attached-role-policies \
  --role-name "${SHARED_ROLE}" \
  --query 'AttachedPolicies[].PolicyArn' --output text)"
[[ -z "${remaining_inline}" || "${remaining_inline}" == None ]] || {
  echo "migrate_edge_ssm_roles: shared role still has inline policies: ${remaining_inline}" >&2
  exit 1
}
[[ "${remaining_attached}" == "${CORE_POLICY_ARN}" ]] || {
  echo "migrate_edge_ssm_roles: shared role attached policies are not core-only: ${remaining_attached}" >&2
  exit 1
}

echo "shared role retirement verified: ${SHARED_ROLE} retains only AmazonSSMManagedInstanceCore"
