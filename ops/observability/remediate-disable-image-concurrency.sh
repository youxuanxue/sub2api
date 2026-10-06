#!/usr/bin/env bash
# Write-capable: turn off process-local image concurrency on this host and recreate
# the active app container so images share accounts.concurrency with text.
# Delivered via run-probe.sh. Requires APPLY=yes-disable-image-concurrency.
set -euo pipefail

APPLY="${APPLY:-}"
ENV_FILE=/var/lib/tokenkey/.env
ACTIVE_FILE=/var/lib/tokenkey/active-color
ROOT=/var/lib/tokenkey

echo "=== before image concurrency env ==="
sudo awk '/^GATEWAY_IMAGE_CONCURRENCY_/' "$ENV_FILE" || true
echo "=== before container env ==="
if [ -r "$ACTIVE_FILE" ]; then
  color="$(sed -n '1p' "$ACTIVE_FILE" | tr -d '[:space:]')"
  echo "active-color=$color"
  sudo docker inspect "tokenkey-$color" --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | awk '/^GATEWAY_IMAGE_CONCURRENCY_/'
else
  color=""
  sudo docker inspect tokenkey --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | awk '/^GATEWAY_IMAGE_CONCURRENCY_/' || true
fi

if [ "$APPLY" != "yes-disable-image-concurrency" ]; then
  echo "=== dry-run ==="
  echo '{"apply":false,"wanted":"GATEWAY_IMAGE_CONCURRENCY_ENABLED=false"}'
  exit 0
fi

echo "=== apply ENABLED=false ==="
if sudo grep -q '^GATEWAY_IMAGE_CONCURRENCY_ENABLED=' "$ENV_FILE"; then
  sudo sed -i 's|^GATEWAY_IMAGE_CONCURRENCY_ENABLED=.*|GATEWAY_IMAGE_CONCURRENCY_ENABLED=false|' "$ENV_FILE"
else
  echo 'GATEWAY_IMAGE_CONCURRENCY_ENABLED=false' | sudo tee -a "$ENV_FILE" >/dev/null
fi
sudo awk '/^GATEWAY_IMAGE_CONCURRENCY_/' "$ENV_FILE"

echo "=== recreate active app container ==="
cd "$ROOT"
if [ -n "$color" ] && [ -f docker-compose.bluegreen.yml ]; then
  case "$color" in
    blue|green) ;;
    *) echo "::error::invalid active-color=$color"; exit 1 ;;
  esac
  sudo docker compose --project-name tokenkey --env-file .env -f docker-compose.bluegreen.yml \
    up -d --no-deps --force-recreate "tokenkey-$color"
  container="tokenkey-$color"
else
  sudo docker compose --env-file .env up -d --no-deps --force-recreate tokenkey
  container=tokenkey
fi

for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
  s="$(sudo docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo missing)"
  echo "health try $i: $s"
  [ "$s" = healthy ] && break
  sleep 5
done
FINAL="$(sudo docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo missing)"
if [ "$FINAL" != healthy ]; then
  echo "::error::container $container not healthy (final=$FINAL)"
  sudo docker logs "$container" --since 2m 2>&1 | tail -40 || true
  exit 1
fi

echo "=== after container env ==="
sudo docker inspect "$container" --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | awk '/^GATEWAY_IMAGE_CONCURRENCY_/'
