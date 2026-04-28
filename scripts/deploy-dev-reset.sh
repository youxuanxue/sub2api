#!/usr/bin/env bash
# Wipe deploy-local volumes and bring stack back with docker-compose.dev.yml (see deploy/README.md).
# Requires sibling ../new-api next to repo root for Docker build (Dockerfile header).
#
#   POSTGRES_PASSWORD='…' ADMIN_EMAIL='…' ADMIN_PASSWORD='…' bash scripts/deploy-dev-reset.sh
#
# If ADMIN_PASSWORD is empty, first-boot prints a one-time password in container logs.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY="$ROOT/deploy"

POSTGRES_PASSWORD="${POSTGRES_PASSWORD:?Set POSTGRES_PASSWORD}"
export POSTGRES_PASSWORD
export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@sub2api.local}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"

COMPOSE=(docker compose -f docker-compose.dev.yml)

echo "==> Stopping stack (if any)"
(cd "$DEPLOY" && "${COMPOSE[@]}" down) 2>/dev/null || true

echo "==> Removing deploy/data deploy/postgres_data deploy/redis_data"
rm -rf "$DEPLOY/data" "$DEPLOY/postgres_data" "$DEPLOY/redis_data"
mkdir -p "$DEPLOY/data"

echo "==> docker compose up --build -d"
(cd "$DEPLOY" && "${COMPOSE[@]}" up --build -d)

echo ""
echo "Open: http://${BIND_HOST:-127.0.0.1}:${SERVER_PORT:-8080}/"
echo "  ADMIN_EMAIL=$ADMIN_EMAIL"
if [[ -n "${ADMIN_PASSWORD}" ]]; then
  echo "  (password from ADMIN_PASSWORD env)"
else
  echo "  (check generated password: cd deploy && docker compose -f docker-compose.dev.yml logs sub2api | grep -i password)"
fi
