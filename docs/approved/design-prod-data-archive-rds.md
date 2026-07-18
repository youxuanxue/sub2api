---
title: Prod 数据归档与 RDS 迁移设计
status: pending
approved_by: pending
---

# Prod 数据归档与 RDS 迁移设计

> 本文件是生产数据层高风险变更的审批锚点。`pending` 期间允许完善代码、搭建
> 非生产环境和演练，但禁止生产切换。执行步骤单一归属
> `docs/deploy/aws-data-layer-migration.md`，本文只保存事实、决策和审批边界。

## 一句话结论

现在不是 CPU 或内存扛不住，而是本机磁盘的增长余量已经进入预警区，同时数据库
仍和一台 EC2 绑在一起。正式推进两条并行工作：

- **归档**负责把不常查的历史明细可靠搬到 S3，再从热库删除，控制长期增长。
- **RDS**负责把在线 PostgreSQL 从单机容器搬到托管数据库，获得 PITR、独立生命周期
  和更清晰的故障边界。

两者不能互相替代。只上 RDS 不归档，费用会随明细无限增长；只做归档不上 RDS，
本机数据库仍与 EC2/数据卷共享故障和维护窗口。

## 当前事实

以下为 2026-07-17 至 2026-07-18 对 prod 的只读观测，是本轮方案的容量基线，
不是永久常量：

| 项目 | 现状 | 判断 |
| --- | --- | --- |
| PostgreSQL | 数据库约 23.7 GiB；`usage_logs` 约 5.3 GiB | 旧方案的 3.3-6.2 GiB 小库假设已失效 |
| 增长 | `usage_logs` 按近 30 天行量外推约 3.6 GiB/30d | 不做归档时，单纯加盘只是推迟问题 |
| 数据卷 | 50 GiB，约 72% 已用，约 13.2 GiB 可用 | 容量 verdict=`approaching`，按当前外推约 3.7 个月余量 |
| QA | S3 export store 已在线；5 个手动 job 成功、4 个失败，成功覆盖 207,470 条；当前没有 auto job，`QA_CAPTURE_AUTO_EXPORT_ENABLED` 未启用 | “QA 已放 S3”对手动导出成立，但当前 7 天 TTL 是短期下载交付，不是每日长期归档 |
| QA 当前热数据 | 本地 blob 约 2.6 GiB / 90,946 文件，`qa_records` 约 0.89 GiB；随 2 天 cleanup 窗口波动 | 不再沿用单次 5.7 GiB 快照作为固定日增量 |
| 离机备份 | 珍贵数据 pgdump 已每小时进 S3，且最近对象新鲜 | 能灾难恢复，但明确排除了 usage/ops/QA 表数据，不能冒充历史归档 |
| Redis | 当前约 5.3 MiB，Redis 自报峰值约 11 MiB、cgroup 峰值约 33 MiB；live `appendonly=no` | 数据可重建，AOF 收益不足以单独制造停服窗口 |
| 容器日志 | app/Caddy 已是 `json-file 100m x 5`；PostgreSQL/Redis 旧容器日志约 346 MiB/13 MiB，仍无上限 | 增长慢，不作为 RDS 前独立停机理由；本机 PostgreSQL 在切换后直接退出 |
| PostgreSQL 内存/连接 | Docker working set 约 2.9 GiB，cgroup 峰值约 6.3 GiB；当前 DB backend 7、全部 activity 15 | 4 GiB `db.t4g.medium` 有压缩 page cache/触发换型的风险，推荐 8 GiB class |
| CPU/IO | EC2 14 天 CPU 均值约 13%，最忙 15 分钟均值约 67%；数据卷最忙 15 分钟约 1321 read IOPS + 389 write IOPS，读写吞吐约 17.8/7.1 MB/s | 2 vCPU 够用；RDS gp3 默认 3000 IOPS/125 MB/s 有余量，无需照搬现盘 6000 IOPS/250 MB/s |
| 数据构成 | `ops_system_logs` 约 12.7 GiB、`ops_error_logs` 约 2.9 GiB、`usage_logs` 约 5.4 GiB、`usage_billing_dedup` 约 1.8 GiB | 最大降本点是 raw ops retention，不是把所有日志长期搬到 S3 |
| 清理任务 | 系统日志清理心跳正常，累计回收 588,886 行 | 清理机制有效，但不覆盖所有需要长期保存的历史类型 |

结论：当前配置对计算资源仍合理，对数据耐久性和存储增长已经不够合理。正式推进，
但不以未经演练的生产切换来换速度。

## 三个概念别混在一起

