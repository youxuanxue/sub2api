#!/usr/bin/env bash
# Render Hetzner prod cloud-init user-data (Ubuntu arm64).
# Mandatory Volume at /var/lib/tokenkey; prod Caddyfile; SSM Hybrid register.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
STAGE0="${REPO_ROOT}/deploy/aws/stage0"
LIGHTSAIL="${REPO_ROOT}/deploy/aws/lightsail"
OUT="${OUT:-${HERE}/generated-prod-user-data.sh}"

mode="apply"
if [[ "${1:-}" == "--check" ]]; then
  mode="check"
fi

# Staging knife uses Caddyfile.edge (single API host). Formal dual-domain /
# apex SITE_DOMAIN cutover later via sync_caddyfile prod path — api-hz would
# incorrectly derive SITE_DOMAIN=hz.tokenkey.dev under render-prod-caddyfile.
for f in \
  "${STAGE0}/docker-compose.yml" \
  "${STAGE0}/Caddyfile.edge" \
  "${STAGE0}/tokenkey-prune-ghcr-app-tags.sh" \
  "${STAGE0}/tokenkey-ghcr-prune-daily.sh" \
  "${STAGE0}/tokenkey-pgdump.sh" \
  "${LIGHTSAIL}/restore-edge-env-secrets.sh" \
  "${LIGHTSAIL}/tokenkey-disk-metrics-edge.sh"; do
  [[ -f "$f" ]] || { echo "missing $f" >&2; exit 1; }
done

# Deterministic gzip+base64 for CI --check: hand-built header (mtime=0, OS=255)
# so macOS gzip.compress / system gzip cannot diverge from Linux runners.
b64_gzip() {
  python3 - "$1" <<'PY'
import base64, pathlib, struct, sys, zlib

data = pathlib.Path(sys.argv[1]).read_bytes()
comp = zlib.compressobj(9, zlib.DEFLATED, -zlib.MAX_WBITS)
body = comp.compress(data) + comp.flush()
header = b"\x1f\x8b\x08\x00" + struct.pack("<I", 0) + b"\x02\xff"
trailer = struct.pack("<II", zlib.crc32(data) & 0xFFFFFFFF, len(data) & 0xFFFFFFFF)
sys.stdout.write(base64.b64encode(header + body + trailer).decode("ascii"))
PY
}

compose_b64="$(b64_gzip "${STAGE0}/docker-compose.yml")"
caddy_b64="$(b64_gzip "${STAGE0}/Caddyfile.edge")"
prune_b64="$(b64_gzip "${STAGE0}/tokenkey-prune-ghcr-app-tags.sh")"
ghcr_daily_b64="$(b64_gzip "${STAGE0}/tokenkey-ghcr-prune-daily.sh")"
pgdump_b64="$(b64_gzip "${STAGE0}/tokenkey-pgdump.sh")"
disk_metrics_b64="$(b64_gzip "${LIGHTSAIL}/tokenkey-disk-metrics-edge.sh")"
restore_secrets_b64="$(b64_gzip "${LIGHTSAIL}/restore-edge-env-secrets.sh")"
cat >"${OUT}.tmp" <<'LAUNCH_HEAD'
#!/bin/bash
# tokenkey Prod Hetzner bootstrap — generated; do not hand-edit.
set -euo pipefail
exec > >(tee -a /var/log/tokenkey-hetzner-bootstrap.log) 2>&1
echo "HETZNER_PROD_BOOTSTRAP_START $(date -u +%FT%TZ)"

: "${INSTANCE_NAME:?INSTANCE_NAME required}"
: "${API_DOMAIN:?API_DOMAIN required}"
: "${ACME_EMAIL:?ACME_EMAIL required}"
: "${MAIN_GATEWAY_ALLOWED_CIDR:?MAIN_GATEWAY_ALLOWED_CIDR required}"
: "${TOKENKEY_IMAGE:?TOKENKEY_IMAGE required}"
: "${SSM_REGION:?SSM_REGION required}"
: "${SSM_ACTIVATION_ID:?SSM_ACTIVATION_ID required}"
: "${SSM_ACTIVATION_CODE:?SSM_ACTIVATION_CODE required}"
: "${VOLUME_ID:?VOLUME_ID required}"
: "${VOLUME_MOUNT:=/var/lib/tokenkey}"
: "${GHCR_PAT_SSM_NAME:=}"
: "${GHCR_PULL_USER:=}"
: "${ALLOW_SECRET_GENERATE:=false}"
: "${AWS_ACCOUNT_ID:=}"
: "${TOKENKEY_PGDUMP_S3_URI:=}"

