#!/usr/bin/env bash
# =============================================================================
# build-cfn.sh — refresh Stage 0 gzip|base64 blobs for docker-compose + Caddy,
# bootstrap.sh (split across SSM parts), plus raw base64 for ops scripts,
# into CloudFormation SSM Parameter values; regenerate thin EC2 UserData launcher.
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
PRUNE_SRC="${HERE}/tokenkey-prune-ghcr-app-tags.sh"
BOOTSTRAP_SRC="${HERE}/stage0-ec2-bootstrap.sh"
LAUNCHER_SRC="${HERE}/stage0-ec2-userdata-launcher.sub.sh"
CFN_FILE="${REPO_ROOT}/deploy/aws/cloudformation/stage0-single-ec2.yaml"
EDGE_CFN_FILE="${REPO_ROOT}/deploy/aws/cloudformation/stage0-edge-ec2.yaml"

EC2_USERDATA_LIMIT=16384
SSM_STANDARD_VALUE_LIMIT=4096
USERDATA_WARN_BYTES=12000

mode="apply"
if [[ "${1:-}" == "--check" ]]; then
  mode="check"
fi

required=(
  "${COMPOSE_SRC}" "${CADDY_SRC}" "${EDGE_CADDY_SRC}"
  "${QA_CLEANUP_SRC}" "${PRUNE_SRC}" "${BOOTSTRAP_SRC}" "${LAUNCHER_SRC}"
  "${CFN_FILE}" "${EDGE_CFN_FILE}"
)
for f in "${required[@]}"; do
  [[ -f "${f}" ]] || { echo "missing ${f}" >&2; exit 1; }
done

encode_gzb64() {
  gzip -9n -c "$1" | base64 | tr -d '\n'
}

encode_b64() {
  base64 <"$1" | tr -d '\n'
}

