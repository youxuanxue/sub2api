#!/usr/bin/env bash
# Export prod qa_records + qa_blobs to local (same as fetch-prod-qa-dump.sh), verify
# manifest counts/checksums, then purge PostgreSQL qa_records and local blob/export files
# on the EC2 host, and remove the S3 staging object — so EBS + S3 do not retain QA
# payload after a good pull.
#
# Online impact (intentionally limited):
# - TRUNCATE qa_records briefly locks that table; capture traffic may block for a moment.
# - User self-service QA zip downloads under qa_blobs/exports/ are removed with the tree.
# - Core gateway (chat/images/video) does not read qa_records on the request path.
#
# Data-loss caveats:
# - Rows/files created after the remote tarball snapshot but before TRUNCATE are NOT in
#   the archive unless you re-export. Prefer low-traffic windows, pause QA capture if you
#   can, and set PURGE_MAX_EXTRA_ROWS to abort TRUNCATE when remote count(*) grew past
#   export_lines+N (count-only gate; not a row-identity proof).
#
# Required env:
#   QA_DUMP_S3_BUCKET              staging bucket for the tarball (see fetch-prod-qa-dump.sh)
#   PROD_QA_PURGE_CONFIRM          must be exactly: yes-delete-prod-qa-data
#
# Optional env:
#   PROD_QA_PURGE_DRY_RUN=1        or pass --dry-run: export + verify only; no TRUNCATE / no s3 rm
#   ALLOW_PURGE_EMPTY_QA_EXPORT=1  allow purge when qa_records.jsonl is empty (disk may still have
#                                   orphans you want to TRUNCATE+find-clean; rare)
#   PURGE_MAX_EXTRA_ROWS           if set (e.g. 0 = strict), run an extra SSM round-trip before
#                                   TRUNCATE: abort unless remote count(*) <= qa_records_lines + N.
#                                   Reduces “new rows after export snapshot” being deleted unseen.
#   AWS_REGION / STACK / OUT_DIR / QA_DUMP_S3_PREFIX / PRESIGN_TTL_SEC / AWS_SSM_WAIT_MAX
#     same as fetch-prod-qa-dump.sh
#   PURGE_SSM_WAIT_MAX             default 900
#   KEEP_LOCAL_TAR_AFTER_PURGE=1   keep the .tar.gz (default: delete after success)
#
# Usage:
#   QA_DUMP_S3_BUCKET=my-bucket PROD_QA_PURGE_CONFIRM=yes-delete-prod-qa-data \
#     bash scripts/prod-qa-export-and-purge.sh
#   PROD_QA_PURGE_DRY_RUN=1 QA_DUMP_S3_BUCKET=my-bucket PROD_QA_PURGE_CONFIRM=yes-delete-prod-qa-data \
#     bash scripts/prod-qa-export-and-purge.sh
#
set -euo pipefail

REGION="${AWS_REGION:-us-east-1}"
STACK="${STACK:-tokenkey-prod-stage0}"
OUT_DIR="${OUT_DIR:-./.dump_trajs}"
PURGE_WAIT="${PURGE_SSM_WAIT_MAX:-900}"
KEEP_TAR="${KEEP_LOCAL_TAR_AFTER_PURGE:-0}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

err() { echo "[prod-qa-export-and-purge] error: $*" >&2; }
log() { echo "[prod-qa-export-and-purge] $*"; }
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

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
fi
if [[ "${PROD_QA_PURGE_DRY_RUN:-0}" == "1" || "${PROD_QA_PURGE_DRY_RUN:-}" == "true" ]]; then
  DRY_RUN=1
fi

if [[ "${PROD_QA_PURGE_CONFIRM:-}" != "yes-delete-prod-qa-data" ]]; then
  err "set PROD_QA_PURGE_CONFIRM=yes-delete-prod-qa-data (literal) to allow destructive purge"
  exit 1
fi

