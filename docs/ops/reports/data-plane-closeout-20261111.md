# 数据面切流收尾核验：2026-11-11

执行基线：`77f6f14977222171b5aba41ae99b8d078f01c42c`，两站初始在服 1.8.359。
收尾分支已快进对齐核验时的 `origin/main` / `016ae75fad88d816614439cfaf9cac5fbc929c33`。
范围由 2026-11-11 会话明确批准：新时间线恢复验收、PG 配置对齐、prod 下一轮分区维护、
归档收敛后的并发索引重建、旧资源核账与文档更新。
现网身份、地址和规格以 [舰队入口](../../../deploy/hetzner/README.md) 为准。

## QA

2026-11-11 01:29–01:32 UTC：prod 最近一小时 2,777 条、cn-prod 1,764 条记录为 captured，
最新记录时间持续推进。provision 预建 72/72 小时，cleanup 成功，无阻塞小时；
两站上轮归档均成功、完成 24 个小时，失败列表为空。prod 最新归档 manifest 与 node1 实物摘要一致；
cn-prod 最新 hot_ssd 归档回执成功，本次没有独立校验其远端 manifest。

这不表示 QA 全绿：`qa_capture` 心跳保留 `runtime_discontinuity`，
prod / cn-prod 分别有 3 / 11 条 unresolved failure。实时链路工作与历史完整性缺口必须区分，
本轮未删除或改写历史 failure。

## 分区维护

prod 2026-11-11 02:00:00 UTC 定时维护成功，02:01 UTC 的数据机探针确认：
`usage_logs`、`ops_error_logs`、`ops_system_logs` 当前和未来覆盖六项全部为 true，
维护无错误；`usage_logs_20261118` 上界为 2026-11-19 00:00 UTC。
先前少一天的未来覆盖已由正常定时任务补齐。

## 旧资源核账

- 两台应用机 `docker ps -a` 均没有旧 PG、旧 Redis 或迁移转发器；已知旧 PG/Redis 目录不存在。
- `tokenkey` socat 是当前蓝绿控制代理，数据库/Redis 客户端容器也仍在使用，不能按旧转发器删除。
- 应用机保留旧 runner、停用的备份/身份 timer、切流前回执和 `.env.pre-dataplane-*`。
  应用机不是退役实例；prod Docker Root、应用文件和 `.env` 仍在 LUKS 上。
- 数据机承接 PG、Redis、备份、归档检查与身份续期；应用机保留 QA 和网关。
- 云端实例规格与续费状态已核账：两台应用机分别 11 月 21 / 22 日到期，两台数据机 12 月 9 日到期，均自动续费。
- 两台数据机 5432/6379 只绑定私网，云防火墙只放行本站应用机私网来源；
  早期检查时主机 DOCKER-USER 为空；本日后续已按用户授权补齐并验证来源规则，见下文。

本轮没有删除云实例、旧材料、历史备份，也没有迁出 LUKS 或重启应用机。

## 运维工具和验证

恢复工具限定当前时间线，排除预建分区对子表/约束数量的正常变化；
临时实例有独立网络、无发布端口、关闭 archive_mode，仅挂载临时 PGDATA 和只读 COS 配置。
PG 配置工具从容器命令渲染恢复文件内重复设置，热加载后检查有效值不变、未重启、无 pending_restart。
去重工具从现有 Go owner 读取归档/超期清理 SQL、7/30 天保留期与每批 10,000 / 每轮 1,000,000 行上限，
复用 cleanup Redis 锁；先按原策略清理超过 30 天的热键，再归档；固定截止时间收敛后才允许重建热表唯一索引和主键。

专项单元测试通过。真实 PostgreSQL 18 本地实验验证了 PG 配置热加载、
归档键保留/冲突处理/近期键保留，以及不停库并发重建索引。
恢复过程发现并逐项核账了两类历史差异：

- cn-prod 全量点处于 `usage_logs` 日分区转换之前，多出的约束恰为
  `usage_logs_pkey` 和 `billing_usage_entries_usage_log_id_fkey`。隔离恢复比对没有其他约束差异；
  `ops/backup/restore-schema-baselines/cn-prod-20261110.json` 精确绑定标签和时间线，最新点仍严格匹配线上 schema。
- prod 全量点的 268 个有效 API key 在当前库全部仍存在，核验时其中 15 个已软删除。
  所以恢复比较改为包含软删除记录的物理总数。
  备份边界有 1 条已抢占去重键、用量尚未落地的请求；该键在当前库已有对应的用量记录。
  对账窗口改为每个备份开始前已完成的整小时，两侧使用完全相同的上下界，保留总量和成本校验。

## 已执行结果

完整数值、时间与恢复点见 [机器可读回执摘录](data-plane-closeout-20261111.json)。

