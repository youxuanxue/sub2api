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
CADDY_GZB64='H4sIAAAAAAACA61V308bRxB+tv+KEeShP7B9hhBFkaKGGpJagoCgFe3TZe1b4y13t9fdPQdjIZEoEKCgpFEaNQGJVgltk6gpqVIlBUeR8qe0Phue+Bc6e+cfGLVRH/pydzs7+30z38zO9YLis9SdpWV4+xKGpqdgxJqhMKUIPg1tyxDLKheYTeO9kLG5byWyLlPgSyqBuiXp56SCAhdwqjI0kTWHx8eGspcX+vQyMzZijuByFJfEtRDgVEXvmpeGPh2ZHvrCHBodHZ8eGTYz2eHJhSRMUpuUwSOqKIEICi5X4Pk5m+U1nUA6JXCfM1fJZDxeiceoQ5jdTRWP9QLJO9TMEygq5clzqZReJyTmxNyZRMnoTxKPJW2qJHXzouypJBczKYsJmldclOMLcQw11MH3pBKUOCBd5nlUHVUf1FcX61urwa2N2v7DYGW3rd+5s8ZZAxoPbkDO5vlZCJae1/aeHry5X3u1WNt/fbi00Xj97Ki6jtCH194ESxtwpYii2BQuENvmV6llCp3/FQjurEMBbTmCMC2nygJu3L7R2H9RX3sUQQa/Vg9uvqhXrzd+2v9r8ToC1+/+gYSHN9eD3ef4Xdtbrr1C5ycQgRxVNz3BrU5JIdj8vvZqL7j1RCOiXzvfKIf6bz807v7cSl5zvKdmzZbT+4AVELREhaQmAs+VT4iB27GC7cuiiRWjokRsSKTR1gvBtZ1g72X94WJ9e6fxy73GtzvB7XXoDg61JnnFShQOft+FVJESWxWPqiuWIMxNkKvYIUfVVa2oRpzKXvpsajINfy7fafmeHzQG4ODZ4wgSmmCNp18HK/v1tbVOsvVvvju8/wjVsTBpKkK8CCJfpCiD1qCJmbI1RrOAYSQhY3QQBMUmE0rrTKTUnoNzcyHch2DnTOxe0/IFUYy7cPDjcmPzHkjFPUQIz0Fja7vxeK++ta2ljsUiStMXrEXfMbYVHZQdo2IO5b6CAW0r4NXo0IVuDpkztVmCLoPvRsfKJrIrX8Kg0Q9as0HjNG6fDDjdb8iOuc2fbvJbVGBnwOeJSQRNZCegIqjDFTWLXKqFEz4XucD6WdTSX//Rc0JwxaEisSYO/Xe3TxAEKi0oJYgrPY7a6lkQdmTMYsRuSxXqEpul1CNhaZtZdiwmw5tj5rnrSjijdYnlueNhpaUWhRcKaEKmBT01ukagJsP5wi0K81JZMDPPvHg8dkGLjRNNh6JHHaSwVXAUpT5oG2w+w5OeO9M2FEiJYQRJfGimZubQgvooQ1CURIbjfOQ29EQzsw+w3Amc4+cH0oMDZwzD6APmOL4iOZv2YCQK+yCMQgub57hCSzrZH70GNBOGGw4lMxrKnZhL6WPxMgdZjidQYhblJ9ehQwjZNe/+B9BYs3+Y984fTMT+j0M3DII5YaMcm3DdJ04qgT3gcdeCHqp/FaL98wImw0kgWF5RqwdOGwNdQO/iEvQrH4+aOW5FQekrK9k8NqZhjH0cOWF/hHvYvx62MDYXfukbz4VDFHwpuasvKk5mG7KXL45H3fk3+iBWJ+sHAAA='
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
