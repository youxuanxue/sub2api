#!/usr/bin/env bash
# Fresh app-only candidate. Never run against the current serving host.
# Docker and AWS CLI come from the signed Ubuntu archive. The SSM package is
# supplied by the caller with a versioned URL and an independently checked SHA256.
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
apt-get install -y --no-install-recommends ca-certificates curl gnupg openssl gzip jq python3
if ! command -v docker >/dev/null; then
  apt-get install -y --no-install-recommends docker.io docker-compose-v2
fi
systemctl enable --now docker
docker compose version
[[ "$(docker info --format '{{.DockerRootDir}}')" == /var/lib/docker ]]
[[ -z "$(docker ps -aq)" ]] || { echo 'Refusing a host with existing containers' >&2; exit 1; }
if ! command -v aws >/dev/null; then
  apt-get install -y --no-install-recommends awscli
fi
if ! command -v amazon-ssm-agent >/dev/null; then
  : "${SSM_AGENT_URL:?SSM_AGENT_URL must point to a pinned, versioned package}"
  : "${SSM_AGENT_SHA256:?SSM_AGENT_SHA256 must contain the published SHA256}"
  [[ "${SSM_AGENT_URL}" != */latest/* ]] || { echo 'SSM_AGENT_URL must not use a mutable latest path' >&2; exit 1; }
  [[ "${SSM_AGENT_SHA256}" =~ ^[0-9a-fA-F]{64}$ ]] || { echo 'SSM_AGENT_SHA256 must be a 64-character hex digest' >&2; exit 1; }
  tmp="$(mktemp --suffix=.deb)"
  curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$SSM_AGENT_URL" -o "$tmp"
  printf '%s  %s\n' "$SSM_AGENT_SHA256" "$tmp" | sha256sum --check --status || {
    rm -f "$tmp"
    echo 'SSM agent package checksum mismatch' >&2
    exit 1
  }
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
