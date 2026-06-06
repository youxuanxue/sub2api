#!/usr/bin/env bash
#
# Off-box the prod .env SECRETS to an SSM SecureString (operator-run).
#
# Why a separate ops script (not in tokenkey-pgdump.sh): the secrets rotate rarely,
# the dump script is already at the SSM Standard-tier embed-size limit, and we do NOT
# want to co-locate the decryption keys with the S3 ledger dump (different blast
# radius). The instance does the PutParameter ITSELF (plaintext never leaves the host
# / never appears in this command's SSM output) using the ssm:PutParameter grant from
# the AppInstanceSecretBackupPolicy ManagedPolicy in stage0-backups.yaml.
#
# Run this:
#   - once at activation (after deploying stage0-backups.yaml), and
#   - after ANY rotation of POSTGRES_PASSWORD / JWT_SECRET / TOTP_ENCRYPTION_KEY.
# Change-detected: a no-op (no new param version) when the secrets are unchanged.
#
# Restore is documented in deploy/aws/RUNBOOK-disaster-recovery.md §4.4 step 0.
#
# Usage:
#   ops/stage0/backup-env-secrets-via-ssm.sh <instance_id> [comment]
# Env:
#   TK_ENV_SECRETS_PARAM   SSM param name (default /tokenkey/prod/stage0/env-secrets-backup)
#   AWS_REGION / AWS_DEFAULT_REGION   region for SSM (optional)
#   STAGE0_SSM_TIMEOUT_SECONDS        SSM poll timeout (default 180)
#   STAGE0_SSM_OUTPUT_DIR             where to drop ssm-params/stdout/stderr (default .)
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-backup-env-secrets}}"
PARAM="${TK_ENV_SECRETS_PARAM:-/tokenkey/prod/stage0/env-secrets-backup}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-180}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "backup_env_secrets_via_ssm: instance id is required" >&2
  exit 1
fi

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

# Host-side script: read the 3 secret lines, change-detect against the current SSM
# value, PutParameter SecureString only if changed. Plaintext stays on the host
# (--value file://, never echoed). Output carries only status + a line COUNT.
HOST_B64="$(base64 <<HOSTEOF | tr -d '\n'
set -uo pipefail
PARAM="${PARAM}"
T=\$(mktemp); C=\$(mktemp); chmod 600 "\$T" "\$C"
grep -E '^(POSTGRES_PASSWORD|JWT_SECRET|TOTP_ENCRYPTION_KEY)=' /var/lib/tokenkey/.env | sort > "\$T" || true
if [ ! -s "\$T" ]; then echo "::error::no secrets found in /var/lib/tokenkey/.env"; rm -f "\$T" "\$C"; exit 1; fi
aws ssm get-parameter --name "\$PARAM" --with-decryption --query Parameter.Value --output text > "\$C" 2>/dev/null || true
if cmp -s "\$T" "\$C"; then
  echo "secrets unchanged; no new SSM version written"
else
  aws ssm put-parameter --name "\$PARAM" --type SecureString --overwrite --value "file://\$T" >/dev/null && echo "secrets off-boxed to SSM \$PARAM"
fi
N=\$(aws ssm get-parameter --name "\$PARAM" --with-decryption --query Parameter.Value --output text 2>/dev/null | grep -c '=' || echo 0)
echo "verify: \$N secret line(s) now in \$PARAM (values not printed)"
command -v shred >/dev/null 2>&1 && shred -u "\$T" "\$C" 2>/dev/null || rm -f "\$T" "\$C"
HOSTEOF
)"

jq -n --arg b64 "${HOST_B64}" '{
  commands: [
    "set -euo pipefail",
    ("echo " + $b64 + " | base64 -d | sudo bash")
  ]
}' > "${params_file}"

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"
echo "ssm command-id=${cmd_id}"

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in Success|Failed|TimedOut|Cancelled) break ;; esac
  if [[ $(date +%s) -ge ${deadline} ]]; then echo "::error::ssm timeout" >&2; status="TimedOut"; break; fi
  sleep 5
done

aws "${ssm_region_args[@]}" ssm get-command-invocation --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" --query 'StandardOutputContent' --output text > "${stdout_file}" 2>/dev/null || true
aws "${ssm_region_args[@]}" ssm get-command-invocation --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" --query 'StandardErrorContent' --output text > "${stderr_file}" 2>/dev/null || true
echo "--- ssm stdout (no secret values) ---"; cat "${stdout_file}"
echo "--- ssm stderr ---"; tail -c 2048 "${stderr_file}"

if [[ "${status}" != "Success" ]]; then echo "::error::ssm command status=${status}" >&2; exit 1; fi
