#!/bin/bash
# tokenkey Stage 0 EC2 bootstrap — fetched from SSM at first boot (see stage0-ec2-userdata-launcher.sub.sh).
# Regenerated into SSM by deploy/aws/stage0/build-cfn.sh. Requires TK_* env vars from the launcher.
set -euxo pipefail

: "${TK_API_DOMAIN:?}"
: "${TK_ACME_EMAIL:?}"
: "${TK_AWS_REGION:?}"
: "${TK_GHCR_PULL_USER:?}"
: "${TK_GHCR_PAT_SSM_NAME:?}"
: "${TK_DATA_VOLUME_ID:?}"
: "${TK_PROJECT_NAME:?}"
: "${TK_ENVIRONMENT:?}"
: "${TK_QA_STALE_RETENTION_DAYS:?}"
: "${TK_TOKENKEY_IMAGE:?}"

API_DOMAIN="${TK_API_DOMAIN}"
ACME_EMAIL="${TK_ACME_EMAIL}"
ADMIN_EMAIL="${TK_ADMIN_EMAIL:-}"
[ -z "${ADMIN_EMAIL}" ] && ADMIN_EMAIL="admin@${API_DOMAIN}"
TZ_VALUE="${TK_TZ:-UTC}"
REGION="${TK_AWS_REGION}"
GHCR_PULL_USER="${TK_GHCR_PULL_USER}"
GHCR_PAT_SSM_NAME="${TK_GHCR_PAT_SSM_NAME}"
DATA_VOLUME_ID="${TK_DATA_VOLUME_ID}"
TOKENKEY_IMAGE="${TK_TOKENKEY_IMAGE}"
STAGE0_PREFIX="/${TK_PROJECT_NAME}/${TK_ENVIRONMENT}/stage0"

# --- 1. system packages -------------------------------------------------
dnf -y update
dnf -y install docker git jq gettext openssl
ARCH="$(uname -m)"
case "${ARCH}" in
  aarch64) CWA_ARCH="arm64" ;;
  x86_64)  CWA_ARCH="amd64" ;;
  *) echo "unsupported arch ${ARCH}" >&2; exit 1 ;;
esac
dnf -y install "https://s3.amazonaws.com/amazoncloudwatch-agent/amazon_linux/${CWA_ARCH}/latest/amazon-cloudwatch-agent.rpm" || true
systemctl enable --now docker

# Compose v2 plugin (AL2023 dnf has no docker-compose-plugin)
DOCKER_COMPOSE_VERSION="v2.29.7"
mkdir -p /usr/local/lib/docker/cli-plugins
curl -fsSL "https://github.com/docker/compose/releases/download/${DOCKER_COMPOSE_VERSION}/docker-compose-linux-$(uname -m)" \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
usermod -aG docker ec2-user

# --- 1b. swap (memory-pressure release valve) ---------------------------
# 2026-06-17 prod incident: an unbounded in-RAM qa export drove memused 7%->92%
# on this 8 GiB box with NO swap; the kernel had no release valve, evicted the
# entire page cache, then thrashed re-reading it off the Postgres data volume
# (iowait 91%, load 20, 22 D-state tasks) until the OS half-deadlocked and had
# to be rebooted. A modest swapfile turns that cliff into graceful (slow)
# degradation. Lives on the ROOT volume so swap I/O never contends with
# Postgres on /var/lib/tokenkey. Idempotent: skip if already active.
SWAPFILE=/swapfile
SWAP_SIZE_MB="${TK_SWAP_SIZE_MB:-4096}"
# Best-effort: swap is defense-in-depth, so a setup failure must NEVER abort the
# essential bootstrap (every step here is failure-guarded under set -e), and we
# persist the fstab entry only after swap is confirmed active so a partial setup
# can't leave a broken entry that trips a swapon at the next boot.
if ! swapon --show=NAME --noheadings 2>/dev/null | grep -qx "${SWAPFILE}"; then
  if [ ! -e "${SWAPFILE}" ]; then
    fallocate -l "${SWAP_SIZE_MB}M" "${SWAPFILE}" 2>/dev/null \
      || dd if=/dev/zero of="${SWAPFILE}" bs=1M count="${SWAP_SIZE_MB}" status=none 2>/dev/null \
      || true  # preflight-allow: swallow — best-effort swap alloc; never abort bootstrap
  fi
  chmod 600 "${SWAPFILE}" 2>/dev/null || true                       # preflight-allow: swallow — best-effort swap; never abort bootstrap
  mkswap "${SWAPFILE}" >/dev/null 2>&1 || true                      # preflight-allow: swallow — best-effort swap; never abort bootstrap
  swapon "${SWAPFILE}" 2>/dev/null || true                         # preflight-allow: swallow — best-effort swap; never abort bootstrap
