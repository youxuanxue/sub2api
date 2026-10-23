#!/usr/bin/env bash
# Install cn-prod disk and memory alerts without restarting the gateway, Caddy, or databases.
set -euo pipefail

DATA=/var/lib/tokenkey

render_service() {
  local layout=$1
  local data_env=''
  case "$layout" in
    volume)
      data_env='Environment=TOKENKEY_DATA_VOLUME_PATH=/var/lib/tokenkey'
      ;;
    root)
      # The directory is on the root filesystem. A separate volume check would
      # page "data volume missing" for the layout Beijing actually uses, and
      # df of this path would duplicate the root alert.
      data_env=''
      ;;
    *)
      echo "refusing data layout: $layout" >&2
      return 1
      ;;
  esac
  cat <<EOF
[Unit]
Description=TokenKey cn-prod host disk and memory alerts
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=TOKENKEY_HOST_ALERT_ENV_FILE=/opt/tokenkey/host-alerts.env
${data_env}
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
TimeoutStartSec=60
EOF
}

if [[ "${TOKENKEY_ALERT_INSTALL_PLAN:-}" == 1 ]]; then
  render_service "${1:?layout volume or root}"
  exit 0
fi

[[ $EUID == 0 ]] || { echo 'Run as root' >&2; exit 1; }
source_script=${1:?Usage: install-host-alerts.sh SHARED_DISK_METRICS_SCRIPT [DATA_LAYOUT_PY]}
layout_py=${2:-$(cd "$(dirname "$0")" && pwd)/data_layout.py}
[[ -s /opt/tokenkey/host-alerts.env ]] || { echo 'Missing protected notification config' >&2; exit 1; }
[[ $(stat -c %a /opt/tokenkey/host-alerts.env) == 600 ]] || exit 1
[[ -f "$layout_py" ]] || { echo "missing data layout classifier: $layout_py" >&2; exit 1; }
layout=$(python3 "$layout_py" --require "$DATA")
bash "$source_script" --selftest
install -m 0755 "$source_script" /usr/local/bin/tokenkey-disk-metrics.sh
cat > /etc/systemd/system/tokenkey-disk-metrics.service <<EOF
$(render_service "$layout")
EOF
cat > /etc/systemd/system/tokenkey-disk-metrics.timer <<'EOF'
[Unit]
Description=Check cn-prod host pressure every five minutes

[Timer]
OnBootSec=3min
OnUnitActiveSec=5min
RandomizedDelaySec=30
Persistent=true

[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now tokenkey-disk-metrics.timer
systemctl start tokenkey-disk-metrics.service
systemctl is-active --quiet tokenkey-disk-metrics.timer
echo "cn-prod host pressure alerts installed layout=$layout"
