#!/usr/bin/env bash
# Write-capable: turn off process-local image concurrency on this host and recreate
# the active app container so images share accounts.concurrency with text.
# Delivered via run-probe.sh. Requires APPLY=yes-disable-image-concurrency.
set -euo pipefail

APPLY="${APPLY:-}"
ENV_FILE=/var/lib/tokenkey/.env
ACTIVE_FILE=/var/lib/tokenkey/active-color
ROOT=/var/lib/tokenkey

container_env_image_concurrency() {
  sudo docker inspect "$1" --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | awk '/^GATEWAY_IMAGE_CONCURRENCY_/'
}

echo "=== before image concurrency env ==="
sudo awk '/^GATEWAY_IMAGE_CONCURRENCY_/' "$ENV_FILE" || true
echo "=== before container env ==="
if [ -r "$ACTIVE_FILE" ]; then
  color="$(sed -n '1p' "$ACTIVE_FILE" | tr -d '[:space:]')"
  echo "active-color=$color"
  container_env_image_concurrency "tokenkey-$color" || true
else
  color=""
  container_env_image_concurrency tokenkey || true
fi

if [ "$APPLY" != "yes-disable-image-concurrency" ]; then
  echo "=== dry-run ==="
  echo '{"apply":false,"wanted":"GATEWAY_IMAGE_CONCURRENCY_ENABLED=false"}'
  exit 0
fi

echo "=== backup .env ==="
ts="$(date +%Y%m%d-%H%M%S)"
sudo cp -a "$ENV_FILE" "${ENV_FILE}.before-disable-image-concurrency-${ts}"

echo "=== apply ENABLED=false ==="
if sudo grep -q '^GATEWAY_IMAGE_CONCURRENCY_ENABLED=' "$ENV_FILE"; then
  sudo sed -i 's|^GATEWAY_IMAGE_CONCURRENCY_ENABLED=.*|GATEWAY_IMAGE_CONCURRENCY_ENABLED=false|' "$ENV_FILE"
else
  echo 'GATEWAY_IMAGE_CONCURRENCY_ENABLED=false' | sudo tee -a "$ENV_FILE" >/dev/null
fi
sudo awk '/^GATEWAY_IMAGE_CONCURRENCY_/' "$ENV_FILE"

cd "$ROOT"
if [ -n "$color" ] && [ -f docker-compose.bluegreen.yml ]; then
  case "$color" in
    blue|green) ;;
    *) echo "::error::invalid active-color=$color"; exit 1 ;;
  esac
  container="tokenkey-$color"
  compose=(sudo docker compose --project-name tokenkey --env-file .env -f docker-compose.bluegreen.yml)
else
  container=tokenkey
  compose=(sudo docker compose --env-file .env)
fi

echo "=== pre-drain $container ==="
OLD_HEALTH="$(sudo docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || echo missing)"
echo "pre-drain: outgoing container health=$OLD_HEALTH"
if [ "$OLD_HEALTH" = healthy ]; then
  sudo docker kill -s USR1 "$container" 2>/dev/null || true
  prev=-1
  stall=0
  for i in $(seq 1 15); do
    body="$(sudo docker exec "$container" wget -q -T 3 -O - http://localhost:8080/health/inflight 2>/dev/null || true)"
    n="$(printf '%s' "$body" | sed -n 's/.*"in_flight":\([0-9]*\).*/\1/p')"
    if printf '%s' "$body" | grep -q '"draining":true'; then d=true; else d=false; fi
    echo "pre-drain: draining=$d in_flight=${n:-?} try=$i/15"
    [ "$d" = true ] && [ "${n:-1}" = 0 ] && break
    if [ -n "$n" ]; then
      if [ "$prev" -ge 0 ] && [ "$n" -ge "$prev" ]; then stall=$((stall + 1)); else stall=0; fi
      prev=$n
      if [ "$stall" -ge 3 ]; then
        echo "pre-drain: in_flight plateaued at $n for 3 tries; stop waiting"
        break
      fi
    fi
    sleep 2
  done
else
  echo "pre-drain SKIPPED: outgoing container not healthy (health=$OLD_HEALTH)"
fi

echo "=== recreate $container ==="
"${compose[@]}" up -d --no-deps --force-recreate --timeout 30 "$container"

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
after_env="$(container_env_image_concurrency "$container")"
printf '%s\n' "$after_env"
if ! printf '%s\n' "$after_env" | grep -qx 'GATEWAY_IMAGE_CONCURRENCY_ENABLED=false'; then
  echo "::error::$container env missing GATEWAY_IMAGE_CONCURRENCY_ENABLED=false"
  exit 1
fi
