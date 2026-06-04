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
