---
name: tokenkey-stage0-local-deploy
description: >-
  Local Docker stack matching deploy/aws Stage 0 (Caddy + app + Postgres + Redis). Skill
  body pins this clone: REPO_ROOT under $HOME/Codes/token/tk/sub2api, new-api sibling parent,
  state under REPO_ROOT/.cache/tokenkey-stage0-local. Use for deploy/aws simulation on
  laptop, port 8088 smoke, AUTO_SETUP admin login, compose down + rm state.
---

# TokenKey：本地模拟 `deploy/aws` Stage 0（Compose + 验证 + 销毁）

与 `deploy/aws` 相同方式在笔记本起栈、验证后销毁。权威栈定义见 `deploy/aws/stage0/docker-compose.yml` 与 `deploy/aws/README.md`。

## 本项目路径约定（本仓库克隆）

以下值为 **TokenKey fork 当前常用落地布局**（`sub2api` 与 `new-api` 同级）；若你的目录不同，只改这三项即可。

```bash
export REPO_ROOT="$HOME/Codes/token/tk/sub2api"
export TOKENKEY_NEWAPI_PARENT="$HOME/Codes/token/tk"   # Dockerfile 构建上下文：内含 sub2api/ 与 new-api/
export TOKENKEY_STAGE0_LOCAL_ROOT="$HOME/Codes/token/tk/sub2api/.cache/tokenkey-stage0-local"
```

下文凡出现路径均使用 `"$HOME/Codes/..."`，在脚本与非交互 shell 中也会可靠展开。

- `REPO_ROOT`：本 git 仓库根（含 `backend/`、`deploy/aws/stage0/docker-compose.yml`）。
- `TOKENKEY_NEWAPI_PARENT`：`Dockerfile` 要求的 **父目录**（与 `CLAUDE.md`「Sibling dependency: New API」一致）。
- `TOKENKEY_STAGE0_LOCAL_ROOT`：本地 override、`.env`、Caddyfile、PG/Redis 数据落盘处；位于 `REPO_ROOT/.cache/...`，仓库已 `.gitignore` `.cache/`。

默认 **GHCR 镜像坐标** 使用 **`:latest`**，避免与 `VERSION` 文件不同步导致拉取失败；需要与某次发版逐位对照时再显式改为 `ghcr.io/youxuanxue/sub2api:<VERSION>` 或 `sha-…`：

```bash
export TOKENKEY_IMAGE_DEFAULT="ghcr.io/youxuanxue/sub2api:latest"
```

`latest` 会随 registry 更新而变，**不一定**等于当前工作区 `backend/cmd/server/VERSION`。发版/对账请用明确 tag。拉取 private 仓库前先 `docker login ghcr.io`。

## 与真机 EC2 的差异（刻意如此）

| 项 | EC2 Stage 0 | 本机模拟 |
| --- | --- | --- |
| 数据目录 | `/var/lib/tokenkey/...` | `${TOKENKEY_STAGE0_LOCAL_ROOT}/...`（默认在 `REPO_ROOT/.cache/...`） |
| Caddy | LE 证书 + `API_DOMAIN` | **纯 HTTP**（映射 `8088→80`），不调 Let's Encrypt |
| 对外端口 | 80/443 | 宿主机 `8088`（HTTP）、`8443`（预留，本地模板可不启 TLS） |
| 镜像 | GHCR `sub2api:<tag>` | **拉取 GHCR** 或 **本地 `docker build`**（见下文） |

## 前置条件

- 仓库根目录；已安装 Docker 与 `docker compose`。
- **private GHCR**：先 `docker login ghcr.io`（PAT 需 `read:packages`）。
- 走**本地构建**镜像时：Docker 构建上下文必须是 **sub2api 与 new-api 同级目录**，见根目录 `Dockerfile` 头注释与 `CLAUDE.md`（`replace github.com/QuantumNous/new-api => ../../new-api`）。

## 环境变量约定

上一节 **`REPO_ROOT` / `TOKENKEY_NEWAPI_PARENT` / `TOKENKEY_STAGE0_LOCAL_ROOT`** 为项目级默认值；Compose 还要求在运行命令的 shell 里 **export `TOKENKEY_STAGE0_LOCAL_ROOT`**，以便 override YAML 展开卷路径。

