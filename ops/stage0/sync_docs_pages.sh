#!/usr/bin/env bash
#
# Stage0 docs → pages sync primitive.
#
# Copies one or more Markdown files from docs/ in this repo to the prod server's
# /var/lib/tokenkey/app/pages/ directory (bind-mounted as /app/data/pages/ inside
# the tokenkey container) so they are served by GET /api/v1/pages/:slug.
#
# Usage:
#   ops/stage0/sync_docs_pages.sh <instance_id> [docs/FILE.md ...]
#   INSTANCE_ID=<id> ops/stage0/sync_docs_pages.sh <instance_id>
#
# If no files are given, the default set SYNC_DOCS_DEFAULT is used.
#
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region for SSM (default: us-east-1)
#   STAGE0_SSM_TIMEOUT_SECONDS        SSM poll timeout (default: 120)
#   STAGE0_SSM_OUTPUT_DIR             directory to write ssm output files (default: .)
#
# Example:
#   INSTANCE_ID=i-0abc... ops/stage0/sync_docs_pages.sh i-0abc... docs/public/USER_GUIDE_CLAUDE_CODE.md
#
# IMPORTANT: only docs/public/* files may be synced. Passing any other path
# (docs/approved/, docs/ops/, docs/spec-delta-*, etc.) is rejected with an error.
#
# The target slug is derived by stripping the docs/public/ prefix and .md suffix, then
# lowercasing and replacing underscores with hyphens:
#   docs/public/USER_GUIDE_CLAUDE_CODE.md  →  user-guide-claude-code
#
# After the sync, register the page in Admin → Settings → custom_menu_items:
#   { "label": "Claude Code 接入指南", "url": "md:user-guide-claude-code", "icon": "book" }

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
shift || true

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "Usage: $0 <instance_id> [docs/FILE.md ...]" >&2
  exit 1
fi

TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-120}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"

# Default docs to sync if none specified.
# Only files under docs/public/ are eligible — docs/ root and subdirectories
# (approved/, ops/, spec-delta-*, accounts/, etc.) contain internal content
# and must never be synced to the public pages endpoint.
SYNC_DOCS_DEFAULT=(
  "docs/public/USER_GUIDE_CLAUDE_CODE.md"
)

FILES=("$@")
if [[ ${#FILES[@]} -eq 0 ]]; then
  FILES=("${SYNC_DOCS_DEFAULT[@]}")
fi

# Safety gate: only docs/public/* may be synced to the public pages endpoint.
# Internal docs (docs/approved/, docs/ops/, docs/spec-delta-*, etc.) must never
# be synced — reject anything outside docs/public/ with a hard error.
for f in "${FILES[@]}"; do
  case "$f" in
    docs/public/*) ;;
    *) echo "::error::only docs/public/* files may be synced to pages (got: $f)" >&2; exit 1 ;;
  esac
done

# Derive slug from filename: strip docs/ prefix, strip .md, lowercase, _ → -
derive_slug() {
  local f="$1"
  local base
  base=$(basename "$f" .md)
  echo "$base" | tr '[:upper:]' '[:lower:]' | tr '_' '-'
}

PAGES_DIR="/var/lib/tokenkey/app/pages"

# Build the remote commands array
remote_cmds=()
remote_cmds+=("mkdir -p '${PAGES_DIR}'")

for rel in "${FILES[@]}"; do
  abs="${REPO_ROOT}/${rel}"
  if [[ ! -f "$abs" ]]; then
    echo "::error::file not found: ${abs}" >&2
    exit 1
  fi
  slug=$(derive_slug "$rel")
  target="${PAGES_DIR}/${slug}.md"

  # base64-encode the file content
  b64=$(base64 < "$abs" | tr -d '\n')

  remote_cmds+=(
    "echo '${b64}' | base64 -d > '${target}.tmp'"
    "mv '${target}.tmp' '${target}'"
    "echo 'synced: ${slug}.md ($(wc -c < "$abs") bytes)'"
  )
done

REMOTE_SCRIPT=$(printf '%s\n' "${remote_cmds[@]}")

# Build SSM params JSON — write to ${OUTPUT_DIR}/ssm-params.json (same
# convention as deploy_via_ssm.sh / sync_caddyfile_via_ssm.sh so the
# check-stage0-ssm-host-parse.sh guard can validate the host script syntax).
mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"

jq -n --arg script "$REMOTE_SCRIPT" \
  '{"commands": {"Value": [$script], "Type": "StringList"}}' \
  > "$params_file"

region_args=()
region="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
[[ -n "$region" ]] && region_args=(--region "$region")

echo "Sending SSM command to ${INSTANCE_ID}..."
cmd_id="$(aws "${region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "sync-docs-pages" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "SSM command-id=${cmd_id}"
[[ -n "${GITHUB_OUTPUT:-}" ]] && echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${deadline} ]]; then
    echo "::error::SSM timeout waiting for ${cmd_id}" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

mkdir -p "${OUTPUT_DIR}"
aws "${region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text \
  > "${OUTPUT_DIR}/sync-docs-pages-stdout.txt" 2>/dev/null || true
aws "${region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardErrorContent' --output text \
  > "${OUTPUT_DIR}/sync-docs-pages-stderr.txt" 2>/dev/null || true

cat "${OUTPUT_DIR}/sync-docs-pages-stdout.txt"
if [[ -s "${OUTPUT_DIR}/sync-docs-pages-stderr.txt" ]]; then
  echo "--- stderr ---"
  cat "${OUTPUT_DIR}/sync-docs-pages-stderr.txt"
fi

if [[ "$status" == "Success" ]]; then
  echo "sync-docs-pages: SUCCESS"
else
  echo "::error::sync-docs-pages: status=${status}" >&2
  exit 1
fi
