#!/bin/bash
# Resolve the latest completed snapshot for the stack's persistent data volume.
set -u

REGION="${1:-}"
STACK_NAME="${2:-}"

emit_signal() {
  local value="${1:-}"
  jq -cn --arg value "$value" '{latest_snapshot_at:(if $value == "" or $value == "None" or $value == "null" then null else $value end)}'
}

if [[ -z "$REGION" || -z "$STACK_NAME" ]]; then
  emit_signal ""
  exit 0
fi

if ! data_volume_id="$(aws cloudformation describe-stacks \
  --region "$REGION" \
  --stack-name "$STACK_NAME" \
  --query 'Stacks[0].Outputs[?OutputKey==`DataVolumeId`].OutputValue | [0]' \
  --output text 2>/dev/null)"; then
  emit_signal ""
  exit 0
fi

case "$data_volume_id" in
  ""|None|null)
    emit_signal ""
    exit 0
    ;;
esac

if ! snapshot_at="$(aws ec2 describe-snapshots \
  --region "$REGION" \
  --owner-ids self \
  --filters "Name=volume-id,Values=$data_volume_id" "Name=status,Values=completed" \
  --query 'reverse(sort_by(Snapshots,&StartTime))[0].StartTime' \
  --output text 2>/dev/null)"; then
  emit_signal ""
  exit 0
fi

emit_signal "$snapshot_at"
