#!/bin/bash
# tokenkey Edge Lightsail bootstrap — generated; do not hand-edit.
set -euo pipefail
exec > >(tee -a /var/log/tokenkey-lightsail-bootstrap.log) 2>&1
echo "LIGHTSAIL_BOOTSTRAP_START $(date -u +%FT%TZ)"

: "${EDGE_ID:?EDGE_ID required}"
: "${INSTANCE_NAME:?INSTANCE_NAME required}"
: "${API_DOMAIN:?API_DOMAIN required}"
: "${ACME_EMAIL:?ACME_EMAIL required}"
: "${MAIN_GATEWAY_ALLOWED_CIDR:?MAIN_GATEWAY_ALLOWED_CIDR required}"
: "${TOKENKEY_IMAGE:?TOKENKEY_IMAGE required}"
: "${LIGHTSAIL_REGION:?LIGHTSAIL_REGION required}"
: "${SSM_ACTIVATION_ID:?SSM_ACTIVATION_ID required}"
: "${SSM_ACTIVATION_CODE:?SSM_ACTIVATION_CODE required}"
# GHCR auth is OPTIONAL: when GHCR_PAT_SSM_NAME is empty the bootstrap relies
# on anonymous pull (works for public ghcr.io/* images, which TokenKey
# currently is). Set GHCR_PAT_SSM_NAME to an SSM SecureString name when the
# image becomes private.
: "${GHCR_PAT_SSM_NAME:=}"
: "${GHCR_PULL_USER:=}"

# Align kernel hostname with Lightsail instance name so SSM ComputerName-based
# discovery matches provision-edge.sh fallbacks (AL2023 default is often a dhcp name).
if command -v hostnamectl >/dev/null 2>&1; then
  hostnamectl set-hostname "${INSTANCE_NAME}" || true
else
  hostname "${INSTANCE_NAME}" 2>/dev/null || true
fi

export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@${API_DOMAIN}}"
export TZ_VALUE="${TZ_VALUE:-UTC}"

yum -y update || dnf -y update || true
(yum -y install docker awscli openssl gzip tar || dnf -y install docker aws-cli openssl gzip tar) || true
systemctl enable --now docker || true
if ! command -v docker >/dev/null; then
  (amazon-linux-extras install docker -y || dnf -y install docker) || true
  systemctl enable --now docker || true
fi
if ! docker compose version >/dev/null 2>&1; then
  mkdir -p /usr/local/lib/docker/cli-plugins
  curl -fsSL "https://github.com/docker/compose/releases/download/v2.29.7/docker-compose-linux-$(uname -m)" \
    -o /usr/local/lib/docker/cli-plugins/docker-compose
  chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
fi

# Match EC2 edge-minimal (stage0-edge-ec2.yaml SwapSizeGiB=2): micro Lightsail
# bundles have no swap by default; without this, memory spikes can hang the VM.
SWAP_SIZE_GIB="${SWAP_SIZE_GIB:-2}"
if [ "${SWAP_SIZE_GIB}" -gt 0 ] && [ ! -f /swapfile ]; then
  fallocate -l "${SWAP_SIZE_GIB}G" /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=$((SWAP_SIZE_GIB * 1024)) status=progress
  chmod 0600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab 2>/dev/null || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

if ! rpm -q amazon-ssm-agent >/dev/null 2>&1; then
  if ! yum -y install amazon-ssm-agent && ! dnf -y install amazon-ssm-agent; then
    echo "BOOTSTRAP_FAIL: cannot install amazon-ssm-agent" >&2
    exit 1
  fi
fi
systemctl enable amazon-ssm-agent
# Register against SSM Hybrid Activation. Fail fast on misconfigured activation —
# silent || true here would mean provision waits 10 minutes before reporting,
# while Lightsail clock + Static IP are already billing.
if ! /usr/bin/amazon-ssm-agent -register -y \
      -id "${SSM_ACTIVATION_ID}" \
      -code "${SSM_ACTIVATION_CODE}" \
      -region "${LIGHTSAIL_REGION}"; then
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent -register failed (activation id/code/region mismatch?)" >&2
  exit 1
fi
systemctl restart amazon-ssm-agent
for i in 1 2 3 4 5 6; do
  if systemctl is-active --quiet amazon-ssm-agent; then break; fi
  echo "amazon-ssm-agent not active yet (try ${i}/6) — sleep 5s"
  sleep 5
  systemctl restart amazon-ssm-agent || true
done
if ! systemctl is-active --quiet amazon-ssm-agent; then
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent failed to stay active after register" >&2
  exit 1