fi
if swapon --show=NAME --noheadings 2>/dev/null | grep -qx "${SWAPFILE}"; then
  grep -q "^${SWAPFILE} " /etc/fstab || echo "${SWAPFILE} none swap sw 0 0" >> /etc/fstab
fi
cat > /etc/sysctl.d/90-tokenkey-swap.conf <<'SYSCTLEOF'
# Only swap under genuine memory pressure (protect steady-state latency), and
# bias the kernel toward keeping the page cache instead of dropping it — both
# directly counter the 2026-06-17 no-swap page-cache-thrash failure mode.
vm.swappiness=10
vm.vfs_cache_pressure=50
SYSCTLEOF
sysctl --system >/dev/null 2>&1 || true                            # preflight-allow: swallow — best-effort sysctl reload; never abort bootstrap

# --- 2a. attach + mount persistent data volume --------------------------
DATA_DEVICE='/dev/sdf'
IMDS_TOKEN="$(curl -fsS -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")"
INSTANCE_ID="$(curl -fsS -H "X-aws-ec2-metadata-token: ${IMDS_TOKEN}" http://169.254.169.254/latest/meta-data/instance-id)"
ATTACHED_TO=""
ATTACH_STATE=""
for attempt in $(seq 1 90); do
  read -r ATTACHED_TO ATTACH_STATE < <(aws ec2 describe-volumes \
    --region "${REGION}" \
    --volume-ids "${DATA_VOLUME_ID}" \
    --query 'Volumes[0].Attachments[0].[InstanceId,State]' \
    --output text 2>/dev/null || echo "None None")
  if [ "${ATTACHED_TO}" = "${INSTANCE_ID}" ]; then
    echo "tokenkey data volume ${DATA_VOLUME_ID} already attached to this instance (${ATTACH_STATE})"
    break
  fi
  if [ -z "${ATTACHED_TO}" ] || [ "${ATTACHED_TO}" = "None" ]; then
    echo "attaching tokenkey data volume ${DATA_VOLUME_ID} to ${INSTANCE_ID}"
    aws ec2 attach-volume \
      --region "${REGION}" \
      --volume-id "${DATA_VOLUME_ID}" \
      --instance-id "${INSTANCE_ID}" \
      --device "${DATA_DEVICE}"
    break
  fi
  echo "waiting for tokenkey data volume ${DATA_VOLUME_ID}; currently ${ATTACH_STATE} on ${ATTACHED_TO}"
  sleep 10
done
if [ "${ATTACHED_TO}" != "${INSTANCE_ID}" ]; then
  for attempt in $(seq 1 60); do
    read -r ATTACHED_TO ATTACH_STATE < <(aws ec2 describe-volumes \
      --region "${REGION}" \
      --volume-ids "${DATA_VOLUME_ID}" \
      --query 'Volumes[0].Attachments[0].[InstanceId,State]' \
      --output text 2>/dev/null || echo "None None")
    [ "${ATTACHED_TO}" = "${INSTANCE_ID}" ] && break
    sleep 5
  done
fi
if [ "${ATTACHED_TO}" != "${INSTANCE_ID}" ]; then
  echo "FATAL: tokenkey data volume ${DATA_VOLUME_ID} did not attach to ${INSTANCE_ID}" >&2
  exit 1
fi

DATA_DEV=""
for attempt in 1 2 3 4 5 6 7 8 9; do
  for cand in /dev/nvme1n1 /dev/nvme2n1 /dev/nvme3n1 /dev/xvdf /dev/sdf; do
    if [ -b "${cand}" ]; then
      ROOT_DEV="$(findmnt -no SOURCE / | sed 's/p[0-9]*$//')"
      if [ "${cand}" != "${ROOT_DEV}" ]; then
        DATA_DEV="${cand}"
        break
      fi
    fi
  done
  [ -n "${DATA_DEV}" ] && break
  sleep 10
done
if [ -z "${DATA_DEV}" ]; then
  echo "FATAL: tokenkey data volume not attached within 90s" >&2
  exit 1
fi
echo "tokenkey data volume detected at ${DATA_DEV}"

