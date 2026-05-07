---
name: tokenkey-stage0-local-deploy
description: >-
  Local Docker stack matching deploy/aws Stage 0 (Caddy + app + Postgres + Redis): export
  $HOME-pinned paths, write .cache override + .env + Caddyfile, docker compose config/pull/up
  with long timeouts for first pull/up, verify A curl via :8088, B bypass Caddy, optional C
  tk_post_deploy_smoke when API key exists; compose down keeps bind-mounted DB/Redis by default.
  Optional rm only for intentional reset. Mirrors prod skill posture (order, verify, teardown).
---

# TokenKey：本地模拟 `deploy/aws` Stage 0（Compose + 验证 + 销毁）

适用于本仓库（TokenKey fork of sub2api）。栈定义见 `deploy/aws/stage0/docker-compose.yml`、`deploy/aws/README.md`；发版与真机 Stage0 路径见 **`tokenkey-prod-release-deploy`**。根目录 **`CLAUDE.md`** 仍为纪律来源（ARM、`new-api` sibling、pnpm 等）。

## 一次性跑完（原则）

- **顺序做完（首次冷启动）**：`export` 路径变量 → §1 目录 → §2 `.env`（含密钥）→ §3 `Caddyfile` → §4 `docker-compose.override.yml` → **`docker compose … config --quiet`** → **`pull`** → **`up -d`** → **`docker compose ps` 等服务 healthy** → **本节「真实测试」A → B →（有网关 key 时再 C）** → 结束或 **§7a `down`**。**日常增量测**：见「日常复用」，往往只需 §5 **`up -d`**，停栈用 §7a，勿默认 `rm -rf`。**不要**在 `up -d` 刚返回就认为成功，须 **`docker compose ps`** 等服务 healthy。
- **`docker compose pull` / 首次拉起 tokenkey**：镜像层下载与数据库初始化可能各占 **数分钟量级**；Agent **须给 Shell 命令足够超时**（与 prod 技能的 `gh run watch` 同级思路），**避免**默认短时中断把 pull/up 误判为失败。
- **健康检查**：执行 **`docker compose ps`**，确认 **`tokenkey` / `postgres` / `redis` / `caddy`**（与 `deploy/aws/stage0/docker-compose.yml` 中 `container_name` 一致）状态与健康就绪；`tokenkey` 依赖 postgres/redis healthy，首轮 **30–90s** 属常见。必要时 **`docker logs tokenkey`** / **`docker logs tokenkey-postgres`**。
- **数据要「跨多次本地测试」保留**：override 把 PG/Redis/app 绑到宿主机 **`${TOKENKEY_STAGE0_LOCAL_ROOT}/{postgres,redis,app}`**。**`docker compose … down`（不带 `-v`）只删容器，不删这些目录**，账号、订阅、网关 key 等会一直在。**不要**把每次跑本 skill 都当成要执行 §7 的 **`rm -rf`**——那是**有意清盘**才做。日常循环：§5 **`up -d`** ↔ §7a **`down`**，固定同一个 `TOKENKEY_STAGE0_LOCAL_ROOT`。
- **`rm -rf "${TOKENKEY_STAGE0_LOCAL_ROOT}"`**：仅 **§7b 有意清空**时用，且 **确认路径**（默认 `.cache/tokenkey-stage0-local`）；勿对错误父目录执行。
- **私有 GHCR**：`docker compose pull` tokenkey 镜像前应先在本机 **`docker login ghcr.io`**；Agent 若在沙箱里无法访问 daemon 或未继承登录态，需在可访问 Docker 的环境里执行 compose。
- **`tk_post_deploy_smoke.sh`（与 prod 同款）**：见下文 **C**；需要 **可用的用户侧网关 API Key**（`POST_DEPLOY_SMOKE_API_KEY` 等）。纯 **AUTO_SETUP** 新栈往往还没有 key——此时 **只做 A+B 或再加管理员登录**即可，不要为了跑 C 而停下向人要 prod key。

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
- `TOKENKEY_STAGE0_LOCAL_ROOT`：本地 override、`.env`、Caddyfile、PG/Redis 数据落盘处；位于 `REPO_ROOT/.cache/...`，仓库根 `.gitignore` 已忽略 `.cache/`。

为方便「单节复制粘贴」，**§2 / §5 / §7 / 部分自检**可能在代码块里重复写出 `export`。若你已在 **`本项目路径约定`** 或 **§1** 导出过 `REPO_ROOT` 与 `TOKENKEY_STAGE0_LOCAL_ROOT`，可跳过这些重复行。**§4 写 override 时仍须在当前 shell `export TOKENKEY_STAGE0_LOCAL_ROOT`**，否则 `${TOKENKEY_STAGE0_LOCAL_ROOT}` 在 YAML 挂载路径中会为空。

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