if [[ -z "${QA_DUMP_S3_BUCKET:-}" ]]; then
  err "set QA_DUMP_S3_BUCKET"
  exit 1
fi

cd "$REPO_ROOT"

export AWS_REGION="$REGION"
export STACK
export OUT_DIR
export QA_DUMP_S3_BUCKET

log "phase 1/4: export to $OUT_DIR (fetch-prod-qa-dump.sh)"
bash "$SCRIPT_DIR/fetch-prod-qa-dump.sh" || { err "export failed; remote prod unchanged"; exit 1; }

MANIFEST="$OUT_DIR/.last-prod-qa-export.json"
if [[ ! -f "$MANIFEST" ]]; then
  err "missing manifest $MANIFEST after export"
  exit 1
fi

if [[ ! -f "$OUT_DIR/metadata/qa_records.jsonl" ]] || [[ ! -d "$OUT_DIR/qa_blobs" ]]; then
  err "local extract incomplete (metadata/qa_records.jsonl or qa_blobs/)"
  exit 1
fi

LINES="$(jq -r '.qa_records_lines // 0' "$MANIFEST")"
BLOBS="$(jq -r '.local_qa_blob_files // 0' "$MANIFEST")"
S3_KEY="$(jq -r .s3_key "$MANIFEST")"
BUCKET="$(jq -r .bucket "$MANIFEST")"
INSTANCE_ID="$(jq -r .instance_id "$MANIFEST")"
TARBALL="$(jq -r .tarball "$MANIFEST")"
TARBALL_SHA256="$(jq -r '.tarball_sha256 // empty' "$MANIFEST")"
QA_RECORDS_SHA256="$(jq -r '.qa_records_sha256 // empty' "$MANIFEST")"

log "local verify: qa_records_lines=$LINES local_qa_blob_files=$BLOBS"
if [[ -n "$QA_RECORDS_SHA256" ]]; then
  ACTUAL_QA_RECORDS_SHA256="$(sha256_file "$OUT_DIR/metadata/qa_records.jsonl")"
  if [[ "$ACTUAL_QA_RECORDS_SHA256" != "$QA_RECORDS_SHA256" ]]; then
    err "refusing purge: qa_records.jsonl checksum mismatch"
    exit 1
  fi
fi
if [[ -n "$TARBALL_SHA256" && -f "$OUT_DIR/$TARBALL" ]]; then
  ACTUAL_TARBALL_SHA256="$(sha256_file "$OUT_DIR/$TARBALL")"
  if [[ "$ACTUAL_TARBALL_SHA256" != "$TARBALL_SHA256" ]]; then
    err "refusing purge: local tarball checksum mismatch"
    exit 1
  fi
fi
if [[ "$LINES" -lt 1 && "${ALLOW_PURGE_EMPTY_QA_EXPORT:-0}" != "1" ]]; then
  err "refusing purge: qa_records_lines=$LINES (set ALLOW_PURGE_EMPTY_QA_EXPORT=1 to override)"
  exit 1
fi
if [[ "$BLOBS" -lt 1 ]]; then
  err "refusing purge: no files under local qa_blobs/"
  exit 1
fi

SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT
PURGE_PARAMS="$SCRATCH/purge-ssm.json"

