#!/usr/bin/env bash
#
# Stage0 data-layer cutover primitive — move an app from local-container PG to
# external RDS. Production activation is gated by the approved design; see
# docs/deploy/aws-data-layer-migration.md.
#
# Why this exists:
#   /var/lib/tokenkey/{.env,docker-compose.yml} are written by bootstrap at
#   instance FIRST boot only — a cutover without reboot must deliver the new
#   compose + override files itself (v5 review finding) and hot-edit .env.
#   The runtime config single source of truth is the SSM SecureString
#   <prefix>/data-layer-env: this script writes it during apply, then makes the
#   live host match it. Any later reboot/replace
#   re-derives the same state from SSM via bootstrap — no split-brain.
#
# apply:
#   1. (local) read the RDS master password from its own SSM SecureString,
#      compose the data-layer-env overlay, validate (no '|' '&' — sed-unsafe),
#      put-parameter (SecureString, overwrite).
#   2. (host) backup .env/compose -> deliver repo compose + external-db override
#      → fetch overlay FROM SSM and apply onto .env (same artifact bootstrap
#      reads at next boot — verifies the executed artifact, not the generator)
#      -> best-effort drain -> force-recreate tokenkey with explicit -f files
#      -> stop local postgres (container only; data volume kept >=14 days)
#      -> verify tokenkey-psql now answers from RDS.
#   3. Before the RDS-backed app is started, a failure restores local files.
#      Once an RDS-backed app start is attempted, automatic rollback is
#      forbidden: RDS may have accepted writes, so the script keeps the overlay
#      and requires forward repair.
#
# There is intentionally no production "rollback to stale local PG" action.
# After writes reopen, returning to local requires a rehearsed RDS delta replay
# and separate approval; it cannot be represented as a safe one-command action.
#
# Usage:
#   TK_DATA_PG_HOST=<rds-endpoint> ops/stage0/cutover_data_layer_via_ssm.sh apply <instance_id>
#
# Env:
#   TK_DATA_PG_HOST            (apply) RDS endpoint DNS name — REQUIRED
#   TK_DATA_PG_PORT            (apply) default 5432
#   TK_DATA_PG_PASSWORD_SSM    (apply) SecureString holding the RDS master
#                              password; default /tokenkey/prod/stage0/rds-master-password
#   TK_DATA_PG_CLIENT_IMAGE    (apply) psql/pg_dump client image; MUST match the
#                              RDS major version; default postgres:18-alpine
#   TK_PROJECT_NAME / TK_ENVIRONMENT   SSM prefix parts; default tokenkey / prod
#   AWS_REGION / AWS_DEFAULT_REGION    region for SSM (optional)
#   STAGE0_SSM_TIMEOUT_SECONDS         SSM poll timeout (default 480)
#   STAGE0_SSM_OUTPUT_DIR              where to drop ssm-params/stdout/stderr

set -euo pipefail

ACTION="${1:-}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-480}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
PROJECT_NAME="${TK_PROJECT_NAME:-tokenkey}"
ENVIRONMENT="${TK_ENVIRONMENT:-prod}"
STAGE0_PREFIX="/${PROJECT_NAME}/${ENVIRONMENT}/stage0"
OVERLAY_PARAM="${STAGE0_PREFIX}/data-layer-env"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
COMPOSE_SRC="${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml"
COMPOSE_EXT_SRC="${REPO_ROOT}/deploy/aws/stage0/docker-compose.external-db.yml"
DATA_LAYER_ENV_SRC="${REPO_ROOT}/deploy/aws/stage0/tokenkey-data-layer-env.sh"
READINESS_CHECK="${REPO_ROOT}/ops/stage0/check_data_layer_cutover_readiness.py"
APPROVAL_DOC="${REPO_ROOT}/docs/approved/design-prod-data-archive-rds.md"

case "${ACTION}" in
  apply) ;;
  *)
    echo "cutover_data_layer_via_ssm: first arg must be 'apply'; rollback to stale local PG is intentionally unsupported (got '${ACTION}')" >&2
    exit 1
    ;;
esac
if [[ -z "${INSTANCE_ID}" ]]; then
  echo "cutover_data_layer_via_ssm: instance id is required" >&2
  exit 1
fi

