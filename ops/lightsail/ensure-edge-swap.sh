#!/usr/bin/env bash
# ensure-edge-swap.sh — Idempotent 2G swap on a Lightsail edge host (parity with EC2 edge-minimal).
# Safe to run on live instances; no-op when /swapfile already active.
set -u

SWAP_SIZE_GIB="${SWAP_SIZE_GIB:-2}"

echo "=== meta ==="
date -u +'%Y-%m-%dT%H:%M:%SZ'
free -h
swapon --show 2>/dev/null || true

if swapon --show 2>/dev/null | grep -q '/swapfile'; then
  # Do NOT exit here: swap being active says nothing about the sysctl tuning
  # below, which the Lightsail bootstrap never wrote. An early return is exactly
  # why every long-lived edge kept kernel-default swappiness (us5, 2026-09-27).
  echo "swap_already_active"
else
  if [ ! -f /swapfile ]; then
    echo "creating_swapfile size_gib=${SWAP_SIZE_GIB}"
    if ! fallocate -l "${SWAP_SIZE_GIB}G" /swapfile 2>/dev/null; then
      dd if=/dev/zero of=/swapfile bs=1M count=$((SWAP_SIZE_GIB * 1024)) status=none
    fi
    chmod 0600 /swapfile
    mkswap /swapfile
  fi

  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab 2>/dev/null || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

# Prod (stage0-ec2-bootstrap.sh §1b) pairs its swapfile with vm.swappiness=10 /
# vm.vfs_cache_pressure=50, but the Lightsail bootstrap only ever created the
# swapfile — measured live on edge us5 (2026-09-27): swappiness=60,
# vfs_cache_pressure=100, no /etc/sysctl.d/90-tokenkey-swap.conf. On a 2GiB edge
# the kernel defaults evict page cache and swap anon pages more eagerly than
# intended. Written here (not in the Lightsail user-data) because the generated
# launch script sits at 14309 of its 14336-byte cap — there is no room left in
# user-data, and this script is the documented live-host swap path.
if [ ! -f /etc/sysctl.d/90-tokenkey-swap.conf ]; then
  cat > /etc/sysctl.d/90-tokenkey-swap.conf <<'SYSCTLEOF'
# Only swap under genuine memory pressure (protect steady-state latency), and
# bias the kernel toward keeping the page cache instead of dropping it.
vm.swappiness=10
vm.vfs_cache_pressure=50
SYSCTLEOF
  sysctl --system >/dev/null 2>&1 || true
  echo "wrote_sysctl_conf"
else
  echo "sysctl_conf_already_present"
fi

echo "=== after ==="
free -h
swapon --show
echo "swappiness=$(cat /proc/sys/vm/swappiness 2>/dev/null) vfs_cache_pressure=$(cat /proc/sys/vm/vfs_cache_pressure 2>/dev/null)"
