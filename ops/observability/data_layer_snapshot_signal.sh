#!/bin/bash
# Resolve the latest completed snapshot for the live instance's data volume.
# A stack is accepted only for explicit legacy callers without an instance ID.
#
# Prefer instance → attached data volume → volume-id snapshots. When
# DescribeInstances returns AccessDenied/UnauthorizedOperation (OIDC role
# historically had only DescribeSnapshots), fall back to DLM data-volume
# tags in the target region: Backup=stage0 + Purpose=
# tokenkey-state-postgres-redis-caddy. Other describe-instances errors
# stay fail-closed.
set -u

REGION="${1:-}"
STACK_NAME="${2:-}"
INSTANCE_ID="${3:-}"

# DLM CopyTags from the Stage0 DataVolume (see stage0-single-ec2.yaml).
DATA_VOLUME_BACKUP_TAG="${DATA_LAYER_SNAPSHOT_BACKUP_TAG:-stage0}"
DATA_VOLUME_PURPOSE_TAG="${DATA_LAYER_SNAPSHOT_PURPOSE_TAG:-tokenkey-state-postgres-redis-caddy}"

emit_signal() {
  local value="${1:-}"
  local probe_error="${2:-}"
  local applicable="${3:-true}"
  jq -cn \
    --arg value "$value" \
    --arg err "$probe_error" \
    --argjson applicable "$applicable" \
    '{
      latest_snapshot_at: (if $value == "" or $value == "None" or $value == "null" then null else $value end),
      probe_ok: (if $err == "" then true else false end),
      probe_error: (if $err == "" then null else $err end),
      applicable: $applicable
    }'
}

emit_latest_snapshot_for_volume() {
  local data_volume_id="$1"
  local snap_err
  snap_err="$(mktemp)"
  local snapshot_at
  if ! snapshot_at="$(aws ec2 describe-snapshots \
    --region "$REGION" \
    --owner-ids self \
    --filters "Name=volume-id,Values=$data_volume_id" "Name=status,Values=completed" \
    --query 'reverse(sort_by(Snapshots,&StartTime))[0].StartTime' \
    --output text 2>"$snap_err")"; then
    emit_signal "" "$(head -1 "$snap_err" | tr -d '\n' | cut -c1-240)"
    rm -f "$snap_err"
    return 0
  fi
  rm -f "$snap_err"
  emit_signal "$snapshot_at" ""
}

emit_latest_snapshot_by_data_volume_tags() {
  # Works with DescribeSnapshots alone (ops-daily OIDC baseline).
  local snap_err
  snap_err="$(mktemp)"
  local snapshot_at
  if ! snapshot_at="$(aws ec2 describe-snapshots \
    --region "$REGION" \
    --owner-ids self \
    --filters \
      "Name=tag:Backup,Values=$DATA_VOLUME_BACKUP_TAG" \
      "Name=tag:Purpose,Values=$DATA_VOLUME_PURPOSE_TAG" \
      "Name=status,Values=completed" \
    --query 'reverse(sort_by(Snapshots,&StartTime))[0].StartTime' \
    --output text 2>"$snap_err")"; then
    emit_signal "" "$(head -1 "$snap_err" | tr -d '\n' | cut -c1-240)"
    rm -f "$snap_err"
    return 0
  fi
  rm -f "$snap_err"
  emit_signal "$snapshot_at" ""
}

# Hybrid mi-* prod has no EBS data volume (pgdump owns backup freshness).
if [[ "${DATA_LAYER_SNAPSHOT_APPLICABLE:-true}" == "false" ]]; then
  emit_signal "" "" false
  exit 0
fi

if [[ -z "$REGION" || ( -z "$STACK_NAME" && -z "$INSTANCE_ID" ) ]]; then
  emit_signal "" "missing region or stack name"
  exit 0
fi

cfn_err="$(mktemp)"
trap 'rm -f "$cfn_err"' EXIT
if [ -n "$INSTANCE_ID" ]; then
  if [[ ! "$INSTANCE_ID" =~ ^i-[0-9a-f]+$ ]]; then
    emit_signal "" "invalid EC2 instance identity"
    exit 0
  fi
  if instance="$(aws ec2 describe-instances --region "$REGION" \
      --instance-ids "$INSTANCE_ID" --query 'Reservations[0].Instances[0]' \
      --output json 2>"$cfn_err")"; then
    # Fail closed if extra data volumes make the owner ambiguous.
    if ! data_volume_id="$(printf '%s' "$instance" | jq -er '
        .RootDeviceName as $root | [.BlockDeviceMappings[] |
        select(.DeviceName != $root and .Ebs.Status == "attached") | .Ebs.VolumeId] |
        if length == 1 then .[0] else error("expected one attached data volume") end' 2>/dev/null)"; then
      emit_signal "" "expected one attached data volume on live instance"
      exit 0
    fi
    case "$data_volume_id" in
      ""|None|null)
        emit_signal "" "live instance data volume id is empty"
        exit 0
        ;;
    esac
    emit_latest_snapshot_for_volume "$data_volume_id"
    exit 0
  fi
  # Only IAM denials fall back to DLM tags (DescribeSnapshots-only OIDC).
  # Other describe-instances failures stay fail-closed so a missing/wrong
  # instance cannot be greened by region-wide Backup/Purpose snapshots.
  describe_err="$(head -1 "$cfn_err" | tr -d '\n' | cut -c1-240)"
  case "$describe_err" in
    *AccessDenied*|*UnauthorizedOperation*)
      emit_latest_snapshot_by_data_volume_tags
      ;;
    *)
      emit_signal "" "${describe_err:-describe-instances failed}"
      ;;
  esac
  exit 0
elif ! data_volume_id="$(aws cloudformation describe-stacks \
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

emit_latest_snapshot_for_volume "$data_volume_id"
