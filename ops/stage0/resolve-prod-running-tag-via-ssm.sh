#!/usr/bin/env bash
#
# Resolve the Stage0 prod host's *runtime* app image tag via read-only SSM.
#
# Why this exists:
#   CloudFormation ImageTag is a bootstrap-only value. Normal prod releases use
#   deploy-stage0's SSM blue/green path, so CFN ImageTag intentionally lags the
#   running image. Any operation that replaces the EC2 instance must pin ImageTag
#   to the runtime tag first, or the new instance can bootstrap an old image.
#
# Output:
#   default: tag only (e.g. 1.8.91), suitable for RUNNING_TAG=$(...)
#   --json:  {"instance_id":...,"container":...,"image":...,"tag":...}
#
# Read-only: CloudFormation Describe* + SSM RunShellScript probe containing only
# docker inspect / active-color reads.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat <<'USAGE'
Resolve the Stage0 prod host's *runtime* app image tag via read-only SSM.

Why this exists:
  CloudFormation ImageTag is a bootstrap-only value. Normal prod releases use
  deploy-stage0's SSM blue/green path, so CFN ImageTag intentionally lags the
  running image. Any operation that replaces the EC2 instance must pin ImageTag
  to the runtime tag first, or the new instance can bootstrap an old image.

Output:
  default: tag only (e.g. 1.8.91), suitable for RUNNING_TAG=$(...)
  --json:  {"instance_id":...,"container":...,"image":...,"tag":...}

Read-only: CloudFormation Describe* + SSM RunShellScript probe containing only
docker inspect / active-color reads.

Usage:
  ops/stage0/resolve-prod-running-tag-via-ssm.sh [--region us-east-1] [--stack tokenkey-prod-stage0]
  ops/stage0/resolve-prod-running-tag-via-ssm.sh --instance-id i-... [--json]

Options:
  --region REGION       AWS region. Default: $AWS_REGION / $AWS_DEFAULT_REGION / us-east-1.
  --stack STACK         Prod CFN stack to resolve when --instance-id is omitted.
                        Default: $PROD_STACK_NAME / tokenkey-prod-stage0.
  --instance-id ID      Skip CFN resolution and probe this EC2 instance directly.
  --container NAME      auto | tokenkey | tokenkey-blue | tokenkey-green. Default: auto.
  --json                Emit JSON instead of the bare tag.
  --timeout-seconds N   SSM polling budget. Default: 120.
USAGE
}

REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
STACK="${PROD_STACK_NAME:-tokenkey-prod-stage0}"
INSTANCE_ID=""
APP_CONTAINER="auto"
JSON_OUTPUT=0
TIMEOUT_SECONDS=120
COMMENT="resolve-prod-running-tag"

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --region) REGION="${2:-}"; shift 2 ;;
    --stack) STACK="${2:-}"; shift 2 ;;
    --instance-id) INSTANCE_ID="${2:-}"; shift 2 ;;
    --container) APP_CONTAINER="${2:-}"; shift 2 ;;
    --json) JSON_OUTPUT=1; shift ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    *) echo "resolve-prod-running-tag: unknown arg: $1" >&2; usage >&2; exit 1 ;;
  esac
done

if [[ -z "${REGION}" || -z "${STACK}" ]]; then
  echo "resolve-prod-running-tag: --region and --stack must be non-empty" >&2
  exit 1
fi
if [[ ! "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ || "${TIMEOUT_SECONDS}" -le 0 ]]; then
  echo "resolve-prod-running-tag: --timeout-seconds must be a positive integer" >&2
  exit 1
fi
case "${APP_CONTAINER}" in
  auto|tokenkey|tokenkey-blue|tokenkey-green) ;;
  *) echo "resolve-prod-running-tag: --container must be auto, tokenkey, tokenkey-blue, or tokenkey-green" >&2; exit 1 ;;
esac

aws_region() {
  aws --region "${REGION}" "$@"
}

if [[ -z "${INSTANCE_ID}" ]]; then
  INSTANCE_ID="$(aws_region cloudformation describe-stacks \
    --stack-name "${STACK}" \
    --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue" \
    --output text 2>&1)" || {
      echo "resolve-prod-running-tag: describe-stacks failed for ${STACK} in ${REGION}" >&2
      printf '%s\n' "${INSTANCE_ID}" >&2
      exit 2
    }
  if [[ -z "${INSTANCE_ID}" || "${INSTANCE_ID}" == "None" ]]; then
    INSTANCE_ID="$(aws_region cloudformation describe-stack-resources \
      --stack-name "${STACK}" \
      --query "StackResources[?ResourceType=='AWS::EC2::Instance']|[0].PhysicalResourceId" \
      --output text 2>/dev/null || true)"  # preflight-allow: swallow -- describe-stacks output is authoritative; this fallback is optional and checked below
  fi
fi
if [[ -z "${INSTANCE_ID}" || "${INSTANCE_ID}" == "None" ]]; then
  echo "resolve-prod-running-tag: could not resolve InstanceId for stack ${STACK}" >&2
  exit 2
fi
if [[ "${INSTANCE_ID}" != i-* ]]; then
  echo "resolve-prod-running-tag: expected EC2 instance id (i-*), got ${INSTANCE_ID}" >&2
  exit 1
fi

read -r -d '' REMOTE_PROBE <<PROBE || true  # preflight-allow: swallow -- read -d '' returns nonzero at heredoc EOF under set -e
set -u
requested_container='${APP_CONTAINER}'
app_container="\$requested_container"
active_color=""
fallback_used=false

