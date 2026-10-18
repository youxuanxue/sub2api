#!/usr/bin/env bash
# Write-capable: stop this host from auto-installing packages at 06:00-07:00 UTC.
# Delivered via run-probe.sh. Requires APPLY=yes-disable-host-auto-upgrade.
#
# Brings a LIVE host in line with the bootstrap contract that new hosts already get
# (deploy/hetzner/render-prod-bootstrap.sh, gated by
# scripts/checks/host-auto-upgrade-disabled.py). Rationale, incident evidence and the
# blue/green patching flow: docs/global/claude-hard-rules.md §Host patching.
#
# What it does: mask + stop apt-daily-upgrade.timer, write
# /etc/apt/apt.conf.d/99tokenkey-no-auto-upgrade pinning Periodic=0.
#
# What it deliberately does NOT do — this is the stop-the-bleeding half only:
#   - installs/removes/upgrades nothing
#   - reboots nothing, restarts no service, touches no container
#   - leaves apt-daily.timer enabled (list refresh only; keeps patch debt visible)
# It therefore FREEZES the host in its current patched-on-disk / unpatched-in-memory
# state. Clearing that debt is the blue/green half: full-upgrade + real reboot on the
# standby side, health-check, then cut over.
#
# Idempotent: re-running on an already-hardened host reports no-op.
set -euo pipefail

APPLY="${APPLY:-}"
PIN_FILE=/etc/apt/apt.conf.d/99tokenkey-no-auto-upgrade
PIN_BODY='// Applied by ops/observability/remediate-disable-host-auto-upgrade.sh.
// Patching is a blue/green release — see docs/global/claude-hard-rules.md.
APT::Periodic::Unattended-Upgrade "0";'

# `systemctl is-enabled` on a masked unit prints "masked" AND exits non-zero, so a
# bare `|| echo absent` would append a second line and every later string compare
# against "masked" would fail. Keep the first line only.
unit_state() {
  systemctl "$1" "$2" 2>/dev/null | sed -n '1p' | tr -d '[:space:]'
}

state() {
  printf 'host=%s\n' "$(hostname)"
  printf 'timer_apt_daily_upgrade=%s\n' "$(unit_state is-enabled apt-daily-upgrade.timer)"
  printf 'timer_apt_daily=%s\n' "$(unit_state is-enabled apt-daily.timer)"
  printf 'timer_active=%s\n' "$(unit_state is-active apt-daily-upgrade.timer)"
  printf 'periodic_unattended=%s\n' "$(apt-config dump 2>/dev/null | awk -F'"' '/^APT::Periodic::Unattended-Upgrade /{print $2}' | tail -1)"
  printf 'pin_file_present=%s\n' "$([ -e "$PIN_FILE" ] && echo yes || echo no)"
  # A masked unit makes `list-timers` print "0 timers listed", which a blind
  # `awk NR==2` renders as the literal garbage "- - Sun". Say the state plainly.
  if [ "$(unit_state is-enabled apt-daily-upgrade.timer)" = "masked" ]; then
    printf 'next_fire=%s\n' "none (masked)"
  else
    printf 'next_fire=%s\n' "$(systemctl list-timers apt-daily-upgrade.timer --no-pager 2>/dev/null | awk 'NR==2 && $1 != "0" {print $1" "$2" "$3}')"
  fi
  # Carried, not acted on: proof that masking alone leaves the debt in place.
  printf 'reboot_required=%s\n' "$([ -e /var/run/reboot-required ] && echo yes || echo no)"
  printf 'kernel_running=%s\n' "$(uname -r)"
}

echo "=== before ==="
state

if [ "$APPLY" != "yes-disable-host-auto-upgrade" ]; then
  echo "=== dry-run ==="
  echo '{"apply":false,"wanted":{"apt-daily-upgrade.timer":"masked","APT::Periodic::Unattended-Upgrade":"0"},"installs":0,"reboots":0}'
  exit 0
fi

echo "=== apply: mask + stop apt-daily-upgrade.timer ==="
# mask, not disable: disable is silently undone by package upgrades and systemd presets.
sudo systemctl mask apt-daily-upgrade.timer
sudo systemctl stop apt-daily-upgrade.timer || true
# A unit already running right now would keep installing; stop that instance too.
if systemctl is-active --quiet apt-daily-upgrade.service 2>/dev/null; then
  echo "note: apt-daily-upgrade.service was mid-run; stopping it"
  sudo systemctl stop apt-daily-upgrade.service || true
fi

echo "=== apply: pin Periodic=0 ==="
# A 99- file outranks the installer's 20auto-upgrades (apt reads in lexical order),
# so 20auto-upgrades is left untouched and the change stays reversible by one rm.
printf '%s\n' "$PIN_BODY" | sudo tee "$PIN_FILE" >/dev/null
sudo chmod 0644 "$PIN_FILE"

echo "=== after ==="
state

echo "=== verify ==="
rc=0
[ "$(unit_state is-enabled apt-daily-upgrade.timer)" = "masked" ] || { echo "VERIFY_FAIL: timer not masked"; rc=1; }
[ "$(apt-config dump 2>/dev/null | awk -F'"' '/^APT::Periodic::Unattended-Upgrade /{print $2}' | tail -1)" = "0" ] || { echo "VERIFY_FAIL: Periodic != 0"; rc=1; }
[ "$(unit_state is-enabled apt-daily.timer)" = "enabled" ] || echo "VERIFY_WARN: apt-daily.timer not enabled (patch debt will be invisible)"
if [ "$rc" -eq 0 ]; then
  echo '{"apply":true,"timer":"masked","periodic":"0","installs":0,"reboots":0,"patch_debt":"unchanged — clear it blue/green"}'
fi
exit "$rc"