| 站点 | 时间线 | 历史点 / 最新点恢复 | PG 配置 |
|---|---|---|---|
| prod | 4 | 均通过，总耗时 980.826 秒 | 7 个文件配置项对齐；有效值不变，未重启，pending_restart=0 |
| cn-prod | 2 | 均通过，总耗时 278.107 秒 | 同上 |

prod 最新验收点 `20261110-013449F_20261111-023004I`，
cn-prod 最新验收点 `20261110-013338F_20261111-023005I`；历史点分别为对应全量。
每个恢复点的计费封闭窗口内，去重键与用量行数完全一致。临时资源均已清理。

归档收敛以固定截止时间 **2026-11-04 03:00 UTC** 判断。
cn-prod 补归档 **25,292** 条后积压清零，串行重建唯一索引和主键均成功、有效：
唯一索引 93,790,208 → 30,613,504 bytes，主键 23,101,440 → 8,904,704 bytes，
重建耗时 2.09 秒，主库未重启。

prod 首次只读守卫因膨胀堆表扫描触发 15 秒查询超时，在写入前退出；
随后把只读证明的语句时间预算提高到 120 秒，确认仍有超期热键，改为先调用现有 purge owner 清理；归档和 30 天守卫统一使用固定时间锚点。归档批次的锁/语句时间限制与 20 分钟操作预算保持不变。

prod 按原 30 天策略清理 **29,054** 条超期热键，再补归档 **896,791** 条，两个动作均确认 remaining=false。
随后串行执行 `REINDEX INDEX CONCURRENTLY`，唯一索引与主键均 valid/ready：
唯一索引 2,735,136,768 → 56,827,904 bytes，主键 695,959,552 → 16,515,072 bytes，
重建耗时 4.621 秒，主库未重启。两站归档与重建回执均保存在数据机 `/var/lib/tokenkey/postgres-backup/`。

05:56 UTC 公网 `/health` 两站均为 ok；在服容器无重启/OOM。
最近 15 分钟 QA captured 数为 prod 1,075、cn-prod 61，最后记录持续推进；
历史 unresolved 仍为 3 / 11，无 pending、inflight 或 unknown-format drift。

两站最终数据机检查均为主库、pending_restart=0、WAL 归档失败数为 0；prod 可用磁盘 74G，cn-prod 57G。

恢复、配置和索引验证使用专项测试及完整 preflight；执行基线上的代码门禁 PASS（524 秒）。
早期对齐后的门禁曾因本机争用中断；本日后续已在 `e4849f543` 基线上完成完整门禁（含来源规则锁修复），PASS 257 秒。
变更提交状态以 git 为准。
备份专项 33 项、配置专项 1 项、去重专项 3 项通过；真实 PostgreSQL 18 实验验证热加载、
超期键清理不进入归档、归档冲突保护、近期键保留与并发重建。


## 应用存储准备与来源规则（09:36 UTC 更新）

用户已同意安全迁出准备与两站主机来源规则。存储切换未执行。

- 两站 `tokenkey-data-source-acl.service` 已 committed、active、enabled，DOCKER-USER 和 INPUT
  仅限制本站私网地址上的 PG/Redis 端口；其它 Docker 与恢复出口规则保留。
  隔离真实 TCP 已验证允许/拒绝来源、DNAT/INPUT、无关端口与回滚。线上应用的 SQL、Redis PING、公网健康均通过。
- prod 首次持久服务启动暴露了父提交持锁、子服务尝试取同锁的问题。已修为释放锁后启动并检查 active；
  服务启动失败时立即移除自有规则并保存回滚状态。新增锁重入与启动失败回归；prod 服务已现场重启验收，
  cn-prod 用修复版本首次提交通过。未重启 Docker 或数据库。
- prod 在线预复制实际传输 12,811,464,329 bytes，耗时 425.939 秒；后续 checksum dry-run 扫描 213.826 秒，
  需最终同步的文件总量为 644,137,981 bytes。在线源持续写入，副本明确标记 consistent=false。
  源与目标都保留，不能用在线副本直接启动。副本位于根盘私有 `/var/lib/tokenkey-luks-exit/staging`。
- 隔离复制与反向同步验证权限、UID/GID、ACL、xattr、hardlink、sparse；独立 overlay2 daemon 实际重启后，
  容器 ID、bind、命名卷、匿名卷保持一致，备用容器保持停止。隔离资源已清理。
- 09:36 UTC 两站公网健康 ok、PG 主库、Redis PONG、活动网关重启 0 / OOM false；
  当前 QA runtime pending/inflight/failed 都为 0。历史 unresolved 仍为 prod 3、cn-prod 11，未核销。

LUKS 的具体单窗步骤、回退边界与审批范围见[迁出方案](../prod-app-luks-exit-plan.md)。
