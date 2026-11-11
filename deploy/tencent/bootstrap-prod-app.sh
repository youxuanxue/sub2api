#!/usr/bin/env bash
# Fresh app-only candidate. Never run against the current serving host.
set -euo pipefail
[[ "${APPLY:-}" == yes-fresh-prod-app ]] || { echo 'APPLY=yes-fresh-prod-app required' >&2; exit 1; }
[[ "$EUID" == 0 ]] || exit 1
[[ ! -e /var/lib/tokenkey/.env ]] || { echo 'Refusing an initialized app host' >&2; exit 1; }
[[ ! -e /srv/tokenkey-rehearsal ]] || { echo 'Refusing the legacy production layout' >&2; exit 1; }
[[ "$(uname -m)" == x86_64 ]] || exit 1
umask 077
export DEBIAN_FRONTEND=noninteractive
systemctl mask --now apt-daily-upgrade.timer
if systemctl is-active --quiet apt-daily-upgrade.service; then
  systemctl stop apt-daily-upgrade.service
fi
printf 'APT::Periodic::Unattended-Upgrade "0";\n' > /etc/apt/apt.conf.d/99tokenkey-no-auto-upgrade
printf 'DPkg::Lock::Timeout "120";\n' > /etc/apt/apt.conf.d/99tokenkey-lock-timeout
apt-get update -y
apt-get install -y --no-install-recommends ca-certificates curl gnupg openssl gzip unzip jq python3
if ! command -v docker >/dev/null; then
  tmp="$(mktemp)"
  curl -fsSL https://get.docker.com -o "$tmp"
  sh "$tmp"
  rm "$tmp"
fi
systemctl enable --now docker
docker compose version
[[ "$(docker info --format '{{.DockerRootDir}}')" == /var/lib/docker ]]
[[ -z "$(docker ps -aq)" ]] || { echo 'Refusing a host with existing containers' >&2; exit 1; }
if ! command -v aws >/dev/null; then
  tmp="$(mktemp -d)"
  curl -fsSL https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip -o "$tmp/aws.zip"
  unzip -q "$tmp/aws.zip" -d "$tmp"
  "$tmp/aws/install"
  rm -r "$tmp"
fi
if ! command -v amazon-ssm-agent >/dev/null; then
  tmp="$(mktemp --suffix=.deb)"
  curl -fsSL https://s3.eu-west-2.amazonaws.com/amazon-ssm-eu-west-2/latest/debian_amd64/amazon-ssm-agent.deb -o "$tmp"
  dpkg -i "$tmp"
  rm "$tmp"
fi
install -d -m 0755 /var/lib/tokenkey /var/lib/tokenkey/app /var/lib/tokenkey/caddy
chown 1000:1000 /var/lib/tokenkey/app
install -d -m 0700 /root/tokenkey-parallel-transfer
if [[ ! -e /root/tokenkey-parallel-transfer/recipient.key ]]; then
  openssl req -x509 -newkey rsa:3072 -nodes -days 3 -subj /CN=tokenkey-candidate-transfer \
    -keyout /root/tokenkey-parallel-transfer/recipient.key \
    -out /root/tokenkey-parallel-transfer/recipient.crt 2>/dev/null
fi
[[ "$(systemctl is-enabled apt-daily-upgrade.timer)" == masked ]]
echo 'APP_ONLY_BOOTSTRAP_READY'
findmnt --json --target /var/lib/tokenkey --output SOURCE,TARGET,FSTYPE,UUID,FSROOT
