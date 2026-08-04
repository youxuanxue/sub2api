#!/bin/bash
# Daily GHCR app-tag prune + dangling Docker image cleanup.
# Invoked by tokenkey-ghcr-prune-daily.timer on prod EC2 and Lightsail edges.
# SSOT: deploy/aws/stage0/tokenkey-ghcr-prune-daily.sh
set -euo pipefail

install_ghcr_prune_daily_units() {
  local systemd_dir="${1:-/etc/systemd/system}"
  install -d -m 0755 "${systemd_dir}"
  cat >"${systemd_dir}/tokenkey-ghcr-prune-daily.service" <<'EOF'
[Unit]
Description=Daily GHCR app-tag prune and dangling Docker image cleanup
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-ghcr-prune-daily.sh
EOF
  cat >"${systemd_dir}/tokenkey-ghcr-prune-daily.timer" <<'EOF'
[Unit]
Description=Daily GHCR prune (low-traffic window, after QA cleanup)

[Timer]
OnCalendar=*-*-* 05:00:00
RandomizedDelaySec=30min
Persistent=true

[Install]
WantedBy=timers.target
EOF
}

validate_keep_tags() {
  local keep="$1"
  if ! [[ "${keep}" =~ ^[1-9][0-9]*$ ]]; then
    logger -t tokenkey-ghcr-prune-daily "invalid TOKENKEY_GHCR_KEEP_TAGS=${keep}"
    return 1
  fi
}

run_ghcr_prune_daily() {
  local keep="${TOKENKEY_GHCR_KEEP_TAGS:-3}"
  local prune="${1:-/usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh}"
  local failures=0
  validate_keep_tags "${keep}" || return 1
  if [ -z "${1:-}" ] && [ ! -x "${prune}" ]; then
    prune=/usr/local/bin/tokenkey-prune-ghcr-app-tags.sh
  fi
  if [ -x "${prune}" ]; then
    logger -t tokenkey-ghcr-prune-daily "start keep_tags=${keep}"
    if ! env TOKENKEY_GHCR_KEEP_TAGS="${keep}" "${prune}"; then
      logger -t tokenkey-ghcr-prune-daily "ghcr tag prune failed"
      failures=$((failures + 1))
    fi
  else
    logger -t tokenkey-ghcr-prune-daily "no prune script installed"
    failures=$((failures + 1))
  fi
  if ! docker image prune -f >/dev/null 2>&1; then
    logger -t tokenkey-ghcr-prune-daily "docker image prune failed"
    failures=$((failures + 1))
  fi
  if [ "${failures}" -gt 0 ]; then
    logger -t tokenkey-ghcr-prune-daily "failed stages=${failures}"
    return 1
  fi
  logger -t tokenkey-ghcr-prune-daily "done"
}

main() {
  case "${1:-}" in
    --selftest) validate_keep_tags "${TOKENKEY_GHCR_KEEP_TAGS:-3}" ;;
    --install-units) install_ghcr_prune_daily_units ;;
    '') run_ghcr_prune_daily ;;
    *) echo "tokenkey-ghcr-prune-daily: unknown argument $1" >&2; return 1 ;;
  esac
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
