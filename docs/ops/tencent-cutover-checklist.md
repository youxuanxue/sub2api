# Tokyo → 腾讯美西：切流清单（正式 · 已切）

状态：**腾讯在服 / Tokyo 已冻结 / 新 timeline 备份已保护 / 账号出口已迁 usw-eval\***  
回执：`docs/ops/reports/tencent-cutover-20261022.json`  
交接：[`tencent-migration-handoff.md`](tencent-migration-handoff.md)

## 已完成

- [x] 切流 + DNS + 公网 `/health`
- [x] WAL 归档（timeline 2 → 候选前缀）
- [x] Full backup `20261022-020832F`（~580s）
- [x] ~~Edge CIDR~~（无 edge）
- [x] `auto` resolve → 腾讯（`deploy_profile=prod`）
- [x] 运维收敛（2026-10-22）：DNS TTL 60→300；身份 timer + 备份/归档检查 timer；活备份 SSOT=`candidates/tencent-usw-20261022/postgres`（不升格混写 `prod/postgres`）
- [x] 账号出口 JP→USW（2026-10-22）：`jp-*` 绑定清零；`usw-eval1..4` 承接原 JP 账号；`cn-ip*` 未动；迁后只读观察 `jp_remaining=0`、`/health=ok`
- [x] Stage0 **例外路径**布局（2026-10-22）：`.env`→`/var/lib/tokenkey/.env`；`tokenkey`/`tokenkey-caddy`；Docker root **例外**保留；PG/Redis 靠网络别名。见 [`stage0-layout-convergence.md`](../../deploy/tencent/stage0-layout-convergence.md)
- [x] 外部飞书宿主告警：`tokenkey-https-watch.timer` 已接腾讯机

## 后续（另批，勿擅自执行）

- [ ] Tokyo 保留 ≥7 天再退役（禁止解冻双写）
- [ ] 日本 Lightsail 代理关停（账号绑定已迁出后再批）
- [ ] LUKS 自动解锁验收（**禁止拿现网整机重启试验**；契约见 [`tencent-unlock-contract.md`](tencent-unlock-contract.md)）
- [ ] 腾讯 Lighthouse 实例续费或换包年（prod 与 `usw-eval1..4` 到期约 2026-11-21/22 UTC，`NOTIFY_AND_MANUAL_RENEW`；不续即停机）
- [ ] **Redis 无持久化（现网风险）**：`tk-cutover-prod-redis` 为 `appendonly no`，RDB 写在容器可写层、无挂载，容器重建即丢（2026-10-22 只读复核：约 1.07 万 key）。修复需变更审批
- [ ] **PG `archive_timeout=0`（现网风险）**：与基线 300 不符，低流量时 WAL 归档无时间上界、RPO 无界。修复为 `ALTER SYSTEM` + reload（不重启），需变更审批
- [ ] 腾讯机整机/卷级快照策略（无 EBS/DLM 替代；目前仅 pgBackRest 覆盖 PG，Redis/配置/密钥无快照）
- [ ] 隔离物理恢复演练：#2799 已对 Hybrid 失败关闭并支持 amd64；推荐 AWS us-east-1 临时 c7i.xlarge（预算上限 $5）。待批：RestoreRole 读活链前缀的 CFN、CI OIDC 的 EC2/PassRole 权限、预算。方案见 [`tencent-isolated-restore-plan.md`](tencent-isolated-restore-plan.md)
- [ ] 腾讯宿主备份 operator `tencent_pgbackrest.py` 不在仓库且无 `accept` 命令，恢复回执无法导入；需入仓
- [ ] 配置/密钥离机备份：推荐宿主用操作员公钥加密 tar 上传 `candidates/…/host-config/`（无需改 IAM），另开 PR
- [ ] 宿主探针 PG/Redis 容器名：data-layer 与 daily error ledger 走 `tk_resolve_postgres_container`；daily diagnostics 的 inline SSM 从 `resolve_app_container.py` 渲染同一「恰好一个 running」规则。其余硬写 `tokenkey-postgres` 的脚本逐批迁移
- [ ] QA phase2 探针在 PG 解析修复后输出约 23 KB，接近 SSM 24 KB 上限；需改为分块/落盘回传后再接 PG 解析
- [ ] 冻结 Tokyo 退役时一并：DLM 快照、`fresh-migration-20261015.json` 中的 QA 暂停 owner 迁到腾讯矩阵、`cicd-oidc.yaml` 的 EC2-only SendCommand 资源