split_b64_for_ssm() {
  local b64="$1"
  local max="${SSM_STANDARD_VALUE_LIMIT}"
  local len=${#b64}
  local parts=()
  local i=0
  while (( i < len )); do
    parts+=("${b64:i:max}")
    i=$((i + max))
  done
  if ((${#parts[@]} == 0)); then
    parts+=("")
  fi
  if ((${#parts[@]} > 2)); then
    echo "bootstrap gzip+base64 needs ${#parts[@]} SSM parts (>2); raise part slots in CFN template" >&2
    exit 1
  fi
  while ((${#parts[@]} < 2)); do
    parts+=("")
  done
  printf '%s\n' "${parts[@]}"
}

COMPOSE_GZB64="$(encode_gzb64 "${COMPOSE_SRC}")"
CADDY_GZB64="$(encode_gzb64 "${CADDY_SRC}")"
EDGE_CADDY_GZB64="$(encode_gzb64 "${EDGE_CADDY_SRC}")"
QA_CLEANUP_B64="$(encode_b64 "${QA_CLEANUP_SRC}")"
PRUNE_B64="$(encode_b64 "${PRUNE_SRC}")"
BOOTSTRAP_GZB64="$(encode_gzb64 "${BOOTSTRAP_SRC}")"

BOOTSTRAP_PART1="$(split_b64_for_ssm "${BOOTSTRAP_GZB64}" | sed -n '1p')"
BOOTSTRAP_PART2="$(split_b64_for_ssm "${BOOTSTRAP_GZB64}" | sed -n '2p')"

check_ssm_len() {
  local label="$1"
  local val="$2"
  if [[ "${#val}" -gt "${SSM_STANDARD_VALUE_LIMIT}" ]]; then
    echo "SSM Standard tier limit (${SSM_STANDARD_VALUE_LIMIT}) exceeded for ${label}: ${#val} chars" >&2
    exit 1
  fi
}

check_ssm_len compose "${COMPOSE_GZB64}"
check_ssm_len caddy "${CADDY_GZB64}"
check_ssm_len edge_caddy "${EDGE_CADDY_GZB64}"
check_ssm_len qa "${QA_CLEANUP_B64}"
check_ssm_len prune "${PRUNE_B64}"
check_ssm_len bootstrap_part1 "${BOOTSTRAP_PART1}"
check_ssm_len bootstrap_part2 "${BOOTSTRAP_PART2}"

indent_launcher() {
  local indent='          '
  while IFS= read -r line || [[ -n "${line}" ]]; do
    printf '%s%s\n' "${indent}" "${line}"
  done <"${LAUNCHER_SRC}"
}

USERDATA_BODY="$(indent_launcher)"
USERDATA_BYTES=$(printf '%s' "${USERDATA_BODY}" | wc -c | awk '{print $1}')
if (( USERDATA_BYTES > EC2_USERDATA_LIMIT )); then
  echo "EC2 UserData launcher is ${USERDATA_BYTES} bytes (limit ${EC2_USERDATA_LIMIT})" >&2
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
  local new_prune="${indent}Value: '${PRUNE_B64}'"
  local new_bootstrap1="${indent}Value: '${BOOTSTRAP_PART1}'"
  local new_bootstrap2="${indent}Value: '${BOOTSTRAP_PART2}'"
  local userdata_tmp
  userdata_tmp="$(mktemp)"
  printf '%s\n' "${USERDATA_BODY}" >"${userdata_tmp}"

  awk -v new_compose_ssm="${new_compose}" \
      -v new_caddy_ssm="${new_caddy}" \
      -v new_qa_ssm="${new_qa}" \
      -v new_prune_ssm="${new_prune}" \
      -v new_bootstrap1_ssm="${new_bootstrap1}" \
      -v new_bootstrap2_ssm="${new_bootstrap2}" \
      -v userdata_file="${userdata_tmp}" '
    BEGIN { skip = 0 }
    />>> COMPOSE_GZB64_SSM START/ { print; print new_compose_ssm; skip = 1; next }
    />>> COMPOSE_GZB64_SSM END/ { skip = 0; print; next }
    />>> CADDY_GZB64_SSM START/ { print; print new_caddy_ssm; skip = 1; next }
    />>> CADDY_GZB64_SSM END/ { skip = 0; print; next }
    />>> QA_CLEANUP_B64_PARAM START/ { print; print new_qa_ssm; skip = 1; next }
    />>> QA_CLEANUP_B64_PARAM END/ { skip = 0; print; next }
    />>> GHCR_PRUNE_B64_PARAM START/ { print; print new_prune_ssm; skip = 1; next }
    />>> GHCR_PRUNE_B64_PARAM END/ { skip = 0; print; next }
    />>> BOOTSTRAP_GZB64_SSM_PART1 START/ { print; print new_bootstrap1_ssm; skip = 1; next }
    />>> BOOTSTRAP_GZB64_SSM_PART1 END/ { skip = 0; print; next }
    />>> BOOTSTRAP_GZB64_SSM_PART2 START/ { print; print new_bootstrap2_ssm; skip = 1; next }
    />>> BOOTSTRAP_GZB64_SSM_PART2 END/ { skip = 0; print; next }
    />>> USERDATA_LAUNCHER START/ {
      print
      while ((getline line < userdata_file) > 0) print line
      close(userdata_file)
      skip = 1
      next
    }
    />>> USERDATA_LAUNCHER END/ { skip = 0; print; next }
    { if (!skip) print }
  ' "${src}" > "${dst}"
  rm -f "${userdata_tmp}"
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
echo "  ghcr prune base64 (SSM): ${#PRUNE_B64} chars"
echo "  bootstrap gzip+base64 (SSM total): ${#BOOTSTRAP_GZB64} chars (part1=${#BOOTSTRAP_PART1}, part2=${#BOOTSTRAP_PART2})"
echo "  prod UserData launcher: ${USERDATA_BYTES} bytes (EC2 limit ${EC2_USERDATA_LIMIT})"
echo "  edge UserData body (raw): ${edge_body_bytes} bytes (EC2 limit ${EC2_USERDATA_LIMIT})"
if (( USERDATA_BYTES > USERDATA_WARN_BYTES )) || (( edge_body_bytes > USERDATA_WARN_BYTES )); then
  echo "WARNING: UserData approaching EC2 limit." >&2
fi
