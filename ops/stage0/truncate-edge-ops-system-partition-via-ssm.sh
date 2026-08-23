#!/usr/bin/env bash
# truncate-edge-ops-system-partition-via-ssm.sh — drop August ops_system_logs rows on an edge.
#
# Explicit operator action: TRUNCATE a named monthly leaf partition and VACUUM it.
# Does not touch ops_error_logs or usage_logs.
#
# Usage:
#   TOKENKEY_TRUNCATE_OPS_SYSTEM_PARTITION_CONFIRM=edge:us6:ops_system_logs_202608 \
#     bash ops/stage0/truncate-edge-ops-system-partition-via-ssm.sh --edge-id us6
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
RUN_PROBE="${REPO_ROOT}/ops/observability/run-probe.sh"
REMOTE_SCRIPT="${HERE}/truncate-edge-ops-system-partition-remote.sh"

EDGE_ID=""
PARTITION="${TOKENKEY_TRUNCATE_OPS_SYSTEM_PARTITION:-ops_system_logs_202608}"
CONFIRM="${TOKENKEY_TRUNCATE_OPS_SYSTEM_PARTITION_CONFIRM:-}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --edge-id) EDGE_ID="${2:-}"; shift 2 ;;
    --partition) PARTITION="${2:-}"; shift 2 ;;
    *) echo "truncate-edge-ops-system-partition: unknown arg '$1'" >&2; exit 1 ;;
  esac
done

if [ -z "${EDGE_ID}" ]; then
  echo "truncate-edge-ops-system-partition: --edge-id is required" >&2
  usage >&2
  exit 1
fi

if [[ ! "${PARTITION}" =~ ^ops_system_logs_20[0-9]{4}$ ]]; then
  echo "truncate-edge-ops-system-partition: partition must match ops_system_logs_YYYYMM" >&2
  exit 1
fi

expected_confirm="edge:${EDGE_ID}:${PARTITION}"
if [ "${CONFIRM}" != "${expected_confirm}" ]; then
  echo "truncate-edge-ops-system-partition: set TOKENKEY_TRUNCATE_OPS_SYSTEM_PARTITION_CONFIRM=${expected_confirm}" >&2
  exit 1
fi

if [ ! -f "${REMOTE_SCRIPT}" ]; then
  echo "truncate-edge-ops-system-partition: missing remote script ${REMOTE_SCRIPT}" >&2
  exit 1
fi

echo "truncate-edge-ops-system-partition: edge=${EDGE_ID} partition=${PARTITION}"

bash "${RUN_PROBE}" \
  --target "edge:${EDGE_ID}" \
  --script "${REMOTE_SCRIPT}" \
  --env "PARTITION_NAME=${PARTITION}" \
  --comment "truncate ${PARTITION} on ${EDGE_ID}" \
  --timeout-seconds "${TIMEOUT_SECONDS}"