fi

mkdir -p /var/lib/tokenkey/caddy/data /var/lib/tokenkey/caddy/config /var/lib/tokenkey/app
COMPOSE_GZB64='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'
CADDY_GZB64='H4sIAAAAAAACA61VXU8bRxR9tn/FFeQhabG9xiGKIlWNa0hrCQIilWifNmPv2J6yu7OdmXViLEskCgVSEGmVRAqJ1I8UlSYqkCoSBIgi9a/Ua8MTf6F3dsHGqI360AcvO3funnPvmTOXflB8mrrTtAZ/bkN26gaMWGUKNxTBp6FjOWJZtRKzabwfcjb3rUTeZQp8SSVQtyr9glRQ4gLO1bMTeXN4fCybv94Y0Mvc2Ig5gstRXBLXQoBzdb1rfpr9fGQq+6WZHR0dnxoZNnP54clGEiapTWrgEVWRQAQFlyvw/ILNippOIJ0SuM+Zq2QyHq/HY9QhzO6lisf6gRQdahYJVJTy5JVUSq8TEntibjlRNQaTxGNJmypJ3aKoeSrJRTllMUGLiotavBHHUkMdfE8qQYkD0mWeR9XR/mprcbb1bDFYWW7uPQ8Wtjr6XblsXDagvXoPCjYvTkMw96q5+/Lg3ZPmzmxz7+3h3HL77cbR/hJCH955F8wtw80KimJTuEpsm9+ilil0/zch+H4JShgrEIQ5Sao3cOPBvfbe69b9XyLIYHP/YP51a/9u+9e9v2bvInDr4RskPJxfCrZe4Xtz95vmDia/gAjkaP+pJ7jVPVIInv7Y3NkNVl5oRMzr9Bv10Prjp/bD9ZPmNcd5NW2eJF0APAFBq1RIaiLw7doZMXA7VrJ9WTHxxKioEhsSaYz1Q3BnLdjdbj2fbf2w1v79cfvRWvBgCXqLQ62bO3vB/fX2s28hVaHEVpWUzaoUzuunS6W8oPUM8Va+ay8ugCUIc+FwdvXg3Tz6SEqdHbHAh2AXTPSPafmCKMZdONj4Ldj4ubWw3X65eTi/fLD5CM+z9Xgr2HgTPFnX3cZiEa3pC9ZTQnen09mQ7AYVcyj3FWR0rIQW7ZKGaQ65beqwBC2H70af1Uy0qPIlDBmD+Mvg7yJuny07Y8hutEOfPqa3qMADgi8Sk4iZyE9AXVCHK2pWuFSNMznXuLhFhEUt/fYfMycEVxzqslihDv33tM8QBOonUEoQV3pcqPBKhsaIWYzYHaVCWWLTlHokPOL0oNEbMRka2Cxy15VwScsSK3LHE2gCrQkvlTCETA19eXsmkSbDa84tCjNSWVCeYV48HruqtcbBokvREwdSaBecCKkPOgGbl3nSc8udQIlUGVaQxIdmOu4cTqA+zhEUJZHjOKa4DX3R6BoAPO0EjtOPMumhzCXDMAaAOY6vSMGmfViJQhuEVWhhixxXGEknB6M/Gc2E5YazwYxmY7fmavpUvcxBltMNVJlF+dl1mBBC9oyd/wE0duwf5r13zkfs/zj7wiKYExrl1KDp/eKsEugBj7sW9FE9sUXnfwgwiStEYEVFrT64aGR6gN7HJejXPn5qFrgVFaVvrGQzaEzDGPskSkJ/hHvoXw8tjObCN33huXCIgq8kd/VFxQFpQ/76tfHInX8DlFfDSXIHAAA='
QA_B64='H4sIAAAAAAACA61VbXPaRhD+zq94TNwB4hwCT5xOSdWOM1ZdTxJwAI8n4ziaQ7eCG8RJlg4cAvS3dyX8WmjrpP1yI3R3y/O2q2c7zkAbZyCzUekZujQkQ6m0pKCNjZFZOaSGyLQZRiQo2K/P5STCYN6CoiSK5468zpz1KWcw1ZESQWjqN8UsGatjA50hIwtpMYhji5mWcMgGjo3HZMY0d66k4Br8D+ntnTqZGaofZC9/fVfpSM6zWr2UFxM0jZHohEKpo1LX63vt/kmn7R8dfuy55d1Fv/PWa7/1PvofDv1e//Cd5z8+0xLN+sGqXNIhdnBxAb7y+MCqDPcPfK42lhdN8dPlRYOX57Xqp3rxtFf7dReXl69hR2RKQBQPh5RCWNySEnekgoikmSYoazOTkVb4N2zuU+AzdoC+aItmKdQ5D3k9hphBudu4VN54xydtLNZXdqoKrotGDavKt3HIxjqB0pkcRBySbQa5jTtkjRtkO8imKoaKgzH/QZJBiDBOJxyIymJRb8sJZatVBUsMU0ogrr7cI0jizPLb7DtQbtSA4fSlU2M4zn/BaPICEYSCmKDx48EBnJlMnUgP7lMqk4ST6g+ieJD9/baKrrD/i6No5pgpl1wuYdMplUqnx6eHvTyb1UKMgmrl82mn1z/uej0/3zzvdI/cypbaRTssEUw5+MqFCPdFrcjuBcTXPLvr4uzz5XcIZWJswHisT9vr3wG/cZElSyiwW1R+aG4qzZCwO36xO0PLRb1N9jpOxz2ylk3Ibn+z/YvF7jhfySjOArMrPYkAK55SEKcq8xVFnEOfD6Rs820kfZVncrMjNnPJyWDs6YQXs4aVK8vUWVZBWEtcWPRQ8FvareYrIaNEG8In1i7JrjhOo23ynN29zPN2/zwDg/O63U6XW75z6jaLQiJA+ch7x+jxW7fzHveEcf671/UQpJQPbJ8F/xntznm1BoFqZYNxpdUy0wmlOsDzfLpTytMIlSZYoErtdfnbgrOpez6IST2eSvwZOIz00CDkPSSsMLuOa21HCFMZ5P4whtwhVENt8u6zekL5F+N6FPOVYis20ZzH/mn3rO3570/aZ32v6KOnDbwkZbIhyj+o8gso5r7/kpdXDeyhUT9AkbUiBgWA/9L2YsIVKGFyTVZvnhDCtYUT3sBenpqHFPJYrbXbOjD+J0xrQJvQ1BoaTRI7/0ccT+vDmwdfxYbKpT8B+vKaMlcIAAA='
PRUNE_B64='H4sIAAAAAAACA7VV627iRhT+76c48WYDSdfYRKutlgSkKCUpSpasAq3abio02Mcwwh67M2MSmiD1IfqEfZKe8YXAhk2bVisZCc+cy/d95+JXO+6YC3fM1NR6BcNkhuICFzDQbIIe/PXHn5DKTCBEic8iOP/+9BpYmgKP6R7ISAELNUpA5k/BT+I0UQhpFkVQ50LQhfIlT/V+g6IPUM4xgFAmMQwGH6DRaLiTqS+dPEVj/O4t1AkIvnu7f0QJWUDuTIObKenm+XOo2mCc4aLwcvIABMkxYBo5ixC1P0VCJgLAO/QV6ClXEPIIG3CBmJoDhDhRGiT6KDRcdLsfR/2C0HhR0juVyDQB1jzGN5BGmQKco1xQCjKEMJF5nMKYwAguJsAFySA044a8XYG1ib9RIMmkjy0IMI2Shctulatypf+JVV4JiaFE+j/nbEuEccajwPFDQeZG7atU80SwqAXDq4tu/6L788iUb5RTHZ6cDwxUd85IWz5e5XcbKOZQDzBkWaSh6VHlLIUaHMwSSHlKFzyyrEKwtr17/4XoLafpLW2r2/9xdNa77Lbt7alsy+IhfIIdcEKgaJX90oZfj4y8wgJAf5o8arlNohbEXCmj/0aIzt6hcb/jGjwr5BbJoqYYRdQe/gwCrtg4wvbgtOm99wqWzGpswsiPv2ElTOd3WKfc+3By3m05Lwa7GQAwTvViC9rr7ser9pN8r1sHBGsNjjEzEODhgc4eD9pPsb4YqZ9kUQAi0ZAySaMtMU2KCd4MvA39D/1+r38+6n3XtnO8QeLPaCi4UCn6GqrE0HEDnLvCLI3Dzl5zhW89wG79M29ql9r9faNnhm+5rK2i7dt5cpVIGl3jF8806UvHM5r8cONES5ZCTcZl7xU+JBH9z42Xdg26P/WGlnU7peVB5FkAjjSDeERsCGJZAjow+u/t5bPPRYZ059Mqq+5o1A5qxyIR2Kkd7K+s4OgIUDHfWJst82We5S4yTMuQh2uyUeHzYtaa77/1HK9Jz9DzWvnzS42YAnAePKPiM4G1zDCPkEouNNm/Vje6/AnjlCO3K297lzLZVkBU4RiOVwlNndRaczoO7c+Y1rvJf01dpbhO5GK5bNH7kE0MoG1I4IagPMDl6ejk8rJ9CqZo4GiKXK8A3miiDM6s+aYpobNWWMtq5e9lcauy9s4G7SfuVa1HPtEyFTcKblb9nk6L4f+s8sVsipztqocru0+lRMV4bt6vWr8azkLTzjpooA9ZKe8GtRdxGf17MjNaAUK3vRUvtTYfG5BLy936rQ9OlMOrzEzxwAmgBrX9ggEUq6rwMf0wMcNyX3xXKhhjQj4rEu/AhHYPOGd3vz222ir+Wqf8fw2/5pj/RyblFMmYPzOl67S+wuRZVrnc/wZ6n3lINAoAAA=='
printf '%s' "$COMPOSE_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/docker-compose.yml
printf '%s' "$CADDY_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/caddy/Caddyfile.template
envsubst '${API_DOMAIN} ${ACME_EMAIL} ${MAIN_GATEWAY_ALLOWED_CIDR}' \
  < /var/lib/tokenkey/caddy/Caddyfile.template > /var/lib/tokenkey/caddy/Caddyfile

