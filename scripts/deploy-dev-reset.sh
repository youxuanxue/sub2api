#!/usr/bin/env bash
# 清空 deploy 本地数据目录并以 docker-compose.dev.yml 重新构建启动。
# Docker 构建上下文为「包含 sub2api 与 new-api 的父目录」：请在本机保证存在
#   /path/to/parent/sub2api   （或指向仓库根的符号链接 sub2api）
#   /path/to/parent/new-api   （与 backend/go.mod replace 一致）
#
# 用法（在仓库根目录）：
#   POSTGRES_PASSWORD='your_pg' ADMIN_EMAIL='a@b.c' ADMIN_PASSWORD='secret' bash scripts/deploy-dev-reset.sh
#
# 未设置 ADMIN_PASSWORD 时首次启动会随机生成并在容器日志中打印一次（见 deploy/README.md）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY="$ROOT/deploy"

# Docker build context: directory that contains `sub2api/` + `new-api/` subdirs (Dockerfile COPY paths).
# Auto-pick /tk when present (symlink layout used in some sandboxes); else repo parent's parent.
if [[ -z "${BUILD_CONTEXT:-}" ]]; then
  if [[ -d /tk/sub2api && -d /tk/new-api ]]; then
    BUILD_CONTEXT=/tk
  else
    BUILD_CONTEXT="$(cd "$DEPLOY/../.." && pwd)"
  fi
fi
export BUILD_CONTEXT

POSTGRES_PASSWORD="${POSTGRES_PASSWORD:?Set POSTGRES_PASSWORD (PostgreSQL user password)}"
export POSTGRES_PASSWORD
export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@sub2api.local}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
export BIND_HOST="${BIND_HOST:-0.0.0.0}"
export SERVER_PORT="${SERVER_PORT:-8080}"
# Normal installs use DATABASE_HOST=postgres / REDIS_HOST=redis (Docker DNS).
# On hosts where in-container DNS fails, set DATABASE_HOST=REDIS_HOST=host.docker.internal (requires postgres/redis ports published on 0.0.0.0 — see docker-compose.dev.yml defaults).

COMPOSE=(docker compose -f "$DEPLOY/docker-compose.dev.yml")
echo "==> Docker BUILD_CONTEXT=$BUILD_CONTEXT"

echo "==> Stopping stack (if any)"
"${COMPOSE[@]}" down 2>/dev/null || true

echo "==> Removing local data dirs under deploy/"
rm -rf "$DEPLOY/data" "$DEPLOY/postgres_data" "$DEPLOY/redis_data"
mkdir -p "$DEPLOY/data"

echo "==> Building and starting (this may take several minutes)"
"${COMPOSE[@]}" --project-directory "$DEPLOY" up --build -d

echo ""
echo "Stack started. Admin UI (embedded): http://${BIND_HOST:-127.0.0.1}:${SERVER_PORT:-8080}/"
echo "  ADMIN_EMAIL=$ADMIN_EMAIL"
if [[ -n "${ADMIN_PASSWORD}" ]]; then
  echo "  ADMIN_PASSWORD=(from env, fixed)"
else
  echo "  ADMIN_PASSWORD=(auto-generated on first install — run: docker compose -f deploy/docker-compose.dev.yml logs sub2api | grep -i password)"
fi
