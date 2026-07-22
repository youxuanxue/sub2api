---
title: 生产 ops 长期 archive bucket 与 promote
status: pending
approved_by: pending
authors: [agent]
created: 2026-07-22
---

# 生产 ops 长期 archive bucket 与 promote

## 决策

export staging（`tokenkey-stage0-backups` / `prod/pgdump/archive-export/`）仅 **7 天**
周转；长期归档使用 **独立 S3 桶**，一条 canonical promote 路径，drop 分区前必须
promote receipt 齐全。

不接入 workflow / schedule / runtime；v1 为显式 CLI + CFN 栈。

## 固定边界

### 两桶分工

| 桶 | CFN 栈 | 前缀 | 保留 |
| --- | --- | --- | --- |
| pgdump 备份桶（现有） | `tokenkey-stage0-backups` | `prod/pgdump/archive-export/` | **7 天** Standard，不变 |
| 长期 archive 桶（新建） | `tokenkey-stage0-archive` | `prod/ops-archive/{batch_id}/` | **90 天** Standard → **91–400 天** Glacier → **400 天**删除 |

### Promote

- 唯一入口：`ops/archive/data_layer_archive_promote_batch.py`
- 确认串：`tokenkey-prod-archive-promote-batch-v1`
- 从 staging 已提交 batch（manifest 最后上传）复制到 archive 桶；逐对象 head 校验
  `sha256` metadata 与 manifest 一致
- 输出 `prod_archive_promote_receipt`；`promote-ledger` 按 export ledger 批量推进
- `source_mutated=false`、`deletion_authorized=false` 不变

### Drop 分区门禁（本文件不实现 drop）

以下 **全部** 满足后才可批准 legacy 分区 drop：

1. 对应表 export ledger：`more_cold_rows_remaining=false`
2. ledger 中 **每个** `batch_id` 有 promote receipt，且 archive 前缀 manifest
   sha256 与 export 一致
3. cleanup hold 仍有效，或 drop 走单独审批工单

## 状态机

```text
plan (no AWS mutation)
  -> resolve staging prefix + archive bucket from CFN outputs
  -> show lifecycle contract (90d Standard / 400d expire)

promote
  -> refuse without confirmation token
  -> head/download manifest from staging; verify_batch semantics (export mode)
  -> cp artifact(s) then manifest last (same order as export upload)
  -> head verify archive copies
  -> write promote receipt

promote-ledger
  -> load export ledger + promote ledger
  -> for each completed export batch missing receipt: promote
  -> update promote ledger atomically
```

## CFN

- 模板：`deploy/aws/cloudformation/stage0-archive.yaml`
- 栈名：`tokenkey-stage0-archive`
- 参数：`AppInstanceRoleArn`（与 backups 栈相同模式）、`ArchiveStandardDays=90`、
  `ArchiveGlacierTransitionDay=91`、`ArchiveExpireDays=400`
- 桶名：`tokenkey-prod-archive-${AccountId}`
- 输出：`BucketName`、`ArchiveS3Uri`（`s3://.../prod/ops-archive`）

Operator 从工作站 promote 时使用 IAM 用户凭证（与 export 控制面相同）；桶策略授予
`AppInstanceRoleArn` 读写 `prod/ops-archive/*` 以备将来 SSM 路径扩展。

## 验收

- `plan` 不调用 mutating AWS API
- promote 拒绝错误 batch id、缺失 manifest、checksum 漂移、未确认
- promote-ledger 幂等：已 promote 的 batch 跳过
- 单测覆盖 plan / promote 负向 / receipt 校验
- CFN 模板通过 `aws cloudformation validate-template`
