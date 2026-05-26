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
  echo "swap_already_active"
  exit 0
fi

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

echo "=== after ==="
free -h
swapon --show
