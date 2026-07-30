#!/usr/bin/env bash
# Local UI preview on http://127.0.0.1:8088 (bare binary + host Postgres/Redis).
# Usage:
#   bash scripts/local-preview-8088.sh --foreground # keep this terminal open (recommended)
#   bash scripts/local-preview-8088.sh --rebuild    # force rebuild embed binary
#   bash scripts/local-preview-8088.sh --status
#   bash scripts/local-preview-8088.sh --stop
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="${TOKENKEY_STAGE0_LOCAL_ROOT:-$REPO_ROOT/.cache/tokenkey-stage0-local}/.env"
BIN="/tmp/tokenkey-local-server"
LOG="/tmp/tokenkey-local-server.log"
PID_FILE="${TOKENKEY_STAGE0_LOCAL_ROOT:-$REPO_ROOT/.cache/tokenkey-stage0-local}/local-preview.pid"
DIST_INDEX="$REPO_ROOT/backend/internal/web/dist/index.html"

cmd="${1:-start}"

stop_preview() {
  docker stop tokenkey-caddy tokenkey 2>/dev/null || true
  if [[ -f "$PID_FILE" ]]; then
    local old_pid
    old_pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [[ -n "$old_pid" ]] && kill -0 "$old_pid" 2>/dev/null; then
      kill "$old_pid" 2>/dev/null || true
      sleep 1
    fi
    rm -f "$PID_FILE"
  fi
  pkill -f "$BIN" 2>/dev/null || true
}

status_preview() {
  if [[ -f "$PID_FILE" ]]; then
    local pid
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      echo "running pid=$pid"
      curl -sf -o /dev/null "http://127.0.0.1:8088/health" && echo "health: OK" || echo "health: FAIL"
      return 0
    fi
  fi
  if pgrep -fl "$BIN" >/dev/null 2>&1; then
    pgrep -fl "$BIN"
    return 0
  fi
  echo "not running"
  return 1
}

needs_rebuild() {
  [[ "${FORCE_REBUILD:-0}" -eq 1 ]] && return 0
  [[ ! -x "$BIN" ]] && return 0
  [[ ! -f "$DIST_INDEX" ]] && return 0
  [[ "$DIST_INDEX" -nt "$BIN" ]] && return 0
  return 1
}

case "$cmd" in
  --stop|stop)
    stop_preview
    echo "Stopped."
    exit 0
    ;;
  --status|status)
    status_preview
    exit $?
    ;;
  --foreground|foreground)
    MODE=foreground
    FORCE_REBUILD=0
    ;;
  --rebuild)
    MODE=background
    FORCE_REBUILD=1
    ;;
  start|"")
    MODE=background
    FORCE_REBUILD=0
    ;;
  *)
    echo "Usage: $0 [--foreground|--rebuild|--status|--stop]" >&2
    exit 1
    ;;
esac

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing $ENV_FILE — run: bash deploy/aws/stage0/local-bootstrap.sh" >&2
  exit 1
fi

# Ensure host deps
if ! docker ps --format '{{.Names}}' | grep -q '^tokenkey-postgres$'; then
  echo "tokenkey-postgres not running. Start Stage0 DB first:" >&2
  echo "  bash deploy/aws/stage0/local-bootstrap.sh && docker-compose … up -d postgres" >&2
  exit 1
fi
if ! redis-cli -h 127.0.0.1 -p 6379 ping >/dev/null 2>&1; then
  echo "Redis not reachable at 127.0.0.1:6379 (need sub2api-redis or tokenkey-redis)." >&2
  exit 1
fi

stop_preview

if needs_rebuild; then
  echo "Building frontend + backend (embed)…"
  (cd "$REPO_ROOT/frontend" && pnpm build)
  (cd "$REPO_ROOT/backend" && go build -tags embed -o "$BIN" ./cmd/server)
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a
export DATABASE_HOST=127.0.0.1 DATABASE_PORT=5433
export DATABASE_USER="$POSTGRES_USER" DATABASE_PASSWORD="$POSTGRES_PASSWORD" DATABASE_DBNAME="$POSTGRES_DB"
export REDIS_HOST=127.0.0.1 REDIS_PORT=6379
export SERVER_HOST=127.0.0.1 SERVER_PORT=8088 AUTO_SETUP=true

mkdir -p "$(dirname "$PID_FILE")"
cd "$REPO_ROOT/backend"

if [[ "${MODE:-background}" == "foreground" ]]; then
  echo "Foreground mode — keep this terminal open. Ctrl+C to stop."
  echo "Open http://127.0.0.1:8088/models after ~15s"
  echo "$$" >"$PID_FILE"
  trap 'rm -f "$PID_FILE"' EXIT INT TERM
  exec "$BIN"
fi

nohup "$BIN" >>"$LOG" 2>&1 &
pid=$!
disown -h 2>/dev/null || true
echo "$pid" >"$PID_FILE"
echo "Started pid=$pid (log: $LOG)"
echo "Tip: if the site dies after you close Terminal, use: $0 --foreground"

echo "Waiting for http://127.0.0.1:8088/health (cold start ~15–20s)…"
ok=0
for _ in $(seq 1 45); do
  if curl -sf -o /dev/null "http://127.0.0.1:8088/health"; then
    ok=1
    break
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "Process exited during startup. Log:" >&2
    tail -20 "$LOG" >&2
    exit 1
  fi
  sleep 1
done

if [[ "$ok" -ne 1 ]]; then
  echo "Timed out. tail -30 $LOG" >&2
  exit 1
fi

sleep 2
if ! kill -0 "$pid" 2>/dev/null; then
  echo "Process died right after health check. tail -30 $LOG" >&2
  tail -30 "$LOG" >&2
  exit 1
fi

echo "OK — open http://127.0.0.1:8088/models"
echo "Status: bash scripts/local-preview-8088.sh --status"
echo "Stop:   bash scripts/local-preview-8088.sh --stop"
