#!/usr/bin/env bash
# Replace a Lightsail EDGE's Gemini Web Worker container with a CI-built image,
# via SSM Run-Command. Mirrors ops/stage0/sync-edge-host-units-via-ssm.sh.
#
# Why this exists: the Worker used to be built and run by hand on each host,
# which produced tags that named commits absent from main. Images now come from
# gemini-web-worker.yml tagged with the build digest, and this script is the only
# way they reach a host.
#
# The Worker owns Google browser sessions, crash leases and uncertain-generation
# protection, so this drains before replacing:
#   1. verify the requested image reports the digest its tag claims
#   2. SIGTERM the old container and allow the configured stop timeout
#   3. start the new container under the documented security limits
#   4. wait for /readyz and confirm it reports the expected digest
# On failure after the old container is gone, the new one is left in place for
# inspection; scheduling of Web accounts is a separate operator decision
# (ops/gemini-web/README.md).
#
# Usage:
#   bash ops/stage0/deploy-gemini-web-worker-via-ssm.sh <digest> <instance-id> [comment]

set -euo pipefail

DIGEST="${1:-${GEMINI_WEB_DIGEST:-}}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
COMMENT="${3:-${SSM_COMMENT:-ops-gemini-web-worker-deploy}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-900}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
IMAGE_REPO="${GEMINI_WEB_IMAGE_REPO:-}"
STOP_TIMEOUT="${GEMINI_WEB_STOP_TIMEOUT:-600}"

if [ -z "${DIGEST}" ]; then
  echo "deploy_gemini_web_worker: build digest is required" >&2
  exit 1
fi
case "${DIGEST}" in
  [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
  *) echo "deploy_gemini_web_worker: digest must be 12 hex chars, got: ${DIGEST}" >&2; exit 1 ;;
esac
if [ -z "${INSTANCE_ID}" ]; then
  echo "deploy_gemini_web_worker: instance id is required" >&2
  exit 1
fi
if [ -z "${IMAGE_REPO}" ]; then
  echo "deploy_gemini_web_worker: GEMINI_WEB_IMAGE_REPO is required (e.g. ghcr.io/owner/repo-gemini-web)" >&2
  exit 1
fi

IMAGE="${IMAGE_REPO}:${DIGEST}"

ssm_region_args=()
if [ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

# The host-side payload lives in its own file so the container contract stays
# reviewable shell rather than escaped strings inside JSON.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PAYLOAD_SRC="${SCRIPT_DIR}/../gemini-web/deploy-worker-on-host.sh"
[ -f "${PAYLOAD_SRC}" ] || { echo "missing ${PAYLOAD_SRC}" >&2; exit 1; }
PAYLOAD_B64="$(base64 <"${PAYLOAD_SRC}" | tr -d '\n')"

jq -n \
  --arg payload "${PAYLOAD_B64}" \
  --arg image "${IMAGE}" \
  --arg digest "${DIGEST}" \
  --arg stop "${STOP_TIMEOUT}" \
  '{
    commands: [
      "set -euo pipefail",
      ("echo " + $payload + " | base64 -d | sudo tee /tmp/deploy-worker-on-host.sh > /dev/null"),
      "sudo chmod +x /tmp/deploy-worker-on-host.sh",
      ("sudo bash /tmp/deploy-worker-on-host.sh " + $image + " " + $digest + " " + $stop),
      "sudo rm -f /tmp/deploy-worker-on-host.sh"
    ]
  }' > "${params_file}"

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "ssm command-id=${cmd_id}"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- ssm stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo

if [ "${status}" != "Success" ]; then
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
if ! grep -q 'tk_gemini_web_worker_deploy: OK' "${stdout_file}"; then
  echo "::error::deploy marker missing from SSM output" >&2
  exit 1
fi
