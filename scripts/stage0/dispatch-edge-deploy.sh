#!/usr/bin/env bash
# Canonical Edge deploy dispatch — Lightsail + Hetzner paths.
#
# Usage:
#   bash scripts/stage0/dispatch-edge-deploy.sh \
#     --edge-id uk1 --operation upgrade --tag 1.2.3 \
#     [--platform auto|lightsail|hetzner] [--allow-planned] \
#     [--smoke-phase infra|full|edge-native-oauth|main-via-edge] [--ref REF]
#
# Phase-1 Hetzner validate (no paid create, no DNS):
#   bash scripts/stage0/dispatch-edge-deploy.sh \
#     --edge-id uk1 --operation validate --platform hetzner --allow-planned
#
# --tag accepts X.Y.Z or vX.Y.Z; a leading v is stripped before workflow_dispatch.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

EDGE_ID=""
OPERATION=""
TAG=""
SMOKE_PHASE=""
WORKFLOW_REF=""
PLATFORM_PREF="auto"
ALLOW_PLANNED=0
CONFIRM_PAID=0

usage() {
  sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --edge-id) EDGE_ID="${2:-}"; shift 2 ;;
    --operation) OPERATION="${2:-}"; shift 2 ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    --smoke-phase) SMOKE_PHASE="${2:-}"; shift 2 ;;
    --ref) WORKFLOW_REF="${2:?--ref requires a Git ref}"; shift 2 ;;
    --platform) PLATFORM_PREF="${2:-}"; shift 2 ;;
    --allow-planned) ALLOW_PLANNED=1; shift ;;
    --confirm-paid) CONFIRM_PAID=1; shift ;;
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

case "${PLATFORM_PREF}" in
  auto|lightsail|hetzner) ;;
  *)
    echo "dispatch-edge-deploy: invalid --platform=${PLATFORM_PREF}" >&2
    exit 1
    ;;
esac

case "${OPERATION}" in
  validate|provision|upgrade|rollback|smoke|rotate_egress_ip|decommission) ;;
  *)
    echo "dispatch-edge-deploy: unsupported operation=${OPERATION}" >&2
    exit 1
    ;;
esac

if [[ "${OPERATION}" == "upgrade" || "${OPERATION}" == "rollback" ]]; then
  if [[ -z "${TAG}" ]]; then
    echo "dispatch-edge-deploy: --tag is required for operation=${OPERATION}" >&2
    exit 1
  fi
fi

if [[ -n "${TAG}" ]]; then
  if ! TAG="$(bash ops/stage0/normalize-deploy-tag.sh "${TAG}")"; then
    echo "dispatch-edge-deploy: invalid --tag (want X.Y.Z or vX.Y.Z, optionally -rc.N/-beta.N)" >&2
    exit 1
  fi
fi

ROUTE_ARGS=(--edge-id "${EDGE_ID}" --platform "${PLATFORM_PREF}")
if [[ "${ALLOW_PLANNED}" -eq 1 ]]; then
  ROUTE_ARGS+=(--allow-planned)
fi

WORKFLOW=""
CONFIRM_FLAG=""
CONFIRM_VALUE=""
PLATFORM=""
while IFS='=' read -r key value; do
  case "${key}" in
    workflow_file) WORKFLOW="${value}" ;;
    confirm_flag) CONFIRM_FLAG="${value}" ;;
    confirm_value) CONFIRM_VALUE="${value}" ;;
    platform) PLATFORM="${value}" ;;
  esac
done < <(python3 scripts/stage0/resolve-edge-deploy-route.py "${ROUTE_ARGS[@]}")

if [[ -z "${WORKFLOW}" || -z "${CONFIRM_FLAG}" || -z "${CONFIRM_VALUE}" || -z "${PLATFORM}" ]]; then
  echo "dispatch-edge-deploy: incomplete route resolution" >&2
  exit 1
fi

# Hetzner Phase-1 provision does not deploy an app image yet; tag optional until SSM upgrade lands.
# Lightsail/EC2 provision still requires an image tag.
if [[ "${OPERATION}" == "provision" && "${PLATFORM}" != "hetzner" && -z "${TAG}" ]]; then
  echo "dispatch-edge-deploy: --tag is required for operation=provision on platform=${PLATFORM}" >&2
  exit 1
fi

if [[ "${OPERATION}" == "rotate_egress_ip" || "${OPERATION}" == "decommission" ]]; then
  if [[ "${PLATFORM}" != "ec2" ]]; then
    echo "dispatch-edge-deploy: operation=${OPERATION} is EC2-only; edge ${EDGE_ID} is not on EC2/CFN (platform=${PLATFORM})" >&2
    exit 1
  fi
fi

if [[ "${OPERATION}" == "validate" && "${PLATFORM}" != "hetzner" ]]; then
  echo "dispatch-edge-deploy: operation=validate is Hetzner Phase-1 only (got platform=${PLATFORM})" >&2
  exit 1
fi

if [[ "${OPERATION}" == "upgrade" || "${OPERATION}" == "rollback" || "${OPERATION}" == "smoke" ]]; then
  if [[ "${PLATFORM}" == "hetzner" ]]; then
    echo "dispatch-edge-deploy: hetzner ${OPERATION} not wired yet (Phase-1 = validate|provision dry-run)" >&2
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
if [[ -n "${WORKFLOW_REF}" ]]; then
  GH_ARGS+=(--ref "${WORKFLOW_REF}")
fi

if [[ "${PLATFORM}" == "hetzner" ]]; then
  if [[ "${CONFIRM_PAID}" -eq 1 ]]; then
    GH_ARGS+=(-f "confirm_paid=true")
  else
    GH_ARGS+=(-f "confirm_paid=false")
  fi
  if [[ "${ALLOW_PLANNED}" -eq 1 ]]; then
    GH_ARGS+=(-f "allow_planned=true")
  else
    GH_ARGS+=(-f "allow_planned=false")
  fi
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
    infra|edge-native-oauth|main-via-edge|full) ;;
    *)
      echo "dispatch-edge-deploy: invalid --smoke-phase=${PHASE} (want infra|edge-native-oauth|main-via-edge|full)" >&2
      exit 1
      ;;
  esac
  GH_ARGS+=(-f "smoke_phase=${PHASE}")
fi

echo "dispatch-edge-deploy: platform=${PLATFORM} workflow=${WORKFLOW} edge=${EDGE_ID} op=${OPERATION} tag=${TAG:-none} smoke_phase=${PHASE:-auto-skip} ref=${WORKFLOW_REF:-default}"
gh "${GH_ARGS[@]}"
