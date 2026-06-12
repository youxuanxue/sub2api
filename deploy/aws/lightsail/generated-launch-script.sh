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

# Swap (2 GiB): micro Lightsail bundles have no swap by default; without this,
# memory spikes can hang the VM.
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
CADDY_GZB64='H4sIAAAAAAACA61VbW8TRxD+bP+KUcIHqLB9jhOEqFBxQwBLAaIkFe2nY323trc531539xyMFSkgEghNRIoCKi8SrXgroPJSUZEmRkj9KdRnO5/yFzp7F9s4pagfKiW2d3b2mZlnZp8dBMVnqDtDq/DnG8iemYIxu0hhShH8NLRtlNh2tcAcGh+EUYf7diLnMgW+pBKoW5F+XioocAF7atmJnHn09Mls7tTcfr0cPTlmjuFyHJfEtRFgT03vmsez02Nnst+Y2fHx02fGjpqjuaOTc0mYpA6pgkdUSQIRFFyuwPPzDrN0OIHhlMB9zlwlkzodKpTUWzA9PpXIjk+cShhp2MtdGB7O7PscTkxPT2gLk2AzSfIOtSFPLaJPqBKFcVYsKUmYg1gFJugscRyYodSTnbAeFwoOGmA5XFI7GY/X4jFaxhP95cVjg0CsMjUtAiWlPHkoldLrhEQemVtMVIyhJPFY0qFKUtcSVU8luSimbIxqKS6q8bk4JhFy73tSCUrKIF3meVRt1283l+abd5eCayuNzfvBlZfdnh06aGByrduXIO9wawaChVeNjWftd7ca6/ONzbdbCyutt8+368sIvXXhXbCwAmdL2AiHwhEslc9S2xSa87MQXF+GAtryBGE6TrU53Fi91Np83bz6IIIMXtTbl1836xdbjzf/mr+IwM21PzDg1uXl4OUr/N3YWGyso/NTiEC263c8we3eGEFw56fG+kZw7alGRL9uvVENzd9+bq390ilex9irZsyO0z7ADghaoUJSE4HPVXeRgduxguPLkolTQkWFOJBIo20QggsPg403zfvzzXsPW7/ebN14GKwuQ39yyDWxFKtQaP/+ElIlShxV2q5fsQVhboLM4lRu15c0oxpxKnf8q6nJNLxfvN7xPTxiZKD9/EkECTtgrWffB1c2m1ev9opt/vDj1q0HyI6NRVMR4kUQVokiDZqDHcyUozF2GhhmEkaMDoKgOGRCaZ6JlNoTqW0/fxAijhhDqRFjGCto3lhoXrrWWF8JVp+hOfN+fg3/WquLrbVX7ReP2o8v/IOKpfajxdadm62791pPNpp37+lexGJRTqYvWCe/nrFL+YjsGRUrU+4ryGhbAe+OafuCKIbXNHQrk3OmNkvQffLd6FjVxLKUL3UJ+D+MW07exPvfO50xZM/aDZ3eCW1TgVMDXycmES+Rm4CaoGWuqFniUs3t8jnGBfbWprb+9R89JwRXHGoS+1Wm/+52AkGg1oFSgrgyVBWtE+G0xmxGnC5LISUxLUIkbHt6yOi3mAxvlWlx15VwQNMSs3jZwymQmhNeKKAJI81pRemTZB0MtYfbFM5LZUPxPPPi8dgRzTNKnU5FSy+kcIxQplKfdQ0OL/Kk5xa7hgKpMMwgiR860k7l0IH6YpQgKYlRjnrNHRiIxHQ/YKcT+K4czqRHMgcMw9gPrFz2lRbmAcxE4QiEWWhiLY4rtKSTQ9FXBneYlD7G0dIaUfdRLdaMhmpvao5Nq4S6Rt0i7RCDNYeqZ0YvTa/wSvqDolkZU/2QhQqzKd+9Dh1CyD5B/R9AYztDyLxPvppR9I+qepgEK4fT9oGE9p/YzQQOksddGwaofotE90XWj6iWGsEsRe0BGDYyfUCfiiXodz4eNfPcjpLSV16y8zjdhnHyy8gJhyzcw0vg4T3ACcVfWjG4KBMF30ru6tuO0u9A7tSx09GI/w1OHHzNwAgAAA=='
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
GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED=true
ENVEOF
chmod 0600 /var/lib/tokenkey/.env

if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then
  # Private-image path: PAT from SSM SecureString.
  GHCR_PAT="$(aws --region "${LIGHTSAIL_REGION}" ssm get-parameter \
    --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
    --query Parameter.Value --output text)"
  echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin
  unset GHCR_PAT
else
  # Public-image path (default): anonymous pull, no docker login.
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