- Docker 与本机 **`docker compose`** 可用（Agent 须有权限与 daemon 通信）。
- **`curl`**：`docker exec tokenkey wget`（镜像内）；宿主机也需 `curl` 做 `:8088` 探活。**可选完整烟测 C** 时需 **`jq` + `python3`**（`scripts/tk_post_deploy_smoke.sh` 依赖）。
- **private GHCR**：先 `docker login ghcr.io`（PAT 需 `read:packages`），再拉 `ghcr.io/youxuanxue/sub2api:…`。
- **本地镜像构建**：上下文必须是 **`TOKENKEY_NEWAPI_PARENT`**（`sub2api` + `new-api` 同级），见根目录 `Dockerfile` 头注释与 `CLAUDE.md`（`replace … => ../../new-api`）。

## 环境变量约定

- **`REPO_ROOT` / `TOKENKEY_NEWAPI_PARENT` / `TOKENKEY_STAGE0_LOCAL_ROOT`**：见上节；凡运行 **`docker compose`** 的同一会话里都 **`export TOKENKEY_STAGE0_LOCAL_ROOT`**，否则 override 里 **`${TOKENKEY_STAGE0_LOCAL_ROOT}`** 卷路径为空或错位。

## 日常复用（同一套本地数据）

固定 **`TOKENKEY_STAGE0_LOCAL_ROOT`**（不要每次换一个目录），则：

- **第二次及以后**：可 **跳过 §1–§4**（目录、`.env`、Caddyfile、override 已就绪），直接 **§5** `config` →（镜像有变再 `pull`）→ **`up -d`**。
- **`.env` 与已有 Postgres 数据必须一致**：`POSTGRES_PASSWORD`（以及库名/用户）在 **首次 init** 时写入数据目录；若你 **重新跑 §2 随机生成新密码** 但 **没有删 `postgres/`**，新 `.env` 与旧库 **不匹配**，Postgres 会认证失败。**想保留数据** → 保留原 `.env`，只改 `TOKENKEY_IMAGE` 等非 PG 字段；**想换一套库** → 走 §7b 删掉 `postgres/`（或整目录）后再 §2。
- **`AUTO_SETUP`**：库已存在时通常不会重复造管理员；继续用原 **`ADMIN_EMAIL` / `ADMIN_PASSWORD`**（见 §2 当时写入的 `.env`）。

## 1) 准备目录

```bash
export REPO_ROOT="$HOME/Codes/token/tk/sub2api"
export TOKENKEY_STAGE0_LOCAL_ROOT="$HOME/Codes/token/tk/sub2api/.cache/tokenkey-stage0-local"
mkdir -p "${TOKENKEY_STAGE0_LOCAL_ROOT}"/{caddy,app,postgres,pgdump,redis}
```

## 2) 生成秘密并写入 `.env`

**何时跑本节**：**首次建站**或 **§7b 清空数据后**。**若 Postgres 数据目录已存在且要保留账号/业务数据**：**不要**整张覆盖 `.env` 或至少 **勿改 `POSTGRES_*`**（否则见「日常复用」与故障速查「password authentication failed」）。

用新随机值（**勿复用示例或会话里出现过的十六进制串**）：

```bash
# 若未导出：见上文「本项目路径约定」或 §1
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
# 若未导出 REPO_ROOT / TOKENKEY_STAGE0_LOCAL_ROOT：见「本项目路径约定」或 §1

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  config --quiet

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  pull

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  up -d
```

**本地镜像、`pull_policy: never`**：`docker compose … pull` 可能因 **tokenkey** 仅存在本机 tag 而失败（仍会去 registry 解析）。可改为只拉基础镜像：  
**`docker compose … pull caddy postgres redis`**，再 **`up -d`**（`tokenkey` 使用本地 `TOKENKEY_IMAGE`）。

可选本地构建（父目录需含 `sub2api` + `new-api`）：

```bash
export TOKENKEY_NEWAPI_PARENT="$HOME/Codes/token/tk"
cd "${TOKENKEY_NEWAPI_PARENT}"
docker build -f sub2api/Dockerfile -t tokenkey-local:dev .
```

然后在 `.env` 中设置 `TOKENKEY_IMAGE=tokenkey-local:dev` 且 override 里 `pull_policy: never`。

## 6) 真实测试（会话里尽量做完）

`compose up -d` 成功只代表容器调度成功；Agent **仍需**按下面顺序自检（对齐 prod 技能的 **部署后还须本地验收**，只是探针改为本机 `:8088`）。

### A — 经 Caddy 快速探活（无需网关 API Key）

```bash
curl -sS -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:8088/health"
curl -sS -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:8088/api/v1/settings/public"
```

