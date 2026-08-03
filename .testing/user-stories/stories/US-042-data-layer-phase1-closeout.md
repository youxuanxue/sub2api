# US-042-data-layer-phase1-closeout

- ID: US-042
- Title: Data-layer 第一阶段安全收口
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **收口分区维护、归档恢复证明和原始 telemetry 冷层**，**以便** 在不影响在线请求、不误删热数据的前提下，为后续 RDS 阶段建立可信的数据边界。

- Trace:
  - 设计锚点：`docs/approved/design-data-layer-phase1-closeout.md`
  - 前序归档：US-039、US-040
- Risk Focus:
  - 逻辑错误：错误分区边界、ledger 水位或 checksum 允许热数据删除。
  - 行为回归：cleanup hold 关闭分区维护，或 PostgreSQL 成功后 telemetry shadow 丢失。
  - 安全问题：错误确认串、未验证 CHECK 或不完整恢复证据仍允许 cutover/release。
  - 运行时问题：调用方超时、S3 队列/上传失败、维护心跳过期或行数漂移。

## Acceptance Criteria

1. **AC-001（维护与删除解耦）**：Given cleanup disabled，When 调度 tick，Then 当前/未来分区仍被维护，只更新维护 heartbeat，不执行 retention 或更新 cleanup heartbeat。
2. **AC-002（边界驱动回收）**：Given 分区表，When 计算过期分区，Then 只使用 PostgreSQL 声明的有限上界；未知/default bound 在任何 drop 前 fail closed。
3. **AC-003（显式 usage cutover）**：Given operator prepare receipt，When abort/cutover，Then 精确确认串、数据库时钟上界、operator-owned CHECK、短锁等待、外键和行数校验全部成立；不复制历史行且不随应用启动执行。
4. **AC-004（归档后才 release）**：Given export/promote ledgers，When closeout，Then 每个 batch 的 manifest/checksum 精确绑定并随机恢复到独立 PostgreSQL；任一证据不一致时保留 cleanup hold。
5. **AC-005（提交后 shadow）**：Given usage/ops PostgreSQL 写入，When commit/insert 成功，Then 才尝试非阻塞 telemetry enqueue；调用方在批写处理中超时但后台最终成功时仍由 worker 入队一次，队列或上传失败不改变 OLTP 结果。
6. **AC-006（保护信号独立）**：Given 任一分区、备份、archive、hold 或恢复证据缺失/过期，When 生成 safety verdict，Then 产生独立 fail-closed finding，容量 green 不得覆盖。
7. **AC-007（阶段隔离）**：Given 本 Story，When 审查资源和入口，Then RDS 第二阶段保持 hold，且合并本 PR 不执行生产 schema、archive、cleanup release、部署或发布。

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
- `backend/internal/telemetryarchive/shadow_test.go`::`TestShadowQueueFullDropsOnlyShadowCopy`
- `ops/observability/test_data_layer_safety_verdict.py`::`DataLayerSafetyVerdictTest.test_capacity_independent_failures_are_separate_findings`

运行命令：

```bash
cd backend && go test ./internal/pkg/pgpartition ./internal/repository ./internal/service ./internal/telemetryarchive
python3 ops/migration/test_usage_logs_daily_partition.py
python3 ops/archive/test_data_layer_archive_closeout.py
python3 ops/observability/test_data_layer_safety_verdict.py
```

## Status

- [x] InTest - 自动化实现与隔离 PostgreSQL 演练已覆盖；所有生产动作和 RDS 第二阶段仍保持 hold。
