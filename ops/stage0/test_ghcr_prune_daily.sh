#!/usr/bin/env bash
# Static contract checks for daily GHCR prune timer rollout.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DAILY="${ROOT}/deploy/aws/stage0/tokenkey-ghcr-prune-daily.sh"
BOOTSTRAP="${ROOT}/deploy/aws/stage0/stage0-ec2-bootstrap.sh"
EDGE_SYNC="${ROOT}/ops/stage0/sync-edge-host-units-via-ssm.sh"
PROD_SYNC="${ROOT}/ops/stage0/sync-ghcr-prune-timer-via-ssm.sh"

[ -x "${DAILY}" ] || chmod +x "${DAILY}"
"${DAILY}" --selftest

for file in "${BOOTSTRAP}" "${PROD_SYNC}"; do
  text="$(cat "${file}")"
  for needle in 'tokenkey-ghcr-prune-daily' '05:00:00'; do
    if ! printf '%s' "${text}" | grep -F -e "${needle}" >/dev/null; then
      echo "FAIL: ${file} missing anchor ${needle}" >&2
      exit 1
    fi
  done
done

text="$(cat "${EDGE_SYNC}")"
for needle in \
  'tokenkey-ghcr-prune-daily' \
  '05:00:00' \
  'GHCR_SRC'; do
  if ! printf '%s' "${text}" | grep -F -e "${needle}" >/dev/null; then
    echo "FAIL: ${EDGE_SYNC} missing anchor ${needle}" >&2
    exit 1
  fi
done

daily_text="$(cat "${DAILY}")"
for needle in 'tokenkey-prune-ghcr-app-tags-core.sh' 'docker image prune'; do
  if ! printf '%s' "${daily_text}" | grep -F -e "${needle}" >/dev/null; then
    echo "FAIL: ${DAILY} missing anchor ${needle}" >&2
    exit 1
  fi
done

if ! grep -F -e 'tokenkey-ghcr-prune-daily.timer' "${BOOTSTRAP}" | grep -q 'enable --now'; then
  echo "FAIL: bootstrap does not enable tokenkey-ghcr-prune-daily.timer" >&2
  exit 1
fi

echo "test_ghcr_prune_daily: ok"
