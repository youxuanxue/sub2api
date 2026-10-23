# 腾讯美西 · Stage0 布局（迁机同窗 / 例外路径）

**政策（硬）：** 新机从 `plan` 起就按**正式迁机终态**规划布局与发版契约；目标在 **cutover 同窗**落地，切流结束即可默认 `deploy-stage0`。  
**禁止**把「搬 Docker data-root / 全量容器改名 / 布局债」排成迁机完成后的第二次停机或反复返工窗。  
Owner skill：[`tokenkey-host-replacement`](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

状态：**维护窗已执行（2026-10-22）**；Docker Root **例外保留**；网关已恢复。  
回执（主机）：`/var/lib/tokenkey/layout-staging/receipts/window-cutover-20261022T061053Z.json`  
探针/执行器：`ops/stage0/probe-tencent-layout-prep.sh`、`ops/stage0/apply-tencent-layout-window.sh`（仅显式 `APPLY=…`）。

关联：[unlock 契约](../../docs/ops/tencent-unlock-contract.md)、[切流清单](../../docs/ops/tencent-cutover-checklist.md)、[舰队 SSOT](../hetzner/README.md)。

## 默认例外路径（发版前置 · plan 即写入）

切流结束当日须满足：

| 项 | 要求 |
|---|---|
| 业务 `.env` | `/var/lib/tokenkey/.env` |
| 网关容器 | `tokenkey` / `tokenkey-caddy` |
| PG / Redis | Stage0 名 **或** 网络别名 `postgres` / `redis`（探测脚本双名兼容） |
| Docker Root | **默认不搬**；可保留演练挂载下的 data-root；不改 `daemon.json`、不停 Docker 守护进程 |
| 完整字面布局 | 物理改名 PG/Redis、搬 data-root = **可选整洁**，不得挡发版、不得绑迁机主路径 |

## 例外（已落地）

| 项 | 决策 |
|---|---|
| Docker Root Dir | **保持** `/srv/tokenkey-rehearsal/docker`；未改 `daemon.json`；未停 Docker 守护进程 |
| PG 容器名 | **未改** `tk-archive-standby`；网络别名含 `postgres` |
| Redis 容器名 | **未改** `tk-cutover-prod-redis`；网络别名含 `redis` / `tokenkey-redis`；2026-10-22 起 AOF everysec，`-v /srv/tokenkey-rehearsal/redis:/data`。重建时须保留该挂载与 `--appendonly yes`（直接以 AOF 启动只有 RDB 的目录会丢数据，按复制切换） |
| Caddy 日志 | 蓝绿编排在布局例外主机上记下 `tokenkey-caddy`：json-file 100m×5，挂载现网 Caddyfile。发版只 `caddy reload`，不在这里重建容器。没有 Stage0 `docker-compose.yml` 时，开机 `docker start tokenkey-caddy` |

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
| 容器（2026-10-22 首次蓝绿后） | `tokenkey-blue` / `tokenkey-green`（活动色）、`tokenkey`（socat 控制代理）、`tokenkey-caddy`、`tk-cutover-prod-redis`、`tk-archive-standby` |
| 网络别名 | PG:`postgres`；Redis:`redis`,`tokenkey-redis` |
| Docker Root | `/srv/tokenkey-rehearsal/docker`（例外） |
| 活镜像 | 以 `run-probe` 实测为准（不在此记 tag） |

## 发版门禁

- 首次蓝绿已完成（2026-10-22）：标准 `deploy-stage0` 为日常发版路径；依赖网络别名 `postgres`/`redis`。
- Redis/PG **容器名**仍非 Stage0 字面名。网络别名只解决容器间 DNS，**不**解决宿主 `docker exec`：宿主探针须用 `ops/lib/resolve-app-container.sh` 的 `tk_resolve_postgres_container`，不要硬写 `tokenkey-postgres`。物理改名另批窗口（可选整洁）。
- 蓝绿前只读门禁仍可用：`bash ops/observability/run-probe.sh --target prod --script ops/stage0/probe-tencent-bluegreen-prereqs.sh`，须 `verdict=ready`。

## 明确不做（仍成立）

- 不拿现网重启验收 LUKS。
- 不把 Tokyo 历史链恢复成写库，不双写。
- 不把「停 Docker → 搬 data-root」写成迁机默认步骤。