case "${ALLOW_SECRET_GENERATE}" in
  true|false) ;;
  *) echo "BOOTSTRAP_FAIL: ALLOW_SECRET_GENERATE must be true or false" >&2; exit 1 ;;
esac

hostnamectl set-hostname "${INSTANCE_NAME}" || hostname "${INSTANCE_NAME}" || true
export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@${API_DOMAIN}}"
export TZ_VALUE="${TZ_VALUE:-UTC}"

export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y --no-install-recommends \
  ca-certificates curl gnupg openssl gzip gettext-base unzip e2fsprogs

# Ubuntu 24.04 has no awscli apt package; install AWS CLI v2 for the host arch.
if ! command -v aws >/dev/null 2>&1; then
  arch="$(uname -m)"
  case "${arch}" in
    aarch64|arm64) aws_cli_arch=aarch64 ;;
    x86_64|amd64) aws_cli_arch=x86_64 ;;
    *) echo "BOOTSTRAP_FAIL: unsupported arch for awscliv2 ${arch}" >&2; exit 1 ;;
  esac
  tmp_aws="$(mktemp -d)"
  curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${aws_cli_arch}.zip" -o "${tmp_aws}/awscliv2.zip"
  unzip -q "${tmp_aws}/awscliv2.zip" -d "${tmp_aws}"
  "${tmp_aws}/aws/install" --update
  rm -rf "${tmp_aws}"
fi
command -v aws >/dev/null 2>&1 || {
  echo "BOOTSTRAP_FAIL: aws CLI missing after install" >&2
  exit 1
}

if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker
if ! docker compose version >/dev/null 2>&1; then
  mkdir -p /usr/local/lib/docker/cli-plugins
  curl -fsSL "https://github.com/docker/compose/releases/download/v2.29.7/docker-compose-linux-$(uname -m)" \
    -o /usr/local/lib/docker/cli-plugins/docker-compose
  chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
fi

SWAP_SIZE_GIB="${SWAP_SIZE_GIB:-2}"
if [ "${SWAP_SIZE_GIB}" -gt 0 ] && [ ! -f /swapfile ]; then
  fallocate -l "${SWAP_SIZE_GIB}G" /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=$((SWAP_SIZE_GIB * 1024)) status=progress
  chmod 0600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab 2>/dev/null || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

# Mandatory Hetzner Volume → /var/lib/tokenkey (attach before first boot).
vol_dev="/dev/disk/by-id/scsi-0HC_Volume_${VOLUME_ID}"
echo "waiting for volume device ${vol_dev}"
for _ in $(seq 1 60); do
  if [ -e "${vol_dev}" ]; then break; fi
  sleep 5
done
if [ ! -e "${vol_dev}" ]; then
  echo "BOOTSTRAP_FAIL: volume device ${vol_dev} not present" >&2
  exit 1
fi
if ! blkid "${vol_dev}" >/dev/null 2>&1; then
  echo "formatting new volume ${vol_dev}"
  mkfs.ext4 -F -L tokenkey-data "${vol_dev}"
fi
mkdir -p "${VOLUME_MOUNT}"
vol_uuid="$(blkid -s UUID -o value "${vol_dev}")"
if ! grep -q "UUID=${vol_uuid}" /etc/fstab 2>/dev/null; then
  echo "UUID=${vol_uuid} ${VOLUME_MOUNT} ext4 defaults,nofail 0 2" >> /etc/fstab
fi
mount "${VOLUME_MOUNT}" || mount "${vol_dev}" "${VOLUME_MOUNT}"
findmnt -n -o SOURCE,TARGET "${VOLUME_MOUNT}" || {
  echo "BOOTSTRAP_FAIL: ${VOLUME_MOUNT} not mounted" >&2
  exit 1
}

# amazon-ssm-agent (arm64/amd64 deb from AWS)
arch="$(dpkg --print-architecture)"
case "${arch}" in
  arm64) ssm_deb_arch=arm64 ;;
  amd64) ssm_deb_arch=amd64 ;;
  *) echo "BOOTSTRAP_FAIL: unsupported arch ${arch}" >&2; exit 1 ;;
esac
if ! systemctl is-active --quiet amazon-ssm-agent 2>/dev/null; then
  tmp="$(mktemp -d)"
  curl -fsSL "https://s3.amazonaws.com/ec2-downloads-windows/SSMAgent/latest/debian_${ssm_deb_arch}/amazon-ssm-agent.deb" \
    -o "${tmp}/amazon-ssm-agent.deb"
  dpkg -i "${tmp}/amazon-ssm-agent.deb" || apt-get install -fy
  rm -rf "${tmp}"
