---
status: approved
approved_by: user (conversation)
approved_at: 2026-10-16
scope: repair approved post-migration backup, timer and monitoring gaps; prepare old Hcloud edge retirement
---

# AWS 迁移收尾

用户已批准修复迁移审计发现的备份、timer、告警遗留，并将四台旧 Hcloud edge 回收列为中低优先级。
本次仅修复宿主运维设施，保持网关、账单、余额、额度和订阅不变；不发布应用镜像。
现网节点身份只由 [舰队入口](../../deploy/hetzner/README.md) 与其矩阵持有。

## 执行范围

- 新 prod 纳入既有每日 DLM，建立首次双卷快照；pg_dump 保留原排除契约，不能称其包含使用明细。
- 五台现役主机分别下载当前主机的备份，在限 CPU/内存/IO 的独立 PostgreSQL 恢复并核对核心数据。
- 修复 uk2 Redis 的 restart policy；不重启容器。
- 恢复新 prod 原有 QA archive/boundary timer，沿用独立 maintenance pin、存储和清理门禁。
  对象存储失败时保持归档清理保护，不改变 cutover 或伪造恢复回执。
- 旧 AWS standby 禁止业务自启动，停 pgdump/GHCR prune timer，并隔离未来备份前缀。
  停旧 Hcloud 的 GHCR prune，保留旧 prod 转发、必要凭据刷新与磁盘监控。
- 补独立宿主 HTTPS 监控：us5 每两分钟监测五个正式健康入口，prod 反向监测 us5。
  复用已有 HTTPS probe 与飞书 delivery；连续两次失败告警，持续故障每 30 分钟提醒，恢复通知。
  初始健康不发测试消息。GitHub 定时检查保留作独立补充。
  监控无需新增付费资源；跨节点覆盖仍不能覆盖 AWS 双区域同时中断。
- 备份后移除已禁用、无通知动作且绑定旧 EC2 的过时 CloudWatch alarms。

## 宿主 HTTPS 监控

Owner：`ops/observability/host_https_watch.py`、`install-host-https-watch.sh`。
安装器只接受 prod/us5 角色并核对活矩阵的主机名，先 dry-run 全部健康再启用 timer。
更新使用内容寻址的独立代码目录，避免覆盖仍可能被旧 timer 读取的模块。
传入现有 probe/delivery 三个 companion、fresh prod 清单、Lightsail 活矩阵；
凭据仅从主机现有 `.env` 读取，不通过 SSM 传送。
`APPLY=yes-host-https-watch` 是安装 opt-in，`WATCH_ROLE=prod|us5` 选择职责。
状态文件为 `/var/lib/tokenkey/https-watch/state.json`；systemd service 最长 110 秒、10% CPU、128 MiB。
通知成功后持久化状态；多目标中途通知失败允许下次重复先前已投递的通知，避免漏报。
回退先停用 `tokenkey-https-watch.timer`，待 service 空闲后恢复安装输出的备份文件并 daemon-reload；
首次安装直接保留禁用状态即可。代码合并本身不会向其他主机自动安装。

## 验收与待办

截至 2026-10-16 02:51 UTC：

- 新 prod 已加入既有 `Backup=stage0` DLM 范围，每日 UTC 03:00，保留七份。
  首次手工双卷快照已均达到 completed/100%；首次自动快照完成并核验后清理带
  `Purpose=migration-acceptance` / `CleanupAfter=first-verified-dlm-snapshot` 的手工验收快照。
- 五台现役 AWS 的当前主机备份均已完成 S3 下载 SHA-256 比对、隔离完整 SQL 恢复、
  核心表计数记录和临时资源清理。四边静态数据的核心计数与在线源相同；
  prod 验收源为 `tokenkey-20261016T000016Z.sql.gz`（1,412,911,956 bytes），02:48 UTC 恢复完成。
  prod 恢复计数为 users=65、accounts=321、api_keys=463、groups=92、settings=320、
  usage_billing_dedup=15,403,966；现库持续写入，与后续在线计数不同，不把差异宣称为完全一致。
  修复 canary 对有分区 NVMe 整盘的 `lsblk` 父盘误判，四边已用修复版本实际执行。
- uk2 Redis 已改为 `unless-stopped`，容器 ID、启动时间和重启计数未变。
- 四台旧 AWS standby 的 pgdump/GHCR prune timer 已停，业务 systemd 开机入口禁用，
  `ARCHIVAL_HOLD` 阻止误启动；未来备份写独立 `archive-standby-20261016/legacy-<node>/pgdump`。
  历史混合前缀对象仍保留，恢复时必须绑定当前主机的 dump 文件名和 SHA，不能任取旧对象。
