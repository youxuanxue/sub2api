#!/usr/bin/env bash
# Host-side bounded Docker log policy sync (Caddy-only recreate).
# Delivered via run-probe --via tat with --with deploy/stage0/docker-compose.yml.
# Refuses to replace a Tencent multi-app layout compose with the Stage0 file.
set -euo pipefail

ROOT=/var/lib/tokenkey
COMPOSE=$ROOT/docker-compose.yml
# run-probe --with uploads additional files to /tmp/<basename> on the remote.
SRC=/tmp/docker-compose.yml
[[ -f "$SRC" ]] || { echo "probe-sync-container-log-policy: missing /tmp/docker-compose.yml (--with payload)" >&2; exit 1; }

if sudo grep -Eq 'tk-archive-standby|tk-cutover-prod-redis' "$COMPOSE"; then
  echo "sync-container-log-policy: refusing to replace a Tencent layout compose with the Stage0 file" >&2
  exit 1
fi

CANDIDATE=$ROOT/docker-compose.yml.log-policy-new
BACKUP=$ROOT/docker-compose.yml.before-log-policy-$(date -u +%Y%m%dT%H%M%SZ)

rollback() {
  rc=$?
  trap - ERR
  echo "rollback: restoring previous compose and Caddy" >&2
  if [[ -f "$BACKUP" ]]; then
    sudo cp -a "$BACKUP" "$COMPOSE"
    sudo docker compose --project-name tokenkey --env-file "$ROOT/.env" -f "$COMPOSE" up -d --no-deps --force-recreate caddy || true
  fi
  exit "$rc"
}
trap rollback ERR

echo === before ===
sudo docker inspect tokenkey-caddy --format 'log={{json .HostConfig.LogConfig}} path={{.LogPath}}'
OLD_LOG=$(sudo docker inspect tokenkey-caddy --format '{{.LogPath}}')
OLD_BYTES=$(sudo stat -c %s "$OLD_LOG" 2>/dev/null || echo 0)
df -P /

sudo cp -a "$SRC" "$CANDIDATE"
sudo docker compose --project-name tokenkey --env-file "$ROOT/.env" -f "$CANDIDATE" config --quiet
sudo cp -a "$COMPOSE" "$BACKUP"
sudo mv "$CANDIDATE" "$COMPOSE"
sudo docker compose --project-name tokenkey --env-file "$ROOT/.env" -f "$COMPOSE" up -d --no-deps --force-recreate caddy

for i in $(seq 1 30); do
  status=$(sudo docker inspect tokenkey-caddy --format '{{.State.Status}}' 2>/dev/null || true)
  [[ "$status" = running ]] && break
  sleep 1
done
[[ "$(sudo docker inspect tokenkey-caddy --format '{{.State.Status}}')" = running ]]
[[ "$(sudo docker inspect tokenkey-caddy --format '{{.HostConfig.LogConfig.Type}}')" = json-file ]]
MAX_SIZE=$(sudo docker inspect tokenkey-caddy --format '{{index .HostConfig.LogConfig.Config "max-size"}}')
[[ "$MAX_SIZE" = 100m ]]
MAX_FILE=$(sudo docker inspect tokenkey-caddy --format '{{index .HostConfig.LogConfig.Config "max-file"}}')
[[ "$MAX_FILE" = 5 ]]

API_DOMAIN=$(sed -n 's/^API_DOMAIN=//p' "$ROOT/.env" | head -1)
API_DOMAIN=${API_DOMAIN:-api.tokenkey.dev}
healthy=0
for i in $(seq 1 12); do
  if curl -fsS --max-time 10 "https://${API_DOMAIN}/health" >/dev/null; then
    healthy=1
    break
  fi
  sleep 5
done
[[ "$healthy" = 1 ]]

trap - ERR
echo === after ===
sudo docker inspect tokenkey-caddy --format 'log={{json .HostConfig.LogConfig}} path={{.LogPath}} status={{.State.Status}}'
NEW_LOG=$(sudo docker inspect tokenkey-caddy --format '{{.LogPath}}')
NEW_BYTES=$(sudo stat -c %s "$NEW_LOG" 2>/dev/null || echo 0)
printf 'old_log_bytes=%s new_log_bytes=%s backup=%s\n' "$OLD_BYTES" "$NEW_BYTES" "$BACKUP"
df -P /
echo "sync-container-log-policy: OK"
