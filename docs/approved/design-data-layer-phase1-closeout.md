---
title: Data-layer 第一阶段收尾
status: approved
approved_by: "xuejiao (phase 1 items 1/2/3 approval, 2026-08-03)"
approved_at: 2026-08-03
authors: [agent]
---

# Data-layer 第一阶段收尾

## 决策

第一阶段只完成三个闭环：分区与健康门禁、归档覆盖与恢复证明、原始 telemetry 的日分区和
S3 冷层。RDS 设计、资源、分支、迁移和切换全部 hold，不以容量告警自动触发。

账务状态、余额、幂等键和聚合结果继续由 PostgreSQL 负责；S3 只接收可重放的原始
telemetry 副本。任何 S3、归档或健康检查失败都不得阻塞在线请求，也不得授权删除热数据。

## 分区维护

`OpsCleanupService` 仍是单一调度 owner，但一次 tick 分成两个独立动作：

```text
cron + leader lock
  -> partition maintenance -> ops_partition_maintenance heartbeat
  -> cleanup_enabled=false: stop
  -> cleanup_enabled=true  -> retention/drop/delete -> ops_cleanup heartbeat
```

关闭 cleanup 只关闭删除，不再关闭 cron。分区维护为分区化的 ops 表创建当前月和未来月；
`usage_logs` 完成显式切换后创建当前日和未来日。维护失败只写维护心跳，不伪造 cleanup
运行记录；cleanup hold 期间 `ops_cleanup` heartbeat 必须保持不动。

日常诊断 fail closed：当前或未来分区缺失、分区维护心跳过期、pgdump 或 EBS snapshot
过期、归档 ledger 落后、cleanup hold 长期未收口、恢复演练证据过期，均产生独立 finding。
容量 green 不能覆盖这些错误。

## 归档与回收

复用现有 export ledger、promote ledger 和长期 archive bucket，不引入第二套 exporter。
ledger 的 `more_cold_rows_remaining=false` 只是当次水位结论；时间推进后允许从原 cursor
继续导出新变冷的尾部。

回收状态机固定为：

```text
active cleanup hold
  -> export cursor 继续到目标分区上界，最终 cutoff >= partition upper bound
  -> 每个 batch promote，manifest checksum 与 export ledger 一致
  -> 从长期 archive 随机下载一个 promoted batch
  -> 独立 PostgreSQL restore，row count + logical checksum 一致
  -> closeout receipt
  -> guarded cleanup-hold release
  -> runtime retention 才可 drop 已过期分区
```

任一检查失败时保留 hold 和热数据。closeout receipt 绑定表、分区上界、两个 ledger、随机
恢复 batch 和 checksum；普通 cleanup release 不是归档回收入口。

## Usage 日分区

`usage_logs` 用 attach-legacy 方式切换，不复制历史表。切换前在线创建并验证一个固定上界的
CHECK constraint；切换只做短 catalog lock：旧表改名、创建按 `created_at` 日分区父表、附加
legacy、创建未来日分区。

分区父表不能保留不含分区键的全局唯一约束。账务幂等继续由窄表
`usage_billing_dedup` 负责；usage 明细写入改为分区内冲突忽略，并保留 `(request_id,
api_key_id)` 普通查询索引。旧的 `billing_usage_entries.usage_log_id` 只保留索引与逻辑关联，
不再作为跨分区外键。

切换由显式 operator CLI 执行，要求精确确认串、数据库时钟派生上界、已验证 CHECK、锁等待
上限和前后行数校验。它不随应用启动自动执行。

## Telemetry S3

应用在 PostgreSQL 写成功后，把 usage、ops error、ops system 三类事件放入一个有界内存
队列。后台按批次生成 gzip JSONL 并直接 `PutObject` 到专用 archive prefix：

```text
PostgreSQL success -> non-blocking enqueue -> gzip batch -> S3
                                  | queue/full/upload error
                                  `-> metric/log + drop S3 shadow copy; OLTP remains successful
```

功能默认关闭；只有 bucket、region、prefix 完整且显式启用时启动。对象 key 按 dataset 和 UTC
入队日期分层。每条 JSONL 使用 `schema_version=1` envelope，保留 dataset、入队时间和原始
payload；对象 metadata 写入 schema version、record count、首尾入队时间和 gzip body 的
SHA-256。应用 instance role 对 raw telemetry prefix 只有 `PutObject`，没有 read/list 权限。

所有默认值只采用下面这一组，代码、Compose 和 env 示例必须机械保持一致：

| 配置 | 默认值 |
| --- | ---: |
| `enabled` | `false` |
| `region` / `bucket` | 空 |
| `prefix` | `prod/raw-telemetry` |
| `queue_size` | `8192` |
| `queue_max_bytes` | `33554432` |
| `max_event_bytes` | `1048576` |
| `batch_size` | `256` |
| `worker_count` | `4` |
| `flush_interval_seconds` | `5` |
| `put_timeout_seconds` | `10` |
| Glacier transition / expiry | `8` 天 / `120` 天 |

record slot 和 `max_event_bytes` budget 均在 JSON 序列化前预占，饱和请求不再消耗额外序列化
CPU/内存；序列化成功后退还未使用的 byte budget。已排队和上传中的 payload 同时受 record
count 与 serialized byte 两个硬上限保护。4 个固定 worker 独立批量上传，失败
只累计 shadow loss，不回滚 PostgreSQL。

启用后，唯一健康 owner 每分钟把累计统计写入 `ops_job_heartbeats` 的
`telemetry_archive_shadow` 行，JSON 统计存入 `last_result`。日常诊断要求 3 分钟内存在 clean
heartbeat，且 dropped/failed 均为零；配置已启用但 runtime 未启动、心跳缺失/过期、统计非法
或出现任何 loss 时均 fail closed。S3 shadow 连续稳定后才可另行审批缩短 PostgreSQL raw
retention；本阶段不把 S3 变成账务 source of truth。

## 回滚边界

- 分区维护回滚：关闭新维护逻辑不改变已创建的空分区；不得删除分区。
- archive 回滚：不 release hold；staging/promoted 对象保留并可幂等重跑。
- usage 切换前回滚：删除未使用的预验证 CHECK；切换后不自动改回普通表，需独立审批。
- telemetry 回滚：关闭 feature flag；PostgreSQL 写路径不变。
- 任意生产 schema、release、drop 都必须经过 preflight、人工合并批准和 Stage0 rollout。

## 验收

- cleanup disabled 时未来分区仍创建，只有维护 heartbeat 更新。
- cleanup enabled 时维护成功后才执行 retention，两个 heartbeat 独立。
- 归档尾部可从已完成 ledger 的 cursor 继续；未达到分区上界不得 closeout。
- promote checksum 或随机 restore 不一致时拒绝 release。
- usage 切换脚本拒绝未验证约束、错误上界、活跃锁和行数漂移。
- telemetry 未配置时零行为变化；队列满或 S3 失败不影响 PostgreSQL 成功。
- telemetry 默认值在 Go、Compose、env 示例和设计表中完全一致；启用后没有新鲜零损失心跳则
  protection gate 不得为 green。
- RDS 相关文件、资源和 branch 无改动。