| 能力 | 大白话 | 能解决 | 不能解决 |
| --- | --- | --- | --- |
| 备份 | 把数据库整体做成可恢复副本 | 误删、卷坏、整库恢复 | 便宜查询多年历史；当前 pgdump 也不含 usage/ops/QA 行 |
| 归档 | 把过期明细按时间切片搬到 S3，校验成功后再删热库 | 控增长、低成本保留历史、按批次取回 | 在线事务、分钟级恢复 |
| RDS | AWS 托管在线 PostgreSQL | PITR、升级维护、监控、与 app 主机解耦 | 自动决定保留多久；无限存明细仍会越来越贵 |

## 数据分级与候选保留策略

| 数据 | 在线 owner | 热库候选 | 冷存候选 | 删除前硬门槛 |
| --- | --- | --- | --- | --- |
| 用户、密钥、订单、余额、计费去重等核心 OLTP | RDS | 全量在线 | PITR + 珍贵类 pgdump | 不走历史归档删除 |
| `usage_logs` 请求明细 | RDS | **推荐 90 天** | **推荐 365 天**；S3 Standard 30 天后转 Glacier Flexible Retrieval | 对象、manifest、行数、checksum 全部验证成功 |
| `ops_system_logs` / `ops_error_logs` | RDS | **推荐 30 天** | **默认不冷存 raw 行**；长期趋势由聚合表保留，监管要求出现后再改 | cleanup 心跳和近期排障查询均正常 |
| QA records + blobs | RDS + 本地卷 + S3 | **推荐 2 天** | manual/auto ZIP 共用现有 Standard 7 天 TTL，不做长期冷存 | 每日 auto job 连续成功、ZIP 非空且对象可恢复后才 cleanup |

归档必须是确定性闭环：`导出 -> 上传 -> 校验 -> 记录水位 -> 删除/整分区 drop`。
任何一步失败都停在“保留热数据”，不能为了腾空间吞错或先删后验。

## 目标架构

```text
用户请求
   |
EC2: Caddy + app + local Redis
   | private VPC / TLS / 5432
RDS PostgreSQL
   |-- 14 天 PITR
   |-- 每小时珍贵类 pgdump -> S3（灾难备份）
   `-- 到期历史批次 -> S3 archive（历史归档）
