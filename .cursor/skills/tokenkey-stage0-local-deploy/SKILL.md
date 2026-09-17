---
name: tokenkey-stage0-local-deploy
description: >-
  Run the local Docker Stage0 stack for TokenKey. Use for Caddy/app/Postgres/Redis local deploy, compose setup, smoke verification, teardown, or matching deploy/aws behavior on a workstation.
---

# TokenKey：本地模拟 `deploy/aws` Stage 0（Compose + 验证 + 销毁）

适用于本仓库（TokenKey fork of sub2api）。栈定义见 `deploy/aws/stage0/docker-compose.yml`、`deploy/aws/README.md`；发版与真机 Stage0 路径见 **`tokenkey-stage0-release-rollout`**。根目录 **`CLAUDE.md`** 仍为纪律来源（ARM、`new-api` sibling、pnpm 等）。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。

| 步骤 | 类型 | 承载 |
|---|---|---|
| 写 .cache 目录 / .env / Caddyfile / docker-compose.override.yml（幂等） | 机械 | `bash deploy/aws/stage0/local-bootstrap.sh [--reset \| --dry-run]` |
| docker compose config / pull / up -d | 机械 | docker compose 标准命令链（见 §5） |
| A 经 Caddy 探活 / B 绕开 Caddy / C 完整网关烟测 | 机械 | curl / `ops/stage0/post_deploy_smoke.sh` |
| 本地代码与上一 tag 变更摘要 | 机械 | `bash scripts/release-rollout-summary.sh --mode local` |
| 日常复用 vs 清盘判断（`down` vs `--reset`） | 判断 | prompt（保留 DB 数据的爆炸半径） |
| 故障速查（GHCR 403 / postgres auth fail / exec format error） | 判断 | prompt（诊断分支） |
| Caddy 8088 端口 / `pull_policy: never` 等本地与真机差异 | 判断 | prompt（设计判断） |

## 一次性跑完（原则）

首次：bootstrap → compose config/pull/up → 等服务 healthy → §6 验证。`up -d` 返回不代表成功；首次拉镜像/初始化允许数分钟。日常保留同一数据目录与 `.env`，只 `up/down`，不默认 reset/删数据。

## 本项目路径约定（本仓库克隆）

只有目录布局、自建镜像或明确 tag 对齐时读取路径配置。默认用当前仓库与 sibling new-api；`latest` 不代表当前源码。 见 [操作细则](references/paths.md)。

## 与真机 EC2 的差异（刻意如此）

| 项 | EC2 Stage 0 | 本机模拟 |
| --- | --- | --- |
| 数据目录 | `/var/lib/tokenkey/...` | `${TOKENKEY_STAGE0_LOCAL_ROOT}/...`（默认在 `REPO_ROOT/.cache/...`） |
| Caddy | LE 证书 + `API_DOMAIN` | **纯 HTTP**（映射 `8088→80`），不调 Let's Encrypt |
| 对外端口 | 80/443 | 宿主机 `8088`（HTTP）、`8443`（预留，本地模板可不启 TLS） |
| 镜像 | GHCR `sub2api:<tag>` | **拉取 GHCR** 或 **本地 `docker build`**（见下文） |

## 前置条件

- Docker 与本机 **`docker compose`** 可用（Agent 须有权限与 daemon 通信）。
- **`curl`**：`docker exec tokenkey wget`（镜像内）；宿主机也需 `curl` 做 `:8088` 探活。**可选完整烟测 C** 时需 **`jq` + `python3`**（`ops/stage0/post_deploy_smoke.sh` 依赖）。
- **private GHCR**：先 `docker login ghcr.io`（PAT 需 `read:packages`），再拉 `ghcr.io/youxuanxue/sub2api:…`。
- **本地镜像构建**：上下文必须是 **`TOKENKEY_NEWAPI_PARENT`**（`sub2api` + `new-api` 同级），见根目录 `Dockerfile` 头注释与 `CLAUDE.md`（`replace … => ../../new-api`）。

## 环境变量约定

bootstrap 子进程不会给当前 shell 导出变量。运行 bootstrap/compose 前，在同一 shell 设置（与脚本默认值一致）：

```bash
export REPO_ROOT="$(git rev-parse --show-toplevel)"
export TOKENKEY_STAGE0_LOCAL_ROOT="${TOKENKEY_STAGE0_LOCAL_ROOT:-$REPO_ROOT/.cache/tokenkey-stage0-local}"
export TOKENKEY_NEWAPI_PARENT="${TOKENKEY_NEWAPI_PARENT:-$(dirname "$REPO_ROOT")}"
```

已有本地栈沿用原 `TOKENKEY_STAGE0_LOCAL_ROOT`；不要无意改到空目录。

## 日常复用（同一套本地数据）

固定 **`TOKENKEY_STAGE0_LOCAL_ROOT`**（不要每次换一个目录），则：

