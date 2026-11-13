#!/usr/bin/env bash
# Install the TokenKey CN SOCKS5 egress (GOST + source allowlist + BBR + host alerts)
# on a fresh Ubuntu 24.04 host. Cloud-agnostic: used for Tencent cn-ip1..4 layout and
# Alibaba SWAS cn-ip5..8 (fleet: deploy/hetzner/README.md).
#
# Run as root from a copy of the repo `deploy/` tree (keeps the relative owners below):
#   TOKENKEY_PROXY_NODE_NAME=cn-ip9 \
#     bash deploy/tencent/install-cn-socks-proxy.sh /secure/gost.json /secure/host-alerts.env
#
# gost.json holds the per-node SOCKS credentials; generate it off-host, never commit it.
# Idempotent: re-running re-applies units/firewall and skips a verified GOST binary.
#
# Cloud firewall is not changed here. A new instance must allow TCP 1080 only from
# ALLOWED_SOURCES below, and must not publish 22 or 8080 to 0.0.0.0/0. The host
# rules still enforce that if the cloud rules are looser.
set -euo pipefail

# Sources allowed to reach :1080. cn-prod dials the proxies' public IPs, so its
# source is the public 154.8.214.179. 106.75.29.139 (retired UCloud cn-prod) is kept
# to match cn-ip1..4 until the fleet-wide allowlist is changed in one place.
ALLOWED_SOURCES=(43.162.83.233 43.162.126.44 154.8.214.179 106.75.29.139)

proxy_fw_rules() {
  local src rules
  rules="/usr/sbin/iptables -N TK_CN_PROXY 2>/dev/null || true; /usr/sbin/iptables -F TK_CN_PROXY;"
  for src in "${ALLOWED_SOURCES[@]}"; do
    rules+=" /usr/sbin/iptables -A TK_CN_PROXY -s ${src}/32 -j ACCEPT;"
  done
  rules+=" /usr/sbin/iptables -A TK_CN_PROXY -j DROP;"
  rules+=" /usr/sbin/iptables -C INPUT -p tcp --dport 1080 -j TK_CN_PROXY 2>/dev/null || /usr/sbin/iptables -I INPUT 1 -p tcp --dport 1080 -j TK_CN_PROXY;"
  rules+=" /usr/sbin/iptables -C INPUT -p tcp --dport 22 -m conntrack --ctstate NEW -j DROP 2>/dev/null || /usr/sbin/iptables -I INPUT 1 -p tcp --dport 22 -m conntrack --ctstate NEW -j DROP;"
  rules+=" /usr/sbin/iptables -C INPUT -p tcp --dport 8080 -m conntrack --ctstate NEW -j DROP 2>/dev/null || /usr/sbin/iptables -I INPUT 1 -p tcp --dport 8080 -m conntrack --ctstate NEW -j DROP"
  printf '%s' "$rules"
}

if [[ "${TOKENKEY_PRINT_FW_RULES:-}" == 1 ]]; then
  proxy_fw_rules
  printf '\n'
  exit 0
fi

[[ $EUID -eq 0 ]] || { echo "need root" >&2; exit 1; }

HERE="$(cd "$(dirname "$0")" && pwd)"
NODE_NAME="${TOKENKEY_PROXY_NODE_NAME:?set TOKENKEY_PROXY_NODE_NAME (e.g. cn-ip9)}"
GOST_JSON="${1:?Usage: TOKENKEY_PROXY_NODE_NAME=NAME $0 /path/gost.json /path/host-alerts.env}"
ALERT_ENV="${2:?Usage: TOKENKEY_PROXY_NODE_NAME=NAME $0 /path/gost.json /path/host-alerts.env}"

GOST_VERSION=3.3.0
GOST_SHA256=1d8f971e9447cf4114fb1376b85c8c14e840db50ae8dbe895398a34e97c13e08
GOST_URL="https://github.com/go-gost/gost/releases/download/v${GOST_VERSION}/gost_${GOST_VERSION}_linux_amd64.tar.gz"
# GitHub downloads from mainland hosts can stall for minutes; allow a long budget.
GOST_DOWNLOAD_MAX_TIME="${GOST_DOWNLOAD_MAX_TIME:-900}"

[[ -s "$GOST_JSON" ]] || { echo "missing gost.json: $GOST_JSON" >&2; exit 1; }
[[ -s "$ALERT_ENV" ]] || { echo "missing host-alerts.env: $ALERT_ENV" >&2; exit 1; }
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$GOST_JSON" \
  || { echo "gost.json is not valid JSON" >&2; exit 1; }

gost_ok() { [[ -x /usr/local/bin/gost ]] && echo "$GOST_SHA256  /usr/local/bin/gost" | sha256sum -c --quiet; }
if ! gost_ok; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL --retry 3 --max-time "$GOST_DOWNLOAD_MAX_TIME" -o "$tmp/gost.tgz" "$GOST_URL"
  tar -xzf "$tmp/gost.tgz" -C "$tmp" gost
  echo "$GOST_SHA256  $tmp/gost" | sha256sum -c --quiet \
    || { echo "gost sha256 mismatch" >&2; exit 1; }
  install -m 0755 "$tmp/gost" /usr/local/bin/gost
fi
/usr/local/bin/gost -V

command -v iptables >/dev/null || {
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq iptables
}
id tokenkey-proxy >/dev/null 2>&1 \
  || useradd --system --no-create-home --shell /usr/sbin/nologin tokenkey-proxy
install -d -m 0750 -o root -g tokenkey-proxy /etc/tokenkey-proxy
install -m 0640 -o root -g tokenkey-proxy "$GOST_JSON" /etc/tokenkey-proxy/gost.json

fw_rules="$(proxy_fw_rules)"
bash "$HERE/lock-down-sshd.sh"

cat > /etc/systemd/system/tokenkey-proxy-firewall.service <<UNIT
[Unit]
Description=CN proxy source restriction
Before=tokenkey-proxy.service
After=network-pre.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c '${fw_rules}'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/tokenkey-proxy.service <<'UNIT'
[Unit]
Description=CN authenticated SOCKS5 proxy
After=network-online.target tokenkey-proxy-firewall.service
Wants=network-online.target
Requires=tokenkey-proxy-firewall.service
[Service]
User=tokenkey-proxy
Group=tokenkey-proxy
ExecStart=/usr/local/bin/gost -C /etc/tokenkey-proxy/gost.json
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
LimitNOFILE=65536
[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now tokenkey-proxy-firewall.service
systemctl restart tokenkey-proxy-firewall.service
systemctl enable tokenkey-proxy.service
systemctl restart tokenkey-proxy.service
systemctl is-active --quiet tokenkey-proxy.service \
  || { echo "tokenkey-proxy failed to start" >&2; exit 1; }

bash "$HERE/../host/ensure-tokenkey-bbr.sh"
TOKENKEY_PROXY_NODE_NAME="$NODE_NAME" bash "$HERE/install-proxy-host-alerts.sh" "$ALERT_ENV"
bash "$HERE/install-proxy-host-alerts.sh" --verify
bash "$HERE/../host/ensure-tokenkey-bbr.sh" --check
ss -ltn | grep -qE ':1080\b' || { echo ":1080 not listening" >&2; exit 1; }
echo "cn-socks-proxy ok node=$NODE_NAME"
