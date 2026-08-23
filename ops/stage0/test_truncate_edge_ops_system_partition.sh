#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WRAPPER="${ROOT}/ops/stage0/truncate-edge-ops-system-partition-via-ssm.sh"
REMOTE="${ROOT}/ops/stage0/truncate-edge-ops-system-partition-remote.sh"

fail=0
for script in "${WRAPPER}" "${REMOTE}"; do
  bash -n "${script}" || fail=1
done

if ! grep -q 'TOKENKEY_TRUNCATE_OPS_SYSTEM_PARTITION_CONFIRM' "${WRAPPER}"; then
  echo "FAIL: wrapper must require explicit confirmation token" >&2
  fail=1
fi

if ! grep -q 'TRUNCATE TABLE' "${REMOTE}"; then
  echo "FAIL: remote script must truncate partition" >&2
  fail=1
fi

if TOKENKEY_TRUNCATE_OPS_SYSTEM_PARTITION_CONFIRM=wrong bash "${WRAPPER}" --edge-id us6 >/dev/null 2>&1; then
  echo "FAIL: wrapper must reject missing confirmation" >&2
  fail=1
fi

if [ "${fail}" -ne 0 ]; then
  exit 1
fi

echo "test_truncate_edge_ops_system_partition: ok"