期望均为 **HTTP 200**。若要对 public 接口更严：`curl -sS "http://127.0.0.1:8088/api/v1/settings/public" | jq -e '.code == 0' >/dev/null`（需本机 **`jq`**）。

### B — 绕开 Caddy（应用本体 / 503 排查）

确认 tokenkey 容器内直连 **8080** 正常：

```bash
docker exec tokenkey wget -q -T 5 -O - http://localhost:8080/health
```

### C — 本地完整网关烟测（可选，与 prod 同款脚本）

与 **`tokenkey-prod-release-deploy`** 中 **C** 使用同一 **`scripts/tk_post_deploy_smoke.sh`**，仅 **`TOKENKEY_BASE_URL`** 指向本机反代：

```bash
cd “${REPO_ROOT}” # 须在含 scripts/ 的仓库根；未导出 REPO_ROOT 时见「本项目路径约定」
export TOKENKEY_BASE_URL=http://127.0.0.1:8088    # 或 TK_GATEWAY_URL（脚本两个都识别）
# 主 key：POST_DEPLOY_SMOKE_API_KEY → ANTHROPIC_AUTH_TOKEN → TK_TOKEN → TOKENKEY_API_KEY
# 可选 Gemini 探针：POST_DEPLOY_SMOKE_GEMINI_API_KEY=sk-...
# 可选 OpenAI OAuth 探针：POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY=sk-...
bash scripts/tk_post_deploy_smoke.sh
```

**前提**：须已有 **可用的用户侧网关 API Key**（新 AUTO_SETUP 栈通常没有——先在管理后台创建订阅用户与 key，或使用你专用于本地的测试 key）。**不得**打印完整 key；脚本只输出 `key_hint`。若缺 key：**不要卡住会话**，验收 **A+B**（及下方管理员登录）即可。

**烟测 key**：与 prod skill § C 要求完全一致——`POST_DEPLOY_SMOKE_API_KEY`、`POST_DEPLOY_SMOKE_GEMINI_API_KEY`、`POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY` 三个均须导出，任一缺失不得视为验收通过。

**结构化验收要求**：与 prod skill § C 完全一致，以该节为准。唯一差异是 `TOKENKEY_BASE_URL=http://127.0.0.1:8088`（本地反代端口）。若有多个分组/key，按 key 分别记录 `key_hint`、group platform、`account_id/platform/model`；不要把一个 key 的通过误当成全部通过。

本地 Caddy 开启压缩时，`tk_post_deploy_smoke.sh` 可能只输出启动行后等待连接关闭。若脚本卡住，不要降低验收标准：停止脚本后用同一 key 重跑等价请求，并显式加 `Accept-Encoding: identity`，仍按上面的结构化要求判定。

### 管理员会话（常与 A/B 一起做，≠ C 的网关 key）

用于验证 **AUTO_SETUP** 账密（**勿把密码粘贴到聊天**；只从本机 `.env` 引用）：

```bash
# TOKENKEY_STAGE0_LOCAL_ROOT 未导出时：见「本项目路径约定」或 §1
set -a
source "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env"
set +a
curl -sS -H 'Content-Type: application/json' \
  -d "$(printf '{"email":"%s","password":"%s"}' "${ADMIN_EMAIL}" "${ADMIN_PASSWORD}")" \
  "http://127.0.0.1:8088/api/v1/auth/login"
```

## 完成后：当前代码与上一 tag 的变更摘要

本地栈验证通过后（A+B，或 A+B+C），运行以下命令，向用户呈现"当前工作区相对上一个正式发版的差异"。目的是在本地测试阶段就识别高风险变更，不要等到发版才发现。

```bash
LAST_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | head -1)
BASE="${LAST_TAG:-origin/main}"
echo "base: ${BASE}  head: $(git rev-parse --short HEAD)"

# 尚未发版的提交（排除 VERSION bump）
git log "${BASE}..HEAD" --oneline --no-merges \
  | grep -v 'chore: bump VERSION' | grep -v '\[skip ci\]'

# 变更文件统计
git diff --stat "${BASE}..HEAD" -- backend/ frontend/src/ | tail -10

# sentinel 文件有无改动
git diff --name-only "${BASE}..HEAD" -- scripts/ | grep 'sentinels' || true
```

基于输出，向用户呈现（无变更则跳过对应行）：

**当前代码领先 `${LAST_TAG}` N 个提交，运行于本地栈 `:8088`**

- **feat / fix 提交**：列出关键条目及影响模块（gateway / scheduler / frontend / sentinel）
- **高风险路径**（根据 diff 判断）：
  - Gemini 路径改动 → 本地 C 节有无跑 Gemini 探针；tool-schema 清理是否已验证
  - OpenAI-compat / Responses 改动 → chat completions shape 与 reasoning_tokens 是否正常
  - pricing / model-list → `/v1/models` 返回与预期是否一致
  - frontend 改动 → 浏览器打开 `http://127.0.0.1:8088` 手动验证关键页面
  - sentinel 新增 → 列出文件名，后续 upstream merge 时 CI 会联检