if ! blkid "${DATA_DEV}" >/dev/null 2>&1; then
  echo "Formatting ${DATA_DEV} as ext4 (first-time provisioning)"
  mkfs.ext4 -L tokenkey-data "${DATA_DEV}"
else
  echo "Reusing existing filesystem on ${DATA_DEV} (preserved across instance replacement)"
fi
DATA_UUID="$(blkid -o value -s UUID "${DATA_DEV}" || true)"
if [ -z "${DATA_UUID}" ]; then
  echo "FATAL: unable to read UUID from ${DATA_DEV}" >&2
  exit 1
fi

install -d -m 0755 -o root -g root /var/lib/tokenkey
mount "${DATA_DEV}" /var/lib/tokenkey
if ! grep -q '/var/lib/tokenkey' /etc/fstab; then
  echo "UUID=${DATA_UUID} /var/lib/tokenkey ext4 defaults,nofail,x-systemd.device-timeout=90 0 2" >> /etc/fstab
fi

# --- 2b. data directory layout ------------------------------------------
install -d -m 0755 /var/lib/tokenkey/app
install -d -m 0700 /var/lib/tokenkey/postgres
install -d -m 0755 /var/lib/tokenkey/redis
install -d -m 0755 /var/lib/tokenkey/pgdump
install -d -m 0755 /var/lib/tokenkey/caddy
install -d -m 0755 /var/lib/tokenkey/caddy/data
install -d -m 0755 /var/lib/tokenkey/caddy/config
install -d -m 0755 /var/lib/tokenkey/logs
cd /var/lib/tokenkey

# --- 3. docker-compose + Caddy from SSM ---------------------------------
COMPOSE_PARAM="${STAGE0_PREFIX}/docker-compose.gzip.b64"
CADDY_PARAM="${STAGE0_PREFIX}/caddyfile.template.gzip.b64"
COMPOSE_B64="$(aws ssm get-parameter --name "${COMPOSE_PARAM}" --region "${REGION}" --query Parameter.Value --output text)"
CADDY_B64="$(aws ssm get-parameter --name "${CADDY_PARAM}" --region "${REGION}" --query Parameter.Value --output text)"
printf '%s' "${COMPOSE_B64}" | base64 -d | gunzip > docker-compose.yml
printf '%s' "${CADDY_B64}" | base64 -d | gunzip > caddy/Caddyfile.template
API_DOMAIN="${API_DOMAIN}" ACME_EMAIL="${ACME_EMAIL}" \
  envsubst '$API_DOMAIN $ACME_EMAIL' \
  < caddy/Caddyfile.template > caddy/Caddyfile

install -d -m 0755 /etc/tokenkey
printf 'TOKENKEY_QA_STALE_RETENTION_DAYS=%s\n' "${TK_QA_STALE_RETENTION_DAYS}" > /etc/tokenkey/qa-stale-retention.env
QA_B64_PARAM_NAME="${STAGE0_PREFIX}/qa-stale-cleanup.b64"
RAW="$(aws ssm get-parameter --name "${QA_B64_PARAM_NAME}" --region "${REGION}" --query Parameter.Value --output text)"
printf '%s' "${RAW}" | base64 -d > /usr/local/bin/tokenkey-qa-stale-cleanup.sh
chmod +x /usr/local/bin/tokenkey-qa-stale-cleanup.sh

printf '%s\n' "${STAGE0_PREFIX}/ghcr-prune.b64" > /etc/tokenkey/ghcr-prune-ssm.path
install -m 0755 /dev/stdin /usr/local/bin/tokenkey-prune-ghcr-app-tags.sh <<'LOADEREOF'
#!/bin/bash
set -euo pipefail
PATHFILE=/etc/tokenkey/ghcr-prune-ssm.path
if [ ! -f "$PATHFILE" ]; then
  echo "tokenkey-prune-ghcr-app-tags: missing path file" >&2
  exit 0
fi
IMDS_TOKEN="$(curl -fsS -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")"
REGION="$(curl -fsS -H "X-aws-ec2-metadata-token: $IMDS_TOKEN" http://169.254.169.254/latest/meta-data/placement/region)"
PNAME="$(tr -d '\n' <"$PATHFILE")"
RAW="$(aws ssm get-parameter --name "$PNAME" --region "$REGION" --query Parameter.Value --output text)"
TMP="$(mktemp)"
cleanup() { rm -f "$TMP"; }
trap cleanup EXIT
printf '%s' "$RAW" | base64 -d >"$TMP"
chmod +x "$TMP"
exec bash "$TMP"
LOADEREOF

