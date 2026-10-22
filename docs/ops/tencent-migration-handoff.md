# 腾讯美西迁机交接

更新：2026-10-22 03:12 UTC。**腾讯美西已在服（timeline 2）；Tokyo 冻结保留；账号出口已迁到美西 `usw-eval*`；日本 Lightsail 代理保留待关停。**

## 1. 接手先读（当前态）

- 现网入口与坐标唯一来源：[舰队入口](../../deploy/hetzner/README.md)。切流回执：[tencent-cutover-20261022.json](reports/tencent-cutover-20261022.json)。清单：[tencent-cutover-checklist.md](tencent-cutover-checklist.md)。
- **禁止解冻 Tokyo、禁止双写。** Tokyo `i-045f062ce33e1a5af` 保留 ≥7 天后再另批退役。
- **禁止拿现网做整机重启试验。** LUKS 自动解锁未验收；重启须人工交付解锁材料（契约：[tencent-unlock-contract.md](tencent-unlock-contract.md)）。
- 工作区 `/Users/feng/Codes/sub2api-wt-tengxun1020`，分支 `chore/tengxun1020`。私有 helper/回执在 `~/.local/state/tokenkey-tencent-usw-validation/`（含密钥/密文，**不进 git**）。
- `resolve_prod_ssm_target.py --target auto` → 腾讯 Hybrid `mi-080ed76dc6d97d8b5`（矩阵 `deployable=true`）。
- GitHub Actions 仓库变量 `PROD_SSM_TARGET` 必须为 `auto`（已从 `aws-fresh` 改回）；钉死 `aws-fresh` 会把 deploy/诊断打到冻结 Tokyo。`aws-fresh` 路径默认拒绝，保留只读须 `ALLOW_FROZEN_TOKYO_TARGET=1`。
- `PROD_SSM_TARGET=hetzner` 与 `deploy-prod-hetzner-stage0` provision 均失败关闭；Hetzner Cloud 已退役。
- 迁机流程 owner：[pgbackrest-host-migration.md](../../ops/stage0/pgbackrest-host-migration.md)。

## 2. 现网要点

| 项 | 状态 |
|---|---|
| prod 入口 | `43.162.83.233`；`tokenkey.dev` / `api.tokenkey.dev` / `api-cn.tokenkey.dev` / `callmodel.io` / `api.callmodel.io`（TTL 300） |
| DB | timeline 2；活备份前缀 `candidates/tencent-usw-20261022/postgres`；历史 `prod/postgres` 只读 |
| 备份 | `tokenkey-backup-identity.timer` + `tokenkey-postgres-backup/archive-check.timer`；全量标签 `20261022-020832F` |
| 账号出口 | `usw-eval1..4`（私网 `10.4.0.{2,11,5,4}:1080`）；`jp-*` 绑定已清零；`cn-ip*` 未动 |
| 外部飞书备份告警 | 尚未接到腾讯机；连续性靠本机 timer + `archive_check.json` |

## 3. 身份与备份边界（仍有效）

- 身份角色：`tokenkey-lightsail-ssm-hybrid`；inline `TencentUswPhysicalBackup`：源前缀只读，候选前缀可管理。生成器：[tencent_backup_identity.py](../../ops/stage0/tencent_backup_identity.py)。
- **不要原样重跑** 旧 `install-hybrid-backup-identity.sh`（有拒绝守卫）；修复用 `repair-identity-runtime-preserve.sh`。
- RuntimeDirectoryPreserve=yes 必须保持；oneshot 结束后目录不可被 systemd 清掉。

## 4. 断线与回滚纪律

1. 操作走本机 CLI + TAT/SSM，不经待切网关。先查原 `InvocationId`，不盲目重发。
2. Tokyo 已冻结：目标已有业务写入后，**禁止直接解冻旧库回滚**。出口回滚用私有回执 `p1-20261022/jp-to-usw-migration.json` 的 `rollback` 清单按账号恢复 `proxy_id`。
3. 无法确定阶段时先只读：DNS、`/health`、`default_transaction_read_only`、代理绑定分布、归档 `failed_count`。

## 5. 接手入口

- Draft PR：[#2753](https://github.com/youxuanxue/sub2api/pull/2753)（分支 `chore/tengxun1020`）。PR 不含私有 helper/密钥。
- 只读观测：`ops/observability/run-probe.sh --target prod`（显式 `--expected-instance-id` 取舰队 SSOT）或腾讯 TAT：`helpers/tokenkey-usw-tool.py`。

**接手第一句话应是「腾讯美西在服、Tokyo 冻结、出口已迁 usw-eval*；下一步是观察与 ≥7 天后东京退役审批」。**
