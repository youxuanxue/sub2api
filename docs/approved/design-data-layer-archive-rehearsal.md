---
title: Data-layer 非生产归档恢复演练
status: approved
approved_by: "xuejiao (phase 2 + phase 3 approval, 2026-07-20)"
approved_at: 2026-07-20
authors: [agent]
created: 2026-07-20
---

# Data-layer 非生产归档恢复演练

## 决策

SQLite 是确定性基线，第三阶段接入隔离的非生产 PostgreSQL；两者都证明冷数据能够被确定性
筛选、封口、校验并恢复，第三阶段额外记录真实查询耗时、数据量、压缩率和独立 PostgreSQL
恢复校验结果。本设计不接入对象存储、workflow/schedule/runtime，不执行任何生产命令，也不
提供删除能力。

SQLite 源和恢复目标必须以 read-only URI / `query_only` 打开。PostgreSQL 源只允许 localhost
Docker 数据库和专用 sentinel；生产 PostgreSQL、S3、生产 canary 和删除分别进入新的审批阶段，
不能由本阶段 merge 自动激活。

### Phase 3 PostgreSQL 边界

- CLI 入口为 `snapshot-postgres`，只接受 `postgresql://` localhost DSN；源数据库必须精确为
  `tokenkey_archive_rehearsal`，并存在 `archive_rehearsal_sentinel(label='tokenkey_archive_rehearsal')`。
  远程主机、生产数据库名、libpq 非 URI 和缺 sentinel 均 fail closed。
- 只读事务按白名单读取 `usage_logs`、`ops_system_logs`、`ops_error_logs`、`qa_records` 的
  `created_at` 冷数据；设置 2 秒锁等待、显式 statement timeout 和最大导出行数。
- 工件仍使用 canonical JSONL + gzip + manifest + SHA-256；manifest 标记
  `source_mutated=false`、`deletion_authorized=false`，并记录 logical/artifact bytes、压缩率和
  PostgreSQL 查询指标。ops 表记录 ID 以表名前缀隔离，避免跨表冲突。
- 恢复目标 DSN 必须是同一 localhost Docker PostgreSQL 上以 `tokenkey_archive_restore_` 开头的
  独立数据库。仅创建 `archive_rehearsal_restored` 并幂等插入；冲突在写入前拒绝，恢复后重新读取
  并校验行数和 logical SHA-256。
- 全链路固定为 `dry-run -> seal -> verify -> restore-random`；没有 purge/delete、生产地址、S3、
  定时任务或自动部署入口。

## 工件契约

封口批次包含一个 `manifest.json` 和每个有冷数据的数据集对应的确定性 gzip JSONL：

源 SQLite 表固定为 `archive_rehearsal_records(dataset, record_id, created_at,
payload_json)`；dataset 只接受 `usage/ops/qa`，主键为 dataset + record_id，时间必须带时区，
payload 必须是 finite JSON。

- 数据集固定为 `usage`、`ops`、`qa`；默认热层分别为 90、30、2 天；
- 水位使用带时区的 `as_of`，仅选择严格早于 cutoff 的记录；UTC 标准化固定输出六位微秒，
  既不丢失精度，也保证 JSONL 与 SQLite 字符串排序等价于时间顺序；
- JSONL 逐行 canonicalize，按 `created_at, record_id` 排序；
- manifest 记录行数、时间范围、logical/artifact 字节数与双 SHA-256；
- 批次 ID 从水位、保留期、数据集行数和 logical checksum 派生；
- 相同输入重复封口复用同一已验证批次，内容不同则拒绝覆盖；
- manifest 永远写入 `source_mutated: false` 与 `deletion_authorized: false`。
- 报告/receipt 不得覆盖源库、恢复库或 sealed batch；源路径指纹与文件 device/inode
  纳入批次身份，恢复目标不得通过原路径、symlink 或 hard link 指回源文件。

## 状态机

```text
local SQLite / nonprod PostgreSQL --read-only--> dry-run 水位报告
  -> candidate=0: 拒绝封口
  -> candidate>0: 原子写临时目录 -> manifest + artifacts -> verify
       -> 校验失败: 拒绝发布批次
       -> 校验通过: sealed batch
            -> seeded random artifact -> independent local/PG restore DB -> row/checksum verify
                 -> 冲突/损坏: rollback + fail closed
                 -> 一致: restore receipt；重复恢复为幂等复用
```

SQLite CLI 只允许 `dry-run`、`seal`、`verify`、`restore-random`；PostgreSQL 只增加
`snapshot-postgres` 全链路入口。不存在 purge/delete 子命令、生产地址、AWS、对象存储、定时任务
或自动部署入口。

## 恢复验收

- 归档压缩文件损坏、路径逃逸、manifest 汇总漂移或 logical checksum 不一致时，恢复前拒绝；
- 随机恢复必须以显式 seed 选择一个数据集，结果可复现；
- 恢复表以 batch/dataset/record 唯一键幂等写入，重复运行不新增行；
- 目标已有同键异值时，checksum 校验失败并回滚本次写入；
- dry-run 和封口前后源 SQLite 文件内容 checksum 保持不变；
- PostgreSQL 演练源只执行 read-only transaction，恢复目标是独立临时数据库；测试使用真实
  `postgres:18-alpine` 容器（无容器运行时则跳过集成层），不依赖生产服务或外部网络。

## 后续独立审批

本阶段通过后仍不能操作生产。后续顺序保持为：严格限时只读生产快照、生产导出但不删除的
单批 canary、随机恢复验证、单独批准后的小批删除。扩盘、归档 canary 和删除互不授权。
