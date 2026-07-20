---
title: Data-layer 非生产归档恢复演练
status: approved
approved_by: "xuejiao (phase 2 approval, 2026-07-20)"
approved_at: 2026-07-20
authors: [agent]
created: 2026-07-20
---

# Data-layer 非生产归档恢复演练

## 决策

第二阶段只实现本地与非生产归档演练，证明冷数据能够被确定性筛选、封口、校验并恢复。
本阶段不连接 PostgreSQL 或对象存储网络端点，不接入 workflow/schedule/runtime，不执行
任何生产命令，也不提供删除能力。

演练源和恢复目标均为本地 SQLite 文件。源库必须以 read-only URI 和 `query_only` 打开，
既能验证真实数据库筛选/恢复行为，又从能力边界上排除生产 PostgreSQL。未来的 PostgreSQL
导出适配器、S3、生产 canary 和删除分别进入新的审批阶段，不能由本阶段 merge 自动激活。

## 工件契约

封口批次包含一个 `manifest.json` 和每个有冷数据的数据集对应的确定性 gzip JSONL：

源 SQLite 表固定为 `archive_rehearsal_records(dataset, record_id, created_at,
payload_json)`；dataset 只接受 `usage/ops/qa`，主键为 dataset + record_id，时间必须带时区，
payload 必须是 finite JSON。

- 数据集固定为 `usage`、`ops`、`qa`；默认热层分别为 90、30、2 天；
- 水位使用带时区的 `as_of`，仅选择严格早于 cutoff 的记录；
- JSONL 逐行 canonicalize，按 `created_at, record_id` 排序；
- manifest 记录行数、时间范围、logical/artifact 字节数与双 SHA-256；
- 批次 ID 从水位、保留期、数据集行数和 logical checksum 派生；
- 相同输入重复封口复用同一已验证批次，内容不同则拒绝覆盖；
- manifest 永远写入 `source_mutated: false` 与 `deletion_authorized: false`。
- 报告/receipt 不得覆盖源库、恢复库或 sealed batch，恢复目标不得指回源库路径。

## 状态机

```text
local/nonprod source --read-only--> dry-run 水位报告
  -> candidate=0: 拒绝封口
  -> candidate>0: 原子写临时目录 -> manifest + artifacts -> verify
       -> 校验失败: 拒绝发布批次
       -> 校验通过: sealed batch
            -> seeded random artifact -> local restore DB -> row/checksum verify
                 -> 冲突/损坏: rollback + fail closed
                 -> 一致: restore receipt；重复恢复为幂等复用
```

CLI 只允许 `dry-run`、`seal`、`verify`、`restore-random`。不存在 purge/delete 子命令，
也不存在 prod environment 选项、网络 DSN、AWS、Docker、`psql` 或数据库 DDL/DML 删除入口。

## 恢复验收

- 归档压缩文件损坏、路径逃逸、manifest 汇总漂移或 logical checksum 不一致时，恢复前拒绝；
- 随机恢复必须以显式 seed 选择一个数据集，结果可复现；
- 恢复表以 batch/dataset/record 唯一键幂等写入，重复运行不新增行；
- 目标已有同键异值时，checksum 校验失败并回滚本次写入；
- dry-run 和封口前后源 SQLite 文件内容 checksum 保持不变；
- 测试使用临时本地数据库，不依赖手工服务或外部网络。

## 后续独立审批

本阶段通过后仍不能操作生产。后续顺序保持为：严格限时只读生产快照、生产导出但不删除的
单批 canary、随机恢复验证、单独批准后的小批删除。扩盘、归档 canary 和删除互不授权。