printf '%s' "$QA_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-qa-stale-cleanup.sh
chmod +x /usr/local/bin/tokenkey-qa-stale-cleanup.sh
printf '%s' "$PRUNE_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
chmod +x /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh

SECRET_FILE=/var/lib/tokenkey/.env.secret
if [ ! -f "$SECRET_FILE" ]; then
  umask 077
  gen_secret() { openssl rand -hex 32; }
  gen_pwd() { openssl rand -hex 24; }
  cat > "$SECRET_FILE" <<SECEOF
POSTGRES_PASSWORD=$(gen_pwd)
JWT_SECRET=$(gen_secret)
TOTP_ENCRYPTION_KEY=$(gen_secret)
SECEOF
  chmod 0600 "$SECRET_FILE"
fi
set -a; . "$SECRET_FILE"; set +a

cat > /var/lib/tokenkey/.env <<ENVEOF
API_DOMAIN=${API_DOMAIN}
ACME_EMAIL=${ACME_EMAIL}
TZ=${TZ_VALUE}
SERVER_MODE=release
RUN_MODE=standard
TOKENKEY_IMAGE=${TOKENKEY_IMAGE}
POSTGRES_USER=tokenkey
POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
POSTGRES_DB=tokenkey
DATABASE_MAX_OPEN_CONNS=10
DATABASE_MAX_IDLE_CONNS=2
REDIS_PASSWORD=
REDIS_DB=0
REDIS_POOL_SIZE=64
REDIS_MIN_IDLE_CONNS=2
ADMIN_EMAIL=${ADMIN_EMAIL}
ADMIN_PASSWORD=
JWT_SECRET=${JWT_SECRET}
JWT_EXPIRE_HOUR=1
TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}
ENVEOF
chmod 0600 /var/lib/tokenkey/.env

