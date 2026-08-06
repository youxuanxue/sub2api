#!/bin/bash
# Hourly QA archive maintenance (Phase 2). Cleanup remains disabled until Phase 4.
# Owner: design-prod-qa-24h-s3-lifecycle.md §7; invoked by tokenkey-qa-maintenance.timer (*:15 UTC).
set -euo pipefail

install_qa_maintenance_units() {
  local systemd_dir="${1:-/etc/systemd/system}"
  install -d -m 0755 "${systemd_dir}"
  cat >"${systemd_dir}/tokenkey-qa-maintenance.service" <<'EOF'
[Unit]
Description=TokenKey hourly QA raw archive maintenance
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-qa-maintenance.sh
EOF
  cat >"${systemd_dir}/tokenkey-qa-maintenance.timer" <<'EOF'
[Unit]
Description=Hourly QA archive maintenance at :15 UTC

[Timer]
OnCalendar=*-*-* *:15:00
Persistent=true

[Install]
WantedBy=timers.target
EOF
}

run_qa_maintenance() {
  cd /var/lib/tokenkey
  app_container=tokenkey
  if [ -r active-color ]; then
    color=$(sed -n '1p' active-color 2>/dev/null | tr -d '[:space:]')
    case "${color}" in
      blue|green) app_container="tokenkey-${color}" ;;
    esac
  fi
  if ! sudo docker ps --format '{{.Names}}' | grep -qx "${app_container}"; then
    logger -t tokenkey-qa-maintenance "skip ${app_container} not running"
    exit 0
  fi
  logger -t tokenkey-qa-maintenance "archive_start container=${app_container}"
  if ! sudo docker exec "${app_container}" /app/sub2api \
    --qa-maintenance-once \
    --confirm=tokenkey-prod-qa-maintenance-v1; then
    logger -t tokenkey-qa-maintenance "archive_failed"
    exit 1
  fi
  logger -t tokenkey-qa-maintenance "archive_done"
}

main() {
  case "${1:-}" in
    --selftest)
      command -v logger >/dev/null
      ;;
    --install-units)
      install_qa_maintenance_units
      ;;
    '')
      run_qa_maintenance
      ;;
    *)
      echo "tokenkey-qa-maintenance: unknown argument $1" >&2
      return 1
      ;;
  esac
}

main "$@"
