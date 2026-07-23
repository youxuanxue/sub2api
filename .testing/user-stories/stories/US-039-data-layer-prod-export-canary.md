# US-039-data-layer-prod-export-canary

- ID: US-039
- Title: Data-layer 生产只读导出 canary
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **从生产 PostgreSQL 只读导出一个有界冷数据批次并在独立 PostgreSQL 恢复验证**，**以便** 在任何分区删除前证明离机归档真实可恢复。

- Trace:
  - 设计锚点：`docs/approved/design-data-layer-prod-export-canary.md`
  - 前序演练：`docs/approved/design-data-layer-archive-rehearsal.md`
  - 容量证据：`docs/ops/data-layer-retention-inventory-20260721.md`
- Risk Focus:
  - 逻辑错误：错误水位把 30 天内热数据纳入 canary。
  - 行为回归：prod manifest 破坏既有 nonprod verify/restore 契约。
  - 安全问题：错误实例、表、确认串或非只读会话绕过生产边界。
  - 运行时问题：查询/输出超量、S3 半提交、artifact 损坏或恢复不一致。

## Acceptance Criteria

1. **AC-001（无副作用 plan）**：Given 合法 canary 参数，When 运行 plan，Then 不调用 AWS/Docker/PostgreSQL，并输出固定 prod 目标、冷水位、硬上限和 `execution_authorized=false`。
2. **AC-002（cleanup hold）**：Given 生产归档操作，When plan/apply/verify，Then 只修改完整 advanced-settings 中的 cleanup bool，证明 runtime reload，重复 apply 拒绝，并在 hold 后出现 cleanup heartbeat 时拒绝；release 恢复 receipt 记录的原 enabled 状态。
3. **AC-003（有界冷数据）**：Given ops 表含冷热记录，When 导出，Then 按 `(created_at, id)` 只封口严格早于 30 天 cutoff 的确定性首段；行数、logical bytes 和 statement timeout 不超过硬上限，且冷行总量超过 `max_rows` 时记录 continuation 而非拒绝。
4. **AC-004（提交式离机封口）**：Given 一个批次，When 上传，Then 远端代码 bundle 经加密 S3 对象和 SHA-256 交付且 SSM 参数不超过硬上限，artifact 先上传并验证服务端加密，manifest 最后上传；缺 manifest 的前缀不构成完成批次。
5. **AC-005（独立恢复）**：Given 已提交批次，When 下载并恢复到 `tokenkey_archive_restore_*`，Then 行数和 logical SHA-256 一致，源库内容不变。
6. **AC-006（损坏拒绝）**：Given manifest/artifact 被篡改、目标已有同键异值或 S3 未加密，When verify/restore，Then fail closed。
7. **AC-007（无删除能力）**：Given 本 PR，When 检查 CLI、workflow、deploy 和 runtime，Then 不存在数据删除、分区 drop、定时任务、自动部署或 RDS/cutover 接线。

## Assertions

- 本 Story 没有 UI 工件，不要求 Playwright e2e；CLI 用行为测试和真实 PostgreSQL 容器验证。
- `tokenkey-stage0-backups` 仅是短期 canary staging；其生命周期不能作为长期归档保留策略。
- merge 不批准生产执行；每次 run 仍需单独生产审批和精确确认串。
- export-only 不增加 `df` runway；只有后续独立批准的物理分区退役才释放空间。

## Linked Tests

- `ops/archive/test_data_layer_archive_prod_canary.py`::`ProdArchiveCanaryTest.test_us039_plan_is_offline_and_bounded`
- `ops/archive/test_data_layer_archive_prod_canary.py`::`ProdArchiveCanaryTest.test_us039_run_guards_fail_before_aws`
- `ops/archive/test_data_layer_archive_prod_canary.py`::`ProdArchiveCanaryTest.test_us039_remote_bundle_imports_and_host_export_rechecks_hold`
- `ops/archive/test_data_layer_archive_prod_canary.py`::`ProdArchiveCanaryTest.test_us039_seal_rejects_hot_or_oversized_rows`
- `ops/archive/test_data_layer_archive_prod_canary.py`::`ProdArchiveCanaryTest.test_us039_s3_manifest_is_uploaded_last_and_encrypted`
- `ops/archive/test_data_layer_archive_prod_canary.py`::`ProdArchiveCanaryPostgresIntegrationTest.test_us039_prod_canary_restores_without_mutating_source`
- `ops/archive/test_data_layer_archive_cleanup_hold.py`::`CleanupHoldRemoteTest.test_us039_apply_preserves_full_document_and_proves_reload`
- `ops/archive/test_data_layer_archive_cleanup_hold.py`::`CleanupHoldRemoteTest.test_us039_apply_refuses_to_replace_an_active_hold_receipt`
- `ops/archive/test_data_layer_archive_cleanup_hold.py`::`CleanupHoldRemoteTest.test_us039_verify_rejects_cleanup_after_hold`
- `ops/archive/test_data_layer_archive_cleanup_hold.py`::`CleanupHoldControlTest.test_us039_apply_writes_and_reverifies_receipt`

运行命令：

```bash
python3 ops/archive/test_data_layer_archive_prod_canary.py
python3 ops/archive/test_data_layer_archive_cleanup_hold.py
```

## Evidence

- cleanup hold receipt：`.testing/user-stories/attachments/US-039-prod-cleanup-hold-20260721.json`。
- 单元测试使用 stubbed AWS/S3/admin API command runner；集成测试使用临时 `postgres:18-alpine` 源库与独立恢复库。

## Status

- [x] Done — prod canary 已执行；legacy 分批 export 与 promote 见 US-040；分区 drop 仍须单独审批。
