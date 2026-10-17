#!/usr/bin/env bash
# ensure-tokenkey-bbr.sh — idempotent TokenKey host BBR enablement.
#
# SSOT for prod host TCP congestion control. Consumed by:
#   - deploy/aws/stage0/stage0-ec2-bootstrap.sh (new EC2 prod hosts)
#   - deploy/hetzner/render-prod-bootstrap.sh (new Hetzner prod hosts)
#   - ops/stage0/sync-host-bbr-via-ssm.sh (live EC2 prod heal)
#
# Proxy/egress hosts are out of scope here (owned by network ops).
#
# Usage:
#   ensure-tokenkey-bbr.sh           # apply + verify
#   ensure-tokenkey-bbr.sh --check   # verify only; exit 0 iff live state matches
set -euo pipefail

MODE=apply
case "${1:-}" in
  ""|--apply) MODE=apply ;;
  --check) MODE=check ;;
  -h|--help)
    sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "usage: $0 [--apply|--check]" >&2
    exit 2
    ;;
esac

MODULES_LOAD_FILE=/etc/modules-load.d/99-tokenkey-bbr.conf
SYSCTL_FILE=/etc/sysctl.d/91-tokenkey-bbr.conf
WANTED_CC=bbr
WANTED_QDISC=fq

wanted_modules_load() {
  cat <<'EOF'
# Owned by deploy/host/ensure-tokenkey-bbr.sh — load BBR at boot.
tcp_bbr
EOF
}

wanted_sysctl() {
  cat <<'EOF'
# Owned by deploy/host/ensure-tokenkey-bbr.sh — BBR + fq pairing.
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
EOF
}

live_cc() {
  sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || true
}

live_qdisc() {
  sysctl -n net.core.default_qdisc 2>/dev/null || true
}

available_cc() {
  sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null || true
}

verify_live() {
  local cc qdisc avail errors=0
  cc="$(live_cc)"
  qdisc="$(live_qdisc)"
  avail="$(available_cc)"
  if [ ! -f "${MODULES_LOAD_FILE}" ]; then
    echo "bbr_check: missing ${MODULES_LOAD_FILE}" >&2
    errors=$((errors + 1))
  elif ! grep -qx 'tcp_bbr' "${MODULES_LOAD_FILE}"; then
    echo "bbr_check: ${MODULES_LOAD_FILE} missing tcp_bbr line" >&2
    errors=$((errors + 1))
  fi
  if [ ! -f "${SYSCTL_FILE}" ]; then
    echo "bbr_check: missing ${SYSCTL_FILE}" >&2
    errors=$((errors + 1))
  else
    if ! grep -qx 'net.core.default_qdisc=fq' "${SYSCTL_FILE}"; then
      echo "bbr_check: ${SYSCTL_FILE} missing net.core.default_qdisc=fq" >&2
      errors=$((errors + 1))
    fi
    if ! grep -qx 'net.ipv4.tcp_congestion_control=bbr' "${SYSCTL_FILE}"; then
      echo "bbr_check: ${SYSCTL_FILE} missing net.ipv4.tcp_congestion_control=bbr" >&2
      errors=$((errors + 1))
    fi
  fi
  case " ${avail} " in
    *" ${WANTED_CC} "*) ;;
    *)
      echo "bbr_check: ${WANTED_CC} not in tcp_available_congestion_control (${avail})" >&2
      errors=$((errors + 1))
      ;;
  esac
  if [ "${cc}" != "${WANTED_CC}" ]; then
    echo "bbr_check: tcp_congestion_control=${cc:-unset} want=${WANTED_CC}" >&2
    errors=$((errors + 1))
  fi
  if [ "${qdisc}" != "${WANTED_QDISC}" ]; then
    echo "bbr_check: default_qdisc=${qdisc:-unset} want=${WANTED_QDISC}" >&2
    errors=$((errors + 1))
  fi
  if [ "${errors}" -ne 0 ]; then
    return 1
  fi
  echo "bbr_ok: cc=${cc} qdisc=${qdisc} available=${avail}"
  return 0
}

if [ "${MODE}" = check ]; then
  verify_live
  exit $?
fi

wanted_modules_load | tee "${MODULES_LOAD_FILE}" >/dev/null
chmod 0644 "${MODULES_LOAD_FILE}"
wanted_sysctl | tee "${SYSCTL_FILE}" >/dev/null
chmod 0644 "${SYSCTL_FILE}"

if ! modprobe tcp_bbr 2>/dev/null; then
  echo "bbr_apply: modprobe tcp_bbr failed" >&2
  verify_live || true
  exit 1
fi

# Prefer targeted sysctl apply so unrelated broken confs cannot mask BBR.
sysctl -p "${SYSCTL_FILE}" >/dev/null

verify_live
