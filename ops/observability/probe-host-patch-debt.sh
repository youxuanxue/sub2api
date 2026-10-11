#!/usr/bin/env bash
# Remote read-only entrypoint: report this host's patch debt and auto-upgrade exposure.
#
# Why this exists: the fleet bootstraps mask apt-daily-upgrade.timer and pin
# APT::Periodic::Unattended-Upgrade "0", because an unattended glibc upgrade runs
# `systemctl daemon-reexec` and severs every in-flight upstream TLS connection
# (2026-10-11 06:46 UTC on prod: ~56 user-visible 502s in nine minutes). Patching
# moved to a blue/green release instead. The risk that replaces it is silence —
# "we turned off auto-upgrades and then never patched again". This probe is the
# counterweight: it reports what is pending so the debt stays visible.
#
# Pure reads: systemctl/apt-config queries, an apt-get -s dry run, journal counts.
# Nothing is installed, enabled, masked, or restarted. Output is field=value lines,
# one host per invocation (run-probe.sh targets one host at a time).
set -uo pipefail

kv() { printf '%s=%s\n' "$1" "$2"; }

kv host "$(hostname)"
kv os "$(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-unknown}")"
kv born "$(stat -c '%y' /etc/machine-id 2>/dev/null | cut -d. -f1)"
kv now_utc "$(date -u +%FT%TZ)"

# --- exposure: is the bootstrap contract still in force on this live host? ----
# Expected on a correctly-provisioned node: masked / 0.
kv timer_apt_daily_upgrade "$(systemctl is-enabled apt-daily-upgrade.timer 2>/dev/null || echo absent)"
kv timer_apt_daily "$(systemctl is-enabled apt-daily.timer 2>/dev/null || echo absent)"
kv periodic_unattended "$(apt-config dump 2>/dev/null | awk -F'"' '/^APT::Periodic::Unattended-Upgrade /{print $2}' | tail -1)"
kv automatic_reboot "$(apt-config dump 2>/dev/null | awk -F'"' '/^Unattended-Upgrade::Automatic-Reboot /{print $2}' | tail -1)"
kv pin_file_present "$([ -e /etc/apt/apt.conf.d/99tokenkey-no-auto-upgrade ] && echo yes || echo no)"
# On a masked unit `list-timers` prints a header plus "0 timers listed", so a blind
# `awk NR==2` yields the literal garbage "- - Sun". Report the state instead: this is
# the field a reader scans to answer "is it really off?".
if [ "$(systemctl is-enabled apt-daily-upgrade.timer 2>/dev/null | sed -n 1p)" = "masked" ]; then
  kv next_unattended_fire "none (masked)"
else
  kv next_unattended_fire "$(systemctl list-timers apt-daily-upgrade.timer --no-pager 2>/dev/null | awk 'NR==2 && $1 != "0" {print $1" "$2" "$3}')"
fi

# --- debt: what is installed-but-not-active, and what is still pending -------
kv reboot_required "$([ -e /var/run/reboot-required ] && echo yes || echo no)"
kv reboot_pkgs "$(tr '\n' ',' < /var/run/reboot-required.pkgs 2>/dev/null)"
kv kernel_running "$(uname -r)"
kv kernel_installed "$(dpkg -l 2>/dev/null | awk '/^ii +linux-image-[0-9]/{printf "%s ", $2}')"
# apt-get -s makes no changes; NoLocking avoids contending with a real apt run.
kv upgradable_count "$(apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep -c '^Inst ')"
# Anchor on the archive name, not a bare "security" substring: package names like
# libapache2-mod-security would otherwise inflate this, and a security count that
# silently overstates is worse than none — it reads as debt that may not exist.
kv upgradable_security_count "$(apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep '^Inst ' | grep -cE '[a-z]+-security[,) ]')"
kv uptime_days "$(awk '{printf "%.1f", $1/86400}' /proc/uptime 2>/dev/null)"

# --- did anything patch-related actually happen recently? --------------------
kv unattended_last_run "$(grep -h 'All upgrades installed' /var/log/unattended-upgrades/unattended-upgrades.log 2>/dev/null | tail -1 | cut -c1-19)"
kv apt_upgrade_runs_7d "$(journalctl --since '7 days ago' --no-pager -u apt-daily-upgrade 2>/dev/null | grep -c 'Finished apt-daily-upgrade')"
# Both are blast-radius signals: a daemon-reexec restarts the network stack.
kv networkd_restarts_7d "$(journalctl --since '7 days ago' --no-pager -u systemd-networkd 2>/dev/null | grep -c 'Started systemd-networkd')"
kv daemon_reexec_7d "$(journalctl --since '7 days ago' --no-pager 2>/dev/null | grep -c 'Reexecuting requested')"

kv containers "$(docker ps --format '{{.Names}}:{{.Status}}' 2>/dev/null | tr '\n' ' ')"