- **第二次及以后**：可 **跳过 §1–§4**（目录、`.env`、Caddyfile、override 已就绪），直接 **§5** `config` →（镜像有变再 `pull`）→ **`up -d`**。
- **`.env` 与已有 Postgres 数据必须一致**：`POSTGRES_PASSWORD`（以及库名/用户）在 **首次 init** 时写入数据目录；若你 **手工重新生成密码** 但 **没有删 `postgres/`**，新 `.env` 与旧库 **不匹配**，Postgres 会认证失败。**想保留数据** → 保留原 `.env`，只改 `TOKENKEY_IMAGE` 等非 PG 字段；**想换一套库** → 走 §7b 删掉 `postgres/`（或整目录）后再跑 bootstrap。
- **`AUTO_SETUP`**：库已存在时通常不会重复造管理员；继续用原 **`ADMIN_EMAIL` / `ADMIN_PASSWORD`**（保存在 `.env`）。

## 1) 准备目录 + 2) 生成密钥并写入 `.env` + 3) Caddyfile + 4) override（机械化）

§1-§4 已合并为单条幂等脚本 `deploy/aws/stage0/local-bootstrap.sh`：

```bash
# 首次或日常 bootstrap（已存在 .env 会自动保留 —— 防止覆盖 POSTGRES_PASSWORD）
bash deploy/aws/stage0/local-bootstrap.sh

# 看将要做什么（不写盘）
bash deploy/aws/stage0/local-bootstrap.sh --dry-run

# 想从「空栈」开始（删 DB / Redis / app 数据 + 重写 .env，等价老 §7b）
bash deploy/aws/stage0/local-bootstrap.sh --reset
```

行为契约（脚本顶部 docstring 是 ground truth）：

- **幂等**：`.env` 已存在 ⇒ 不重写（保留 `POSTGRES_PASSWORD` 与已有 DB 数据兼容）；Caddyfile / override 每次重写（无密钥）。
- **`--reset` 安全栏**：拒绝在 `$HOME` 外执行（需 `--i-know-what-im-doing` 强制）。
- **不打印任何密钥**：密码只落到 `.env`（chmod 600）。
- env 入口：`REPO_ROOT` / `TOKENKEY_STAGE0_LOCAL_ROOT` / `TOKENKEY_NEWAPI_PARENT` / `TOKENKEY_IMAGE` 均可显式覆盖。

## 2) 生成秘密并写入 `.env`（由 bootstrap 生成）

已由 `deploy/aws/stage0/local-bootstrap.sh` 拥有；不再维护第二份写 `.env` 的 shell 模板。已有 PostgreSQL 数据时保留原凭证，禁止重随机密码。

## 3) `Caddyfile`（本地 HTTP）

由同一 bootstrap 生成本地 HTTP `:80` 站点；修改模板到脚本 owner。

## 4) `docker-compose.override.yml`

由同一 bootstrap 生成 override；修改模板到脚本 owner。本地 build 的路径/镜像参数见 paths 细则。

## 5) 校验配置、拉依赖、启动

启动或更新本地栈时读命令。必须先 config 校验，再 pull/up，并等待 healthy。 见 [操作细则](references/compose.md)。

## 6) 真实测试（会话里尽量做完）

启动后必须按 A（经 Caddy）→ B（直达 app）验证；有全能 smoke key 再做 C，否则明确 C 未验证。这些 API smoke 不等于真实 UI e2e。 见 [操作细则](references/verification.md)。

## 完成后：当前代码与上一 tag 的变更摘要（机械化）

需要比较本地代码与上一 tag 时调用 `bash scripts/release-rollout-summary.sh --mode local`；输出口径见细则。 见 [操作细则](references/summary.md)。

## 7) 停栈 / 重置

需要停栈时读 §7a，默认 down 保留数据；用户明确要求清盘时才读 §7b，核对精确路径后执行。 见 [操作细则](references/teardown.md)。

## 收尾备忘

- **`${TOKENKEY_STAGE0_LOCAL_ROOT}`**（默认 `REPO_ROOT/.cache/tokenkey-stage0-local`）：勿将 `.env`、`docker-compose.override.yml`、PG/Redis 数据卷产物提交 git；路径在 `.gitignore` 的 `.cache/` 之下。**勿**把该目录当作仓库制品归档进 git。
- 与 **`tokenkey-stage0-release-rollout`** 不同：本地栈**无** `release.yml` 回写 **`VERSION`/sync-version`，流程末尾不必为这个栈再 **`git fetch`/`pull`**（除非仓库本身有其他变更）。

## 故障速查

出现 GHCR、数据库认证、架构或 Caddy 故障时查询。 见 [操作细则](references/troubleshooting.md)。

## 扩展阅读

- [tokenkey-stage0-release-rollout](../tokenkey-stage0-release-rollout/SKILL.md) — main / tag / `release.yml` / `deploy-stage0` / prod 烟测
- `deploy/aws/README.md` — Stage 0 总览与 EC2 升级 SOP  
- `.github/workflows/deploy-stage0.yml` — 真机 `tag` 形参（无 `v` 前缀）  
- `ops/stage0/post_deploy_smoke.sh` — 与 prod **C** 相同的网关烟测脚本，改 `TOKENKEY_BASE_URL` 即可打本地 `:8088`  
- `scripts/release-tag.sh` — 仅 prod 打 tag；本地默认不调用