fi
systemctl enable amazon-ssm-agent
if ! /usr/bin/amazon-ssm-agent -register -y \
      -id "${SSM_ACTIVATION_ID}" \
      -code "${SSM_ACTIVATION_CODE}" \
      -region "${SSM_REGION}"; then
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent -register failed" >&2
  exit 1
fi
systemctl restart amazon-ssm-agent
for i in 1 2 3 4 5 6; do
  systemctl is-active --quiet amazon-ssm-agent && break
  sleep 5
  systemctl restart amazon-ssm-agent || true
done
systemctl is-active --quiet amazon-ssm-agent || {
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent not active" >&2
  exit 1
}

mkdir -p "${VOLUME_MOUNT}/caddy/data" "${VOLUME_MOUNT}/caddy/config"
install -d -m 0755 -o 1000 -g 1000 "${VOLUME_MOUNT}/app"
LAUNCH_HEAD

cat >>"${OUT}.tmp" <<LAUNCH_EMBED
COMPOSE_GZB64='${compose_b64}'
CADDY_GZB64='${caddy_b64}'
PRUNE_B64='${prune_b64}'
GHCR_DAILY_B64='${ghcr_daily_b64}'
PGDUMP_B64='${pgdump_b64}'
DISK_METRICS_B64='${disk_metrics_b64}'
RESTORE_SECRETS_B64='${restore_secrets_b64}'
LAUNCH_EMBED

cat >>"${OUT}.tmp" <<'LAUNCH_TAIL'
printf '%s' "$COMPOSE_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/docker-compose.yml
printf '%s' "$CADDY_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/caddy/Caddyfile.template
envsubst '${API_DOMAIN} ${ACME_EMAIL} ${MAIN_GATEWAY_ALLOWED_CIDR}' \
  < /var/lib/tokenkey/caddy/Caddyfile.template > /var/lib/tokenkey/caddy/Caddyfile

printf '%s' "$PRUNE_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
chmod +x /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh

printf '%s' "$GHCR_DAILY_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-ghcr-prune-daily.sh
chmod +x /usr/local/bin/tokenkey-ghcr-prune-daily.sh
/usr/local/bin/tokenkey-ghcr-prune-daily.sh --selftest
/usr/local/bin/tokenkey-ghcr-prune-daily.sh --install-units

printf '%s' "$PGDUMP_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-pgdump.sh
chmod +x /usr/local/bin/tokenkey-pgdump.sh
mkdir -p /var/lib/tokenkey/pgdump

printf '%s' "$DISK_METRICS_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-disk-metrics.sh
chmod +x /usr/local/bin/tokenkey-disk-metrics.sh
/usr/local/bin/tokenkey-disk-metrics.sh --selftest

cat > /etc/systemd/system/tokenkey-disk-metrics.service <<'DMSEOF'
[Unit]
Description=tokenkey PROD on-box disk-full and memory-pressure Feishu alerts
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
DMSEOF

cat > /etc/systemd/system/tokenkey-disk-metrics.timer <<'DMTEOF'
[Unit]
Description=Fire tokenkey PROD disk/memory pressure alerts every 5 minutes

[Timer]
OnBootSec=3min
OnUnitActiveSec=5min
RandomizedDelaySec=30
Persistent=true

[Install]
WantedBy=timers.target
DMTEOF

cat > /etc/systemd/system/tokenkey-pgdump.service <<'PSEOF'
[Unit]
Description=tokenkey pg_dump (every 2 hours)
After=tokenkey.service
Requires=tokenkey.service

[Service]
Type=oneshot
Nice=19
CPUSchedulingPolicy=other
CPUQuota=40%
IOSchedulingClass=best-effort
IOSchedulingPriority=7
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-pgdump.sh
PSEOF

cat > /etc/systemd/system/tokenkey-pgdump.timer <<'PTEOF'
[Unit]
Description=Run tokenkey-pgdump every 2 hours

[Timer]
OnCalendar=*-*-* 00/2:00:00
Persistent=true
RandomizedDelaySec=2min

[Install]
WantedBy=timers.target
PTEOF

printf '%s' "$RESTORE_SECRETS_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-restore-edge-env-secrets.sh
chmod 0755 /usr/local/bin/tokenkey-restore-edge-env-secrets.sh

SECRET_FILE=/var/lib/tokenkey/.env.secret
restore_secret_args=(
  --parameter "/tokenkey/hetzner/prod/stage0/env-secrets-backup" \
  --output "$SECRET_FILE"
)
if [ "${ALLOW_SECRET_GENERATE}" = true ]; then
  restore_secret_args+=(--allow-generate)
