# US-037-data-layer-archive-rehearsal

- ID: US-037
- Title: Data-layer 非生产归档恢复演练
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **先在隔离的本地/非生产数据库完成冷数据封口、校验和随机恢复**，**以便** 在任何生产归档或删除审批前证明数据可恢复、流程可重试且源库不被修改。

- Trace:
  - 设计锚点：`docs/approved/design-data-layer-archive-rehearsal.md`
  - 前序容量设计：`docs/approved/design-capacity-first-data-layer-safety.md`
  - 演练工具：`ops/archive/data_layer_archive_rehearsal.py`
- Risk Focus:
  - 逻辑错误：水位边界或保留期算错，把热数据纳入归档。
  - 行为回归：重复封口/恢复产生不同批次或重复数据，无法安全重试。
  - 安全问题：工具接受 prod/network 输入、修改源库或出现删除入口。
  - 运行时问题：artifact 损坏、manifest 漂移、恢复冲突未 fail closed。

## Acceptance Criteria

1. **AC-001（只读水位）**：Given 本地 SQLite 含 usage/ops/QA 冷热记录，When 以 90/30/2 天保留期 dry-run，Then 只报告严格早于 cutoff 的记录，源文件 checksum 不变，带微秒的时间在 seal/restore 后精度不变。
2. **AC-002（可验证封口）**：Given 存在冷记录，When seal，Then 生成确定性 batch、canonical gzip JSONL 和包含行数/范围/双 checksum 的 manifest；重复 seal 复用同一批次。
3. **AC-003（损坏拒绝）**：Given artifact 被篡改或 manifest 不一致，When verify/restore，Then 在创建恢复库或提交数据前 fail closed。
4. **AC-004（随机恢复）**：Given 已验证批次，When 使用显式 seed 随机选取一个 artifact 恢复，Then 恢复行数和 logical checksum 与 manifest 一致。
5. **AC-005（幂等与冲突）**：Given 同批次重复恢复，When 目标内容一致，Then 不新增行；When 同键异值，Then checksum 失败并回滚。
6. **AC-006（无生产能力）**：Given CLI，When 提供 prod environment、网络 DSN、源文件 symlink/hard link 恢复目标或 delete 命令，Then 参数解析/安全守卫拒绝；工具没有 runtime/prod consumer。
7. **AC-007（阶段隔离）**：Given 本 PR 合并，When 检查 workflow/deploy/prod 工件，Then 没有生产接线、PostgreSQL/S3/AWS 调用或数据删除授权。

## Assertions

- 本 Story 没有 UI 工件，不要求 Playwright e2e；CLI 全链路用临时真实 SQLite 验证。
- SQLite 是本阶段刻意的能力边界，不是生产归档数据库选型。
- dry-run 字节数是 logical JSONL 体积，不冒充 PostgreSQL `df` 物理回收量。
- merge 只批准非生产演练工件，不批准 prod probe、扩盘、导出、canary 或删除。

## Linked Tests

- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_dry_run_uses_retention_without_mutating_source`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_cutoff_is_strict_and_empty_batches_are_refused`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_seal_verify_and_reseal_are_deterministic`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_corrupt_artifact_fails_closed_before_restore`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_random_restore_is_verified_and_idempotent`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_conflicting_restore_target_rolls_back`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_cli_rejects_prod_and_network_inputs`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_output_paths_cannot_overwrite_source_restore_or_batch`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_manifest_identity_tampering_is_rejected`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_restore_rejects_manifest_changed_after_verify`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_symlinks_are_rejected_and_timezone_order_is_canonical`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_cli_runs_full_local_rehearsal`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us037_tool_has_no_runtime_or_prod_consumer`

运行命令：

```bash
python3 ops/archive/test_data_layer_archive_rehearsal.py
```

## Evidence

- 自动化测试使用临时 SQLite 源库、封口目录和恢复库，运行后自动清理。

## Status

- [x] InTest — 本地/非生产归档恢复闭环已覆盖；所有 prod 操作仍需独立审批。
