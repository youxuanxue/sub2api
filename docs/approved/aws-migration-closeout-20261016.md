---
status: approved
approved_by: user (conversation)
approved_at: 2026-10-16
scope: repair approved post-migration backup, timer and monitoring gaps; retire old Hcloud edges
---

# AWS 迁移收尾

用户已批准修复迁移审计发现的备份、timer、告警遗留，并将四台旧 Hcloud edge 回收列为中低优先级。
本次修复宿主运维设施并回收旧 edge；回收仅移除新 edge 对对应旧 IP 的过渡信任。
账单、余额、额度、订阅和应用镜像不变。
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
安装器接受 prod、cn-prod 角色，核对目标清单的主机名；先 dry-run，HTTPS 目标必须健康，本地任务的已有失败允许安装以便及时告警。
更新使用内容寻址的独立代码目录，避免覆盖仍可能被旧 timer 读取的模块。
传入现有 probe/delivery 三个 companion、fresh prod 清单及 `deploy/domestic/prod-target.json`；
AWS 凭据仅从主机现有 `.env` 读取，cn-prod 使用现有 `/opt/tokenkey/host-alerts.env`，不通过传输工具投递密钥。
`APPLY=yes-host-https-watch` 是安装 opt-in，`WATCH_ROLE=prod|cn-prod` 选择职责。
prod 与 cn-prod 互相监测生产入口；国内入口的当前目标与覆盖边界见 `deploy/domestic/README.md`。
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
初次审计时仅准备回收清单；用户随后明确要求安全推进回收，按下方完成记录执行。关闭电源不停止 Hcloud 主机计费。
旧 Hcloud prod 尚有真实转发，不在本轮回收清单。


## 四台旧 Hcloud edge 回收（用户已批准）

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
初次审计发现新 edge 的运行中 Caddy `remote_ip` matcher 仍包含对应旧 Hcloud IP。
用户随后明确要求“四台旧 Hcloud edge 尚未回收，安全推进”。该授权替代原先仅准备清单
和默认七天保留的安排；回收前只移除对应旧 edge IP 的过渡信任。
候选配置与运行配置逐项比较，必须仅少一个旧 IP，随后 validate / 热加载并校验健康；
旧 prod、旧 AWS 与当前 prod 信任保持原状，不发布应用镜像或变更账单。
共享 SSM role、网络、对象存储与旧 Hcloud prod 均不在回收范围。

## 二次审计修复（2026-10-16，用户批准）

- QA 超时根因是 AWS prod 仍沿用 Hetzner 归档目标；约 1.4 GB 的包经跨云连接仅约 6 Mbps。按用户纠正，只将日常新增归档改为同区域 AWS S3，不迁历史 QA，也不优化 Hetzner uploader。独立 pin 镜像保持 1.8.288。09:00:31 UTC 已切 AWS，`07:00Z` 小时 4,167 条记录完成真实归档与隔离恢复，缺失 blob 为 0。历史小时补偿仍报 `commit_existence_unknown`（2026-10-17 已在原存储找到有效归档，撤回空窗口判断，见 QA 运维入口），不将存储验收写成整体 QA 健康。具体小时归属与验收见 QA 运维入口。宿主 runner 同时修正 TERM/INT 与未完成 child 的退出回执，不能再把超时记为成功。
- 快照巡检从实际 EC2 ID 解析唯一已挂载非根 EBS，不能用旧 CFN 输出覆盖新机身份。目标矩阵的 prod 平台改为 AWS；已实查新数据盘的自动快照完成。
- 四个 edge 的 prune 不能 source 整个 dotenv（双 CIDR 含空格）；改为只解析镜像值。四台均已安装并运行 daily service 成功，正在运行的容器未变化。
- 本地 pgdump、磁盘指标、prune 及 prod QA job 纳入原两分钟宿主 watch，沿用连续失败、重复抑制与恢复通知；通知注明节点与任务。探针异常本身记失败，不吞掉其他任务检查。四边均已安装并通过无通知 dry-run；prod 于 09:22 UTC 安装，其他任务与 HTTPS 正常，QA maintenance 的历史补偿失败按真实状态纳入告警，不伪造绿灯。
- prod Redis 的原 compose 多行 shell 把 flags 当作后续命令，实际未启用 AOF。已在线配置 AOF/everysec 和 RDB `60 1`，首次 AOF rewrite 与写入均为 ok；容器未重启。主机 compose 和源模板均已修正启动 argv 与密码引用。**现有容器的 Docker Cmd 不可在线修改，仍需在单独维护窗口仅 recreate Redis 后才能证明重启持久性；不能只 restart 旧容器。**本轮不操作这次重建。

变更前状态：四边 `/var/backups/tokenkey-prune/`，prod `/var/backups/tokenkey-redis/`，各节点告警沿用 `/var/backups/tokenkey-https-watch/`。该轮未改历史主机保留策略，后续回收按下节用户授权执行。


## 本轮补漏与 Redis 决策（2026-10-16）

- 用户确认忽略 QA 历史状态，不回补历史 raw、不伪造成功或清空失败；日常新增 AWS S3 归档继续运行。
- 用户选择 **prod 保留 AOF / everysec，现有 RDB 暂不调整**。Redis 还保存登录刷新会话、
  限流与在途预留等状态，全关会丢失这些保护和会话连续性。开启会增加磁盘写入，fork / rewrite
  有短暂资源开销；everysec 仍有近期写入丢失窗口，不能代替 PostgreSQL 账单备份或高可用。
  本次实测 used_memory 约 5.6 MiB、AOF 约 48 MiB、最近 fork 约 1 毫秒，未见持久化错误。
  当前仅记录决策，不重建 Redis；容器旧 Cmd 的生效验证仍待单独短维护窗口。
- prod 的镜像清理执行脚本漏更新，本次按 daily service 实际选择的路径补装 main 版本，
  文件备份到 `/var/backups/tokenkey-prune/`。真实 daily service 执行成功，前后容器 ID / StartedAt 一致。
- 五节点宿主 watch 改为先读 systemd 属性、再取高精度 `CLOCK_MONOTONIC` 时间，
  避免任务在采样间启动及 `/proc/uptime` 厘秒截断造成假超时。新增时序回归和真实超时反例；
  已按原安装器安装并执行。历史 QA maintenance 失败照实保留，其他宿主任务与 HTTPS 检查正常。


## 旧 Hcloud edge 回收验收

2026-10-16 10:34–10:36 UTC（北京时间 18:34–18:36）逐台完成上述四台 server 删除，
云 API 确认四台 server 和八个随附 Primary IP 均不存在，各自 SSM managed-instance 已注销。
按本次核验价格减少约 52.36 USD/月的主机与 IPv4 上限费用；旧 prod、共享网络 / IAM / 桶不变。
回收前再次核验 S3 manifest、对象大小、SHA 元数据、无过期头；原有下载校验回执保留。
删除及注销回执汇总在同一归档前缀 `retirement.json`，冻结整库继续按用户约定自行清理。

即时依赖核查：prod 未删除账号和代理引用四个旧 IP 的数量均为零，旧机业务自启动和备份
均已禁用；六小时有界访问样本仅见 308 跳转，未见成功业务响应。
新 edge Caddy 候选 JSON 与运行配置的差异仅为删除对应旧 IP，validate / 热加载成功，
健康检查成功且容器 ID / StartedAt 不变；备份位于新 edge 的
`/var/backups/tokenkey-retire-edge-trust/`。旧 IP 已释放，**不得恢复含这些地址的白名单**。
承接账号的四条真实代理出口再次实测通过；账号、凭据、调度状态与账单均未修改。
