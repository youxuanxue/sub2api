#!/usr/bin/env bash
# Canonical Edge deploy dispatch — one entry for EC2 CFN and Lightsail paths.
#
# Usage:
#   bash scripts/stage0/dispatch-edge-deploy.sh \
#     --edge-id uk1 --operation upgrade --tag 1.2.3 [--smoke-phase infra]
#
# Resolves platform via scripts/stage0/resolve-edge-deploy-route.py and calls
# gh workflow run on deploy-edge-stage0.yml or deploy-edge-lightsail-stage0.yml.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

EDGE_ID=""
OPERATION=""
TAG=""
SMOKE_PHASE=""

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --edge-id) EDGE_ID="${2:-}"; shift 2 ;;
    --operation) OPERATION="${2:-}"; shift 2 ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    --smoke-phase) SMOKE_PHASE="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *)
      echo "dispatch-edge-deploy: unknown argument: $1" >&2
      usage
      ;;
  esac
done

if [[ -z "${EDGE_ID}" || -z "${OPERATION}" ]]; then
  echo "dispatch-edge-deploy: --edge-id and --operation are required" >&2
  usage
fi

case "${OPERATION}" in
  provision|upgrade|rollback|smoke|rotate_egress_ip|decommission) ;;
  *)
    echo "dispatch-edge-deploy: unsupported operation=${OPERATION}" >&2
    exit 1
    ;;
esac

if [[ "${OPERATION}" == "provision" || "${OPERATION}" == "upgrade" || "${OPERATION}" == "rollback" ]]; then
  if [[ -z "${TAG}" ]]; then
    echo "dispatch-edge-deploy: --tag is required for operation=${OPERATION}" >&2
    exit 1
  fi
fi

ROUTE_JSON="$(python3 scripts/stage0/resolve-edge-deploy-route.py --edge-id "${EDGE_ID}" --json)"
mapfile -t _route_fields < <(
  printf '%s' "${ROUTE_JSON}" | python3 -c '
import json
import sys

payload = json.load(sys.stdin)
for key in ("workflow_file", "confirm_flag", "confirm_value", "platform"):
    print(payload[key])
'
)
WORKFLOW="${_route_fields[0]}"
CONFIRM_FLAG="${_route_fields[1]}"
CONFIRM_VALUE="${_route_fields[2]}"
PLATFORM="${_route_fields[3]}"

if [[ "${OPERATION}" == "rotate_egress_ip" || "${OPERATION}" == "decommission" ]]; then
  if [[ "${PLATFORM}" != "ec2" ]]; then
    echo "dispatch-edge-deploy: operation=${OPERATION} is EC2-only; edge ${EDGE_ID} is not on EC2/CFN (platform=${PLATFORM})" >&2
    exit 1
  fi
fi

GH_ARGS=(
  workflow run "${WORKFLOW}"
  -f "edge_id=${EDGE_ID}"
  -f "operation=${OPERATION}"
  -f "${CONFIRM_FLAG}=${CONFIRM_VALUE}"
)

if [[ -n "${TAG}" ]]; then
  GH_ARGS+=(-f "tag=${TAG}")
fi

resolve_smoke_phase() {
  if [[ -n "${SMOKE_PHASE}" ]]; then
    echo "${SMOKE_PHASE}"
    return
  fi
  case "${OPERATION}" in
    smoke) echo "full" ;;
    upgrade|rollback) echo "infra" ;;
    *) echo "" ;;
  esac
}

PHASE="$(resolve_smoke_phase)"
if [[ -n "${PHASE}" ]]; then
  case "${PHASE}" in
    infra|main-via-edge|full) ;;
    *)
      echo "dispatch-edge-deploy: invalid --smoke-phase=${PHASE} (want infra|main-via-edge|full)" >&2
      exit 1
      ;;
  esac
  GH_ARGS+=(-f "smoke_phase=${PHASE}")
fi

echo "dispatch-edge-deploy: platform=${PLATFORM} workflow=${WORKFLOW} edge=${EDGE_ID} op=${OPERATION} tag=${TAG:-none} smoke_phase=${PHASE:-auto-skip}"
gh "${GH_ARGS[@]}"
