#!/usr/bin/env bash
# Render Lightsail launch script (user_data) with embedded Stage0 assets.
# Mirrors deploy/aws/stage0/build-cfn.sh but outputs a single bash script for
# aws lightsail create-instances --user-data file://...
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../../.." && pwd)"
STAGE0="${REPO_ROOT}/deploy/aws/stage0"
OUT="${HERE}/generated-launch-script.sh"

mode="apply"
if [[ "${1:-}" == "--check" ]]; then
  mode="check"
fi

for f in \
  "${STAGE0}/docker-compose.yml" \
  "${STAGE0}/Caddyfile.edge" \
  "${STAGE0}/tokenkey-qa-stale-cleanup.sh" \
  "${STAGE0}/tokenkey-prune-ghcr-app-tags.sh"; do
  [[ -f "$f" ]] || { echo "missing $f" >&2; exit 1; }
done

compose_b64="$(gzip -9n -c "${STAGE0}/docker-compose.yml" | base64 | tr -d '\n')"
caddy_b64="$(gzip -9n -c "${STAGE0}/Caddyfile.edge" | base64 | tr -d '\n')"
qa_b64="$(base64 <"${STAGE0}/tokenkey-qa-stale-cleanup.sh" | tr -d '\n')"
prune_b64="$(base64 <"${STAGE0}/tokenkey-prune-ghcr-app-tags.sh" | tr -d '\n')"

cat >"${OUT}.tmp" <<'LAUNCH_HEAD'
#!/bin/bash
# tokenkey Edge Lightsail bootstrap — generated; do not hand-edit.
set -euo pipefail
exec > >(tee -a /var/log/tokenkey-lightsail-bootstrap.log) 2>&1
echo "LIGHTSAIL_BOOTSTRAP_START $(date -u +%FT%TZ)"

: "${EDGE_ID:?EDGE_ID required}"
: "${API_DOMAIN:?API_DOMAIN required}"
: "${ACME_EMAIL:?ACME_EMAIL required}"
: "${MAIN_GATEWAY_ALLOWED_CIDR:?MAIN_GATEWAY_ALLOWED_CIDR required}"
: "${TOKENKEY_IMAGE:?TOKENKEY_IMAGE required}"
: "${GHCR_PULL_USER:?GHCR_PULL_USER required}"
: "${GHCR_PAT_SSM_NAME:?GHCR_PAT_SSM_NAME required}"
: "${LIGHTSAIL_REGION:?LIGHTSAIL_REGION required}"
: "${SSM_ACTIVATION_ID:?SSM_ACTIVATION_ID required}"
: "${SSM_ACTIVATION_CODE:?SSM_ACTIVATION_CODE required}"

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
LAUNCH_HEAD

cat >>"${OUT}.tmp" <<LAUNCH_EMBED
COMPOSE_GZB64='${compose_b64}'
CADDY_GZB64='${caddy_b64}'
QA_B64='${qa_b64}'
PRUNE_B64='${prune_b64}'
LAUNCH_EMBED

cat >>"${OUT}.tmp" <<'LAUNCH_TAIL'
printf '%s' "$COMPOSE_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/docker-compose.yml
printf '%s' "$CADDY_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/caddy/Caddyfile.template
envsubst '${API_DOMAIN} ${ACME_EMAIL} ${MAIN_GATEWAY_ALLOWED_CIDR}' \
  < /var/lib/tokenkey/caddy/Caddyfile.template > /var/lib/tokenkey/caddy/Caddyfile

printf '%s' "$QA_B64" | base64 -d > /usr/local/bin/tokenkey-qa-stale-cleanup.sh
chmod +x /usr/local/bin/tokenkey-qa-stale-cleanup.sh
printf '%s' "$PRUNE_B64" | base64 -d > /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
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

GHCR_PAT="$(aws --region "${LIGHTSAIL_REGION}" ssm get-parameter \
  --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
  --query Parameter.Value --output text)"
echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin
unset GHCR_PAT

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
LAUNCH_TAIL

if [[ "$mode" == "check" ]]; then
  if [[ ! -f "$OUT" ]]; then
    echo "render-bootstrap: FAIL — ${OUT} missing (run 'bash deploy/aws/lightsail/render-bootstrap.sh' to (re)generate, then commit)" >&2
    rm -f "${OUT}.tmp"
    exit 1
  fi
  if cmp -s "$OUT" "${OUT}.tmp"; then
    echo "render-bootstrap: OK (no drift)"
    rm -f "${OUT}.tmp"
    exit 0
  fi
  echo "render-bootstrap: FAIL — ${OUT} is out of sync with current template/sources" >&2
  echo "  Run: bash deploy/aws/lightsail/render-bootstrap.sh && git add ${OUT}" >&2
  if command -v diff >/dev/null 2>&1; then
    echo "  Diff (first 40 lines):" >&2
    diff -u "$OUT" "${OUT}.tmp" | head -40 >&2 || true
  fi
  rm -f "${OUT}.tmp"
  exit 1
fi

mv "${OUT}.tmp" "$OUT"
chmod +x "$OUT"
echo "wrote ${OUT}"
