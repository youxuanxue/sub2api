#!/usr/bin/env bash
# Apply the Stage0 root-volume alarm without a full CFN update. Updating the
# drifted prod stack can replace the instance, while put-metric-alarm is an
# idempotent control-plane upsert.
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
      sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "sync-instance-root-disk-alarm: unknown arg $1" >&2; exit 1 ;;
  esac
done

if [ -z "$INSTANCE_ID" ] && [ -n "$STACK" ]; then
  INSTANCE_ID="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
    --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)"
fi
if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "None" ]; then
  echo "sync-instance-root-disk-alarm: provide --instance-id or --stack with InstanceId output" >&2
  exit 1
fi

if [ -n "$STACK" ]; then
  ALARM_NAME="$(printf '%s' "$STACK" | sed -E 's/-stage0$/-root-volume-used/')"
else
  ALARM_NAME="tokenkey-prod-root-volume-used"
fi

args=(
  --region "$REGION"
  --alarm-name "$ALARM_NAME"
  --alarm-description "tokenkey root volume critical — check bounded Docker logs and image cleanup before the host fills."
  --namespace tokenkey/EC2
  --metric-name RootVolumeUsedPercent
  --dimensions "Name=InstanceId,Value=${INSTANCE_ID}"
  --statistic Average
  --period 300
  --evaluation-periods 2
  --datapoints-to-alarm 2
  --threshold 85
  --comparison-operator GreaterThanThreshold
  --treat-missing-data notBreaching
)
if [ -n "$SNS_ARN" ]; then
  args+=(--alarm-actions "$SNS_ARN")
fi

aws cloudwatch put-metric-alarm "${args[@]}"
echo "sync-instance-root-disk-alarm: ok alarm=${ALARM_NAME} instance=${INSTANCE_ID} region=${REGION}"
