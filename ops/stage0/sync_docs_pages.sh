#!/usr/bin/env bash
#
# Stage0 docs → pages sync primitive.
#
# Copies one or more Markdown files from docs/public/ in this repo to the prod
# server's /var/lib/tokenkey/app/pages/ directory (bind-mounted as
# /app/data/pages/ inside the tokenkey container) so they are served by
# GET /api/v1/pages/:slug.
#
# Usage:
#   ops/stage0/sync_docs_pages.sh <instance_id> docs/public/FILE.md [docs/public/FILE2.md ...]
#
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region for SSM (default: us-east-1)
#   STAGE0_SSM_TIMEOUT_SECONDS        SSM poll timeout (default: 120)
#   STAGE0_SSM_OUTPUT_DIR             directory to write ssm output files (default: .)
#
# Example:
#   INSTANCE_ID=i-0abc... ops/stage0/sync_docs_pages.sh i-0abc... docs/public/HELP.md
#
# Slug derivation: strip docs/public/ prefix and .md suffix, lowercase, _ → -
#   docs/public/HELP_CENTER.md  →  help-center
#
# After the sync, register the page in Admin → Settings → custom_menu_items:
#   { "label": "Help", "url": "md:help-center", "icon_svg": "<svg>...</svg>" }
#
# IMPORTANT: only docs/public/*.md may be synced. Internal docs paths are rejected.

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
shift || true

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "Usage: $0 <instance_id> docs/public/FILE.md [docs/public/FILE2.md ...]" >&2
  exit 1
fi

TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-120}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"

FILES=("$@")
if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "sync-docs-pages: no files specified (pass docs/public/*.md paths explicitly)" >&2
  exit 1
fi

# Safety gate: only docs/public/*.md may be synced to the public pages endpoint.
# ops/stage0/fixtures/*.md is allowed for preflight host-parse checks only.
for f in "${FILES[@]}"; do
  case "$f" in
    docs/public/*.md) ;;
    ops/stage0/fixtures/*.md) ;;
    *) echo "::error::only docs/public/*.md files may be synced to pages (got: $f)" >&2; exit 1 ;;
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
# Format: {"commands": ["cmd1", "cmd2", ...]} — flat array, NOT the
# {"commands": {"Value": [...], "Type": "StringList"}} dict form which
# causes a botocore ParamValidation error with aws-cli v2.
mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"

jq -n --arg script "$REMOTE_SCRIPT" \
  '{"commands": [$script]}' \
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
