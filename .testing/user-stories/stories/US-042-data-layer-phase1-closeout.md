# US-042-data-layer-phase1-closeout

- ID: US-042
- Title: Data-layer 第一阶段安全收口
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **收口分区维护、归档恢复证明和原始 telemetry 冷层**，**以便** 在不影响在线请求、不误删热数据的前提下，为后续 RDS 阶段建立可信的数据边界。

- Trace:
  - 设计锚点：`docs/approved/design-data-layer-phase1-closeout.md`
  - 生产启用门禁：`docs/approved/design-phase1-prod-activation-gates.md`
  - 归档设计：`docs/approved/design-data-layer-prod-export-canary.md`、`docs/approved/design-prod-archive-bucket.md`
- Risk Focus:
  - 逻辑错误：错误分区边界、ledger 水位或 checksum 允许热数据删除。
  - 行为回归：cleanup hold 关闭分区维护，或 PostgreSQL 成功后 telemetry shadow 丢失。
  - 安全问题：错误确认串、未验证 CHECK 或不完整恢复证据仍允许 cutover/release。
  - 运行时问题：调用方超时、S3 队列/字节上限/上传失败、telemetry 或维护心跳过期、行数漂移。

## Acceptance Criteria

1. **AC-001（维护与删除解耦）**：Given cleanup disabled，When 调度 tick，Then 当前/未来分区仍被维护，只更新维护 heartbeat，不执行 retention 或更新 cleanup heartbeat。
2. **AC-002（边界驱动回收）**：Given 分区表，When 计算过期分区，Then 只使用 PostgreSQL 声明的有限上界；未知/default bound 在任何 drop 前 fail closed。
3. **AC-003（显式 usage cutover）**：Given operator prepare receipt，When abort/cutover，Then 精确确认串、数据库时钟上界、operator-owned CHECK、短锁等待、外键和行数校验全部成立；不复制历史行且不随应用启动执行。
4. **AC-004（归档后才 release）**：Given export/promote ledgers，When closeout，Then 每个 batch 的 manifest/checksum 精确绑定并随机恢复到独立 PostgreSQL；任一证据不一致时保留 cleanup hold。
5. **AC-005（提交后 shadow）**：Given usage/ops PostgreSQL 写入，When commit/insert 成功，Then 才尝试非阻塞 telemetry enqueue；调用方在批写处理中超时但后台最终成功时仍由 worker 入队一次；队列在序列化前预占，并同时限制 queued/in-flight record count、serialized payload bytes 和单事件大小；队列或上传失败不改变 OLTP 结果。
6. **AC-006（保护信号独立）**：Given 任一分区、备份、archive、hold、恢复或已启用 telemetry 的健康证据缺失/过期，When 生成 safety verdict，Then 产生独立 fail-closed finding；telemetry 还必须具有 3 分钟内的 clean heartbeat 和 dropped=failed=0，容量 green 不得覆盖。
7. **AC-007（阶段隔离）**：Given 本 Story，When 审查资源和入口，Then RDS 第二阶段保持 hold，且合并本 PR 不执行生产 schema、archive、cleanup release、部署或发布。
8. **AC-008（单一默认与可重放格式）**：Given telemetry 默认配置，When 对比 Go、Compose、env 示例与设计，Then 均为 disabled、空 region/bucket、`prod/raw-telemetry`、8192 records、32 MiB、1 MiB/event、256/batch、4 workers、5s flush、10s put；对象为 schema v1 envelope + checksum metadata，生命周期默认 8 天转 Glacier、120 天过期，runtime 对 raw prefix 只有写权限。
9. **AC-009（生产启用门禁）**：Given 第一阶段代码已合并但仍未启用，When 修复只读诊断和一次性分区入口，Then 活跃容器与 DataVolume 快照均失败关闭，容量查询保持有界，cron 与一次性模式共享同一个分区 owner，固定 controller 在二次生产审批前不得执行。

## Assertions

- 本 Story 没有 UI 工件，不要求 Playwright e2e；Go 行为测试、Python CLI 测试和隔离 PostgreSQL 集成覆盖关键路径。
- S3 是可重放 shadow，不是账务或幂等 source of truth；功能默认关闭。
- 生产 cutover、cleanup release、部署和 RDS 仍是独立人工门禁。

## Linked Tests

- `backend/internal/service/ops_cleanup_service_test.go`::`TestOpsCleanupScheduled_DisabledStillMaintainsPartitionsWithoutCleanupHeartbeat`
- `backend/internal/pkg/pgpartition/partition_test.go`::`TestDropExpired_UnparseableBoundFailsBeforeAnyDrop`
- `ops/migration/test_usage_logs_daily_partition.py`::`UsageLogsDailyPartitionTest.test_cutover_sql_has_short_lock_and_no_data_copy`
- `ops/archive/test_data_layer_archive_closeout.py`::`ArchiveCloseoutTest.test_receipt_rejects_unbound_restore_and_invalid_evidence`
- `backend/internal/repository/telemetry_archive_hooks_test.go`::`TestUS042UsageBestEffortLateCompletionStillEnqueuesTelemetry`
- `backend/internal/repository/telemetry_archive_hooks_test.go`::`TestUS042UsageBatchLateCompletionEnqueuesImmutableTelemetryOnce`
- `backend/internal/repository/telemetry_archive_hooks_test.go`::`TestUS042OpsErrorTelemetryExcludesNonPersistedDiagnostics`
- `backend/internal/telemetryarchive/shadow_test.go`::`TestShadowQueueFullDropsOnlyShadowCopy`
- `backend/internal/telemetryarchive/shadow_test.go`::`TestShadowQueueBytesAndEventSizeAreBounded`
- `backend/internal/service/telemetry_archive_health_test.go`::`TestTelemetryArchiveHealthPublishesCleanAndFailedStats`
- `ops/observability/test_data_layer_archive_health.py`::`DataLayerArchiveHealthTest.test_checked_in_ledgers_pass_their_owner_validator`
- `ops/observability/test_data_layer_archive_health.py`::`DataLayerArchiveHealthReleaseTest.test_release_receipt_must_bind_to_latest_hold`
- `ops/observability/test_data_layer_safety_verdict.py`::`DataLayerSafetyVerdictTest.test_capacity_independent_failures_are_separate_findings`
- `ops/observability/test_data_layer_safety_verdict.py`::`DataLayerSafetyVerdictTest.test_enabled_telemetry_requires_fresh_clean_zero_loss_stats`
- `backend/internal/pkg/partitionmaintenance/maintenance_test.go`::`TestEnsureStrictCreatesAndVerifiesAllTargets`
- `backend/cmd/server/partition_maintenance_test.go`::`TestPartitionMaintenanceSuccessUsesStrictBoundedPath`
- `ops/migration/test_data_layer_partition_maintenance.py`::`DataLayerPartitionMaintenanceTest.test_success_uses_fixed_instance_command_and_full_json`

运行命令：

```bash
cd backend && go test ./internal/pkg/pgpartition ./internal/repository ./internal/service ./internal/telemetryarchive
cd backend && go test -tags unit ./cmd/server ./internal/pkg/partitionmaintenance ./internal/service
python3 ops/migration/test_usage_logs_daily_partition.py
python3 ops/migration/test_data_layer_partition_maintenance.py
python3 ops/archive/test_data_layer_archive_closeout.py
python3 ops/observability/test_data_layer_archive_health.py
python3 ops/observability/test_data_layer_safety_verdict.py
```

## Status

- [x] Done — prod steady state 已达成；`OpsCleanupService` 运行 retention；RDS 第二阶段仍 hold。
