#!/usr/bin/env bash
# =============================================================================
# build-cfn.sh — refresh Stage 0 gzip|base64 blobs for docker-compose + Caddy,
# plus raw base64 for tokenkey-qa-stale-cleanup.sh, into AWS::SSM::Parameter
# Values in deploy/aws/cloudformation/stage0-single-ec2.yaml.
#
# Why: EC2 UserData is capped at 16384 bytes. Embedding large base64 strings in
# UserData exceeds the limit; bootstrap reads three Standard SSM Parameters under
# /${ProjectName}/${Environment}/stage0/* instead.
#
# Usage:
#   bash deploy/aws/stage0/build-cfn.sh                    # in-place rewrite
#   bash deploy/aws/stage0/build-cfn.sh --check            # diff-only, exit 1 if drift
# =============================================================================
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../../.." && pwd)"
COMPOSE_SRC="${HERE}/docker-compose.yml"
CADDY_SRC="${HERE}/Caddyfile"
QA_CLEANUP_SRC="${HERE}/tokenkey-qa-stale-cleanup.sh"
CFN_FILE="${REPO_ROOT}/deploy/aws/cloudformation/stage0-single-ec2.yaml"

mode="apply"
if [[ "${1:-}" == "--check" ]]; then
  mode="check"
fi

[[ -f "${COMPOSE_SRC}"   ]] || { echo "missing ${COMPOSE_SRC}"   >&2; exit 1; }
[[ -f "${CADDY_SRC}"     ]] || { echo "missing ${CADDY_SRC}"     >&2; exit 1; }
[[ -f "${QA_CLEANUP_SRC}" ]] || { echo "missing ${QA_CLEANUP_SRC}" >&2; exit 1; }
[[ -f "${CFN_FILE}"      ]] || { echo "missing ${CFN_FILE}"      >&2; exit 1; }

# gzip is deterministic if we strip the mtime header (-n). Without -n the
# encoded blob churns every run, polluting diffs.
encode_gzb64() {
  gzip -9n -c "$1" | base64 | tr -d '\n'
}

COMPOSE_GZB64="$(encode_gzb64 "${COMPOSE_SRC}")"
CADDY_GZB64="$(encode_gzb64 "${CADDY_SRC}")"
encode_qa_b64() {
  base64 <"$1" | tr -d '\n'
}
QA_CLEANUP_B64="$(encode_qa_b64 "${QA_CLEANUP_SRC}")"

if [[ "${#COMPOSE_GZB64}" -gt 4096 ]] || [[ "${#CADDY_GZB64}" -gt 4096 ]] || [[ "${#QA_CLEANUP_B64}" -gt 4096 ]]; then
  echo "One or more SSM Parameter values exceed Standard tier limit (4096 chars):" >&2
  echo "  compose: ${#COMPOSE_GZB64}  caddy: ${#CADDY_GZB64}  qa: ${#QA_CLEANUP_B64}" >&2
  exit 1
fi

INDENT_SSM_VAL='      '

new_compose_ssm_line="${INDENT_SSM_VAL}Value: '${COMPOSE_GZB64}'"
new_caddy_ssm_line="${INDENT_SSM_VAL}Value: '${CADDY_GZB64}'"
new_qa_ssm_value_line="${INDENT_SSM_VAL}Value: '${QA_CLEANUP_B64}'"

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

awk -v new_compose_ssm="${new_compose_ssm_line}" \
    -v new_caddy_ssm="${new_caddy_ssm_line}" \
    -v new_qa_ssm="${new_qa_ssm_value_line}" '
  BEGIN { skip = 0 }
  />>> COMPOSE_GZB64_SSM START/ { print; print new_compose_ssm; skip = 1; next }
  />>> COMPOSE_GZB64_SSM END/   { skip = 0; print; next }
  />>> CADDY_GZB64_SSM START/   { print; print new_caddy_ssm; skip = 1; next }
  />>> CADDY_GZB64_SSM END/     { skip = 0; print; next }
  />>> QA_CLEANUP_B64_PARAM START/ { print; print new_qa_ssm; skip = 1; next }
  />>> QA_CLEANUP_B64_PARAM END/   { skip = 0; print; next }
  { if (!skip) print }
' "${CFN_FILE}" > "${tmp}"

if [[ "${mode}" == "check" ]]; then
  if diff -q "${CFN_FILE}" "${tmp}" >/dev/null; then
    echo "stage0 CFN embeds are up to date."
    exit 0
  else
    echo "stage0 CFN drift detected — run: bash deploy/aws/stage0/build-cfn.sh" >&2
    diff -u "${CFN_FILE}" "${tmp}" | head -n 80 >&2 || true
    exit 1
  fi
fi

mv "${tmp}" "${CFN_FILE}"
trap - EXIT

body_bytes=$(awk '
  /UserData:/        { in_userdata = 1; next }
  /^  [A-Z]/         { if (in_userdata) exit }
  { if (in_userdata) print }
' "${CFN_FILE}" | wc -c | awk '{print $1}')
echo "stage0 CFN refreshed."
echo "  compose gzip+base64 (SSM): ${#COMPOSE_GZB64} chars"
echo "  caddy   gzip+base64 (SSM): ${#CADDY_GZB64} chars"
echo "  qa cleanup base64 (SSM): ${#QA_CLEANUP_B64} chars"
echo "  UserData body (raw, rough awk span): ${body_bytes} bytes  (EC2 limit 16384 after substitution)"
if (( body_bytes > 14000 )); then
  echo "WARNING: UserData body may still be close to the 16384-byte EC2 limit." >&2
fi