# --- 4. secrets + .env --------------------------------------------------
SECRET_FILE=/var/lib/tokenkey/.env.secret
if [ ! -f "${SECRET_FILE}" ]; then
  umask 077
  if [ -f /var/lib/tokenkey/.env ] \
    && grep -q '^POSTGRES_PASSWORD=' /var/lib/tokenkey/.env \
    && grep -q '^JWT_SECRET=' /var/lib/tokenkey/.env \
    && grep -q '^TOTP_ENCRYPTION_KEY=' /var/lib/tokenkey/.env; then
    echo "Seeding ${SECRET_FILE} from legacy /var/lib/tokenkey/.env"
    {
      grep -m1 '^POSTGRES_PASSWORD=' /var/lib/tokenkey/.env
      grep -m1 '^JWT_SECRET=' /var/lib/tokenkey/.env
      grep -m1 '^TOTP_ENCRYPTION_KEY=' /var/lib/tokenkey/.env
    } > "${SECRET_FILE}"
  else
    echo "Generating new persistent secrets at ${SECRET_FILE} (first boot of data volume)"
    gen_secret() { openssl rand -hex 32; }
    gen_pwd()    { openssl rand -hex 24; }
    cat > "${SECRET_FILE}" <<SECEOF
POSTGRES_PASSWORD=$(gen_pwd)
JWT_SECRET=$(gen_secret)
TOTP_ENCRYPTION_KEY=$(gen_secret)
SECEOF
  fi
  chmod 0600 "${SECRET_FILE}"
  chown root:root "${SECRET_FILE}"
else
  echo "Reusing existing persistent secrets from ${SECRET_FILE} (data volume preserved across instance replacement)"
fi
# shellcheck disable=SC1090
set -a; . "${SECRET_FILE}"; set +a

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
DATABASE_MAX_OPEN_CONNS=50
DATABASE_MAX_IDLE_CONNS=10
REDIS_PASSWORD=
REDIS_DB=0
REDIS_POOL_SIZE=1024
REDIS_MIN_IDLE_CONNS=10
ADMIN_EMAIL=${ADMIN_EMAIL}
ADMIN_PASSWORD=
JWT_SECRET=${JWT_SECRET}
JWT_EXPIRE_HOUR=1
TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}
ENVEOF
chmod 0600 /var/lib/tokenkey/.env

# --- 5. GHCR private-image login ----------------------------------------
GHCR_PAT="$(aws --region "${REGION}" ssm get-parameter \
  --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
  --query Parameter.Value --output text)"
echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin
unset GHCR_PAT

# --- 6. systemd units + helper scripts ----------------------------------
PGDUMP_B64_PARAM_NAME="${STAGE0_PREFIX}/pgdump.b64"
RAW="$(aws ssm get-parameter --name "${PGDUMP_B64_PARAM_NAME}" --region "${REGION}" --query Parameter.Value --output text)"
# pgdump is gzip+base64 (like compose/caddy/bootstrap) — see build-cfn.sh.
printf '%s' "${RAW}" | base64 -d | gunzip > /usr/local/bin/tokenkey-pgdump.sh
chmod +x /usr/local/bin/tokenkey-pgdump.sh

install -m 0755 /dev/stdin /usr/local/bin/tokenkey-disk-metrics.sh <<'DISKEOF'
#!/bin/bash
set -euo pipefail
IMDS_TOKEN="$(curl -fsS -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")"
REGION="$(curl -fsS -H "X-aws-ec2-metadata-token: $IMDS_TOKEN" http://169.254.169.254/latest/meta-data/placement/region)"
IID="$(curl -fsS -H "X-aws-ec2-metadata-token: $IMDS_TOKEN" http://169.254.169.254/latest/meta-data/instance-id)"