if [ -r /var/lib/tokenkey/active-color ]; then
  active_color=\$(sed -n '1p' /var/lib/tokenkey/active-color 2>/dev/null | tr -d '[:space:]')
fi

if [ "\$requested_container" = auto ]; then
  app_container=tokenkey
  case "\$active_color" in
    blue|green)
      candidate="tokenkey-\$active_color"
      if docker inspect "\$candidate" >/dev/null 2>&1; then
        app_container="\$candidate"
      fi
      ;;
  esac
fi

img=\$(docker inspect "\$app_container" --format '{{.Config.Image}}' 2>/dev/null || true)  # preflight-allow: swallow -- missing active-color candidate falls back or is reported by parser
if [ -z "\$img" ] && [ "\$requested_container" = auto ] && [ "\$app_container" != tokenkey ]; then
  app_container=tokenkey
  fallback_used=true
  img=\$(docker inspect "\$app_container" --format '{{.Config.Image}}' 2>/dev/null || true)  # preflight-allow: swallow -- legacy fallback may be absent; parser fails closed
fi

printf 'ACTIVE_COLOR {"value":"%s"}\n' "\$active_color"
printf 'APPCONTAINER {"name":"%s","requested":"%s","fallback_used":%s}\n' "\$app_container" "\$requested_container" "\$fallback_used"
printf 'RUNIMAGE {"image":"%s"}\n' "\$img"
PROBE

REMOTE_B64="$(printf '%s' "${REMOTE_PROBE}" | base64 | tr -d '\n')"
PARAMS="$(python3 - "${REMOTE_B64}" <<'PY'
import json
import sys

print(json.dumps({"commands": [f"echo {sys.argv[1]} | base64 -d | bash"]}))
PY
)"

cmd_id="$(aws_region ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "${PARAMS}" \
  --query 'Command.CommandId' --output text 2>&1)" || {
    echo "resolve-prod-running-tag: could not start SSM command on ${INSTANCE_ID}" >&2
    printf '%s\n' "${cmd_id}" >&2
    exit 2
  }

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="Pending"
while :; do
  status="$(aws_region ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo Pending)"
  case "${status}" in
    Success|Failed|Cancelled|TimedOut) break ;;
  esac
  [[ $(date +%s) -ge ${deadline} ]] && { status=TimedOut; break; }
  sleep 2
done

probe_out="$(aws_region ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text 2>/dev/null || true)"  # preflight-allow: swallow -- status/output validation below turns this into a hard helper failure

if [[ "${status}" != "Success" || -z "${probe_out}" ]]; then
  probe_err="$(aws_region ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'StandardErrorContent' --output text 2>/dev/null || true)"  # preflight-allow: swallow -- diagnostic stderr is best-effort after a failed probe
  echo "resolve-prod-running-tag: could not read host state (ssm status=${status})" >&2
  [[ -n "${probe_err}" && "${probe_err}" != "None" ]] && printf '%s\n' "${probe_err}" >&2
  exit 2
fi

parsed="$(PROBE_OUT="${probe_out}" INSTANCE_ID="${INSTANCE_ID}" python3 - <<'PY'
import json
import os
import sys

facts = {"instance_id": os.environ["INSTANCE_ID"], "active_color": "", "container": "", "requested_container": "", "fallback_used": False, "image": ""}
for raw in os.environ["PROBE_OUT"].splitlines():
    raw = raw.strip()
    if not raw:
        continue
    parts = raw.split(None, 1)
    if len(parts) != 2:
        continue
    tag, payload = parts
    try:
        obj = json.loads(payload)
    except ValueError:
        continue
    if tag == "ACTIVE_COLOR":
        facts["active_color"] = obj.get("value") or ""
    elif tag == "APPCONTAINER":
        facts["container"] = obj.get("name") or ""
        facts["requested_container"] = obj.get("requested") or ""
        facts["fallback_used"] = bool(obj.get("fallback_used"))
    elif tag == "RUNIMAGE":
        facts["image"] = obj.get("image") or ""

image = facts["image"]
if not image:
    print(json.dumps({"error": "running image unknown", **facts}, sort_keys=True), file=sys.stderr)
    sys.exit(2)
if ":" not in image:
    print(json.dumps({"error": "running image has no tag", **facts}, sort_keys=True), file=sys.stderr)
    sys.exit(2)
tag = image.rsplit(":", 1)[1].strip()
if not tag:
    print(json.dumps({"error": "running image tag empty", **facts}, sort_keys=True), file=sys.stderr)
    sys.exit(2)
facts["tag"] = tag
print(json.dumps(facts, sort_keys=True))
PY
)" || {
  echo "resolve-prod-running-tag: failed to parse runtime image from host probe" >&2
  exit 2
}

tag="$(printf '%s' "${parsed}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag"])')"
image="$(printf '%s' "${parsed}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["image"])')"
if ! bash "${SCRIPT_DIR}/validate-deploy-tag.sh" "${tag}" >/dev/null; then
  echo "resolve-prod-running-tag: running image tag is not a Stage0 release tag: tag=${tag} image=${image}" >&2
  exit 2
fi

if [[ "${JSON_OUTPUT}" -eq 1 ]]; then
  printf '%s\n' "${parsed}"
else
  printf '%s\n' "${tag}"
fi
