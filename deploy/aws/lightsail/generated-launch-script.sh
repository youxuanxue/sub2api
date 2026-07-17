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
QA_B64='H4sIAAAAAAACA61V73PaRhD9zl/xTNwB4hwCT5xOSdWOM1ZdTxJwAI8n4ziaQ7eCG8RJlg4cAvRv70r4Z6Gtk/bLzelWt3rv7dvVsx1noI0zkNmo9AxdGpKhVFpS0MbGyKwcUkNk2gwjEhTs1+dyEmEwb0FREsVzR15nzvotZzDVkRJBaOo3ySwZq2MDnSEjC2kxiGOLmZZwyAaOjcdkxjR3rqTgHPyF9PZOncwM1Q+ylx/fZTqS86xWL+XJBE1jJDqhUOqo1PX6Xrt/0mn7R4cfe255d9HvvPXab72P/odDv9c/fOf5j99pieaqXNIhdnBxAb7wOLwqw/0Dn6uN5UVT/HR50eDlea36qV7s9mq/7uLy8jXsiEwJiOLhkFIIi1tK4o5SEJE00wRlbWYy0gr/hsx9CnjGDtAXbdEshTrnIa/HEDModxuXyhvv+KSNxfrKTlXBddGoYVX5Ng7ZWCdQOpODiC2yrTxu4w5Z4wbZDrKpiqHiYMwfSDIIEcbphO1QWSzqbTmhbLWqYIlhSgnE1Zd7BEmcWT7NvgPlRg4Y9l46NYbN/BeMJk8QQSiICRo/HhzAmcnUifTg3qMySdin/iCKB9nfh1V0hf1fHEUzx0w55XIJm06pVDo9Pj3s5c6sFmIUVCufTzu9/nHX6/l58LzTPXIrW3IXzbBEMGXbKxci3Be1wrsXEF9z766Tc50vv0MoE2MDxmN92l7/DvhNFVmyhAK7ReWHxU2lGRJ2xy92Z2i5qLfJXsfpuEfWchGy22cu/2KxO85XMoq9wOxKTyLAiqcUxKnKfEUR+9DnF1Iu860lfZV7crMjNn3JzmDs6YQXs4aVK8vUWVZBWEtclOih4Le0W81XQkaJNoRPrF2SXbGdRtvkObs7zP12v5+BwXndbqfLLd85dZtFIhGgfOS9Y/T4rdt5j3vCOP/d63oIUsrHtc+C/4x257xag0C1ssG40mqZ6YRSHeB5Ptsp5WmEShMsUKX2uvxtxtnUPR/DpB5PJf4JHEZ6aBByDAkrzFXHtbYjhKkM8vowhrxCqIba5N1n9YTy/8X1KOYrRSg20ZyH/mn3rO3570/aZ32v6KOnDbwkZbIhyj+o8gso5r7/kpdXDeyhUT9A4bXCBgWA/9L2YsIZKGFyTVZvnhDCdQknHMBe7pqHFHJbrbXbOjD+J0xrQJvQ1BoaTRI7/0ccT+vDm42vYkPl0p+nBF2dVQgAAA=='
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
COMPOSE_PROFILES=localpg,localredis
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