# These alerts MUST NOT route through the app (it is dead exactly when needed: a
# full data volume crashes Postgres; a memory spike thrashes the box into an I/O
# half-deadlock). This timer runs every few minutes independent of Docker, so it
# is the robust place to fire. (2026-06-15 disk P0 + 2026-06-17 memory/IO P0.)
# Webhook + secret injected via /var/lib/tokenkey/.env (same off-box-secret
# point as TOKENKEY_PGDUMP_S3_URI); absent webhook => silent no-op.
COOLDOWN="${TOKENKEY_DISK_ALERT_COOLDOWN_SEC:-1800}"
WEBHOOK=""; SECRET=""; NODE="${IID}"
if [ -r /var/lib/tokenkey/.env ]; then
  WEBHOOK="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_URL=//p' /var/lib/tokenkey/.env | head -1)"
  SECRET="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_SECRET=//p' /var/lib/tokenkey/.env | head -1)"
  DOM="$(sed -n 's/^API_DOMAIN=//p' /var/lib/tokenkey/.env | head -1)"
  [ -n "${DOM}" ] && NODE="${DOM}"
fi

# Self-contained Feishu alert with per-stamp cooldown. $1=stamp file, $2=text.
# Feishu returns HTTP 200 even when it REJECTS; the real status is the body's
# "code" field (0 = delivered). Stamp the cooldown only on code:0, so a
# misconfigured webhook keeps retrying instead of silently dropping every alert.
tk_feishu_alert() {
  local stamp="$1" text="$2" now last sign payload resp
  [ -n "${WEBHOOK}" ] || return 0
  now="$(date +%s)"; last=0
  [ -r "${stamp}" ] && last="$(cat "${stamp}" 2>/dev/null || echo 0)"
  [ "$((now - last))" -ge "${COOLDOWN}" ] || return 0
  if [ -n "${SECRET}" ]; then
    sign="$(printf '' | openssl dgst -sha256 -hmac "${now}"$'\n'"${SECRET}" -binary 2>/dev/null | base64)"
    payload="$(printf '{"timestamp":"%s","sign":"%s","msg_type":"text","content":{"text":"%s"}}' "${now}" "${sign}" "${text}")"
  else
    payload="$(printf '{"msg_type":"text","content":{"text":"%s"}}' "${text}")"
  fi
  resp="$(curl -sS -m 10 -X POST "${WEBHOOK}" -H 'Content-Type: application/json' -d "${payload}" 2>/dev/null || true)"  # preflight-allow: swallow — curl failure ⇒ retry next tick
  case "${resp}" in *'"code":0'*) echo "${now}" > "${stamp}" || true ;; esac  # preflight-allow: swallow — best-effort cooldown stamp
}

# --- DataVolume usage metric + disk-full alert (2026-06-15 prod P0) -----------
USED="$(df -P /var/lib/tokenkey 2>/dev/null | awk 'NR==2 {gsub(/%/,"",$5); print $5}')"
if [ -n "${USED}" ]; then
  aws cloudwatch put-metric-data --region "${REGION}" \
    --namespace tokenkey/EC2 \
    --metric-data "MetricName=DataVolumeUsedPercent,Value=${USED},Unit=Percent,Dimensions=[{Name=InstanceId,Value=${IID}}]"
  DISK_THRESHOLD="${TOKENKEY_DISK_ALERT_THRESHOLD:-85}"
  if [ "${USED}" -ge "${DISK_THRESHOLD}" ]; then
    tk_feishu_alert /run/tokenkey-disk-alert.stamp "🔴 P0 磁盘将满 ${NODE} — DataVolume /var/lib/tokenkey 使用率 ${USED}% (阈值 ${DISK_THRESHOLD}%)。Postgres 满盘会崩溃→登录/网关全挂。立即清 pgdump/日志或扩容数据卷。instance=${IID}"
  fi
fi

# --- memory-pressure alert (2026-06-17 prod P0) ------------------------------
# Leading indicator: MemAvailable collapses (memused% high) minutes BEFORE the
# page-cache-thrash wedge, so 90% fires while the box is still reachable and the
# operator can kill the offending workload (heavy export / full-table query).
# Swap (added 2026-06-17) softens the cliff; this alert is the early heads-up.
MEM_THRESHOLD="${TOKENKEY_MEM_ALERT_THRESHOLD:-90}"
MEMUSEDPCT="$(awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END{ if(t>0) printf "%d",(t-a)*100/t; else print 0 }' /proc/meminfo 2>/dev/null || echo 0)"
if [ "${MEMUSEDPCT:-0}" -ge "${MEM_THRESHOLD}" ]; then
  SWAPPCT="$(awk '/^SwapTotal:/{t=$2} /^SwapFree:/{f=$2} END{ if(t>0) printf "%d",(t-f)*100/t; else print 0 }' /proc/meminfo 2>/dev/null || echo 0)"
  LOAD1="$(awk '{print $1}' /proc/loadavg 2>/dev/null || echo 0)"
  tk_feishu_alert /run/tokenkey-mem-alert.stamp "🟠 P1 内存压力 ${NODE} — 内存 ${MEMUSEDPCT}% (阈值 ${MEM_THRESHOLD}%), swap ${SWAPPCT}%, load1 ${LOAD1}。无 headroom 会驱逐 page cache→数据盘 I/O 颠簸→OS 半死锁。立即查并终止重导出/全表查询等吃内存任务。instance=${IID}"
