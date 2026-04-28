#!/usr/bin/env bash
# Export all prod qa_records (JSONL) + qa_blobs tree from AWS Stage-0 EC2 to ./.dump_trajs/
#
# Flow: SSM builds a tarball on the instance (metadata/qa_records.jsonl + qa_blobs/),
# uploads it with curl + S3 presigned PUT (no EC2 instance S3 IAM required), then
# downloads, extracts locally, and writes a manifest with line/file counts plus checksums.
#
# Requires (operator IAM):
#   - ssm:SendCommand, ssm:GetCommandInvocation on the prod instance
#   - s3:PutObject + s3:GetObject on QA_DUMP_S3_BUCKET (for presign + download)
#
# Required env:
#   QA_DUMP_S3_BUCKET   staging bucket (e.g. private bucket in the same account)
#
# Optional env:
#   AWS_REGION          default: us-east-1
#   STACK               default: tokenkey-prod-stage0
#   QA_DUMP_S3_PREFIX   default: tokenkey/prod-qa-dump  (no leading/trailing slash)
#   OUT_DIR             default: ./.dump_trajs
#   PRESIGN_TTL_SEC     default: 7200
#   AWS_SSM_WAIT_MAX    default: 900
#   RM_LOCAL_TAR_AFTER_EXTRACT=1  delete the .tar.gz after successful extract (saves ~1× blob size on disk)
#
# Usage:
#   QA_DUMP_S3_BUCKET=my-bucket bash scripts/fetch-prod-qa-dump.sh
#   bash scripts/fetch-prod-qa-dump.sh --check
#
# After a verified export, optional full prod cleanup (DB + EBS blobs + S3 staging):
#   scripts/prod-qa-export-and-purge.sh
#
set -euo pipefail

REGION="${AWS_REGION:-us-east-1}"
STACK="${STACK:-tokenkey-prod-stage0}"
OUT_DIR="${OUT_DIR:-./.dump_trajs}"
PREFIX="${QA_DUMP_S3_PREFIX:-tokenkey/prod-qa-dump}"
PRESIGN_TTL="${PRESIGN_TTL_SEC:-7200}"
WAIT_MAX="${AWS_SSM_WAIT_MAX:-900}"
RM_LOCAL_TAR="${RM_LOCAL_TAR_AFTER_EXTRACT:-0}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

err() { echo "[fetch-prod-qa-dump] error: $*" >&2; }
log() { echo "[fetch-prod-qa-dump] $*"; }
sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  err "shasum or sha256sum missing"
  exit 1
}

if [[ "${1:-}" == "--check" ]]; then
  command -v aws >/dev/null 2>&1 || { err "aws CLI missing"; exit 1; }
  command -v jq >/dev/null 2>&1 || { err "jq missing"; exit 1; }
  command -v curl >/dev/null 2>&1 || { err "curl missing"; exit 1; }
  command -v python3 >/dev/null 2>&1 || { err "python3 missing (for venv + boto3 presign)"; exit 1; }
  command -v shasum >/dev/null 2>&1 || command -v sha256sum >/dev/null 2>&1 || { err "shasum or sha256sum missing"; exit 1; }
  [[ -n "${QA_DUMP_S3_BUCKET:-}" ]] || { err "set QA_DUMP_S3_BUCKET"; exit 1; }
  log "OK (tools + QA_DUMP_S3_BUCKET)"
  exit 0
fi

if ! command -v aws >/dev/null 2>&1; then err "aws CLI missing"; exit 1; fi
if ! command -v jq >/dev/null 2>&1; then err "jq missing"; exit 1; fi
if ! command -v python3 >/dev/null 2>&1; then err "python3 missing"; exit 1; fi
[[ -n "${QA_DUMP_S3_BUCKET:-}" ]] || { err "set QA_DUMP_S3_BUCKET (staging bucket for the tarball)"; exit 1; }

BUCKET="$QA_DUMP_S3_BUCKET"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
S3_KEY="${PREFIX}/${STAMP}.tar.gz"
TARBALL_NAME="tokenkey-qa-dump-${STAMP}.tar.gz"

