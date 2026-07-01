#!/usr/bin/env bash
# sync-instance-cpu-alarm.sh — apply tokenkey Stage0 InstanceCpuAlarm without a full
# CFN stack update (prod stack template drift can otherwise trigger instance replace).
#
# Contract matches deploy/aws/cloudformation/stage0-single-ec2.yaml InstanceCpuAlarm:
#   AWS/EC2 CPUUtilization Average > 80 for 3×5m (15 minutes sustained).
#
# Usage:
#   bash ops/stage0/sync-instance-cpu-alarm.sh --stack tokenkey-prod-stage0
#   bash ops/stage0/sync-instance-cpu-alarm.sh --instance-id i-0123456789abcdef0
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION (default us-east-1)
#   ALARM_SNS_TOPIC_ARN — optional; when set, alarm notifies SNS on breach
set -euo pipefail

REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
STACK=""
INSTANCE_ID=""
SNS_ARN="${ALARM_SNS_TOPIC_ARN:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --stack) STACK="$2"; shift 2 ;;
    --instance-id) INSTANCE_ID="$2"; shift 2 ;;
    --sns-topic-arn) SNS_ARN="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "sync-instance-cpu-alarm: unknown arg $1" >&2; exit 1 ;;
  esac
done

if [ -z "$INSTANCE_ID" ] && [ -n "$STACK" ]; then
  INSTANCE_ID="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
    --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)"
fi
if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "None" ]; then
  echo "sync-instance-cpu-alarm: provide --instance-id or --stack with InstanceId output" >&2
  exit 1
fi

if [ -n "$STACK" ]; then
  ALARM_NAME="$(printf '%s' "$STACK" | sed -E 's/-stage0$/-cpu-sustained-high/')"
else
  ALARM_NAME="tokenkey-prod-cpu-sustained-high"
fi

args=(
  --region "$REGION"
  --alarm-name "$ALARM_NAME"
  --alarm-description "tokenkey EC2 CPU > 80% (5m Average) for 15 consecutive minutes — deploy burst or traffic spike; check load, active-color container, and consider resize."
  --namespace AWS/EC2
  --metric-name CPUUtilization
  --dimensions "Name=InstanceId,Value=${INSTANCE_ID}"
  --statistic Average
  --period 300
  --evaluation-periods 3
  --datapoints-to-alarm 3
  --threshold 80
  --comparison-operator GreaterThanThreshold
  --treat-missing-data notBreaching
)
if [ -n "$SNS_ARN" ]; then
  args+=(--alarm-actions "$SNS_ARN")
fi

aws cloudwatch put-metric-alarm "${args[@]}"
echo "sync-instance-cpu-alarm: ok alarm=${ALARM_NAME} instance=${INSTANCE_ID} region=${REGION}"
