#!/usr/bin/env bash
# Poll until ssm:DescribeInstanceInformation reports PingStatus=Online for the
# given instance, or fail.
#
# This is the post-mutation invariant that operation=rotate_egress_ip (and any
# future operation that touches Elastic Network Interface / IAM / metadata)
# must satisfy before it claims success. Its absence is precisely how the
# 2026-05-22 edge-uk1 EIP rotation + CFN IMPORT left SSM silently disconnected
# from 2026-05-22 through 2026-05-23 — data plane stayed healthy (Caddy +
# Anthropic upstream) but ops-daily-diagnostics could not SendCommand, which
# is exactly the diagnostics blind-spot a rotation runbook must close on its
# own.
#
# Usage:
#   verify-ssm-online.sh <instance-id> <region> [--timeout-seconds N]
#
# Exit codes:
#   0  — PingStatus=Online
#   1  — timeout exceeded; instance is still ConnectionLost / Inactive / never registered
#   2  — bad input / aws CLI failure

set -euo pipefail

INSTANCE_ID="${1:-}"
REGION="${2:-}"
TIMEOUT_SECONDS="180"
INTERVAL_SECONDS="6"

if [ -z "$INSTANCE_ID" ] || [ -z "$REGION" ]; then
  echo "usage: $0 <instance-id> <region> [--timeout-seconds N]" >&2
  exit 2
fi
shift 2 || true

while [ $# -gt 0 ]; do
  case "$1" in
    --timeout-seconds)
      TIMEOUT_SECONDS="$2"
      shift 2
      ;;
    *)
      echo "unknown flag: $1" >&2
      exit 2
      ;;
  esac
done

if ! command -v aws >/dev/null 2>&1; then
  echo "aws CLI not found" >&2
  exit 2
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
attempts=0
last_status="unknown"

while [ "$(date +%s)" -lt "$deadline" ]; do
  attempts=$((attempts + 1))
  ping_status=$(aws ssm describe-instance-information --region "$REGION" \
    --filters "Key=InstanceIds,Values=$INSTANCE_ID" \
    --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null || echo "ERROR")
  last_status="$ping_status"
  if [ "$ping_status" = "Online" ]; then
    echo "verify-ssm-online: $INSTANCE_ID is Online after ${attempts} polls"
    exit 0
  fi
  sleep "$INTERVAL_SECONDS"
done

echo "::error::verify-ssm-online: timed out after ${TIMEOUT_SECONDS}s; last PingStatus=${last_status} (instance=${INSTANCE_ID} region=${REGION})" >&2
echo "::error::Likely causes after an EIP rotation: (1) SSM agent on the instance needs restart, (2) IAM instance-profile no longer carries AmazonSSMManagedInstanceCore, (3) the new EIP routes egress through a path that cannot reach the regional SSM endpoint." >&2
exit 1