fi
AWS_REGION="${SSM_REGION}" /usr/local/bin/tokenkey-restore-edge-env-secrets.sh \
  "${restore_secret_args[@]}"
set -a; . "$SECRET_FILE"; set +a

# Resolve pgdump S3 URI (Hybrid IAM allows prod/pgdump/* on this account bucket).
if [ -z "${TOKENKEY_PGDUMP_S3_URI:-}" ]; then
  if [ -z "${AWS_ACCOUNT_ID:-}" ]; then
    AWS_ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text 2>/dev/null || true)"
  fi
  if [ -n "${AWS_ACCOUNT_ID:-}" ] && [ "${AWS_ACCOUNT_ID}" != "None" ]; then
    TOKENKEY_PGDUMP_S3_URI="s3://tokenkey-prod-pgdump-${AWS_ACCOUNT_ID}/prod/pgdump"
  fi
fi

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
QA_CAPTURE_ENABLED=false
TOKENKEY_PGDUMP_S3_URI=${TOKENKEY_PGDUMP_S3_URI}
ENVEOF
chmod 0600 /var/lib/tokenkey/.env
if ! grep -q 'QA_CAPTURE_ENABLED=' /var/lib/tokenkey/docker-compose.yml; then
  sed -i '/^      - SERVER_FRONTEND_URL=/a\      - QA_CAPTURE_ENABLED=${QA_CAPTURE_ENABLED:-false}' \
    /var/lib/tokenkey/docker-compose.yml
fi
if ! grep -q 'QA_CAPTURE_ENABLED=' /var/lib/tokenkey/docker-compose.yml; then
  echo "BOOTSTRAP_FAIL: failed to insert compose QA_CAPTURE_ENABLED mapping" >&2
  exit 1
fi

if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then
  if GHCR_PAT="$(aws --region "${SSM_REGION}" ssm get-parameter \
    --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
    --query Parameter.Value --output text 2>/dev/null)" && [ -n "${GHCR_PAT}" ]; then
    if ! echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin; then
      echo "GHCR docker login failed; continuing with anonymous pull for ${TOKENKEY_IMAGE}"
    fi
    unset GHCR_PAT
  else
    echo "GHCR PAT unavailable at ${GHCR_PAT_SSM_NAME}; anonymous pull for ${TOKENKEY_IMAGE}"
  fi
else
  echo "GHCR_PAT_SSM_NAME unset; anonymous pull for ${TOKENKEY_IMAGE}"
fi

cat > /etc/systemd/system/tokenkey.service <<'UNITEOF'
[Unit]
Description=tokenkey prod hetzner stack (docker compose)
Requires=docker.service
After=docker.service network-online.target local-fs.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/var/lib/tokenkey
EnvironmentFile=/var/lib/tokenkey/.env
ExecStartPre=-/usr/bin/docker compose --env-file /var/lib/tokenkey/.env pull
ExecStart=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env up -d --remove-orphans
ExecStop=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env down
TimeoutStartSec=10min

[Install]
WantedBy=multi-user.target
UNITEOF

systemctl daemon-reload
systemctl enable --now tokenkey.service
# Host parity with AWS prod / edge HZ: dump + disk/mem Feishu + ghcr prune.
# QA maintenance/boundary + live QA_BUNDLE_* stay off until Wave B cutover
# (staging must not point empty DB at live SQS). Feishu webhook env still
# comes from post-boot sync (copy from AWS prod .env or sync-feishu-config).
systemctl enable --now tokenkey-pgdump.timer
systemctl enable --now tokenkey-disk-metrics.timer
systemctl enable --now tokenkey-ghcr-prune-daily.timer
sleep 30
docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps || true
echo "HETZNER_PROD_BOOTSTRAP_DONE $(date -u +%FT%TZ)"
LAUNCH_TAIL

if [[ "$mode" == "check" ]]; then
  if [[ ! -f "$OUT" ]]; then
    echo "render-prod-bootstrap: FAIL — ${OUT} missing; run without --check and commit" >&2
    rm -f "${OUT}.tmp"
    exit 1
  fi
  if ! cmp -s "$OUT" "${OUT}.tmp"; then
    echo "render-prod-bootstrap: FAIL — ${OUT} drift; re-run render-prod-bootstrap.sh" >&2
    rm -f "${OUT}.tmp"
    exit 1
  fi
  echo "render-prod-bootstrap: OK ($(wc -c <"$OUT" | tr -d ' ') bytes)"
  rm -f "${OUT}.tmp"
  exit 0
fi

mv "${OUT}.tmp" "$OUT"
chmod 0755 "$OUT"
echo "wrote ${OUT} ($(wc -c <"$OUT" | tr -d ' ') bytes)"
