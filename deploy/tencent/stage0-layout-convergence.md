# 腾讯美西 · Stage0 布局收敛（例外路径）

状态：**维护窗已执行（2026-10-22）**；Docker Root **例外保留**；网关已恢复。  
回执（主机）：`/var/lib/tokenkey/layout-staging/receipts/window-cutover-20261022T061053Z.json`  
探针/执行器：`ops/stage0/probe-tencent-layout-prep.sh`、`ops/stage0/apply-tencent-layout-window.sh`。

关联：[unlock 契约](../../docs/ops/tencent-unlock-contract.md)、[切流清单](../../docs/ops/tencent-cutover-checklist.md)、[舰队 SSOT](../hetzner/README.md)。

## 例外（已落地）

| 项 | 决策 |
|---|---|
| Docker Root Dir | **保持** `/srv/tokenkey-rehearsal/docker`；未改 `daemon.json`；未停 Docker 守护进程 |
| PG 容器名 | **未改** `tk-archive-standby`；网络别名含 `postgres` |
| Redis 容器名 | **未改** `tk-cutover-prod-redis`；网络别名含 `redis` / `tokenkey-redis` |

## 窗内已完成

| 项 | 结果 |
|---|---|
| 业务 `.env` | `/var/lib/tokenkey/.env`（自 `.env.stage0-ready`；含 `TOKENKEY_IMAGE` + Feishu） |
| App / Caddy 名 | `tokenkey` / `tokenkey-caddy`（由 `tk-cutover-prod-*` rename，同镜像） |
| Caddy upstream | `tokenkey:8080`；副本 `/var/lib/tokenkey/caddy/Caddyfile` |
| 网络 | 仍为 `tk-cutover-prod` |
| 停机体感 | SSM 总时长约 80s；app 约 5–10s 内 healthy；公网 `/health` 200 |
| 回滚工件 | `layout-staging/env.alert-only.bak.*`、`Caddyfile.cutover.bak.*` |

## 现网实况（收敛后）

| 项 | 值 |
|---|---|
| 容器 | `tokenkey`、`tokenkey-caddy`、`tk-cutover-prod-redis`、`tk-archive-standby` |
| App DB/Redis env | 仍为 `tk-archive-standby` / `tk-cutover-prod-redis`（容器内未改） |
| 网络别名 | PG:`postgres`；Redis:`redis`,`tokenkey-redis` |
| Docker Root | `/srv/tokenkey-rehearsal/docker`（例外） |
| 活镜像 tag | `1.8.313`（窗内未 pull） |

## 发版门禁

- **可以**开始走标准 `deploy-stage0` 的前置条件：活 `.env` 在 `/var/lib/tokenkey/.env`，存在 legacy `tokenkey` 容器供蓝绿首次迁移。
- **注意**：首次 stock 蓝绿仍会按脚本从 `tokenkey` 迁到 `tokenkey-blue/green`；依赖网络别名 `postgres`/`redis`。发版前建议先 `operation=check` / warm，不要与本窗混做。
- Redis/PG **容器名**仍非 Stage0 字面名；靠 alias。若要物理改名，另批窗口。
- **首次蓝绿前硬门禁**（只读）：`bash ops/observability/run-probe.sh --target prod --script ops/stage0/probe-tencent-bluegreen-prereqs.sh`，须 `verdict=ready`。覆盖：`docker compose` 插件、`tokenkey_tokenkey-network` + DNS 别名、`formal/app/config.yaml`、**实挂** Caddyfile 路径（本机为 `formal/caddy/Caddyfile.cutover`，不是 Stage0 副本）、本地镜像或 GHCR 登录。
- 蓝绿脚本已对齐例外路径：解析实挂 Caddyfile、app 数据用 `formal/app`、缺 `/var/lib/tokenkey/aws` 时跳过挂载、digest 镜像回退 `.env` tag、本地已有镜像则跳过 pull。**人工批准前禁止 dispatch deploy。**

## 明确不做（仍成立）

- 不拿现网重启验收 LUKS。
- 不解冻 Tokyo / 双写。
