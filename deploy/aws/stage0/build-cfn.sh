#!/usr/bin/env bash
# =============================================================================
# build-cfn.sh — refresh Stage 0 gzip|base64 blobs for docker-compose + Caddy,
# plus raw base64 for tokenkey-qa-stale-cleanup.sh, into CloudFormation SSM
# Parameter values.
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
EDGE_CADDY_SRC="${HERE}/Caddyfile.edge"
QA_CLEANUP_SRC="${HERE}/tokenkey-qa-stale-cleanup.sh"
CFN_FILE="${REPO_ROOT}/deploy/aws/cloudformation/stage0-single-ec2.yaml"
EDGE_CFN_FILE="${REPO_ROOT}/deploy/aws/cloudformation/stage0-edge-ec2.yaml"

mode="apply"
if [[ "${1:-}" == "--check" ]]; then
  mode="check"
fi

[[ -f "${COMPOSE_SRC}" ]] || { echo "missing ${COMPOSE_SRC}" >&2; exit 1; }
[[ -f "${CADDY_SRC}" ]] || { echo "missing ${CADDY_SRC}" >&2; exit 1; }
[[ -f "${EDGE_CADDY_SRC}" ]] || { echo "missing ${EDGE_CADDY_SRC}" >&2; exit 1; }
[[ -f "${QA_CLEANUP_SRC}" ]] || { echo "missing ${QA_CLEANUP_SRC}" >&2; exit 1; }
[[ -f "${CFN_FILE}" ]] || { echo "missing ${CFN_FILE}" >&2; exit 1; }
[[ -f "${EDGE_CFN_FILE}" ]] || { echo "missing ${EDGE_CFN_FILE}" >&2; exit 1; }

encode_gzb64() {
  gzip -9n -c "$1" | base64 | tr -d '\n'
}

encode_b64() {
  base64 <"$1" | tr -d '\n'
}

COMPOSE_GZB64="$(encode_gzb64 "${COMPOSE_SRC}")"
CADDY_GZB64="$(encode_gzb64 "${CADDY_SRC}")"
EDGE_CADDY_GZB64="$(encode_gzb64 "${EDGE_CADDY_SRC}")"
QA_CLEANUP_B64="$(encode_b64 "${QA_CLEANUP_SRC}")"

if [[ "${#COMPOSE_GZB64}" -gt 4096 ]] || [[ "${#CADDY_GZB64}" -gt 4096 ]] || [[ "${#EDGE_CADDY_GZB64}" -gt 4096 ]] || [[ "${#QA_CLEANUP_B64}" -gt 4096 ]]; then
  echo "One or more SSM Parameter values exceed Standard tier limit (4096 chars):" >&2
  echo "  compose: ${#COMPOSE_GZB64}  caddy: ${#CADDY_GZB64}  edge_caddy: ${#EDGE_CADDY_GZB64}  qa: ${#QA_CLEANUP_B64}" >&2
  exit 1
fi

refresh_template() {
  local src="$1"
  local dst="$2"
  local caddy_blob="$3"
  local indent='      '
  local new_compose="${indent}Value: '${COMPOSE_GZB64}'"
  local new_caddy="${indent}Value: '${caddy_blob}'"
  local new_qa="${indent}Value: '${QA_CLEANUP_B64}'"

  awk -v new_compose_ssm="${new_compose}" \
      -v new_caddy_ssm="${new_caddy}" \
      -v new_qa_ssm="${new_qa}" '
    BEGIN { skip = 0 }
    />>> COMPOSE_GZB64_SSM START/ { print; print new_compose_ssm; skip = 1; next }
    />>> COMPOSE_GZB64_SSM END/ { skip = 0; print; next }
    />>> CADDY_GZB64_SSM START/ { print; print new_caddy_ssm; skip = 1; next }
    />>> CADDY_GZB64_SSM END/ { skip = 0; print; next }
    />>> QA_CLEANUP_B64_PARAM START/ { print; print new_qa_ssm; skip = 1; next }
    />>> QA_CLEANUP_B64_PARAM END/ { skip = 0; print; next }
    { if (!skip) print }
  ' "${src}" > "${dst}"
}

tmp_main="$(mktemp)"
tmp_edge="$(mktemp)"
trap 'rm -f "${tmp_main}" "${tmp_edge}"' EXIT

refresh_template "${CFN_FILE}" "${tmp_main}" "${CADDY_GZB64}"
refresh_template "${EDGE_CFN_FILE}" "${tmp_edge}" "${EDGE_CADDY_GZB64}"

if [[ "${mode}" == "check" ]]; then
  drift=0
  if ! diff -q "${CFN_FILE}" "${tmp_main}" >/dev/null; then
    echo "stage0 CFN drift detected — run: bash deploy/aws/stage0/build-cfn.sh" >&2
    diff -u "${CFN_FILE}" "${tmp_main}" | head -n 80 >&2 || true
    drift=1
  fi
  if ! diff -q "${EDGE_CFN_FILE}" "${tmp_edge}" >/dev/null; then
    echo "edge Stage0 CFN drift detected — run: bash deploy/aws/stage0/build-cfn.sh" >&2
    diff -u "${EDGE_CFN_FILE}" "${tmp_edge}" | head -n 80 >&2 || true
    drift=1
  fi
  if [[ "${drift}" -eq 0 ]]; then
    echo "stage0 CFN embeds are up to date."
  fi
  exit "${drift}"
fi

mv "${tmp_main}" "${CFN_FILE}"
mv "${tmp_edge}" "${EDGE_CFN_FILE}"
trap - EXIT

body_bytes=$(awk '
  /UserData:/ { in_userdata = 1; next }
  /^  [A-Z]/ { if (in_userdata) exit }
  { if (in_userdata) print }
' "${CFN_FILE}" | wc -c | awk '{print $1}')
edge_body_bytes=$(awk '
  /UserData:/ { in_userdata = 1; next }
  /^  [A-Z]/ { if (in_userdata) exit }
  { if (in_userdata) print }
' "${EDGE_CFN_FILE}" | wc -c | awk '{print $1}')

echo "stage0 CFN refreshed."
echo "  compose gzip+base64 (SSM): ${#COMPOSE_GZB64} chars"
echo "  caddy gzip+base64 (SSM): ${#CADDY_GZB64} chars"
echo "  edge caddy gzip+base64 (SSM): ${#EDGE_CADDY_GZB64} chars"
echo "  qa cleanup base64 (SSM): ${#QA_CLEANUP_B64} chars"
echo "  main UserData body (raw, rough awk span): ${body_bytes} bytes  (EC2 limit 16384 after substitution)"
echo "  edge UserData body (raw, rough awk span): ${edge_body_bytes} bytes  (EC2 limit 16384 after substitution)"
if (( body_bytes > 14000 )) || (( edge_body_bytes > 14000 )); then
  echo "WARNING: a UserData body may be close to the 16384-byte EC2 limit." >&2
fi
