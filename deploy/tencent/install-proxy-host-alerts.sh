#!/usr/bin/env bash
# Install disk-metrics + proxy-watch timers on a TokenKey SOCKS egress host.
#
# Required on every new / replaced proxy host (usw-eval* and cn-ip*).
# Owner: deploy/tencent/install-proxy-host-alerts.sh
#
# Usage (on the host as root):
#   TOKENKEY_PROXY_NODE_NAME=cn-ip1 \
#     ./install-proxy-host-alerts.sh /path/to/host-alerts.env \
#     [disk-metrics.sh] [watch.sh]
#
# Verify already-installed timers (no writes; swap/rebuild acceptance):
#   ./install-proxy-host-alerts.sh --verify
#
# host-alerts.env must contain TOKENKEY_FEISHU_WEBHOOK_URL (+ optional SECRET).
# Never commit host-alerts.env. Live proxy bindings remain prod/cn-prod `proxies`.
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo "need root" >&2; exit 1; }

HERE="$(cd "$(dirname "$0")" && pwd)"

verify_installed() {
  local disk_bin=/usr/local/bin/tokenkey-disk-metrics.sh
  local watch_bin=/usr/local/bin/tokenkey-proxy-host-watch.sh
  [[ -x "$disk_bin" ]] || { echo "missing $disk_bin" >&2; exit 1; }
  [[ -x "$watch_bin" ]] || { echo "missing $watch_bin" >&2; exit 1; }
  [[ -s /opt/tokenkey/host-alerts.env ]] || { echo "missing /opt/tokenkey/host-alerts.env" >&2; exit 1; }
  [[ $(stat -c %a /opt/tokenkey/host-alerts.env) == 600 ]] \
    || { echo "host-alerts.env must be mode 600" >&2; exit 1; }
  bash "$disk_bin" --selftest
  bash "$watch_bin" --selftest
  systemctl is-active --quiet tokenkey-disk-metrics.timer \
    || { echo "tokenkey-disk-metrics.timer not active" >&2; exit 1; }
  systemctl is-active --quiet tokenkey-proxy-watch.timer \
    || { echo "tokenkey-proxy-watch.timer not active" >&2; exit 1; }
  echo "verify ok timers=tokenkey-disk-metrics.timer,tokenkey-proxy-watch.timer"
}

if [[ "${1:-}" == "--verify" ]]; then
  verify_installed
  exit 0
fi

NODE_NAME="${TOKENKEY_PROXY_NODE_NAME:?set TOKENKEY_PROXY_NODE_NAME (e.g. cn-ip1 or usw-eval1)}"
ALERT_ENV_SRC="${1:?Usage: TOKENKEY_PROXY_NODE_NAME=NAME $0 /path/to/host-alerts.env [disk.sh] [watch.sh]
   or: $0 --verify}"
DISK_SRC="${2:-$HERE/../hetzner/tokenkey-disk-metrics-prod.sh}"
WATCH_SRC="${3:-$HERE/tokenkey-proxy-host-watch.sh}"

[[ -s "$ALERT_ENV_SRC" ]] || { echo "missing host-alerts.env: $ALERT_ENV_SRC" >&2; exit 1; }
[[ -s "$DISK_SRC" ]] || { echo "missing disk-metrics script: $DISK_SRC" >&2; exit 1; }
[[ -s "$WATCH_SRC" ]] || { echo "missing proxy-watch script: $WATCH_SRC" >&2; exit 1; }
grep -q '^TOKENKEY_FEISHU_WEBHOOK_URL=' "$ALERT_ENV_SRC" \
  || { echo "host-alerts.env missing TOKENKEY_FEISHU_WEBHOOK_URL" >&2; exit 1; }

bash "$DISK_SRC" --selftest
bash "$WATCH_SRC" --selftest

install -d -m 700 /opt/tokenkey
# Scratch dir so disk-metrics can write state without a data volume mount.
install -d -m 755 /var/lib/tokenkey
install -m 600 "$ALERT_ENV_SRC" /opt/tokenkey/host-alerts.env
if ! grep -q '^API_DOMAIN=' /opt/tokenkey/host-alerts.env; then
  printf '\nAPI_DOMAIN=%s\n' "$NODE_NAME" >> /opt/tokenkey/host-alerts.env
fi

install -m 0755 "$DISK_SRC" /usr/local/bin/tokenkey-disk-metrics.sh
install -m 0755 "$WATCH_SRC" /usr/local/bin/tokenkey-proxy-host-watch.sh

cat > /etc/systemd/system/tokenkey-disk-metrics.service <<UNIT
[Unit]
Description=TokenKey proxy-host disk/memory Feishu alerts
After=network-online.target
Wants=network-online.target
[Service]
Type=oneshot
Environment=TOKENKEY_HOST_ALERT_ENV_FILE=/opt/tokenkey/host-alerts.env
Environment=API_DOMAIN=${NODE_NAME}
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
TimeoutStartSec=60
UNIT

cat > /etc/systemd/system/tokenkey-disk-metrics.timer <<'UNIT'
[Unit]
Description=Proxy-host disk/memory check every 5 minutes
[Timer]
OnBootSec=2min
OnUnitActiveSec=5min
RandomizedDelaySec=30
Persistent=true
[Install]
WantedBy=timers.target
UNIT

cat > /etc/systemd/system/tokenkey-proxy-watch.service <<UNIT
[Unit]
Description=TokenKey SOCKS proxy process/port Feishu watch
After=network-online.target tokenkey-proxy.service
Wants=network-online.target
[Service]
Type=oneshot
Environment=TOKENKEY_HOST_ALERT_ENV_FILE=/opt/tokenkey/host-alerts.env
Environment=TOKENKEY_PROXY_NODE_NAME=${NODE_NAME}
Environment=TOKENKEY_PROXY_SERVICE=tokenkey-proxy
Environment=TOKENKEY_PROXY_PORT=1080
ExecStart=/usr/local/bin/tokenkey-proxy-host-watch.sh
TimeoutStartSec=60
SuccessExitStatus=0 1
UNIT

cat > /etc/systemd/system/tokenkey-proxy-watch.timer <<'UNIT'
[Unit]
Description=SOCKS proxy health check every 2 minutes
[Timer]
OnBootSec=1min
OnUnitActiveSec=2min
RandomizedDelaySec=20
Persistent=true
[Install]
WantedBy=timers.target
UNIT

systemctl daemon-reload
systemctl enable --now tokenkey-disk-metrics.timer tokenkey-proxy-watch.timer
# Fail install if oneshot cannot start. proxy-watch unit allows exit 1 (proxy down).
systemctl start tokenkey-disk-metrics.service
systemctl start tokenkey-proxy-watch.service
systemctl is-active --quiet tokenkey-disk-metrics.timer
systemctl is-active --quiet tokenkey-proxy-watch.timer
echo "installed node=$NODE_NAME timers=tokenkey-disk-metrics.timer,tokenkey-proxy-watch.timer"
# Diagnostic only — proxy may not be up yet during install.
systemctl is-active tokenkey-proxy 2>/dev/null || true
ss -lnt 2>/dev/null | grep -E ':1080\b' || true
