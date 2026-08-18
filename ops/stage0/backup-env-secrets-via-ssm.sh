#!/usr/bin/env bash
#
# Off-box Stage0 .env secrets to an SSM SecureString (operator-run).
#
# Why a separate ops script (not in tokenkey-pgdump.sh): the secrets rotate rarely,
# the dump script is already at the SSM Standard-tier embed-size limit, and we do NOT
# want to co-locate the decryption keys with the S3 ledger dump (different blast
# radius). The instance does the PutParameter ITSELF (plaintext never leaves the host
# / never appears in this command's SSM output) using the ssm:PutParameter grant on the
# prod InstanceRole. Durable home: stage0-single-ec2.yaml (applied on the next prod CFN
# update); until then mirrored as a manual inline policy (EnvSecretsBackup) on the role
# via the IAM console. If the prod instance/role is ever REPLACED before the CFN update
# lands, re-add that inline policy (ssm:PutParameter on .../stage0/env-secrets-backup)
# or this script's PutParameter step will AccessDenied. See RUNBOOK §4.4.
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
#   TK_ENV_SECRETS_SOURCE  source file (default /var/lib/tokenkey/.env)
#   AWS_REGION / AWS_DEFAULT_REGION   region for SSM (optional)
#   STAGE0_SSM_TIMEOUT_SECONDS        SSM poll timeout (default 180)
#   STAGE0_SSM_OUTPUT_DIR             where to drop ssm-params/stdout/stderr (default .)
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-backup-env-secrets}}"
PARAM="${TK_ENV_SECRETS_PARAM:-/tokenkey/prod/stage0/env-secrets-backup}"
SOURCE="${TK_ENV_SECRETS_SOURCE:-/var/lib/tokenkey/.env}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-180}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "backup_env_secrets_via_ssm: instance id is required" >&2
  exit 1
fi
[[ "${PARAM}" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "backup_env_secrets_via_ssm: invalid parameter name" >&2; exit 1; }
[[ "${SOURCE}" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "backup_env_secrets_via_ssm: invalid source path" >&2; exit 1; }

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
# (--value file://, never echoed). Exit 0 proves the final SecureString exactly
# matches the three expected assignments; output carries status + a line count.
HOST_B64="$(base64 <<HOSTEOF | tr -d '\n'
set -euo pipefail
PARAM="${PARAM}"
SOURCE="${SOURCE}"
T=\$(mktemp); C=\$(mktemp); chmod 600 "\$T" "\$C"
cleanup() {
  if command -v shred >/dev/null 2>&1; then
    shred -u "\$T" "\$C" 2>/dev/null || rm -f "\$T" "\$C"
  else
    rm -f "\$T" "\$C"
  fi
}
trap cleanup EXIT
awk 'index(\$0, "POSTGRES_PASSWORD=") == 1 || index(\$0, "JWT_SECRET=") == 1 || index(\$0, "TOTP_ENCRYPTION_KEY=") == 1' "\$SOURCE" | sort > "\$T"
for K in POSTGRES_PASSWORD JWT_SECRET TOTP_ENCRYPTION_KEY; do
  N=\$(awk -v key="\$K" 'index(\$0, key "=") == 1 { count++ } END { print count + 0 }' "\$T")
  if [ "\$N" -ne 1 ]; then
    echo "::error::expected exactly one \${K} assignment in \$SOURCE" >&2
    exit 1
  fi
done
if aws ssm get-parameter --name "\$PARAM" --with-decryption --query Parameter.Value --output text > "\$C" 2>/dev/null \
    && cmp -s "\$T" "\$C"; then
  echo "secrets unchanged; no new SSM version written"
else
  # AWS CLI text output adds one trailing newline; store none so readback matches T.
  awk 'NR > 1 { printf "\\n" } { printf "%s", \$0 }' "\$T" > "\$C"
  if ! aws ssm put-parameter --name "\$PARAM" --type SecureString --overwrite --value "file://\$C" >/dev/null; then
    echo "::error::failed to off-box secrets to SSM \$PARAM" >&2
    exit 1
  fi
  echo "secrets off-boxed to SSM \$PARAM"
fi
if ! aws ssm get-parameter --name "\$PARAM" --with-decryption --query Parameter.Value --output text > "\$C" 2>/dev/null; then
  echo "::error::failed to verify secrets in SSM \$PARAM" >&2
  exit 1
fi
if ! cmp -s "\$T" "\$C"; then
  echo "::error::SSM secret backup does not match source assignments in \$PARAM" >&2
  exit 1
fi
N=\$(wc -l < "\$C" | tr -d ' ')
if [ "\$N" -ne 3 ]; then
  echo "::error::expected 3 secret lines in SSM \$PARAM, found \$N" >&2
  exit 1
fi
echo "verify: \$N secret line(s) now in \$PARAM (values not printed)"
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