INSTANCE_ID="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)"
if [[ -z "$INSTANCE_ID" || "$INSTANCE_ID" == "None" ]]; then
  err "could not resolve InstanceId from stack $STACK"
  exit 1
fi

log "instance=$INSTANCE_ID stamp=$STAMP s3=s3://$BUCKET/$S3_KEY"

SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT
VENV="$SCRATCH/presign-venv"
log "creating venv for boto3 presign (PUT) …"
python3 -m venv "$VENV"
"$VENV/bin/pip" install -q boto3

export AWS_REGION="$REGION"
export PRESIGN_TTL_SEC="$PRESIGN_TTL"
export QA_DUMP_S3_BUCKET="$BUCKET"
export S3_KEY_FOR_PRESIGN="$S3_KEY"

# AWS CLI `s3 presign` in many builds is GET-only; use boto3 for SigV4 PUT URLs.
PUT_URL="$("$VENV/bin/python" -c "
import os
import boto3
b = os.environ['QA_DUMP_S3_BUCKET']
k = os.environ['S3_KEY_FOR_PRESIGN']
r = os.environ['AWS_REGION']
e = int(os.environ['PRESIGN_TTL_SEC'])
c = boto3.client('s3', region_name=r)
print(c.generate_presigned_url(
    'put_object',
    Params={'Bucket': b, 'Key': k},
    ExpiresIn=e,
), end='')
")"
PRESIGN_B64="$(printf '%s' "$PUT_URL" | base64 | tr -d '\n')"

PARAMS="$SCRATCH/ssm-params.json"

jq -n \
  --arg stamp "$STAMP" \
  --arg b64 "$PRESIGN_B64" \
  --arg tarball "$TARBALL_NAME" \
  '{
    commands: [
      "set -euo pipefail",
      ("STAMP=" + $stamp),
      ("sudo rm -rf /tmp/qa-dump-" + $stamp + " /tmp/" + $tarball + " 2>/dev/null || true"),
      ("WORKDIR=/tmp/qa-dump-" + $stamp),
      "sudo mkdir -p \"$WORKDIR/metadata\"",
      "PGPASS=$(sudo grep '\''^POSTGRES_PASSWORD='\'' /var/lib/tokenkey/.env | cut -d= -f2-)",
      "[[ -n \"$PGPASS\" ]] || { echo no_pg_password >&2; exit 2; }",
      "NET=$(sudo docker inspect tokenkey-postgres --format '\''{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}'\'')",
      "sudo docker run --rm --network \"$NET\" -e PGPASSWORD=\"$PGPASS\" postgres:16-alpine psql -h tokenkey-postgres -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -q -t -A -c \"COPY (SELECT row_to_json(r) FROM qa_records r ORDER BY id) TO STDOUT;\" | sudo tee \"$WORKDIR/metadata/qa_records.jsonl\" > /dev/null",
      ("sudo tar czf /tmp/" + $tarball + " -C \"$WORKDIR\" metadata -C /var/lib/tokenkey/app qa_blobs"),
      "sudo rm -rf \"$WORKDIR\"",
      ("echo " + $b64 + " | base64 -d > /tmp/qa-dump-put.url"),
      ("curl -fS --max-time 3600 -X PUT --upload-file /tmp/" + $tarball + " \"$(cat /tmp/qa-dump-put.url)\""),
      ("sudo rm -f /tmp/qa-dump-put.url /tmp/" + $tarball),
      ("echo uploaded=" + $tarball)
    ]
  }' > "$PARAMS"

CMD_ID="$(aws ssm send-command --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "tokenkey prod QA dump $STAMP" \
  --timeout-seconds "$WAIT_MAX" \
  --parameters "file://$PARAMS" \
  --query 'Command.CommandId' --output text)"
log "ssm command-id=$CMD_ID (waiting up to ${WAIT_MAX}s)"

DEADLINE=$(( $(date +%s) + WAIT_MAX ))
while true; do
  STATUS="$(aws ssm get-command-invocation --region "$REGION" \
    --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "$STATUS" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  [[ $(date +%s) -lt $DEADLINE ]] || { STATUS=TimedOut; break; }
  sleep 8