if [[ "${ENVIRONMENT}" == "prod" ]]; then
  [[ -f "${APPROVAL_DOC}" ]] || {
    echo "cutover_data_layer_via_ssm: missing production approval doc ${APPROVAL_DOC}" >&2
    exit 1
  }
  approval_status="$(awk -F': *' '$1 == "status" {print $2; exit}' "${APPROVAL_DOC}")"
  approved_by="$(awk -F': *' '$1 == "approved_by" {print $2; exit}' "${APPROVAL_DOC}")"
  if [[ "${approval_status}" != "approved" || -z "${approved_by}" || "${approved_by}" == "pending" ]]; then
    echo "cutover_data_layer_via_ssm: production blocked; approve ${APPROVAL_DOC} and complete rehearsal gates first (status=${approval_status:-missing}, approved_by=${approved_by:-missing})" >&2
    exit 1
  fi
  python3 "${READINESS_CHECK}"
fi

: "${TK_DATA_PG_HOST:?TK_DATA_PG_HOST (RDS endpoint) is required for apply}"

aws_cli() {
  if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
    aws --region "${AWS_REGION:-${AWS_DEFAULT_REGION}}" "$@"
  else
    aws "$@"
  fi
}

if ! [[ "${INSTANCE_ID}" =~ ^i-[A-Za-z0-9-]+$ ]]; then
  echo "cutover_data_layer_via_ssm: target must be an EC2 instance id (got ${INSTANCE_ID})" >&2
  exit 1
fi
target_tag() {
  aws_cli ec2 describe-tags \
    --filters "Name=resource-id,Values=${INSTANCE_ID}" "Name=key,Values=$1" \
    --query 'Tags[0].Value' --output text
}
TARGET_PROJECT="$(target_tag Project)"
TARGET_ENVIRONMENT="$(target_tag Environment)"
if [[ "${TARGET_PROJECT}" != "${PROJECT_NAME}" || "${TARGET_ENVIRONMENT}" != "${ENVIRONMENT}" ]]; then
  echo "cutover_data_layer_via_ssm: caller scope ${PROJECT_NAME}/${ENVIRONMENT} does not match target ${INSTANCE_ID} tags ${TARGET_PROJECT}/${TARGET_ENVIRONMENT}" >&2
  exit 1
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"
overlay_request_file=""

cleanup_local_artifacts() {
  rc=$?
  if [[ -n "${overlay_request_file}" ]]; then
    rm -f "${overlay_request_file}"
  fi
  trap - EXIT
  exit "${rc}"
}
trap cleanup_local_artifacts EXIT

# --- build the host command set ------------------------------------------
  PG_PORT="${TK_DATA_PG_PORT:-5432}"
  PG_PASSWORD_SSM="${TK_DATA_PG_PASSWORD_SSM:-${STAGE0_PREFIX}/rds-master-password}"
  PG_CLIENT_IMAGE="${TK_DATA_PG_CLIENT_IMAGE:-postgres:18-alpine}"
  for f in "${COMPOSE_SRC}" "${COMPOSE_EXT_SRC}" "${DATA_LAYER_ENV_SRC}"; do
    [[ -f "${f}" ]] || { echo "cutover_data_layer_via_ssm: missing ${f}" >&2; exit 1; }
  done

  echo "reading RDS master password from ${PG_PASSWORD_SSM}"
  PG_PASSWORD="$(aws_cli ssm get-parameter \
    --name "${PG_PASSWORD_SSM}" --with-decryption \
    --query Parameter.Value --output text)"
  [[ -n "${PG_PASSWORD}" ]] || { echo "::error::empty password at ${PG_PASSWORD_SSM}" >&2; exit 1; }

  OVERLAY_CONTENT="$(cat <<EOF