```

Redis 暂时留在本机，因为数据量只有 MiB 级且可重建。上第二个 app 副本之前，或 Redis
成为真实事故源时，再单独评估 ElastiCache；本设计不预埋闲置资源。

## RDS 推荐配置与成本

推荐生产候选：PostgreSQL 18.1、`db.t4g.large`、Single-AZ、gp3 50 GiB、默认
3000 IOPS / 125 MiB/s、autoscaling ceiling 200 GiB、PITR 14 天。

| 方案 | 内存 / vCPU | us-east-1 按需计算 | 50 GiB gp3 | 月度合计 | 结论 |
| --- | --- | --- | --- | --- | --- |
| `db.t4g.medium` Single-AZ | 4 GiB / 2 | $47.45 | $5.75 | **$53.20** | 最省，但低于当前 6.3 GiB cgroup 峰值；不推荐直接生产起步 |
| `db.t4g.large` Single-AZ | 8 GiB / 2 | $94.17 | $5.75 | **$99.92** | **推荐**；CPU 不增加，内存匹配当前 working set，避免迁完立即换型 |
| `db.t4g.medium` Multi-AZ | 4 GiB / 2 | $94.17 | $11.50 | **$105.67** | 价格接近推荐项，但主库仍只有 4 GiB；先解决可用性却引入性能风险 |
| `db.t4g.large` Multi-AZ | 8 GiB / 2 | $188.34 | $11.50 | **$199.84** | DB 可用性最好，但 app 仍是单 EC2 SPOF，当前端到端收益不值翻倍成本 |

价格来自 2026-07-18 AWS Price List 的 PostgreSQL On-Demand、730 小时/月，不含少量
CloudWatch logs、跨 AZ 流量或超过免费额度的备份。上线稳定观察 30 天后，再评估 1 年
Reserved DB Instance；迁移前不锁长期承诺。

- 引擎：PostgreSQL 18.1，已在 `us-east-1` 只读确认可用，并与当前容器主版本一致。
- 计算：`db.t4g.large`。当前 CPU 不要求增加 vCPU，选择 large 只为 8 GiB 内存；若演练
  证明 `FreeableMemory` 长期大于 2 GiB 且读延迟无回退，才允许降到 medium 省约
  $46.72/月。
- 存储：初始 50 GiB，当前 23.7 GiB 数据约占一半；200 GiB ceiling 不预收费，作为
  归档故障时的保险丝。RDS 存储只能扩不能缩，不用 100 GiB 起步提前支付闲置空间。
- I/O：gp3 默认 3000 IOPS / 125 MiB/s 已高于 14 天实测上界，不购买额外 IOPS/吞吐。
- 连接：app 继续 `max_open=50`、`max_idle=10`；连接告警推荐 **120**，覆盖蓝绿两实例
  短时重叠的 2x50 连接池并保留 20 个运维/后台余量。
- 备份：PITR 14 天；珍贵类 pgdump 继续每小时进 S3，形成不同机制的第二恢复路径。
- 网络：私有子网，`PubliclyAccessible=false`，安全组只允许 app SG 访问 5432。
- 可观测性：PostgreSQL logs、7 天 Performance Insights；`FreeableMemory < 1 GiB`
  持续 15 分钟告警，配合 CPU、连接、FreeStorageSpace、Read/WriteLatency 判断换型。
- 生命周期：独立 CFN 栈，`DeletionProtection` + `Retain`，避免 app 换机带走数据库。

### Single-AZ 与 Multi-AZ

**Single-AZ 起步的好处**是成本低，架构也简单；坏处是数据库维护或 AZ 故障可能造成
十几分钟级停机。**Multi-AZ 的好处**是 AWS 有同步备库并能自动切换；坏处是数据库
计算和存储成本接近翻倍，而且不能替代备份和归档。

本轮推荐 Single-AZ，因为 app 本身仍是单 EC2；只把 DB 升 Multi-AZ 不能形成端到端高
可用，却会让约 $100/月翻到约 $200/月。扩成第二 app 前必须同步把 RDS 升 Multi-AZ，
届时这笔成本才对应完整的可用性收益。审批人仍需明确接受当前阶段的 DB 维护/AZ 停机。

## S3 推荐配置与成本

现有 `tokenkey-prod-qa-exports-*` 是用户下载交付桶：Block Public Access、SSE-S3、
versioning suspended、`traj-exports/` Standard 7 天过期。该配置继续保留，不把它改名为
长期归档桶。

长期数据归档推荐独立 `tokenkey-prod-data-archive-*` 桶，避免用户下载对象和内部留存
共用权限/生命周期：

- Block Public Access 全开、TLS-only bucket policy、SSE-S3；暂不引入每月固定 CMK 成本
  和误删 KMS key 导致不可恢复的风险。监管明确要求密钥审计时再改 SSE-KMS + Bucket Key。
- 对象按 `dataset/year/month/day/batch-checksum` 唯一键写入，单批合并为大对象；上传用
  条件写防覆盖，manifest 记录 schema/version/行数/checksum。
- `usage/`：Standard 30 天，之后转 Glacier Flexible Retrieval，365 天过期。
- QA 继续使用现有 `traj-exports/<user>/<key>/{manual|auto}`：Standard 7 天后过期，
  不进 Glacier；这与代码 `autoExportArtifactTTL` 和 CFN lifecycle 一致，也减少用户内容
  暴露面，不为不同 TTL 再增加第二存储配置或对象标签状态机。
- `ops/`：默认不写 raw 行；30 天后在 RDS 删除。长期容量/错误趋势由 daily/hourly
  聚合表保留，避免为约 15.6 GiB 的最大日志族再建一条低价值恢复链。
- AbortIncompleteMultipartUpload=1 天；生命周期负责过期，运行角色只给最小
  Put/Get/List 权限，不给桶级任意删除。

按当前量级估算，`usage_logs` 3.6 GiB/月、保留一年进入 Glacier 后约 **$0.16/月**；
QA 当前 2 天窗口约 2.6 GiB，按同速保留 7 天约 **$0.21/月**，即使按历史 6 GiB/日
上界也约 **$0.97/月**。S3 成本不是决策瓶颈，真正需要审批的是保留价值和敏感数据暴露
时间。以上使用当前 us-east-1 S3 Standard $0.023/GB-month、Glacier Flexible Retrieval
$0.0036/GB-month，不含极少量请求/恢复费。

## 搬迁方式

### 方案 A：停写后 dump/restore

大白话：关写入，把整库打包搬过去，对账后再开门。

- 优点：工具成熟、链路短、容易理解，数据一致性边界最清楚。
- 缺点：23.7 GiB 已不能凭感觉承诺短停机；dump、传输、restore、索引恢复和对账都在
  维护窗口里。
- 适用：完整演练证明总窗口在业务可接受范围内。

### 方案 B：DMS 或 PostgreSQL logical replication

大白话：先搬大部分存量，再持续追源库增量，最后只短暂停写做收尾切换。

- 优点：生产停写时间明显更短。
- 缺点：配置和监控复杂，DDL、sequence、大对象、复制延迟和失败恢复都要额外验证；
  不是“零停机按钮”。
- 适用：演练得到的 dump/restore 窗口超过业务允许值。

### 推荐：DMS full load + CDC

用户体验优先时，不等待 dump/restore 演练超时后才临时改方案，直接以 DMS full load +
CDC 作为推荐路径。源库当前 `wal_level=replica`，需要一次低峰 PostgreSQL restart 改为
`logical`；该前置窗口目标 **<60 秒**，不碰 Redis。随后在线搬全量并追增量，只有
CDC source/target latency 均 <5 秒并稳定 30 分钟、schema/sequence 对账通过，才进入
最终 drain，目标 **<=5 分钟**。

DMS 推荐 `dms.t3.medium` Single-AZ，当前按需价 $0.0745/小时：跑 72 小时约 $5.36，
跑一周约 $12.52（另加少量临时存储）。切流前源库仍是真相，临时 DMS 失败可以重建，
不为它支付 Multi-AZ 双倍价。完成和验证后删除；仍必须演练 replica identity、DDL、
sequence、大对象、失败重放和 WAL 堆积。dump/restore 保留为恢复/校验路径，不再作为
生产首选切换方式。

## 切换状态机与回退原则

```text
设计待批 -> 非生产演练 -> 生产预检 -> 停写/搬运 -> 对账
                                             | 对账失败
                                             `-> 仍未开放写入：撤回到本地库
对账通过 -> 指向 RDS -> 开放写入 -> 观察/前向修复
                              |
                              `-> 只有完成 RDS 增量反向回放，才允许回本地库
