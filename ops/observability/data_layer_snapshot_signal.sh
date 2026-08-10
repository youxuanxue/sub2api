#!/bin/bash
# Resolve the latest completed snapshot for the stack's persistent data volume.
set -u

REGION="${1:-}"
STACK_NAME="${2:-}"

emit_signal() {
  local value="${1:-}"
  local probe_error="${2:-}"
  jq -cn \
    --arg value "$value" \
    --arg err "$probe_error" \
    '{
      latest_snapshot_at: (if $value == "" or $value == "None" or $value == "null" then null else $value end),
      probe_ok: (if $err == "" then true else false end),
      probe_error: (if $err == "" then null else $err end)
    }'
}

if [[ -z "$REGION" || -z "$STACK_NAME" ]]; then
  emit_signal "" "missing region or stack name"
  exit 0
fi

cfn_err="$(mktemp)"
trap 'rm -f "$cfn_err"' EXIT
if ! data_volume_id="$(aws cloudformation describe-stacks \
  --region "$REGION" \
  --stack-name "$STACK_NAME" \
  --query 'Stacks[0].Outputs[?OutputKey==`DataVolumeId`].OutputValue | [0]' \
  --output text 2>"$cfn_err")"; then
  emit_signal "" "$(head -1 "$cfn_err" | tr -d '\n' | cut -c1-240)"
  exit 0
fi

case "$data_volume_id" in
  ""|None|null)
    emit_signal "" "DataVolumeId stack output is empty"
    exit 0
    ;;
esac

snap_err="$(mktemp)"
trap 'rm -f "$cfn_err" "$snap_err"' EXIT
if ! snapshot_at="$(aws ec2 describe-snapshots \
  --region "$REGION" \
  --owner-ids self \
  --filters "Name=volume-id,Values=$data_volume_id" "Name=status,Values=completed" \
  --query 'reverse(sort_by(Snapshots,&StartTime))[0].StartTime' \
  --output text 2>"$snap_err")"; then
  emit_signal "" "$(head -1 "$snap_err" | tr -d '\n' | cut -c1-240)"
  exit 0
fi

emit_signal "$snapshot_at" ""
