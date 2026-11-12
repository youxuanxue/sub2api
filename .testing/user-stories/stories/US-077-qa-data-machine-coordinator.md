# US-077-qa-data-machine-coordinator

- ID: US-077
- Priority: P0
- Title: 数据机 QA 协调与按源清理策略
- As a / I want / So that: 作为生产运维，我要由数据机统一判断共享小时是否可删、由各应用机独立清理自己的热文件，从而避免单节点缺口拖垮所有节点磁盘。
- Trace: `docs/approved/design-prod-app-parallel-replacement.md` 的 QA 集中管理方案与数据机协调器实现。
- Risk Focus:
  - 逻辑错误：source 回执、站点、UTC 小时必须严格匹配。
  - 数据安全：缺源默认 hold，gap 删除必须显式截止时间和批准策略。
  - 运行时问题：计划不可覆盖，单源清理不得触碰另一节点或共享分区。

## Acceptance Criteria

1. AC-001 (正向): Given A、B 两个预期 source 且 B 有合法回执 When 生成计划 Then B 可清理，A 被 hold，共享分区保持 hold。
2. AC-002 (正向): Given 所有预期 source 都有合法回执 When 生成计划 Then 共享分区动作是 `delete`，每个 source 都获准本机清理。
3. AC-003 (负向): Given 缺 source When 未显式启用 gap 策略 Then 计划不能变成 `gap-delete`。
4. AC-004 (正向): Given 缺 source 且超过显式 gap 截止时间 When 允许 gap Then 计划为 `gap-delete`，并保留缺源清单。
5. AC-005 (负向): Given source B 的计划 When 在 source A 主机执行清理 Then 不删除 A 以外的目录，未授权 source 不删除任何文件。
6. AC-006 (负向): Given 已存在不同内容的小时计划 When 再次写入 Then 拒绝覆盖。
7. AC-007 (运行时): Given A 不可达且预建因备份让行 When 协调器 apply Then B 独立清理并持久回执，共享分区不删，失败与积压可观测。
8. AC-008 (负向): Given capture 未封口、封口变化或旧 timer 仍启用 When worker 执行 Then 拒绝授权；删除中断可重试，迟到文件和 symlink 不可删除。
9. AC-009 (集成): Given 真实 PostgreSQL 小时分区 When 缺源、dry-run、完整 apply、重试 Then 依次保留、保留、DROP、幂等；应用机文件和全站磁盘完成标记均不变。
10. AC-010 (运维): Given 安装计划 When 未批准启用 Then 不写单位、不启动 timer；历史退役 source 仍参与旧小时，执行器拒绝自动 gap。

## Assertions

断言 source 状态、共享分区动作和实际删除路径；不把计划文件存在当作验收，必须验证缺源
时 B 可清理、A 被保留，以及 gap 策略未显式批准时不会放行共享删除。

## Linked Tests

- `ops/qa/test_data_machine_coordinator.py`::`CoordinatorTest.test_missing_a_does_not_block_b_source_cleanup_but_holds_shared_partition` (AC-001)
- `ops/qa/test_data_machine_coordinator.py`::`CoordinatorTest.test_explicit_gap_deadline_allows_shared_delete` (AC-003, AC-004)
- `ops/qa/test_data_machine_coordinator.py`::`CoordinatorTest.test_plan_is_immutable` (AC-006)
- `ops/qa/test_data_machine_coordinator.py`::`SourceCleanupTest.test_source_cleanup_removes_only_authorized_source_hour` (AC-005)
- `ops/qa/test_data_machine_coordinator.py`::`SafetyTest.test_complete_sources_authorize_shared_cleanup` (AC-002)
- `ops/qa/test_data_machine_coordinator.py`::`SourceCleanupTest.test_symlink_hour_never_deletes_outside_source` (AC-008)
- `ops/qa/test_coordinator_host.py`::`RunnerTest.test_a_unreachable_does_not_block_b_cleanup_or_authorize_shared_drop` (AC-007)
- `ops/qa/test_coordinator_host.py`::`RunnerTest.test_retired_source_stays_in_its_historical_hour` (AC-010)
- `ops/qa/test_coordinator_host.py`::`RunnerTest.test_no_automatic_gap_policy_can_be_silently_enabled` (AC-003, AC-010)
- `ops/qa/test_coordinator_host.py`::`InstallerTest.test_plan_does_not_write_units_or_enable_timers` (AC-010)
- `ops/qa/test_source_worker.py`::`SourceWorkerTest.test_changed_capture_seal_cannot_authorize_cleanup` (AC-008)
- `ops/qa/test_source_worker.py`::`SourceWorkerTest.test_legacy_owner_prevents_archive_authorization` (AC-008)
- `ops/qa/test_local_maintenance_integration.py`::`LocalCleanupIntegration.test_shared_cleanup_requires_all_sources_and_never_marks_application_disks_clean` (AC-009)
- `ops/observability/test_host_https_watch.py`::`CoveragePlanTest.test_coordinator_receipt_reports_failure_staleness_and_held_hours` (AC-007)
- Run: `python3 -m unittest ops.qa.test_data_machine_coordinator`
- Run: `python3 -m unittest ops.qa.test_coordinator_host ops.qa.test_source_worker ops.observability.test_host_https_watch`
- Run: `make -C backend test-qa-local`

## Status

- InTest