if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then
  # Private-image path: pull the PAT from SSM SecureString and docker login.
  GHCR_PAT="$(aws --region "${LIGHTSAIL_REGION}" ssm get-parameter \
    --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
    --query Parameter.Value --output text)"
  echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin
  unset GHCR_PAT
else
  # Public-image path (default for TokenKey GHCR): no docker login. Anonymous
  # pull works because ghcr.io/* manifests are accessible via anonymous bearer.
  # If the image ever turns private, set GHCR_PAT_SSM_NAME on the workflow side
  # and the docker login branch above engages with no other changes.
  echo "GHCR_PAT_SSM_NAME unset; relying on anonymous pull for public image ${TOKENKEY_IMAGE}"
fi

cat > /etc/systemd/system/tokenkey.service <<'UNITEOF'
[Unit]
Description=tokenkey edge lightsail stack (docker compose)
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/var/lib/tokenkey
EnvironmentFile=/var/lib/tokenkey/.env
ExecStartPre=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env pull
ExecStart=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env up -d --remove-orphans
ExecStop=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env down
TimeoutStartSec=10min

[Install]
WantedBy=multi-user.target
UNITEOF

systemctl daemon-reload
systemctl enable --now tokenkey.service
sleep 30
docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps || true
echo "LIGHTSAIL_BOOTSTRAP_DONE $(date -u +%FT%TZ)"
