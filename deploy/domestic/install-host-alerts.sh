#!/usr/bin/env bash
# Install the shared host-pressure checks without restarting application services.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run as root' >&2; exit 1; }
source_script=${1:?Usage: install-host-alerts.sh SHARED_DISK_METRICS_SCRIPT}
[[ -s /opt/tokenkey/host-alerts.env ]] || { echo 'Missing protected notification config' >&2; exit 1; }
[[ $(stat -c %a /opt/tokenkey/host-alerts.env) == 600 ]] || exit 1
mountpoint -q /var/lib/tokenkey
bash "$source_script" --selftest
install -m 0755 "$source_script" /usr/local/bin/tokenkey-disk-metrics.sh
cat > /etc/systemd/system/tokenkey-disk-metrics.service <<'EOF'
[Unit]
Description=TokenKey cn-prod host disk and memory alerts
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=TOKENKEY_HOST_ALERT_ENV_FILE=/opt/tokenkey/host-alerts.env
Environment=TOKENKEY_DATA_VOLUME_PATH=/var/lib/tokenkey
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
TimeoutStartSec=60
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
echo 'cn-prod host pressure alerts installed'