cat > "$PURGE_PARAMS" <<'PURGEJSON'
{
  "commands": [
    "set -euo pipefail",
    "echo \"=== pre: qa_records count ===\"",
    "PGPASS=$(sudo grep '^POSTGRES_PASSWORD=' /var/lib/tokenkey/.env | cut -d= -f2-)",
    "[[ -n \"$PGPASS\" ]] || { echo no_pg_password >&2; exit 2; }",
    "NET=$(sudo docker inspect tokenkey-postgres --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}')",
    "sudo docker run --rm --network \"$NET\" -e PGPASSWORD=\"$PGPASS\" postgres:16-alpine psql -h tokenkey-postgres -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -c \"SELECT count(*)::bigint AS qa_records_before FROM qa_records;\"",
    "echo \"=== TRUNCATE qa_records (all partitions) ===\"",
    "sudo docker run --rm --network \"$NET\" -e PGPASSWORD=\"$PGPASS\" postgres:16-alpine psql -h tokenkey-postgres -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -c \"TRUNCATE TABLE qa_records RESTART IDENTITY;\"",
    "echo \"=== remove qa_blobs tree contents (keep mount dir) ===\"",
    "sudo find /var/lib/tokenkey/app/qa_blobs -mindepth 1 -delete",
    "echo \"=== remove qa_dlq tree contents (if present) ===\"",
    "sudo find /var/lib/tokenkey/app/qa_dlq -mindepth 1 -delete 2>/dev/null || true",
    "echo \"=== post: verify empty ===\"",
    "QA_AFTER=$(sudo docker run --rm --network \"$NET\" -e PGPASSWORD=\"$PGPASS\" postgres:16-alpine psql -h tokenkey-postgres -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1 -c \"SELECT count(*) FROM qa_records;\" | tr -d '[:space:]')",
    "[[ \"$QA_AFTER\" == \"0\" ]] || { echo \"qa_records_after=$QA_AFTER (expected 0)\" >&2; exit 3; }",
    "BLOB_AFTER=$(sudo find /var/lib/tokenkey/app/qa_blobs -type f | wc -l | tr -d '[:space:]')",
    "[[ \"$BLOB_AFTER\" == \"0\" ]] || { echo \"qa_blobs_files_after=$BLOB_AFTER (expected 0)\" >&2; exit 4; }",
    "sudo du -sh /var/lib/tokenkey/app/qa_blobs /var/lib/tokenkey/app/qa_dlq 2>/dev/null || true",
    "echo purge_ok"
  ]
}
PURGEJSON

if [[ "$DRY_RUN" -eq 1 ]]; then
  log "dry-run: skipping remote purge and s3 rm"
  log "would run SSM purge on instance=$INSTANCE_ID"
  log "would delete s3://$BUCKET/$S3_KEY"
  exit 0
fi

# Optional drift gate: prod row count must not exceed export line count + slack (OPC mechanical guard).
if [[ "${PURGE_MAX_EXTRA_ROWS+set}" == set ]]; then
  CAP=$((LINES + PURGE_MAX_EXTRA_ROWS))
  log "phase 2/4: row-drift precheck (remote count must be <= $CAP = export_lines $LINES + PURGE_MAX_EXTRA_ROWS $PURGE_MAX_EXTRA_ROWS)"
  DRIFT_PARAMS="$SCRATCH/drift-ssm.json"
  ECHO_DRIFT='echo prod_qa_drift_check remote_rows=$R export_lines='"${LINES}"' cap='"${CAP}"
  CMP_DRIFT='[[ "$R" -le '"${CAP}"' ]] || { echo prod_qa_purge_row_drift_abort >&2; exit 5; }'
  jq -n \
    --arg echo_drift "$ECHO_DRIFT" \
    --arg cmp_drift "$CMP_DRIFT" \
    '{
      commands: [
        "set -euo pipefail",
        "PGPASS=$(sudo grep '\''^POSTGRES_PASSWORD='\'' /var/lib/tokenkey/.env | cut -d= -f2-)",
        "[[ -n \"$PGPASS\" ]] || { echo no_pg_password >&2; exit 2; }",
        "NET=$(sudo docker inspect tokenkey-postgres --format '\''{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}'\'')",
        "R=$(sudo docker run --rm --network \"$NET\" -e PGPASSWORD=\"$PGPASS\" postgres:16-alpine psql -h tokenkey-postgres -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1 -c \"SELECT count(*) FROM qa_records;\" | tr -d \"[:space:]\")",
        $echo_drift,
        $cmp_drift,
        "echo drift_ok"
      ]
    }' > "$DRIFT_PARAMS"
  D_CMD="$(aws ssm send-command --region "$REGION" \
    --instance-ids "$INSTANCE_ID" \
    --document-name AWS-RunShellScript \
    --comment "tokenkey prod QA purge drift check" \
    --timeout-seconds "$PURGE_WAIT" \
    --parameters "file://$DRIFT_PARAMS" \
    --query 'Command.CommandId' --output text)"
  D_DEAD=$(( $(date +%s) + PURGE_WAIT ))
  while true; do
    D_ST="$(aws ssm get-command-invocation --region "$REGION" \
      --command-id "$D_CMD" --instance-id "$INSTANCE_ID" \
      --query 'Status' --output text 2>/dev/null || echo InProgress)"
    case "$D_ST" in Success|Failed|TimedOut|Cancelled) break ;; esac
    [[ $(date +%s) -lt $D_DEAD ]] || { D_ST=TimedOut; break; }
    sleep 4
  done
  D_OUT="$(aws ssm get-command-invocation --region "$REGION" \
    --command-id "$D_CMD" --instance-id "$INSTANCE_ID" \
    --query 'StandardOutputContent' --output text)"
  D_ERR="$(aws ssm get-command-invocation --region "$REGION" \
    --command-id "$D_CMD" --instance-id "$INSTANCE_ID" \
    --query 'StandardErrorContent' --output text)"
  if [[ "$D_ST" != "Success" ]] || ! grep -q drift_ok <<<"$D_OUT"; then
    err "row-drift precheck failed status=$D_ST (refusing TRUNCATE; prod unchanged)"
    echo "$D_ERR" >&2
    echo "$D_OUT" >&2
    exit 1
  fi
  echo "$D_OUT"
