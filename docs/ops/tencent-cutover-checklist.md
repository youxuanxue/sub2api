# Tokyo → 腾讯美西：切流清单（正式 · 已切）

状态：**腾讯在服 / Tokyo 已退役（2026-10-23）/ 新 timeline 备份已保护 / 账号出口已迁 usw-eval\***
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

- [x] Tokyo 停机与最终快照（2026-10-22）：`i-045f062ce33e1a5af` 停机前只读核实只读库/0 会话/无 app 容器；最终快照 `snap-06612941333b662e8`（数据卷）/ `snap-0724597870487cf4a`（根卷）。后续删除见下方「Tokyo 退役」
- [x] Tokyo 切流前 DLM 快照：2026-10-22 用户决定**不保留**，任其按 7 日轮换。DB 由 S3 `prod/postgres`（timeline 1）pgBackRest 历史链覆盖；Tokyo 主机上的 Redis/配置/证书原样副本随轮换消失，不再可查
- [x] 日本 Lightsail 代理关停：2026-10-22 快照+停机；`jp-*` `proxies` 行 2-5 由用户在 Admin UI 软删；2026-10-23 00:55 UTC 经用户批准（删前复核：绑定 0、DNS/prod/cn-prod 配置无引用、快照 available）**删除 5 台实例并释放 5 个静态 IP（不可逆）**。最终快照 5 份已于 2026-10-23 经用户批准删除（不可逆）
- [x] LUKS 路径退役（2026-11-19）：明文根盘已接流；用户授权缩短观察窗后完成 X1–X4（载体/钥/unlock 销毁）。原「自动解锁验收」作废，见 [`tencent-unlock-contract.md`](tencent-unlock-contract.md)
- [x] 腾讯续费：2026-10-22 用户决定按月自动续费；5 台 Lighthouse 经 `ModifyInstancesRenewFlag` 改为 `NOTIFY_AND_AUTO_RENEW`（DescribeInstances 复核），合计 395 CNY/月。须保持账户余额充足，余额不足仍会到期停机
- [x] Redis 持久化：2026-10-22 23:44 UTC（低峰）经用户批准，经复制切换重建 `tk-cutover-prod-redis`：`appendonly yes` / `appendfsync everysec`，数据挂 `/srv/tokenkey-rehearsal/redis`（LUKS），网络别名不变；写暂停窗口 0.83s，切换后网关 Redis 报错 0。首次尝试（直接以 AOF 启动拷入的 RDB）因 AOF 优先于 RDB 只载入新写入、触发自动回滚，网关约 20s 连不上 Redis。回滚容器 `tk-cutover-prod-redis-old-20261022T234441Z` 已于 2026-10-23 00:56 UTC 经用户批准删除（删前复核 AOF ok、网关 1h Redis 报错 0）
- [x] PG `archive_timeout`：2026-10-22 15:37 UTC 经用户批准 `ALTER SYSTEM SET archive_timeout='300s'` + reload（未重启，`pending_restart=f`）；6 分钟后复核 `archived_count` 199→201、`failed_count=0`。写在 `postgresql.auto.conf`，容器重建时随数据目录保留
- [ ] 腾讯机整机/卷级快照策略（无 EBS/DLM 替代；目前仅 pgBackRest 覆盖 PG，Redis/配置/密钥无快照）
- [x] 隔离物理恢复演练：**取消**（2026-11-19）。整机迁移已覆盖真实恢复路径；不再另开 AWS 临时机演练。历史方案 [`tencent-isolated-restore-plan.md`](tencent-isolated-restore-plan.md) 仅作档案，不授权执行
- [x] 腾讯宿主备份 operator：#2802 入仓 `deploy/tencent/tencent_pgbackrest.py`（宿主副本不随仓库更新）提供 `backup`/`check`；**accept 路径**为仓库内 `ops/backup/cos_restore_acceptance.py`（隔离容器写 `restore_acceptance.json` / `repository_verification.json`，非 tencent_pgbackrest 子命令）。2026-11-19 澄清：勿把 `.py` 当 run-probe `--script` 用 bash 执行（docstring 反引号曾误触发主机 shutdown）
- [x] 配置/密钥离机备份（入口证书优先）：2026-11-19 决定明文 tar.gz → COS（不做操作员 RSA；见 `decided.md`）。首次上传 `prod/host-config/prod-entry/20261119T134742Z-prod-entry.tar.gz`（27996 B，sha256 `6c4fdf80…`，SSE-AES256，90d）。owner `pack_entry_host_config.py` + `apply_entry_host_config_offsite.sh`。续期后重跑 apply；不打包应用/数据机全量 `.env`
- [x] 宿主探针 PG/Redis 容器名：#2804 将其余脚本迁到共享解析（`ops/lib/resolve-app-container.sh` / `resolve_app_container.py`），新增 preflight 门禁 `scripts/checks/data-container-resolver.py` 防回退；每日容量巡检恢复（2026-10-22 prod 只读验证）。白名单余项：cn-prod 专用 `tpm_burst_monitor_remote.sh`、`ops/migration` 下绑定已退役主机的脚本
- [x] QA 分区预建漏装：2026-10-22 13:29 UTC 经用户批准用 `ops/qa/local_maintenance_host.py install --apply` 补装 `tokenkey-qa-provision.timer`（每小时整点），首轮 72/72 小时覆盖；未重启网关/PG。回滚备份 `/var/backups/tokenkey-qa-local/20261022T132922246441Z`
- [x] QA runtime pin：2026-10-23 曾装回停止态 `tokenkey-qa-runtime`（S3 上传当时暂停）。**2026-11-08/19**：QA S3 退役且确认不恢复；现行 = 本地 24h + node1（`tokenkey-qa-coordinator`）
- [x] ~~恢复 QA 上传前置~~：**作废**（2026-11-19）。不恢复 QA→S3；不为上传改 Hybrid/桶策略。见 [`docs/decisions/declined.md`](../decisions/declined.md)
- [x] prod watcher 重装：2026-10-22 15:24 UTC 经用户批准，`host_watch.local_qa_jobs` 含 `tokenkey-qa-provision`；bundle `95998671d971c8ce`，dry-run 全部健康，回滚备份 `/var/backups/tokenkey-https-watch/20261022T152459871497Z`。cn-prod watcher 未变更。**2026-11-19**：双站数据机重装 https-watch（含 Redis 主库探测，`resolve_app_container`）
- [x] 备份身份重装注意：数据机安装器已入仓（`ops/stage0/install-data-machine-identity.sh`）并强制随包 `candidate-usw-20261022.json` + `TK_TENCENT_PROD_MATRIX`（或 cn-prod `prod-target.json`）。手工只拷 `.py` 仍会 fail-closed；正式重装走该安装器即可，不再单列开放项
- [x] ~~归档 canary（`data_layer_archive_rehearsal.PROD_CANARY_*`）迁腾讯~~：**作废**（2026-11-19）。冷表 export canary 非现网必做；库级靠 pgBackRest+WAL，QA 靠 node1。见 [`declined.md`](../decisions/declined.md)
- [x] Tokyo 退役（2026-10-23 经用户批准）：仓库 `aws-fresh` 路径与 inventory 已删（#2818）；删遗留空栈与主栈 `tokenkey-prod-tokyo-precutover-20261017`（删栈前清理栈外孤儿 SG `tokenkey-node1-tokyo-reader-20261017`），删数据卷/根卷；OIDC 栈 `tokenkey-cicd-oidc` retarget 到 `mi-080ed76dc6d97d8b5`（eu-west-2，美西/北京 run-probe 复核正常）；`ops-stage0-host-mem-guard.yml` 已删；其余 13 份快照先删；两份最终快照 `snap-06612941333b662e8`（数据）/ `snap-0724597870487cf4a`（根）也于 2026-10-23 经用户批准删除（不可逆）。**Tokyo 唯一留存：S3 历史链 `prod/postgres`（timeline 1，仅数据库）**
- [x] ~~三桶 Tokyo `InstanceRole` 残留清理~~：**不作专门项**（2026-11-19）。无授权效果、不影响现网；原计划绑「恢复 QA 上传」一并改模板，上传已作废。若日后动物理备份桶 CFN 再顺带清，避免误触 `DenyLightsailEdgeRole`
- [x] CI 角色 SSM 身份：2026-10-23 `tokenkey-cn-image-distribution` 与 `tokenkey-cn-pgdump-ssm` 的 `SendCommand` 从旧 cn-prod `mi-0efdf781205cff512` 改到北京 `mi-06568963b4b3c3239`。pgdump 只改了栈 `tokenkey-cn-prod-pgdump` 的 `DispatchPolicy`，桶未替换。2026-10-23 同日收紧：栈 `tokenkey-cn-image-distribution` 参数 `ManagedInstanceId` 经 change set 改为北京 `mi-*`（drift IN_SYNC，有效策略不变）；栈外 inline `tokenkey-lightsail-edge-addon`（us-east-1 与 eu-west-2 两个 CI 角色）的 `SendCommand` 从 `managed-instance/*` 收紧为仅美西 `mi-080ed76dc6d97d8b5` + 北京 `mi-06568963b4b3c3239`（7 天 CloudTrail 确认 CI 只发过这两台；改后 `ops-cn-backup-watch` / `client-fidelity-watch` 成功）。原策略备份在操作者本机 `~/.local/state/tokenkey-iam-backups/`，不进 git。**新增或替换 Hybrid 主机时须同步把新 `mi-*` 加进该语句**，否则 CI 发版/诊断会被拒。数据面拆分后，两台数据机 `mi-044ba9da6453c1557`（prod）和 `mi-084a456b3dee71bb4`（cn-prod）也要在该语句里，否则每日容量探针和 `log_dump` 的 PG/Redis 会被拒。2026-11-17：prod 入口机 `mi-026982f3622163a1b` 与候选应用机 `mi-000c0168439c243a8` 补进同一 `SsmManagedInstanceCommand`（否则 `prod-rolling-deploy` 在 entry lease 处 AccessDenied）；备份 `~/.local/state/tokenkey-iam-backups/20261116T232243Z-*-tokenkey-lightsail-edge-addon*.json`
