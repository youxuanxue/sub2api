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

- [ ] Tokyo 保留 ≥7 天再退役（禁止解冻双写；不早于 10-29）。步骤见 #2802 `tokyo-retirement-runbook.md`
- [x] Tokyo 切流前 DLM 快照：2026-10-22 用户决定**不保留**，任其按 7 日轮换。DB 由 S3 `prod/postgres`（timeline 1）pgBackRest 历史链覆盖；Tokyo 主机上的 Redis/配置/证书原样副本随轮换消失，不再可查
- [ ] 日本 Lightsail 代理关停：`jp-*` 四行 live/fallback/backup 绑定均为 0（2026-10-22 只读复核）；另有 `tokenkey-node1-ingress-jp-20261016-vm` 一并关停
- [ ] LUKS 自动解锁验收（**禁止拿现网整机重启试验**；契约见 [`tencent-unlock-contract.md`](tencent-unlock-contract.md)；方案见 [`tencent-luks-autounlock-plan.md`](tencent-luks-autounlock-plan.md)：无 TPM，推荐开机经 SSM Hybrid 身份取 keyfile，演练约 40 CNY + 1–2 USD/月，待批）
- [ ] **腾讯续费（11-14 前决策）**：prod 与 `usw-eval1` 11-21 08:31 UTC 到期，`usw-eval2..4` 11-22 02:59 UTC，均为 `NOTIFY_AND_MANUAL_RENEW`，不续即停机。5 台合计 395 CNY/月，或包年 4317 CNY
- [ ] **Redis 无持久化（现网风险）**：`tk-cutover-prod-redis` 为 `appendonly no`，RDB 写在容器可写层、无挂载，容器重建即丢（2026-10-22 只读复核：约 1.07 万 key）。修复需变更审批
- [ ] **PG `archive_timeout=0`（现网风险）**：与基线 300 不符，低流量时 WAL 归档无时间上界、RPO 无界。修复为 `ALTER SYSTEM` + reload（不重启），需变更审批
- [ ] 腾讯机整机/卷级快照策略（无 EBS/DLM 替代；目前仅 pgBackRest 覆盖 PG，Redis/配置/密钥无快照）
- [ ] 隔离物理恢复演练：[#2799](https://github.com/youxuanxue/sub2api/pull/2799) 已对 Hybrid 失败关闭并支持 amd64；推荐 AWS us-east-1 临时 c7i.xlarge（预算上限 $5）。待批：RestoreRole 读活链前缀的 CFN、CI OIDC 的 EC2/PassRole 权限、预算。方案见 [`tencent-isolated-restore-plan.md`](tencent-isolated-restore-plan.md)
- [ ] 腾讯宿主备份 operator：已由 #2802 入仓为 `deploy/tencent/tencent_pgbackrest.py`（宿主副本不随仓库更新），但仍只有 `backup`/`check`，缺 `accept`，恢复 workflow 末步回执导入会明确报错
- [ ] 配置/密钥离机备份：推荐宿主用操作员公钥加密 tar 上传 `candidates/…/host-config/`（无需改 IAM），另开 PR
- [x] 宿主探针 PG/Redis 容器名：#2804 将其余脚本迁到共享解析（`ops/lib/resolve-app-container.sh` / `resolve_app_container.py`），新增 preflight 门禁 `scripts/checks/data-container-resolver.py` 防回退；每日容量巡检恢复（2026-10-22 prod 只读验证）。白名单余项：cn-prod 专用 `tpm_burst_monitor_remote.sh`、`ops/migration` 下绑定已退役主机的脚本
- [x] QA 分区预建漏装：2026-10-22 13:29 UTC 经用户批准用 `ops/qa/local_maintenance_host.py install --apply` 补装 `tokenkey-qa-provision.timer`（每小时整点），首轮 72/72 小时覆盖；未重启网关/PG。回滚备份 `/var/backups/tokenkey-qa-local/20261022T132922246441Z`
- [ ] QA phase2 剩余真实问题：`tokenkey-qa-runtime` 容器在腾讯机不存在。已解决：探针体积（#2800，ARCHIVE 行 23.6 KB→0.7 KB）、boundary 口径（#2802，暂停清单为唯一真相）、分区缺口（provision 补装）
- [x] prod watcher 重装：2026-10-22 15:24 UTC 经用户批准，`host_watch.local_qa_jobs` 含 `tokenkey-qa-provision`；bundle `95998671d971c8ce`，dry-run 全部健康，回滚备份 `/var/backups/tokenkey-https-watch/20261022T152459871497Z`。cn-prod watcher 未变更
- [ ] 备份身份重装注意：`ops/stage0/tencent_backup_identity.py` 已改为读腾讯矩阵（#2804），但宿主安装器不在仓库；下次重装/修复须把 `candidate-usw-20261022.json` 放在脚本旁或设 `TK_TENCENT_PROD_MATRIX`，否则 `tokenkey-backup-identity` timer 失败。现宿主跑旧副本，不受影响
- [ ] 归档 canary（`data_layer_archive_rehearsal.PROD_CANARY_CONTAINER`）仍绑 EC2 `i-*` 与 CFN stack，需单独迁到腾讯
- [ ] 冻结 Tokyo 退役时一并：删 `aws-fresh` 模式与 `fresh-migration-20261015.json`、OIDC 栈 retarget 到 `mi-*`（#2802 模板已支持）、遗留空栈 `tokenkey-tokyo-ebs-restore-20261017`
