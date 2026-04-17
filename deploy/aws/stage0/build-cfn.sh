#!/usr/bin/env bash
# =============================================================================
# build-cfn.sh — refresh the embedded compose / Caddyfile blocks in the
# tokenkey Stage 0 CloudFormation template so the stack stays self-contained.
#
# Why:  the CFN UserData embeds docker-compose.yml + Caddyfile as gzip+base64
#       so the source repo can stay private (no raw GitHub URL needed at boot).
#       Whenever you edit those two files, run this script to refresh the
#       markers in deploy/aws/cloudformation/stage0-single-ec2.yaml.
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
CFN_FILE="${REPO_ROOT}/deploy/aws/cloudformation/stage0-single-ec2.yaml"

mode="apply"
if [[ "${1:-}" == "--check" ]]; then
  mode="check"
fi

[[ -f "${COMPOSE_SRC}" ]] || { echo "missing ${COMPOSE_SRC}" >&2; exit 1; }
[[ -f "${CADDY_SRC}"   ]] || { echo "missing ${CADDY_SRC}"   >&2; exit 1; }
[[ -f "${CFN_FILE}"    ]] || { echo "missing ${CFN_FILE}"    >&2; exit 1; }

# gzip is deterministic if we strip the mtime header (-n). Without -n the
# encoded blob churns every run, polluting diffs.
encode_gzb64() {
  gzip -9n -c "$1" | base64 | tr -d '\n'
}

COMPOSE_GZB64="$(encode_gzb64 "${COMPOSE_SRC}")"
CADDY_GZB64="$(encode_gzb64 "${CADDY_SRC}")"

# UserData lives inside `Fn::Base64: !Sub |` and the marker lines are indented
# 10 spaces. Preserve that exact indentation when rewriting.
INDENT='          '

new_compose_line="${INDENT}COMPOSE_GZB64='${COMPOSE_GZB64}'"
new_caddy_line="${INDENT}CADDY_GZB64='${CADDY_GZB64}'"

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

awk -v new_compose="${new_compose_line}" \
    -v new_caddy="${new_caddy_line}" '
  BEGIN { skip = 0; section = "" }
  />>> COMPOSE_GZB64 START/ { print; print new_compose; skip = 1; section = "compose"; next }
  />>> COMPOSE_GZB64 END/   { skip = 0; section = ""; print; next }
  />>> CADDY_GZB64 START/   { print; print new_caddy;   skip = 1; section = "caddy";   next }
  />>> CADDY_GZB64 END/     { skip = 0; section = ""; print; next }
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

# size sanity check — UserData hard limit is 16384 bytes after CFN renders it.
# We can only estimate locally; the actual size depends on substitution. Warn
# above 14000 bytes of raw UserData body to keep headroom.
body_bytes=$(awk '
  /UserData:/        { in_userdata = 1; next }
  /^  [A-Z]/         { if (in_userdata) exit }
  { if (in_userdata) print }
' "${CFN_FILE}" | wc -c | awk '{print $1}')
echo "stage0 CFN refreshed."
echo "  compose gzip+base64: ${#COMPOSE_GZB64} bytes"
echo "  caddy   gzip+base64: ${#CADDY_GZB64} bytes"
echo "  UserData body (raw, pre-substitution): ${body_bytes} bytes  (EC2 limit 16384 after substitution)"
if (( body_bytes > 14000 )); then
  echo "WARNING: UserData body is close to the 16384-byte EC2 limit." >&2
fi
