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

while IFS=$'\t' read -r edge_id region ssm_prefix; do
  managed_id="$(bash "${ROOT}/ops/lightsail/resolve_ssm_instance_id.sh" "${ssm_prefix}" "${region}")"
  if [[ "${MODE}" == --dry-run ]]; then
    echo "would reconcile edge=${edge_id} instance=${managed_id} role=tokenkey-lightsail-ssm-hybrid-${edge_id} region=${region}"
  elif [[ "${MODE}" == --check ]]; then
    bash "${ROOT}/ops/lightsail/ensure-edge-ssm-role.sh" "${edge_id}" "${managed_id}" "${region}" --check
  else
    bash "${ROOT}/ops/lightsail/ensure-edge-ssm-role.sh" "${edge_id}" "${managed_id}" "${region}"
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
  echo "check complete; all Edge roles verified and shared role is ready for legacy policy deletion"
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
