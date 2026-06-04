#!/usr/bin/env bash
# ensure-edge-admin-credentials.sh — After Lightsail/EC2 edge provision, persist
# initial admin credentials to $HOME/Codes/keys/tokenkey-<edge_id>-admin-password.txt.
#
# Tries capture from bootstrap logs first; on exit 3 (logs rotated / missing),
# falls back to reset-edge-admin-password.sh. Neither path prints the password.
#
# Usage:
#   bash ops/stage0/ensure-edge-admin-credentials.sh [--platform auto|ec2|lightsail] <edge_id|prod>
#
# Target is an edge id (e.g. uk1, us6) or the literal "prod" (tokenkey-prod-stage0).
# For prod the bootstrap log is usually rotated, so capture misses and this falls
# straight through to reset (rotate) — saved as tokenkey-prod-admin-password.txt.
#
# Exit codes: same as capture (0 ok) or reset (0 ok); propagates 1/2 transport errors.

set -euo pipefail

_OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CAPTURE="${_OPS_DIR}/capture-edge-admin-credentials.sh"
RESET="${_OPS_DIR}/reset-edge-admin-password.sh"

usage() {
  cat <<'EOF'
Usage:
  bash ops/stage0/ensure-edge-admin-credentials.sh [--platform auto|ec2|lightsail] edge-<id>
  bash ops/stage0/ensure-edge-admin-credentials.sh [--platform auto|ec2|lightsail] <id>
  bash ops/stage0/ensure-edge-admin-credentials.sh prod

Target is an edge id (e.g. uk1, us6) or the literal "prod" (tokenkey-prod-stage0, us-east-1).
Writes: $HOME/Codes/keys/tokenkey-<edge_id>-admin-password.txt (chmod 600; prod -> tokenkey-prod-admin-password.txt)
Never prints the password.
EOF
}

PLATFORM_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --platform=*)
      PLATFORM_ARGS=("$1")
      shift
      ;;
    --platform)
      PLATFORM_ARGS=("$1" "${2:?--platform requires a value}")
      shift 2
      ;;
    -*)
      echo "[ensure-edge-admin-credentials] ERROR: unknown flag: $1" >&2
      usage >&2
      exit 1
      ;;
    *)
      break
      ;;
  esac
done

if [[ $# -ne 1 ]]; then
  usage >&2
  exit 1
fi

EDGE_ID="${1#edge-}"

if bash "${CAPTURE}" "${PLATFORM_ARGS[@]}" "${EDGE_ID}"; then
  exit 0
fi

rc=$?
if [[ "$rc" -eq 3 ]]; then
  echo "[ensure-edge-admin-credentials] capture missed logs; resetting password for ${EDGE_ID}..." >&2
  bash "${RESET}" "${PLATFORM_ARGS[@]}" "${EDGE_ID}"
  exit 0
fi

exit "$rc"
