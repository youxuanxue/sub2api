# 腾讯美西 · Stage0 布局收敛（无感准备 / 另批执行）

状态：**仅准备，未在现网执行收敛。**  
约束：收敛会动 Docker data-root / 容器名 / `.env` 路径，**禁止在无维护窗时热改**；当前禁止默认 `deploy-stage0`。

关联：[unlock 契约](../../docs/ops/tencent-unlock-contract.md)、[切流清单](../../docs/ops/tencent-cutover-checklist.md)、[舰队 SSOT](../hetzner/README.md)。

## 现网实况（只读，2026-10-22）

| 项 | cutover 实况 | Stage0 目标 |
|---|---|---|
| LUKS mapper | `tokenkey_recovery` | 不变 |
| 挂载 | `/var/lib/tokenkey` **与** `/srv/tokenkey-rehearsal`（同一 source） | 保留双挂或收敛为单主路径 + 兼容 bind |
| Docker Root Dir | `/srv/tokenkey-rehearsal/docker` | `/var/lib/tokenkey/docker`（或文档化例外） |
| 业务 `.env` | `formal/.env` | `/var/lib/tokenkey/.env` |
| 容器名 | `tk-cutover-prod-{app,caddy,redis}` | Stage0 约定名 |
| PG 容器 | `tk-archive-standby`（已是 primary） | 与备份脚本发现逻辑对齐 |

## 无感已做（不影响流量）

- 飞书宿主告警凭据：从 `formal/.env` 抽出 `TOKENKEY_FEISHU_*` 写入 **仅告警用** `/var/lib/tokenkey/.env`（不含 DB/应用密钥变更；不重启容器）。
- HTTPS/备份 watch 安装走独立 systemd timer，不改网关/Caddy/PG。

## 维护窗收敛顺序（须另批授权）

1. 冻结发版；确认 `/health` 与备份 timer 正常。
2. 停 app/caddy（保留 PG 写者策略按审批）；**不要**整机重启。
3. 迁移 Docker data-root（`systemctl stop docker` → 搬迁/bind → 改 daemon.json → 起 docker）。
4. 用 compose 以目标名重建 app/caddy/redis（同镜像/同 `.env` 内容）；PG 容器名变更须同步备份 ExecStart。
5. 将业务 `.env` 落到 `/var/lib/tokenkey/.env`（或硬链），校验 app 读到同一文件。
6. 冒烟：`/health`、登录、一条计费请求、archive_check、https-watch dry-run。
7. 更新舰队 SSOT / unlock 契约「布局债」段落；打开默认 `deploy-stage0`。

回滚：保留 cutover compose 与路径，Docker root 未搬迁前可直接起旧容器名。

## 明确不做

- 不拿现网重启验收 LUKS。
- 不在本文件批准双写或解冻 Tokyo。
