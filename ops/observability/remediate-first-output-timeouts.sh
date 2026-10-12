#!/usr/bin/env bash
# Write-capable: apply OpenAI-scoped first-output budgets (and restore universal
# stream SSOT defaults) on this host, then recreate the active app container.
# Delivered via run-probe.sh. Requires APPLY=yes-first-output-timeouts.
#
# Evidence (prod 24h): first_output_timeout events were openai-only, so only
# OpenAI knobs are raised. Universal stream SSOT stays 15s / 30s.
#
# Target:
#   GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS=15
#   GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS=30
#   GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS=30
#   GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS=60
set -euo pipefail

APPLY="${APPLY:-}"
ENV_FILE=/var/lib/tokenkey/.env
ACTIVE_FILE=/var/lib/tokenkey/active-color
ROOT=/var/lib/tokenkey
COMPOSE_BG="$ROOT/docker-compose.bluegreen.yml"

WANT_STREAM=15
WANT_STREAM_HIGH=30
WANT_OPENAI=30
WANT_OPENAI_HIGH=60

KEYS=(
  GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS
  GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS
  GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS
  GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS
)
WANTS=("$WANT_STREAM" "$WANT_STREAM_HIGH" "$WANT_OPENAI" "$WANT_OPENAI_HIGH")

container_env_timeouts() {
  sudo docker inspect "$1" --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | awk '/^(GATEWAY_STREAM_FIRST_OUTPUT_|GATEWAY_OPENAI_FIRST_OUTPUT_|GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_)/'
}

upsert_env() {
  local key="$1" val="$2"
  if sudo grep -q "^${key}=" "$ENV_FILE"; then
    sudo sed -i "s|^${key}=.*|${key}=${val}|" "$ENV_FILE"
  else
    printf '%s=%s\n' "$key" "$val" | sudo tee -a "$ENV_FILE" >/dev/null
  fi
}

ensure_compose_openai_lines() {
  # Insert OpenAI timeout env after STREAM high-effort line in both color blocks
  # when missing. Idempotent.
  local f="$1"
  if sudo grep -q 'GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS' "$f"; then
    return 0
  fi
  sudo python3 - "$f" <<'PY'
import pathlib, sys
path = pathlib.Path(sys.argv[1])
text = path.read_text()
candidates = [
    "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS=${GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS:-30}",
    "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS=${GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS:-60}",
]
needle = next((c for c in candidates if c in text), None)
if needle is None:
    raise SystemExit("compose missing STREAM high-effort line; refuse to patch")
insert = (
    needle
    + "\n      - GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS=${GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS:-30}"
    + "\n      - GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS=${GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS:-60}"
)
path.write_text(text.replace(needle, insert))
PY
}

bump_compose_fallbacks() {
  local f="$1"
  sudo sed -i \
    -e 's|GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS=\${GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS:-[0-9]*}|GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS=${GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS:-15}|g' \
    -e 's|GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS=\${GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS:-[0-9]*}|GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS=${GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS:-30}|g' \
    -e 's|GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS=\${GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS:-[0-9]*}|GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS=${GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS:-30}|g' \
    -e 's|GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS=\${GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS:-[0-9]*}|GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS=${GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS:-60}|g' \
    "$f"
}

echo "=== before .env timeout keys ==="
sudo awk '/^(GATEWAY_STREAM_FIRST_OUTPUT_|GATEWAY_OPENAI_FIRST_OUTPUT_|GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_)/' "$ENV_FILE" || true
echo "=== before compose timeout lines ==="
sudo grep -nE 'STREAM_FIRST|OPENAI_FIRST|OPENAI_HIGH' "$COMPOSE_BG" || true
echo "=== before container env ==="
if [ -r "$ACTIVE_FILE" ]; then
  color="$(sed -n '1p' "$ACTIVE_FILE" | tr -d '[:space:]')"
  echo "active-color=$color"
  container_env_timeouts "tokenkey-$color" || true
else
  color=""
  container_env_timeouts tokenkey || true
fi

echo "=== wanted ==="
for i in "${!KEYS[@]}"; do
  echo "${KEYS[$i]}=${WANTS[$i]}"
done

if [ "$APPLY" != "yes-first-output-timeouts" ]; then
  echo "=== dry-run ==="
  echo '{"apply":false}'
  exit 0
fi

echo "=== backup ==="
ts="$(date +%Y%m%d-%H%M%S)"
sudo cp -a "$ENV_FILE" "${ENV_FILE}.before-first-output-timeouts-${ts}"
sudo cp -a "$COMPOSE_BG" "${COMPOSE_BG}.before-first-output-timeouts-${ts}"

echo "=== upsert .env ==="
for i in "${!KEYS[@]}"; do
  upsert_env "${KEYS[$i]}" "${WANTS[$i]}"
done
sudo awk '/^(GATEWAY_STREAM_FIRST_OUTPUT_|GATEWAY_OPENAI_FIRST_OUTPUT_|GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_)/' "$ENV_FILE"

echo "=== patch compose ==="
ensure_compose_openai_lines "$COMPOSE_BG"
bump_compose_fallbacks "$COMPOSE_BG"
sudo grep -nE 'STREAM_FIRST|OPENAI_FIRST|OPENAI_HIGH' "$COMPOSE_BG"

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
after_env="$(container_env_timeouts "$container")"
printf '%s\n' "$after_env"
fail=0
for i in "${!KEYS[@]}"; do
  want_line="${KEYS[$i]}=${WANTS[$i]}"
  if ! printf '%s\n' "$after_env" | grep -qx "$want_line"; then
    echo "::error::missing or wrong: $want_line"
    fail=1
  fi
done
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "=== ok ==="
echo '{"apply":true,"stream":15,"stream_high":30,"openai":30,"openai_high":60,"container":"'"$container"'"}'
