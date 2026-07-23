# US-040-ops-legacy-export-promote

- ID: US-040
- Title: Legacy 冷数据分批 export 与长期 archive promote
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **把 legacy 冷 ops 行分页 export 到 staging 并 promote 到长期 archive 桶**，**以便** 在分区 drop 前凑齐 export + promote 双 ledger 证据链。

- Trace:
  - 设计锚点：`docs/approved/design-prod-archive-bucket.md`
  - 前序 canary：`docs/approved/design-data-layer-prod-export-canary.md`（US-039）
- Risk Focus:
  - 逻辑错误：export  scope 越过 legacy 冷水位或 cursor 乱序。
  - 行为回归：promote 破坏 manifest-last / checksum 契约。
  - 安全问题：无确认串或错误 batch id 仍 mutate S3。
  - 运行时问题：promote-ledger 半失败、staging 7 天过期前未 promote。

## Acceptance Criteria

1. **AC-001（export plan）**：Given 合法表名，When plan，Then 不调用 AWS，输出 legacy 上界、`archive-export/` 前缀与 `execution_authorized=false`。
2. **AC-002（export 续跑）**：Given ledger + cleanup hold，When 重复 run-batch，Then cursor 推进、`more_cold_rows_remaining` 正确，每批需 `tokenkey-prod-archive-export-batch-v1`。
3. **AC-003（promote）**：Given staging 已提交 batch，When promote，Then artifact 先于 manifest 复制、head 校验 sha256、输出 receipt；错误确认串或 checksum 漂移 fail closed。
4. **AC-004（promote-ledger 幂等）**：Given export ledger，When promote-ledger，Then 已 promote 的 batch 跳过；`drop_ready` 仅表证据齐全，**不授权** drop。
5. **AC-005（无删除）**：Given 本 Story 范围，When 检查 CLI/workflow，Then 无分区 drop、DELETE 或 schedule。

## Assertions

- export / promote 无 UI；行为测试 + stubbed AWS runner 覆盖正向与负向。
- `drop_ready=true` 不是删除授权；cleanup hold 仍须单独 release 审批。
- staging 7 天过期；promote 必须在窗口内完成或重 export。

## Linked Tests

- `ops/archive/test_data_layer_archive_prod_export.py`::`ProdArchiveExportTest.test_plan_is_offline_and_legacy_scoped`
- `ops/archive/test_data_layer_archive_prod_export.py`::`ProdArchiveExportTest.test_run_batch_guard_fails_before_aws`
- `ops/archive/test_data_layer_archive_prod_export.py`::`ProdArchiveExportTest.test_run_batch_advances_ledger`
- `ops/archive/test_data_layer_archive_promote_batch.py`::`PromoteBatchTest.test_promote_refuses_invalid_confirmation`
- `ops/archive/test_data_layer_archive_promote_batch.py`::`PromoteBatchTest.test_promote_copies_artifacts_then_manifest`
- `ops/archive/test_data_layer_archive_promote_batch.py`::`PromoteBatchTest.test_promote_ledger_skips_already_promoted_batches`

运行命令：

```bash
python3 ops/archive/test_data_layer_archive_prod_export.py
python3 ops/archive/test_data_layer_archive_promote_batch.py
```

## Evidence

- export ledger：`.testing/user-stories/attachments/US-040-ops-system-logs-export-ledger.json`、`.testing/user-stories/attachments/US-040-ops-error-logs-export-ledger.json`
- promote ledger：`.testing/user-stories/attachments/US-040-ops-system-logs-promote-ledger.json`、`.testing/user-stories/attachments/US-040-ops-error-logs-promote-ledger.json`

## Status

- [x] Done — prod export + promote 已完成；legacy 分区 drop 仍 blocked（cleanup hold 有效）。