- **尚未覆盖的验证**：若本次本地测试未跑 C 节（缺 API key），建议在发版前用 prod smoke 补全

## 7) 停栈 / 重置

### 7a) 停栈，**保留** Postgres / Redis / app 数据（默认）

释放端口与容器，**下次 `up -d` 数据仍在**：

```bash
# 若未导出：见上文

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  down
```

**不要**在本机日常测试末尾自动加 **`rm -rf`**。本栈使用 **bind mount**，**`down` 默认不会删** `${TOKENKEY_STAGE0_LOCAL_ROOT}/postgres` 等目录（与「具名卷 + `down -v`」不同）。

### 7b) **有意清空**（删库 / 回到「全新栈」）

确认 **`TOKENKEY_STAGE0_LOCAL_ROOT`** 指向正确后：

1. 先按 **7a** `down`（避免删目录时容器仍占用文件）。
2. 再删状态目录，例如：

```bash
rm -rf "${TOKENKEY_STAGE0_LOCAL_ROOT}"
```

之后从 **§1** 起重建；**§2** 会生成新密钥与 **新** `POSTGRES_PASSWORD`，与空数据目录一致。

若曾 `docker build` 本地标签且不再使用：`docker rmi tokenkey-local:dev`（替换成实际 tag）。

## 收尾备忘

- **`${TOKENKEY_STAGE0_LOCAL_ROOT}`**（默认 `REPO_ROOT/.cache/tokenkey-stage0-local`）：勿将 `.env`、`docker-compose.override.yml`、PG/Redis 数据卷产物提交 git；路径在 `.gitignore` 的 `.cache/` 之下。**勿**把该目录当作仓库制品归档进 git。
- 与 **`tokenkey-prod-release-deploy`** 不同：本地栈**无** `release.yml` 回写 **`VERSION`/sync-version`，流程末尾不必为这个栈再 **`git fetch`/`pull`**（除非仓库本身有其他变更）。

## 故障速查

| 现象 | 处理 |
| --- | --- |
| Agent / 工具 **`docker compose pull` 或 `up` 超时** | 拉长超时或与用户说明网络慢；可拆成先 `pull` 再 `up`；未完成不要当失败退出。 |
| `docker compose ps` 长期 non-healthy | 看 **`docker logs tokenkey`** / **`docker logs tokenkey-postgres`** / **`docker logs tokenkey-redis`**；等资源初始化或修 `.env` 密钥。 |
| **Postgres 起不来 / `password authentication failed`** | 多为 **§2 重写了 `POSTGRES_PASSWORD`** 但 **`postgres/` 数据目录仍是旧库**：恢复与旧库一致的 `.env`，或 **§7b** 删 `postgres/`（或整 `${TOKENKEY_STAGE0_LOCAL_ROOT}`）后重建。 |
| `exec format error` | 镜像架构与主机不一致；Apple Silicon 拉取 **arm64** 或 **`docker build --platform linux/arm64`**。 |
| Caddy **503**、应用容器已起 | **`docker logs tokenkey-caddy`**；本节 **B** 直连 `localhost:8080`；核对 Caddyfile 站点是否为 **`:80`**。 |
| GHCR pull **403** | `docker login ghcr.io`；PAT 权限与镜像 owner；Agent 若在沙箱无登录态须在用户 shell 登录。 |
| **`tk_post_deploy_smoke.sh` 报缺 KEY** | 正常：新栈尚无用户 API key。**只做 A+B** 或先做管理员登录，再在后台发证后重跑 **C**。 |
| **全量 `pull` 在 tokenkey 上失败（仅本地 tag）** | `.env` 用本地 build 且 override `pull_policy: never`：按上文改为 **`pull caddy postgres redis`** 后 **`up -d`**。 |

## 扩展阅读

- [tokenkey-prod-release-deploy](../tokenkey-prod-release-deploy/SKILL.md) — main / tag / `release.yml` / `deploy-stage0` / prod 烟测
- `deploy/aws/README.md` — Stage 0 总览与 EC2 升级 SOP  
- `.github/workflows/deploy-stage0.yml` — 真机 `tag` 形参（无 `v` 前缀）  
- `scripts/tk_post_deploy_smoke.sh` — 与 prod **C** 相同的网关烟测脚本，改 `TOKENKEY_BASE_URL` 即可打本地 `:8088`  
- `scripts/release-tag.sh` — 仅 prod 打 tag；本地默认不调用