fi

log "phase 3/4: remote purge via SSM on $INSTANCE_ID"
CMD_ID="$(aws ssm send-command --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "tokenkey prod QA purge after verified export" \
  --timeout-seconds "$PURGE_WAIT" \
  --parameters "file://$PURGE_PARAMS" \
  --query 'Command.CommandId' --output text)"
log "purge command-id=$CMD_ID (waiting up to ${PURGE_WAIT}s)"

DEADLINE=$(( $(date +%s) + PURGE_WAIT ))
while true; do
  STATUS="$(aws ssm get-command-invocation --region "$REGION" \
    --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "$STATUS" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  [[ $(date +%s) -lt $DEADLINE ]] || { STATUS=TimedOut; break; }
  sleep 6
done

STDOUT="$(aws ssm get-command-invocation --region "$REGION" \
  --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'StandardOutputContent' --output text)"
STDERR="$(aws ssm get-command-invocation --region "$REGION" \
  --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
  --query 'StandardErrorContent' --output text)"

if [[ "$STATUS" != "Success" ]]; then
  err "purge SSM status=$STATUS"
  echo "$STDERR" >&2
  echo "$STDOUT" >&2
  exit 1
fi

echo "$STDOUT"
if ! grep -q purge_ok <<<"$STDOUT"; then
  err "purge output missing purge_ok marker"
  exit 1
fi

log "phase 4/4: delete S3 staging object s3://$BUCKET/$S3_KEY"
if ! aws s3 rm --region "$REGION" "s3://${BUCKET}/${S3_KEY}"; then
  err "s3 rm failed — remove s3://${BUCKET}/${S3_KEY} manually to avoid storage drift"
  exit 1
fi

if [[ "$KEEP_TAR" != "1" && "$KEEP_TAR" != "true" ]]; then
  if [[ -f "$OUT_DIR/$TARBALL" ]]; then
    rm -f "$OUT_DIR/$TARBALL"
    log "removed local tarball $OUT_DIR/$TARBALL (KEEP_LOCAL_TAR_AFTER_PURGE=1 to retain)"
  fi
fi

log "complete: prod QA data purged; local copy under $OUT_DIR; S3 staging object removed"
