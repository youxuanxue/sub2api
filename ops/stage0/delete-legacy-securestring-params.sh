#!/usr/bin/env bash
# Delete closeout-C5 SecureString / retired pgdump Parameter Store entries.
# Requires explicit APPLY=yes-delete-securestring. Prints a receipt to stdout.
set -euo pipefail

APPLY="${APPLY:-}"
if [[ "${APPLY}" != "yes-delete-securestring" ]]; then
  echo "usage: APPLY=yes-delete-securestring $0" >&2
  echo "Dry inventory only when APPLY is unset." >&2
fi

# Name|Region
TARGETS=(
  "/tokenkey/prod/stage0/env-secrets-backup|us-east-1"
  "/tokenkey/prod/stage0/pgdump.b64|us-east-1"
  "/tokenkey/hetzner/prod/stage0/env-secrets-backup|eu-west-2"
  "/tokenkey/hetzner/prod/stage0/pgdump-storage-recovery|eu-west-2"
  "/tokenkey/hetzner/uk1/stage0/env-secrets-backup|eu-west-2"
  "/tokenkey/hetzner/uk1/stage0/pgdump-storage-recovery|eu-west-2"
  "/tokenkey/hetzner/uk2/stage0/env-secrets-backup|eu-west-2"
  "/tokenkey/hetzner/uk2/stage0/pgdump-storage-recovery|eu-west-2"
  "/tokenkey/hetzner/us4/stage0/env-secrets-backup|eu-west-2"
  "/tokenkey/hetzner/us4/stage0/pgdump-storage-recovery|eu-west-2"
  "/tokenkey/hetzner/us5/stage0/env-secrets-backup|eu-west-2"
  "/tokenkey/hetzner/us5/stage0/pgdump-storage-recovery|eu-west-2"
  "/tokenkey/edge/uk1/stage0/env-secrets-backup|eu-west-2"
  "/tokenkey/edge/uk2/stage0/env-secrets-backup|eu-west-2"
  "/tokenkey/prod-tokyo-precutover/stage0/env-secrets-backup|ap-northeast-1"
)

receipt_ts="$(date -u +%Y%m%dT%H%M%SZ)"
echo "securestring_c5_receipt_ts=${receipt_ts}"

for row in "${TARGETS[@]}"; do
  name="${row%%|*}"
  region="${row##*|}"
  if aws --region "${region}" ssm get-parameter --name "${name}" >/dev/null 2>&1; then
    echo "PRESENT name=${name} region=${region}"
    if [[ "${APPLY}" == "yes-delete-securestring" ]]; then
      aws --region "${region}" ssm delete-parameter --name "${name}"
      echo "DELETED name=${name} region=${region}"
    fi
  else
    echo "ABSENT name=${name} region=${region}"
  fi
done

if [[ "${APPLY}" != "yes-delete-securestring" ]]; then
  echo "dry-run complete; re-run with APPLY=yes-delete-securestring to delete"
  exit 0
fi
echo "securestring_c5_delete_complete=1"
