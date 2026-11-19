# 腾讯美西迁机交接

更新：2026-11-19。**腾讯美西已在服（timeline 2）；Tokyo / 日本 Lightsail 已删；账号出口在 `or-*` + 阿里云 `cn-ip*`；入口+双应用+数据机；LUKS X1–X4 已退役。**

## 1. 接手先读（当前态）

- 现网入口与坐标唯一来源：[舰队入口](../../deploy/hetzner/README.md)。切流回执（历史）：[tencent-cutover-20261022.json](reports/tencent-cutover-20261022.json)。切流清单已关闭：[tencent-cutover-checklist.md](tencent-cutover-checklist.md)。
- **禁止把 Tokyo 历史链恢复成写库、禁止双写。** 见 [退役摘要](tokyo-retirement-runbook.md)。
- **LUKS 已退役**：明文根盘；勿再解锁演练（[unlock stub](tencent-unlock-contract.md)）。整机/卷快照常备策略已拒绝，见 [`declined.md`](../decisions/declined.md)。
- QA：**不**上传 S3；本地 24h + 数据机协调器归档 **node1**（见 [`declined.md`](../decisions/declined.md)）。
- 私有 helper/回执在 `~/.local/state/tokenkey-tencent-usw-validation/`（含密钥/密文，**不进 git**）。
- 多应用机诊断：`run-probe.sh --target prod --node entry|app-old|app-cand|data`（TAT 默认）；整站扇出用 `--all-apps`。
- GitHub Actions 仓库变量 `PROD_SSM_TARGET` 必须为 `auto`；`hetzner`/`aws-fresh` 失败关闭。
- 迁机流程 owner：[pgbackrest-host-migration.md](../../ops/stage0/pgbackrest-host-migration.md)。

## 2. 现网要点

| 项 | 状态 |
|---|---|
| prod 入口 | `43.162.119.210`（五域名 A）；WRR 应用机 `10.4.0.16` + `10.4.0.9` |
| DB / Redis | 数据机 `10.4.0.3`；timeline 2；活备份 COS 前缀 `candidates/tencent-usw-20261022/postgres`；历史 `prod/postgres` 只读 |
| 备份 | `tokenkey-backup-identity.timer` + `tokenkey-postgres-backup/archive-check.timer`；验收 `cos_restore_acceptance.py` |
| 账号出口 | Oregon `or-1..4`（活）；阿里云 `cn-ip5..12`；`usw-eval*` / 腾讯 `cn-ip1..4` / `jp-*` 已退役 |
| 宿主告警 | `tokenkey-https-watch.timer`（数据机含 Redis 主库探测）；凭据 `/opt/tokenkey/host-alerts.env`（data）或应用 `.env` |
| 应用布局 | 明文根盘单容器 `tokenkey`（`:8080`/`:18080`）；日常发版 `prod-rolling-deploy.yml`，非蓝绿 |

## 3. 身份与备份边界（仍有效）

- 身份角色：`tokenkey-lightsail-ssm-hybrid`；inline `TencentUswPhysicalBackup`：源前缀只读，候选前缀可管理。生成器：[tencent_backup_identity.py](../../ops/stage0/tencent_backup_identity.py)。
- **不要原样重跑** 旧 `install-hybrid-backup-identity.sh`（有拒绝守卫）；修复用 `repair-identity-runtime-preserve.sh`。
- RuntimeDirectoryPreserve=yes 必须保持；oneshot 结束后目录不可被 systemd 清掉。

## 4. 断线与回滚纪律

1. 操作走本机 CLI + TAT/SSM，不经待切网关。先查原 `InvocationId`，不盲目重发。
2. Tokyo 已删除，不存在库级回滚目标。出口回滚清单（`jp-to-usw-migration.json` 的 `rollback`）**已作废**：`jp-*` 代理已删除。
3. 无法确定阶段时先只读：DNS、`/health`、`default_transaction_read_only`、代理绑定分布、归档 `failed_count`。

## 5. 接手入口

- 切流收尾 PR [#2753](https://github.com/youxuanxue/sub2api/pull/2753) 已合并；PR 不含私有 helper/密钥。
- 只读观测：`ops/observability/run-probe.sh --target prod`（显式 `--expected-instance-id` 取舰队 SSOT）或腾讯 TAT：`helpers/tokenkey-usw-tool.py`。

**接手第一句话应是「腾讯美西在服、Tokyo 已退役、出口已迁 usw-eval*；开放项见切流清单『后续』节」。**