## 1) 准备目录

```bash
export REPO_ROOT="$HOME/Codes/token/tk/sub2api"
export TOKENKEY_STAGE0_LOCAL_ROOT="$HOME/Codes/token/tk/sub2api/.cache/tokenkey-stage0-local"
mkdir -p "${TOKENKEY_STAGE0_LOCAL_ROOT}"/{caddy,app,postgres,pgdump,redis}
```

## 2) 生成秘密并写入 `.env`

用新随机值（**勿复用示例或会话里出现过的十六进制串**）：

```bash
export REPO_ROOT="$HOME/Codes/token/tk/sub2api"
export TOKENKEY_STAGE0_LOCAL_ROOT="$HOME/Codes/token/tk/sub2api/.cache/tokenkey-stage0-local"
ADMIN_PASSWORD="$(openssl rand -hex 16)"
POSTGRES_PASSWORD="$(openssl rand -hex 12)"
JWT_SECRET="$(openssl rand -hex 32)"
TOTP_ENCRYPTION_KEY="$(openssl rand -hex 32)"
TOKENKEY_IMAGE="${TOKENKEY_IMAGE:-ghcr.io/youxuanxue/sub2api:latest}"   # 可对齐发版时改成 :X.Y.Z；本地 build 改为你的镜像 tag

cat > "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" <<EOF
API_DOMAIN=localhost
ACME_EMAIL=local@tokenkey.local
TZ=UTC
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
ADMIN_EMAIL=admin@tokenkey.local
ADMIN_PASSWORD=${ADMIN_PASSWORD}
JWT_SECRET=${JWT_SECRET}
JWT_EXPIRE_HOUR=1
TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}
EOF
```

执行后把 **`ADMIN_EMAIL` / `ADMIN_PASSWORD`** 记在安全处（或通过 `grep '^ADMIN_' "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env"` 查看）。

说明：

- Compose 已为 `tokenkey` 容器设置 `AUTO_SETUP=true`，首次启动会按 `ADMIN_EMAIL` / `ADMIN_PASSWORD` 创建管理员。
- `TOKENKEY_IMAGE`：默认拉 **`:latest`**；要与 `backend/cmd/server/VERSION` 或某次 CI 产物一致时改成该 tag；**未发布、仅测本机代码**时用本地 `docker build` 的 tag，并在 override 里 `pull_policy: never`（见下）。

## 3) `Caddyfile`（本地 HTTP）

写入 `"${TOKENKEY_STAGE0_LOCAL_ROOT}/caddy/Caddyfile"`。站点用 **`:80`**，避免浏览器/curl 带 `Host: localhost:8088` 时与 `http://localhost` 不匹配导致 503：

```caddyfile
{
	email local@tokenkey.local
}

:80 {
	encode zstd gzip

	@static {
		path /assets/*
		path /logo.png
		path /favicon.ico
	}
	header @static ?Cache-Control "public, max-age=31536000, immutable"

	reverse_proxy tokenkey:8080 {
		health_uri /health
		health_interval 30s
		health_timeout 10s
		header_up X-Real-IP {remote_host}
		header_up X-Forwarded-For {remote_host}
		header_up X-Forwarded-Proto {scheme}
		header_up X-Forwarded-Host {host}
		transport http {
			keepalive 120s
			keepalive_idle_conns 64
			compression off
		}
	}

	request_body {
		max_size 100MB
	}

	log {
		output stdout
		format json
		level INFO
	}
}
```

## 4) `docker-compose.override.yml`

写入 `"${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml"`。  
**在运行 `docker compose` 的同一 shell 里**先 `export TOKENKEY_STAGE0_LOCAL_ROOT`（与 §1 一致），否则 `${TOKENKEY_STAGE0_LOCAL_ROOT}` 不会展开。

