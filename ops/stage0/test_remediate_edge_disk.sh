#!/usr/bin/env bash
# Static contract checks for ops/stage0/remediate-edge-disk-via-ssm.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/stage0/remediate-edge-disk-via-ssm.sh"
DISK="${ROOT}/deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh"

[ -x "${SCRIPT}" ] || chmod +x "${SCRIPT}"

text="$(cat "${SCRIPT}")"
disk="$(cat "${DISK}")"

for needle in \
  'remediate-edge-disk-via-ssm.sh' \
  'get-instance-access-details' \
  'tokenkey-prune-ghcr-app-tags-core.sh' \
  'tokenkey-qa-stale-cleanup.sh' \
  'edge_ssm_execution.py' \
  'all-deployable'; do
  if ! printf '%s' "${text}" | grep -F -e "${needle}" >/dev/null; then
    echo "FAIL: remediate script missing ${needle}" >&2
    exit 1
  fi
done

for needle in \
  'DISK_ACTIVE_STAMP' \
  'tk_feishu_post_now' \
  '磁盘压力已恢复' \
  'TOKENKEY_DISK_RECOVER_THRESHOLD' \
  'tokenkey-disk-alert.stamp' \
  'legacy_has_cooldown'; do
  if ! printf '%s' "${disk}" | grep -F -e "${needle}" >/dev/null; then
    echo "FAIL: disk-metrics-edge missing recovery anchor ${needle}" >&2
    exit 1
  fi
done

echo "test_remediate_edge_disk: ok"
