---
title: Data-layer 生产只读导出 canary
status: approved
approved_by: "xuejiao (phase 4 approval, 2026-07-21)"
approved_at: 2026-07-21
authors: [agent]
created: 2026-07-21
---

# Data-layer 生产只读导出 canary

## 决策

在不迁移 RDS 的前提下，先用一个严格有界的生产 `ops` 冷数据批次证明：生产源只读、
离机封口、对象校验和独立 PostgreSQL 恢复能够闭环。本阶段只增加显式人工入口，不接入
workflow、schedule、deploy 或 runtime，也不提供 `DELETE`、`DROP PARTITION`、`TRUNCATE`、
`VACUUM` 或清理能力。

PR #587 的 RDS/cutover 工件不进入本路径。Phase 3 的 nonprod CLI 继续只接受 localhost
rehearsal 数据库；生产能力放在独立入口，不能通过修改 DSN 绕过。

## 固定边界

- 控制面只接受 `us-east-1` 的 `tokenkey-prod-stage0`，并校验 EC2 tags 精确为
  `Project=tokenkey`、`Environment=prod`。
- 数据面只通过该实例本机的 `tokenkey-postgres` 容器访问数据库 `tokenkey`；连接建立时
  强制 `default_transaction_read_only=on`、`lock_timeout=100ms` 和有上限的
  `statement_timeout`。
- 数据表只允许 `ops_system_logs` 或 `ops_error_logs`，保留期固定 30 天；显式 `as_of`
  必须与数据库时钟接近，查询只选择严格早于 cutoff 的记录。
- 单批行数、logical bytes 和执行时间都有硬上限；任一上限、格式、时钟或只读证明失败
  都拒绝发布批次。
- 归档操作前必须通过 admin API 建立 cleanup hold：保留完整 advanced-settings，只将
  `data_retention.cleanup_enabled` 置为 `false`，并以运行时 reload 日志、DB 值和
  `ops_cleanup` heartbeat、leader lock 共同证明生效。receipt 固定绑定生产实例；canary
  每次运行前重验，正在运行或 hold 后才完成的 cleanup 均拒绝。
- canary 按 `(created_at, id)` 取一个确定性有界样本，封口不超过 `max_rows` 的首段，
  并记录首尾 key 与是否存在后续冷行；冷数据总量大于样本上限不是失败条件。
- 离机 staging 只使用 `tokenkey-stage0-backups` 的 `BucketName` 输出，key 固定在
  `prod/pgdump/archive-canary/`。artifact 先上传并校验，`manifest.json` 最后上传作为提交标记；
  S3 必须报告服务端加密。
- staging 桶的生命周期只适合 canary 证据，不是长期冷存储。任何生产删除前，必须另行批准
  长期保留策略和专用 archive bucket。
- 每次生产执行都要求精确确认串 `tokenkey-prod-archive-export-only-v1`；PR merge、plan 和
  历史审批都不能替代本次执行批准。

## 状态机

```text
plan (no AWS call)
  -> 参数/确认/目标不合法: refuse
  -> resolve exact prod CFN instance + verify prod tags
  -> verify cleanup-hold receipt + live setting + no lock/later heartbeat
  -> resolve backup bucket
  -> SSM host export
       -> read-only session proof + database clock proof
       -> bounded cold-row query
       -> canonical JSONL + gzip + manifest + SHA-256
       -> artifact upload/head verify
       -> manifest upload last
  -> download committed batch
  -> verify manifest/artifact
  -> restore one seeded artifact to tokenkey_archive_restore_* PostgreSQL
  -> reread + row count + logical SHA-256 verify
  -> local receipt
```

任一步失败都停止；源库始终不写。S3 中可能留下未提交的唯一前缀，但缺少 manifest 的前缀
不得用于恢复或作为删除证据。

## 验收

- plan 不调用 AWS、Docker 或 PostgreSQL，并明确输出 `execution_authorized=false`。
- cleanup hold 的 plan/apply/verify/release 有固定确认口令；apply 只改 cleanup bool，
  canary 缺 receipt、实例不匹配、当前 cleanup 已恢复或 hold 后又运行 cleanup 时均拒绝。
- 错误确认串、非 ops 表、远程/生产恢复目标、超量、热数据、时钟漂移和非只读会话均 fail closed。
- 多于 `max_rows` 的冷数据会产出确定性的首个有界样本，不会因存在后续行而拒绝；
  manifest 的排序、首尾 key 与 continuation 状态必须通过 verify。
- manifest 固定 `mode=prod_archive_export_canary`、`source_mutated=false`、
  `deletion_authorized=false`，并记录表、水位、查询耗时、行数、字节和压缩率。
- artifact/manifest 任一损坏、S3 未加密或 restore 内容冲突时，canary 失败。
- 真实 PostgreSQL 集成测试证明导出前后源行数与内容不变，恢复行数和 logical SHA-256 一致。

## 后续独立审批

canary 通过后仍不能删除。下一顺序为：完整导出两个 ops legacy 分区但不删除；到 2026-07-31
后重新确认整分区完全越过 30 天水位；批准长期冷存储保留策略；最后才单独审批分区 drop。
QA 数据必须把 `qa_records` 与 blob 成套归档；`usage_logs` 在出现 90 天冷数据前另做分区化设计。
