# Tokyo → 腾讯美西：切流清单（正式 · 已切）

状态：**腾讯在服 / Tokyo 已完全退役 / 新 timeline 备份已保护 / 账号出口已迁 usw-eval\***  
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

- [x] Tokyo 退役（2026-10-22 用户批准提前退役）：**已停机**（`i-045f062ce33e1a5af` stopped，停前只读核实只读库/0 会话/无 app 容器；停后三正式域名 `/health` 200）；最终快照 `snap-06612941333b662e8`（数据卷）/ `snap-0724597870487cf4a`（根卷）completed。删栈、删卷与 OIDC retarget 已于 2026-10-23 完成，见同清单下方 Tokyo 退役条目。
- [x] Tokyo 切流前 DLM 快照：2026-10-22 用户决定**不保留**，任其按 7 日轮换。DB 由 S3 `prod/postgres`（timeline 1）pgBackRest 历史链覆盖；Tokyo 主机上的 Redis/配置/证书原样副本随轮换消失，不再可查
- [x] 日本 Lightsail 代理关停：2026-10-22 快照+停机；`jp-*` `proxies` 行 2-5 由用户在 Admin UI 软删；2026-10-23 00:55 UTC 经用户批准（删前复核：绑定 0、DNS/prod/cn-prod 配置无引用、快照 available）**删除 5 台实例并释放 5 个静态 IP（不可逆）**。保留最终快照 `<name>-final-20261022`（5 份，计费，清理时机待用户决定）
- [ ] LUKS 自动解锁验收（**禁止拿现网整机重启试验**；契约见 [`tencent-unlock-contract.md`](tencent-unlock-contract.md)；方案见 [`tencent-luks-autounlock-plan.md`](tencent-luks-autounlock-plan.md)：无 TPM，推荐开机经 SSM Hybrid 身份取 keyfile，演练约 40 CNY + 1–2 USD/月，待批）
- [x] 腾讯续费：2026-10-22 用户决定按月自动续费；5 台 Lighthouse 经 `ModifyInstancesRenewFlag` 改为 `NOTIFY_AND_AUTO_RENEW`（DescribeInstances 复核），合计 395 CNY/月。须保持账户余额充足，余额不足仍会到期停机
- [x] Redis 持久化：2026-10-22 23:44 UTC（低峰）经用户批准，经复制切换重建 `tk-cutover-prod-redis`：`appendonly yes` / `appendfsync everysec`，数据挂 `/srv/tokenkey-rehearsal/redis`（LUKS），网络别名不变；写暂停窗口 0.83s，切换后网关 Redis 报错 0。首次尝试（直接以 AOF 启动拷入的 RDB）因 AOF 优先于 RDB 只载入新写入、触发自动回滚，网关约 20s 连不上 Redis。回滚容器 `tk-cutover-prod-redis-old-20261022T234441Z` 已于 2026-10-23 00:56 UTC 经用户批准删除（删前复核 AOF ok、网关 1h Redis 报错 0）
- [x] PG `archive_timeout`：2026-10-22 15:37 UTC 经用户批准 `ALTER SYSTEM SET archive_timeout='300s'` + reload（未重启，`pending_restart=f`）；6 分钟后复核 `archived_count` 199→201、`failed_count=0`。写在 `postgresql.auto.conf`，容器重建时随数据目录保留
- [ ] 腾讯机整机/卷级快照策略（无 EBS/DLM 替代；目前仅 pgBackRest 覆盖 PG，Redis/配置/密钥无快照）
- [ ] 隔离物理恢复演练：[#2799](https://github.com/youxuanxue/sub2api/pull/2799) 已对 Hybrid 失败关闭并支持 amd64；推荐 AWS us-east-1 临时 c7i.xlarge（预算上限 $5）。待批：RestoreRole 读活链前缀的 CFN、CI OIDC 的 EC2/PassRole 权限、预算。方案见 [`tencent-isolated-restore-plan.md`](tencent-isolated-restore-plan.md)
- [ ] 腾讯宿主备份 operator：已由 #2802 入仓为 `deploy/tencent/tencent_pgbackrest.py`（宿主副本不随仓库更新），但仍只有 `backup`/`check`，缺 `accept`，恢复 workflow 末步回执导入会明确报错
- [ ] 配置/密钥离机备份：推荐宿主用操作员公钥加密 tar 上传 `candidates/…/host-config/`（无需改 IAM），另开 PR
- [x] 宿主探针 PG/Redis 容器名：#2804 将其余脚本迁到共享解析（`ops/lib/resolve-app-container.sh` / `resolve_app_container.py`），新增 preflight 门禁 `scripts/checks/data-container-resolver.py` 防回退；每日容量巡检恢复（2026-10-22 prod 只读验证）。白名单余项：cn-prod 专用 `tpm_burst_monitor_remote.sh`、`ops/migration` 下绑定已退役主机的脚本
- [x] QA 分区预建漏装：2026-10-22 13:29 UTC 经用户批准用 `ops/qa/local_maintenance_host.py install --apply` 补装 `tokenkey-qa-provision.timer`（每小时整点），首轮 72/72 小时覆盖；未重启网关/PG。回滚备份 `/var/backups/tokenkey-qa-local/20261022T132922246441Z`
- [x] QA runtime pin：2026-10-23 用户决定 S3 上传**暂停、可能恢复**，经用户批准装回 `tokenkey-qa-runtime`（#2812 后；停止态，`1.8.316`，`tokenkey_tokenkey-network`，`independent-v1`；网关/PG/Redis 未变）。QA phase2 live health `healthy=True`，仅 `qa_archive_instance_role_unavailable` 推迟至恢复上传。回滚 `docker rm tokenkey-qa-runtime`
- [ ] **恢复 QA 上传前置**：S3 写入身份。用户选复用 `tokenkey-hetzner-ssm-hybrid-prod`（已在 QA raw 桶策略 Allow 内）；但切换前须先给该角色补 `TencentUswPhysicalBackup` 同等备份权限（否则 pgBackRest 备份与 WAL 归档立即失败），并同 PR 同步 10 处 `mi-080ed76dc6d97d8b5` 与 SendCommand 授权。现 Lightsail 角色被桶策略 `DenyLightsailEdgeRole` 显式拒绝
- [x] prod watcher 重装：2026-10-22 15:24 UTC 经用户批准，`host_watch.local_qa_jobs` 含 `tokenkey-qa-provision`；bundle `95998671d971c8ce`，dry-run 全部健康，回滚备份 `/var/backups/tokenkey-https-watch/20261022T152459871497Z`。cn-prod watcher 未变更
- [ ] 备份身份重装注意：`ops/stage0/tencent_backup_identity.py` 已改为读腾讯矩阵（#2804），但宿主安装器不在仓库；下次重装/修复须把 `candidate-usw-20261022.json` 放在脚本旁或设 `TK_TENCENT_PROD_MATRIX`，否则 `tokenkey-backup-identity` timer 失败。现宿主跑旧副本，不受影响
- [ ] 归档 canary（`data_layer_archive_rehearsal.PROD_CANARY_CONTAINER`）仍绑 EC2 `i-*` 与 CFN stack，需单独迁到腾讯
- [x] Tokyo 退役（2026-10-23 经用户批准）：仓库 `aws-fresh` 路径与 inventory 已删（#2818）；删遗留空栈与主栈 `tokenkey-prod-tokyo-precutover-20261017`（删栈前清理栈外孤儿 SG `tokenkey-node1-tokyo-reader-20261017`），删数据卷/根卷；OIDC 栈 `tokenkey-cicd-oidc` retarget 到 `mi-080ed76dc6d97d8b5`（eu-west-2，美西/北京 run-probe 复核正常）；`ops-stage0-host-mem-guard.yml` 已删；快照仅保留最终 `snap-06612941333b662e8`（数据）/ `snap-0724597870487cf4a`（根），其余 13 份已删。S3 历史链 `prod/postgres`（timeline 1）保留
- [ ] 三个 S3 桶策略（QA raw / 物理备份 / pgdump）仍含已删除 Tokyo `InstanceRole`（显示为 `AROAZ55ZQM2DFHIHFRTLO`），无授权效果、不影响现网。由各自 CFN 栈参数注入；物理备份与 QA raw 模板的角色参数为必填。**不单独处理**，在恢复 QA 上传（切 hetzner 角色）时一并改模板清理，避免误动物理备份桶的 `DenyLightsailEdgeRole`
- [x] CI 角色 SSM 身份：2026-10-23 `tokenkey-cn-image-distribution` 与 `tokenkey-cn-pgdump-ssm` 的 `SendCommand` 从旧 cn-prod `mi-0efdf781205cff512` 改到北京 `mi-06568963b4b3c3239`。pgdump 只改了栈 `tokenkey-cn-prod-pgdump` 的 `DispatchPolicy`，桶未替换。`tokenkey-lightsail-edge-addon` 的 `managed-instance/*` 未动
