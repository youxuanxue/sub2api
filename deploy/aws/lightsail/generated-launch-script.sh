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
# GHCR auth is OPTIONAL: empty GHCR_PAT_SSM_NAME -> anonymous pull (public
# ghcr.io image). Set it to an SSM SecureString name if the image goes private.
: "${GHCR_PAT_SSM_NAME:=}"
: "${GHCR_PULL_USER:=}"

# Align kernel hostname with the Lightsail instance name for SSM discovery
# (matches provision-edge.sh; AL2023 default is often a dhcp name).
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
CADDY_GZB64='H4sIAAAAAAACA61VW28TRxR+tn/FUcJDW8X2OiYIIaGShkAtcVPSKu3TMvaO42l2d7YzswZjRQqIBEITkaKAColEK24FVC4VFWlihNSfQr27zlP+Qs/sxjZOW9SHSr7snDnznXO+c+bbQVB8hroztA5/vIbRqUkYt6YpTCqCv4a2jRHLqleYTdODMGZz38oUXabAl1QCdWvSL0kFFS5gX2P0TNE8evrkaPHU7JBejp0cN8dxeQKXxLUQYF9D75rHR78Ynxr92hw9ceL01PhRc6x4dGI2CxPUJnXwiKpKIIKCyxV4fslmZR1OYDglcJ8zV8lsOt1Ip6hDmN0fKp0aBFJ2qFkmUFXKk4dyOb3OSKyJudOZmjGcJR7L2lRJ6pZF3VNZLqZzFhO0rLiop2fTmGrMg+9JJShxQLrM86jaad4JF+fC9cXg+nJr615w9UWXv0MHjYMGRHcuQ8nm5RkI5l+2Np+2395ubcy1tt5szy9Hb57tNJcQevvi22B+Gc5WkRSbwhFi2/wctUyh6z8LwY0lqKCtRBCm49SYxY2Vy9HWq/Da/QQyeN5sX3kVNi9Fj7b+nLuEwOHq7xhw+8pS8OIlPrc2F1ob6PwEEpCd5ponuNVrKQRrP7Y2NoPrTzQi+nXrTWoIf/0pWv25U7yO8ZGaMTtOHwN2QNAaFZKaCHy+vocM3E5VbF9WTewYFTViQyaPtkEILj4INl+H9+bCuw+iX25FNx8EK0vQnxxyTcqK1Si0f3sBuSoltqruNK9agjA3Q87hhOw0FzWjGnGyePzLyYk8vFu40fE9PGIUoP3scQIJu2DR0++Cq1vhtWu9YsPvf9i+fR/ZsbBoKmK8BKJcpUiD5mAXM2drjN0GxpnEEZODICgOmVCaZyKl9kRq28/ux4gjxnBuxNiPFYQ358PL11sby8HKUzQX3s2t4idaWYhWX7afP2w/uvg3KhbbDxeitVvR+t3o8Wa4flf3IpVKcjJ9wTr59Yxdykdkz6iYQ7mvoKBtFbw7puULohh3EzeHnDe1WYLuk+8mx+omlqV8qUvA737csksm3sXe6YIhe9Zu6PxuaIsKnBr4KjOBeJniGWgI6nBFzSqXanaPzzEusLcWtfTTf/Q8I7ji0JDYL4f+u9vnCAKNDpQSxJUeFyrWiXhaUxYjdpelmJLUDKUeidueHzb6LSbDW2WWuetKOKBpSZW54+EUSM0Jr1TQhJFmtaL0yaMOhtrDLQoXpLJg+gLz0unUEc0zqp1ORcsg5HCMUKZyn3QNNp/mWc+d7hoqpMYwgyz+6Ei7lUMH6tMxgqRkxjhqJ7dhINHTIcBOZ1DjDxfyI4UDhmEMAXMcX5GSTQcwE4UjEGehiS1zXKElnx1O/go6EqYbC5aZCHYv51r+vXyZg1HeL6DGLMr3rmOHGLJPC/8H0NTu/DDvgy+fJPo/CnKcBHPiQXlP/fpP7GUCZ8DjrgUDVL9GRPfFBkzGKiFYWVFrAPYbhT6gD8US9Fsfj5olbiVJ6dsq2QUcTMM4+VnihPMR7+H8ejjCOFz4pC87Fw5R8I3krr6oqNo2FE8dO51M519K4NUhBwgAAA=='
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
SERVER_FRONTEND_URL=https://${API_DOMAIN}
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