```yaml
services:
  caddy:
    ports:
      - "8088:80"
      - "8443:443"
    volumes:
      - ${TOKENKEY_STAGE0_LOCAL_ROOT}/caddy/Caddyfile:/etc/caddy/Caddyfile:ro
      - ${TOKENKEY_STAGE0_LOCAL_ROOT}/caddy/data:/data
      - ${TOKENKEY_STAGE0_LOCAL_ROOT}/caddy/config:/config
  tokenkey:
    volumes:
      - ${TOKENKEY_STAGE0_LOCAL_ROOT}/app:/app/data
  postgres:
    volumes:
      - ${TOKENKEY_STAGE0_LOCAL_ROOT}/postgres:/var/lib/postgresql/data
      - ${TOKENKEY_STAGE0_LOCAL_ROOT}/pgdump:/pgdump
  redis:
    volumes:
      - ${TOKENKEY_STAGE0_LOCAL_ROOT}/redis:/data
```

**使用 GHCR 发布镜像（推荐 smoke）**：保持上面片段即可（`tokenkey` 继承主文件的 `pull_policy: always`）。

**使用本地构建镜像**：在 `tokenkey` 下增加：

```yaml
  tokenkey:
    pull_policy: never
```

并在 `.env` 里把 `TOKENKEY_IMAGE` 设为与 `docker build -t ...` 一致。

## 5) 校验配置、拉依赖、启动

```bash
export REPO_ROOT="$HOME/Codes/token/tk/sub2api"
export TOKENKEY_STAGE0_LOCAL_ROOT="$HOME/Codes/token/tk/sub2api/.cache/tokenkey-stage0-local"

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  config --quiet

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  pull caddy postgres redis

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  up -d
```

可选本地构建（父目录需含 `sub2api` + `new-api`）：

```bash
export TOKENKEY_NEWAPI_PARENT="$HOME/Codes/token/tk"
cd "${TOKENKEY_NEWAPI_PARENT}"
docker build -f sub2api/Dockerfile -t tokenkey-local:dev .
```

然后在 `.env` 中设置 `TOKENKEY_IMAGE=tokenkey-local:dev` 且 override 里 `pull_policy: never`。

## 6) 验证

**经 Caddy（与会话一致）**：

```bash
curl -sS -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:8088/health"
curl -sS -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:8088/api/v1/settings/public"
```

**绕开 Caddy**（排查 503）：

```bash
docker exec tokenkey wget -q -T 5 -O - http://localhost:8080/health
```

**管理员登录**（不要把真实密码贴进聊天记录；口令仅取自本机 `.env`）：

```bash
export TOKENKEY_STAGE0_LOCAL_ROOT="$HOME/Codes/token/tk/sub2api/.cache/tokenkey-stage0-local"
set -a
source "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env"
set +a
curl -sS -H 'Content-Type: application/json' \
  -d "$(printf '{"email":"%s","password":"%s"}' "${ADMIN_EMAIL}" "${ADMIN_PASSWORD}")" \
  "http://127.0.0.1:8088/api/v1/auth/login"
```

## 7) 销毁（停栈 + 删数据）

```bash
export REPO_ROOT="$HOME/Codes/token/tk/sub2api"
export TOKENKEY_STAGE0_LOCAL_ROOT="$HOME/Codes/token/tk/sub2api/.cache/tokenkey-stage0-local"

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  down

# 删除状态（按需；确认路径后再执行）
rm -rf "${TOKENKEY_STAGE0_LOCAL_ROOT}"
```

若曾 `docker build` 本地标签且不再使用：`docker rmi tokenkey-local:dev`（替换成实际 tag）。

## 故障速查

| 现象 | 处理 |
| --- | --- |
| `exec format error` | 镜像架构与主机不一致；Apple Silicon 拉取 **arm64** 或构建时指定 `--platform linux/arm64`。 |
| Caddy 503、应用 healthy | 看 `docker logs tokenkey-caddy`；用容器内 `wget` 直连 `tokenkey:8080`；检查 Caddyfile 站点是否为 `:80`。 |
| GHCR pull 403 | `docker login ghcr.io`；PAT 权限与镜像 owner。 |
| 构建极慢 | 与会话相同：可改用具已推送到 GHCR 的 tag，跳本地多阶段构建。 |

## 扩展阅读

- `deploy/aws/README.md` — Stage 0 总览与生产升级 SOP  
- `tokenkey-prod-release-deploy` — 打 tag、GHCR、`deploy-stage0` 真机发布