fi
DISKEOF

cat > /etc/systemd/system/tokenkey.service <<'UNITEOF'
[Unit]
Description=tokenkey stack (docker compose)
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
ExecStartPost=-/usr/local/bin/tokenkey-prune-ghcr-app-tags.sh
ExecStop=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env down
TimeoutStartSec=10min

[Install]
WantedBy=multi-user.target
UNITEOF

cat > /etc/systemd/system/tokenkey-disk-metrics.service <<'DMSEOF'
[Unit]
Description=Publish tokenkey DataVolume used_percent to CloudWatch + on-box disk-full & memory-pressure Feishu alerts
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
DMSEOF

cat > /etc/systemd/system/tokenkey-disk-metrics.timer <<'DMTEOF'
[Unit]
Description=Publish DataVolume disk metric every 5 minutes

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
Description=tokenkey pg_dump (hourly)
After=tokenkey.service
Requires=tokenkey.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/tokenkey-pgdump.sh
PSEOF

cat > /etc/systemd/system/tokenkey-pgdump.timer <<'PTEOF'
[Unit]
Description=Run tokenkey-pgdump hourly

[Timer]
OnCalendar=*-*-* *:00:00
Persistent=true
RandomizedDelaySec=2min

[Install]
WantedBy=timers.target
PTEOF

cat > /etc/systemd/system/tokenkey-qa-stale-cleanup.service <<'QASVEOF'
[Unit]
Description=Prune QA records and blob trees older than retention
After=network-online.target tokenkey.service
Wants=network-online.target
Requires=tokenkey.service

[Service]
Type=oneshot
EnvironmentFile=-/etc/tokenkey/qa-stale-retention.env
ExecStart=/usr/local/bin/tokenkey-qa-stale-cleanup.sh
QASVEOF

cat > /etc/systemd/system/tokenkey-qa-stale-cleanup.timer <<'QATIMEOF'
[Unit]
Description=Daily QA stale cleanup (low-traffic window)

[Timer]
OnCalendar=*-*-* 04:15:00
RandomizedDelaySec=30min
Persistent=true

[Install]
WantedBy=timers.target
QATIMEOF

# --- 7. CloudWatch Agent ------------------------------------------------
cat > /opt/aws/amazon-cloudwatch-agent/etc/tokenkey.json <<'CWEOF'
{
  "logs": {
    "logs_collected": {
      "files": {
        "collect_list": [
          {"file_path": "/var/log/tokenkey-bootstrap.log", "log_group_name": "/tokenkey/bootstrap", "log_stream_name": "{instance_id}"},
          {"file_path": "/var/log/cloud-init-output.log", "log_group_name": "/tokenkey/cloud-init", "log_stream_name": "{instance_id}"}
        ]
      }
    }
  },
  "metrics": {
    "namespace": "tokenkey/EC2",
    "append_dimensions": {"InstanceId": "${aws:InstanceId}"},
    "metrics_collected": {
      "cpu":  {"measurement": ["cpu_usage_iowait", "cpu_usage_idle"], "totalcpu": true},
      "mem":  {"measurement": ["mem_used_percent"]},
      "disk": {"measurement": ["used_percent"], "resources": ["/", "/var/lib/tokenkey"]}
    }
  }
}
CWEOF

systemctl daemon-reload
systemctl enable --now tokenkey.service
systemctl enable --now tokenkey-pgdump.timer
systemctl enable --now tokenkey-disk-metrics.timer
systemctl enable --now tokenkey-qa-stale-cleanup.timer
if [ -x /opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent-ctl ]; then
  /opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent-ctl \
    -a fetch-config -m ec2 -c file:/opt/aws/amazon-cloudwatch-agent/etc/tokenkey.json -s || true
fi

sleep 30
( docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps || true ) \
  >> /var/log/tokenkey-bootstrap.log 2>&1
echo "BOOTSTRAP_DONE $(date -u +%FT%TZ)" >> /var/log/tokenkey-bootstrap.log