done

if [[ "$STATUS" != "Success" ]]; then
  err "ssm status=$STATUS"
  aws ssm get-command-invocation --region "$REGION" \
    --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query '{stderr:StandardErrorContent,stdout:StandardOutputContent}' --output text
  exit 1
fi

mkdir -p "$OUT_DIR"
LOCAL_TAR="${OUT_DIR}/${TARBALL_NAME}"
log "downloading to $LOCAL_TAR"
aws s3 cp --region "$REGION" "s3://${BUCKET}/${S3_KEY}" "$LOCAL_TAR"

log "extracting into $OUT_DIR"
tar xzf "$LOCAL_TAR" -C "$OUT_DIR"

RECORDS_LINES="$(wc -l < "$OUT_DIR/metadata/qa_records.jsonl" | tr -d ' ')"
BLOB_FILES="$(find "$OUT_DIR/qa_blobs" -type f 2>/dev/null | wc -l | tr -d ' ')"
TARBALL_SHA256="$(sha256_file "$LOCAL_TAR")"
QA_RECORDS_SHA256="$(sha256_file "$OUT_DIR/metadata/qa_records.jsonl")"
log "validating qa_records blob_uri references"
python3 "$SCRIPT_DIR/check-qa-blob-references.py" "$OUT_DIR/metadata/qa_records.jsonl" "$OUT_DIR/qa_blobs"
BLOB_REFERENCE_REPORT="$(python3 "$SCRIPT_DIR/check-qa-blob-references.py" --json "$OUT_DIR/metadata/qa_records.jsonl" "$OUT_DIR/qa_blobs")"
BLOB_REFERENCED_URIS="$(jq -r '.referenced_blob_uris' <<<"$BLOB_REFERENCE_REPORT")"
BLOB_CHECKED_LOCAL_URIS="$(jq -r '.checked_local_blob_uris' <<<"$BLOB_REFERENCE_REPORT")"

jq -n \
  --arg stamp "$STAMP" \
  --arg s3_key "$S3_KEY" \
  --arg bucket "$BUCKET" \
  --arg tarball "$TARBALL_NAME" \
  --arg tarball_sha256 "$TARBALL_SHA256" \
  --arg qa_records_sha256 "$QA_RECORDS_SHA256" \
  --arg region "$REGION" \
  --arg stack "$STACK" \
  --arg instance_id "$INSTANCE_ID" \
  --arg exported_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson qa_records_lines "$RECORDS_LINES" \
  --argjson local_qa_blob_files "$BLOB_FILES" \
  --argjson referenced_blob_uris "$BLOB_REFERENCED_URIS" \
  --argjson checked_local_blob_uris "$BLOB_CHECKED_LOCAL_URIS" \
  '{
    stamp: $stamp,
    s3_key: $s3_key,
    bucket: $bucket,
    tarball: $tarball,
    tarball_sha256: $tarball_sha256,
    qa_records_sha256: $qa_records_sha256,
    region: $region,
    stack: $stack,
    instance_id: $instance_id,
    exported_at_utc: $exported_at,
    qa_records_lines: $qa_records_lines,
    local_qa_blob_files: $local_qa_blob_files,
    referenced_blob_uris: $referenced_blob_uris,
    checked_local_blob_uris: $checked_local_blob_uris
  }' > "$OUT_DIR/.last-prod-qa-export.json"

if [[ "$RM_LOCAL_TAR" == "1" || "$RM_LOCAL_TAR" == "true" ]]; then
  rm -f "$LOCAL_TAR"
  log "removed local tarball after extract (RM_LOCAL_TAR_AFTER_EXTRACT)"
fi

log "done: $OUT_DIR/metadata/qa_records.jsonl + $OUT_DIR/qa_blobs/ (blob_uri still uses container paths file:///app/data/qa_blobs/...)"
log "manifest: $OUT_DIR/.last-prod-qa-export.json (${RECORDS_LINES} lines, ${BLOB_FILES} local blob files)"