DATABASE_HOST=${TK_DATA_PG_HOST}
DATABASE_PORT=${PG_PORT}
DATABASE_SSLMODE=require
POSTGRES_PASSWORD=${PG_PASSWORD}
TOKENKEY_PG_CLIENT_IMAGE=${PG_CLIENT_IMAGE}
COMPOSE_PROFILES=localredis
COMPOSE_FILE=docker-compose.yml:docker-compose.external-db.yml
EOF
)"
  printf '%s\n' "${OVERLAY_CONTENT}" | bash "${DATA_LAYER_ENV_SRC}" validate

  echo "writing data-layer overlay to ${OVERLAY_PARAM} (SecureString)"
  overlay_request_file="$(mktemp)"
  chmod 0600 "${overlay_request_file}"
  jq -n \
    --arg name "${OVERLAY_PARAM}" \
    --arg value "${OVERLAY_CONTENT}" \
    '{Name: $name, Type: "SecureString", Overwrite: true, Value: $value}' \
    > "${overlay_request_file}"
  aws_cli ssm put-parameter --cli-input-json "file://${overlay_request_file}" >/dev/null
  rm -f "${overlay_request_file}"
  overlay_request_file=""

  # If command submission itself fails, no host could have started the
  # RDS-backed app, so removing the just-created desired-state overlay is safe.
  # After send-command succeeds, only the host phase marker may authorize that
  # deletion; a generic local ERR trap would risk rolling next boot to stale PG.
  command_submitted=0
  cleanup_before_submit() {
    rc=$?
    if [[ "${command_submitted}" -eq 0 ]]; then
      if ! aws_cli ssm delete-parameter --name "${OVERLAY_PARAM}" >/dev/null 2>&1; then
        echo "::error::command submission failed and ${OVERLAY_PARAM} cleanup also failed; remove it before any reboot/replace" >&2
      fi
    fi
    exit "${rc}"
  }
  trap cleanup_before_submit ERR

  COMPOSE_B64="$(base64 < "${COMPOSE_SRC}" | tr -d '\n')"
  COMPOSE_EXT_B64="$(base64 < "${COMPOSE_EXT_SRC}" | tr -d '\n')"
  DATA_LAYER_ENV_B64="$(base64 < "${DATA_LAYER_ENV_SRC}" | tr -d '\n')"

  jq -n --arg compose_b64 "${COMPOSE_B64}" \
        --arg compose_ext_b64 "${COMPOSE_EXT_B64}" \
        --arg data_layer_env_b64 "${DATA_LAYER_ENV_B64}" \
        --arg overlay_param "${OVERLAY_PARAM}" \
        --arg pg_client_image "${PG_CLIENT_IMAGE}" '{
    commands: [
      "set -euo pipefail",
      "cd /var/lib/tokenkey",
      "TS=$(date +%Y%m%d-%H%M%S)",
      "BK=/var/lib/tokenkey/data-layer-backup-$TS",
      "echo \"=== cutover apply (backup=$BK) ===\"",
      "sudo install -d -m 0700 \"$BK\"",
      "sudo cp -a .env \"$BK/.env\"",
      "sudo cp -a docker-compose.yml \"$BK/docker-compose.yml\"",
      "if [ -f docker-compose.external-db.yml ]; then sudo cp -a docker-compose.external-db.yml \"$BK/docker-compose.external-db.yml\"; fi",
      "CUTOVER_PHASE=pre-rds-start",
      "on_error() { rc=$?; if [ \"$CUTOVER_PHASE\" = pre-rds-start ]; then echo \"restoring local mode from $BK before any RDS-backed app start\"; sudo cp -a \"$BK/.env\" .env; sudo cp -a \"$BK/docker-compose.yml\" docker-compose.yml; if [ -f \"$BK/docker-compose.external-db.yml\" ]; then sudo cp -a \"$BK/docker-compose.external-db.yml\" docker-compose.external-db.yml; else sudo rm -f docker-compose.external-db.yml; fi; sudo docker compose --env-file .env up -d --remove-orphans; sudo docker compose --env-file .env up -d --no-deps --force-recreate tokenkey; for i in $(seq 1 18); do LOCAL_H=none; if LOCAL_H_OUT=$(sudo docker inspect tokenkey --format \"{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}\" 2>/dev/null); then LOCAL_H=$LOCAL_H_OUT; fi; [ \"$LOCAL_H\" = healthy ] && break; [ $i -eq 18 ] && { echo \"::error::local tokenkey not healthy after restore\"; exit $rc; }; sleep 5; done; echo \"CUTOVER_ABORTED_BEFORE_RDS_START local restore succeeded\"; else echo \"CUTOVER_FORWARD_FIX_REQUIRED RDS-backed app start was attempted; keeping RDS overlay and refusing stale-local rollback\"; fi; exit $rc; }",
      "trap on_error ERR",
      "echo \"=== deliver compose + external-db override (bootstrap only writes them at first boot) ===\"",
      ("printf '\''%s'\'' \"" + $compose_b64 + "\" | base64 -d | sudo tee docker-compose.yml >/dev/null"),
      ("printf '\''%s'\'' \"" + $compose_ext_b64 + "\" | base64 -d | sudo tee docker-compose.external-db.yml >/dev/null"),
      ("printf '\''%s'\'' \"" + $data_layer_env_b64 + "\" | base64 -d | sudo tee /tmp/tokenkey-data-layer-env >/dev/null"),
      "sudo chmod 0700 /tmp/tokenkey-data-layer-env",
      "echo \"=== fetch overlay from SSM and apply onto .env (same artifact bootstrap reads at next boot) ===\"",
      "IMDS_TOKEN=$(curl -fsS -X PUT http://169.254.169.254/latest/api/token -H \"X-aws-ec2-metadata-token-ttl-seconds: 300\")",
      "REGION=$(curl -fsS -H \"X-aws-ec2-metadata-token: $IMDS_TOKEN\" http://169.254.169.254/latest/meta-data/placement/region)",
      ("sudo /tmp/tokenkey-data-layer-env fetch-apply " + $overlay_param + " \"$REGION\" .env /var/lib/tokenkey/.rds-cutover-started"),
      "sudo rm -f /tmp/tokenkey-data-layer-env",
      "sudo grep -q \"^COMPOSE_FILE=\" .env || { echo \"::error::overlay did not land in .env\"; exit 1; }",
      ("sudo docker pull " + $pg_client_image),
      "echo \"=== drain and prove in_flight=0 before changing the database endpoint ===\"",
      "HEALTH=none; if HEALTH_OUT=$(sudo docker inspect tokenkey --format \"{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}\" 2>/dev/null); then HEALTH=$HEALTH_OUT; fi; [ \"$HEALTH\" = healthy ] || { echo \"::error::tokenkey must be healthy before cutover drain (health=$HEALTH)\"; exit 1; }",
      "sudo docker kill -s USR1 tokenkey",
      "INFLIGHT_DRAINED=0; for i in $(seq 1 18); do IN=; if IN_OUT=$(sudo docker exec tokenkey wget -q -T 3 -O- http://localhost:8080/health/inflight 2>/dev/null); then IN=$IN_OUT; fi; echo \"inflight: $IN\"; if echo \"$IN\" | grep -q \"\\\"in_flight\\\":0\"; then INFLIGHT_DRAINED=1; break; fi; sleep 5; done; [ $INFLIGHT_DRAINED -eq 1 ] || { echo \"::error::in_flight did not reach zero; restoring a fresh local app\"; exit 1; }",
      "echo \"=== force-recreate tokenkey against RDS (explicit -f for determinism) ===\"",
      "CUTOVER_PHASE=rds-start-attempted",
      "sudo touch /var/lib/tokenkey/.rds-cutover-started",
      "sudo chmod 0600 /var/lib/tokenkey/.rds-cutover-started",
      "sudo docker compose -f docker-compose.yml -f docker-compose.external-db.yml --env-file .env up -d --no-deps --force-recreate tokenkey",
      "for i in $(seq 1 18); do H=$(sudo docker inspect tokenkey --format \"{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}\" 2>/dev/null || echo none); echo \"tokenkey health: $H\"; [ \"$H\" = healthy ] && break; [ $i -eq 18 ] && { echo \"::error::tokenkey not healthy after recreate\"; exit 1; }; sleep 5; done",
      "echo \"=== stop local postgres (container only; data volume kept for evidence/delta replay) ===\"",
      "sudo docker stop tokenkey-postgres",
      "echo \"=== verify: wrapper now answers from RDS ===\"",
      "ONE=$(sudo /usr/local/bin/tokenkey-psql -X -A -t -c \"select 1\")",
      "[ \"$ONE\" = \"1\" ] || { echo \"::error::tokenkey-psql probe failed against RDS\"; exit 1; }",
      "sudo /usr/local/bin/tokenkey-psql -X -A -t -c \"select count(*) from users where deleted_at is null\" | sed \"s/^/active users rows: /\"",
      "sudo docker ps --filter name=tokenkey --format \"{{.Names}}\\t{{.Status}}\"",
      "trap - ERR",
      "echo \"=== cutover apply done ===\""
    ]
  }' > "${params_file}"

# --- send + poll -----------------------------------------------------------
cmd_id="$(aws_cli ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "data-layer-${ACTION}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"
command_submitted=1
trap - ERR

echo "ssm command-id=${cmd_id}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws_cli ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${deadline} ]]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws_cli ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws_cli ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- ssm stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo

if [[ "${status}" != "Success" ]]; then
  if grep -q 'CUTOVER_ABORTED_BEFORE_RDS_START' "${stdout_file}"; then
    echo "::warning::apply failed before an RDS-backed app start; deleting ${OVERLAY_PARAM} to keep next-boot state local"
    if ! aws_cli ssm delete-parameter --name "${OVERLAY_PARAM}" >/dev/null; then
      echo "::error::failed to delete ${OVERLAY_PARAM}; remove it before any reboot/replace" >&2
    fi
  else
    echo "::error::RDS-backed app start may have been attempted; keeping ${OVERLAY_PARAM}. Forward-fix only until RDS delta replay is proven." >&2
  fi
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