- prod QA boundary timer 已启用，首次运行未来 72 小时全覆盖；按现有归档门禁清理 23 个过期分区。
  maintenance 首次遇对象存储 GetObject 503；第二次于 02:28 UTC 成功完成：
  新 `2026-10-16T00:00Z` 小时 2,333 条记录、9,332 个 blob 引用，缺失 0、restore_verified=true；
  补偿小时 `2026-10-15T02:00Z` 5,246 条记录也通过恢复校验。随后解除本次临时 pause-drop，
  恢复原 hourly maintenance timer。独立 pin 仍为 1.8.288，不随网关升级。
- us5/prod 的独立 HTTPS timer 已安装；us5 已完成后续正常定时轮次，五入口均正常。
  本轮没有执行网关、Caddy、PG、Redis 发布或重启；同期观察到独立发布于 02:48 UTC
  将 prod 切至 green，02:50 UTC 验证健康。Caddy、PG、Redis 的启动时间未变。
- 三条绑定旧 EC2、无告警动作且禁用的 CloudWatch alarms 已备份定义并删除。
- 五台旧 Hcloud 主机的 GHCR prune timer 均 disabled/inactive；转发、凭据刷新及磁盘监控保留。

上述任务的 root-only 变更前状态留在对应主机 `/var/backups/tokenkey-migration-closeout-20261016/`；
HTTPS 安装备份在 `/var/backups/tokenkey-https-watch/`。云资源变更前状态与验收回执存本次运维工件。
未验收项不作完成声明。
四台旧 Hcloud edge 必须逐项核对冻结整库归档、真实请求残留、代理依赖及资源费用。
当前仅准备回收清单，尚未授权不可逆删除。关闭电源不停止 Hcloud 主机计费。
旧 Hcloud prod 尚有真实转发，不在本轮回收清单。


## 四台旧 Hcloud edge 回收准备（中低优先级）

| 旧节点 | Hcloud server ID | 随主机回收的 Primary IPv4 / IPv6 ID | 月度主机 + IPv4 上限（USD） |
| --- | --- | --- | --- |
| uk1 | 169538563 | 154555178 / 154555179 | 13.09 |
| uk2 | 169600837 | 154659121 / 154659122 | 13.09 |
| us4 | 169600845 | 154659134 / 154659135 | 13.09 |
| us5 | 169591296 | 154642440 / 154642441 | 13.09 |

当前价格 API 返回每台 CAX21 12.49 USD/月、IPv4 0.60 USD/月，合计 **52.36 USD/月**，
按小时计费时约 0.084 USD/小时（未计超额流量等变量）。四机未挂数据卷、未启用服务器备份；
Primary IP 均 `auto_delete=true`，删除主机将同时释放这些地址。共享网络与旧 prod 数据卷保留。

冻结整库的原始副本位于备份桶
`tokenkey-prod-pgdump-682751977094/edge/<node>/pgdump/archive-hcloud-20261015/frozen-full.dump`。
本次核实该桶对全部前缀应用 **7 天自动过期**，不能满足用户长期回查约定；
因此复制到已授权的既有归档桶 `tokenkey-prod-archive-682751977094`，
独立前缀为 `prod/legacy-node-archive/20261016-hcloud-edges/<node>/frozen-full.dump`。
每份复制前后均下载 SHA-256 比对，且核验大小、AES256 和无 Expiration header；
汇总校验清单位于同批前缀的 `manifest.json`。原备份副本未删除。
归档桶的 400 天规则仅覆盖 `prod/ops-archive/`，另有 `prod/raw-telemetry/` 的 120 天规则；
新 legacy-node-archive 前缀不匹配这些规则，当前无自动过期，按用户后续决定清理。
旧机数据库保持默认只读、业务自启动禁用；最近六小时 Caddy 日志样本中仅扫描/308 等，
未见成功业务请求，不能将其表述为完全无请求。
新 prod 的未删除账号与代理中，旧四机 IP 引用均为 0；活代理使用其他 AWS IP。
但四个新 edge 的运行中 Caddy `remote_ip` matcher 仍分别包含对应旧 Hcloud IP。
释放旧 IP 前必须先移除这项过渡来源白名单并验证当前代理/健康入口，防止旧地址被重新分配后仍被放行。
这涉及网关配置，受用户本轮“不动网关”约束，本次仅记录，未修改。

建议先取得四边过渡来源白名单调整与旧机删除的明确授权，再即时核查旧机访问与依赖，
确认归档仍可读；保留 S3 归档及恢复材料，
删除这四个 server 及随附 Primary IP，再注销它们专属的 SSM managed-instance 身份。
不删除共享 SSM role、网络、对象存储或旧 Hcloud prod。
默认至少保留旧机七天；提前永久回收须用户明确确认，不能把本次“可以考虑”当作删除许可。