```

“一键切回旧本地库”只在 **RDS 尚未接收任何新写入** 时成立。一旦开放写入，旧库已经
落后，直接切回会丢用户、计费和用量数据。此后默认是前向修复；若确需回本地，必须先
有经演练的反向增量回放和逐表对账，不接受“允许丢一点”的普通 rollback。

## Redis AOF 与日志轮转：不单独停服

仓库 compose 已声明 Redis `appendonly yes`、`appendfsync everysec`，以及所有容器
`json-file 100m x 5`，但 live Redis/PostgreSQL 仍是旧容器。Docker 只在 recreate 时应用
logging driver；这不等于值得为“配置一致”主动停服。

本轮推荐：

1. **RDS 前不安排 Redis/PostgreSQL recreate 窗口。** Redis 数据只有 MiB 级且可重建，
   PostgreSQL/Redis 一个月日志约 346/13 MiB；收益小于一次用户可见停服风险。
2. DMS 前置窗口只重启 PostgreSQL 以启用 `wal_level=logical`，不顺带改 Redis、不为即将
   退役的本机 PostgreSQL 重建 logging driver，减少同窗变量。
3. 最终 RDS cutover 也保持 Redis 原容器不动。切换成功后本机 PostgreSQL 直接退出，
   它的无界日志问题随之消失。
4. Redis AOF/日志轮转改为**机会式收敛**：下一次本来就必须 recreate Redis 或替换主机
   时再自然应用，不为它单开维护。若未来上第二 app，直接评估 ElastiCache + Multi-AZ
   RDS，不继续给单机 Redis 叠加局部补丁。

代价是当前 Redis 仍可能在主机/容器故障时回到最近 RDB，丢少量可重建缓存/计数；这是
明确接受的风险，不影响 PostgreSQL 账本、余额和计费真值。app/Caddy 已有日志轮转，
数据卷增长的主要风险仍是数据库历史表，不是 Redis 日志。

## 明确不做

- 本 PR 不执行任何生产 CFN、SSM、容器重建、RDS 创建、S3 删除或数据库写操作。
- 本轮不迁 Redis 到 ElastiCache，不上第二个 app，不承诺零停机。
- 不把现有珍贵类 pgdump 改名成“归档”，也不在未校验 S3 对象前删除热数据。
- 不在设计 `pending` 或演练未完成时启用生产 cutover 脚本。

## 审批门

- [ ] 审批推荐保留值：usage 热 90 天/冷 365 天、raw ops 热 30 天不冷存、QA 热 2 天、manual/auto S3 均 7 天。
- [ ] 审批 RDS 推荐值：`db.t4g.large`、Single-AZ、50 GiB gp3、200 GiB ceiling、14 天 PITR、连接告警 120，预算约 $99.92/月。
- [ ] 明确接受 app 单 EC2 阶段的 Single-AZ 停机风险；上第二 app 时同步升 RDS Multi-AZ。
- [ ] 审批低停机目标：logical 前置 restart <60 秒，DMS CDC 稳定后最终 drain <=5 分钟。
- [ ] 审批“不为 Redis AOF/日志轮转单独停服，RDS 迁移期间 Redis 不动”。
- [ ] 归档闭环完成非生产验证，证明失败时不删热数据。
- [ ] 切换、故障注入、PITR 和“开放写入后禁止旧库回滚”均已演练。
- [ ] 单独批准生产创建与切换窗口；批准 PR 设计不等于批准立即执行生产切换。
