# US-077-qa-data-machine-coordinator

- ID: US-077
- Priority: P0
- Title: 数据机 QA 协调与按源清理策略
- As a / I want / So that: 作为生产运维，我要由数据机统一调度归档、各主机独立执行一天热数据兜底，避免 QA 归档故障拖满业务磁盘。
- Trace: `docs/approved/design-prod-app-parallel-replacement.md` 的 QA 集中管理方案与数据机协调器实现。
- Risk Focus:
  - 逻辑错误：source 回执、站点、UTC 小时必须严格匹配。
  - 数据安全：用户已批准小时结束满 24h 接受丢失，只清 QA，不得扩大到业务/计费/备份。
  - 运行时问题：计划不可覆盖，单源清理不得触碰另一节点或共享分区。

## Acceptance Criteria

1. AC-001 (正向): Given A、B 两个预期 source 且 B 有合法回执 When 生成计划 Then B 可清理，A 被 hold，共享分区保持 hold。
2. AC-002 (正向): Given 所有预期 source 都有合法回执 When 生成计划 Then 共享分区动作是 `delete`，每个 source 都获准本机清理。
3. AC-003 (负向): Given 缺 source When 未显式启用 gap 策略 Then 计划不能变成 `gap-delete`。
4. AC-004 (正向): Given 缺 source 且超过显式 gap 截止时间 When 允许 gap Then 计划为 `gap-delete`，并保留缺源清单。
5. AC-005 (负向): Given source B 的计划 When 在 source A 主机执行清理 Then 不删除 A 以外的目录，未授权 source 不删除任何文件。
6. AC-006 (负向): Given 已存在不同内容的小时计划 When 再次写入 Then 拒绝覆盖。
7. AC-007 (运行时): Given A 不可达且预建因备份让行 When 协调器 apply Then B 仍可独立归档，协调器不派发清理，失败与保留期内积压可观测。
8. AC-008 (负向): Given capture 未封口、封口变化或旧 timer 仍启用 When worker 归档 Then 拒绝授权；归档跨过保留期限也不得宣称成功。
9. AC-009 (集成): Given 真实 PostgreSQL 小时分区 When 缺源、dry-run、完整 apply、重试 Then 依次保留、保留、DROP、幂等；应用机文件和全站磁盘完成标记均不变。
10. AC-010 (运维): Given 安装计划 When 未批准启用 Then 不写单位、不启动 timer；两类主机均生成独立 retention units，历史 source 不随在线列表删减。
11. AC-011 (正向/负向): Given 无回执、node1/数据机离线且 archive 锁被占用 When 应用机本地 retention 执行 Then 小时结束满 24h 的 payload/DLQ 删除，提前一秒不删，业务文件不变。
12. AC-012 (运行时): Given 清理中断、迟到旧文件、symlink 或审计 ENOSPC When retention 重跑 Then 可回收正常过期目录，不越界，不因审计满盘而停止释放 payload，并报告错误。
13. AC-013 (集成): Given 无 source 证据的真实过期 QA 分区 When 数据机独立 expire-shared dry-run/apply/retry Then 依次保留、DROP、幂等；最近一天分区、业务表、应用机文件和全站磁盘完成标记不变。
14. AC-014 (运行时): Given 已归档或已过期小时 When 协调器重跑 Then 不重复归档完成小时、不重新归档过期空目录；历史参与源变化时重核保留期内小时。

## Assertions

断言 source 状态、共享分区动作和实际删除路径；不把计划文件存在当作验收，必须验证缺源
时 B 可归档，以及过期兜底不需要 source 证据。原按回执策略工具的 hold/gap 测试
仅说明兼容工具行为，不再代表周期任务采用缺源永久 hold。

## Linked Tests

- `ops/qa/test_data_machine_coordinator.py`::`CoordinatorTest.test_missing_a_does_not_block_b_source_cleanup_but_holds_shared_partition` (AC-001)
- `ops/qa/test_data_machine_coordinator.py`::`CoordinatorTest.test_explicit_gap_deadline_allows_shared_delete` (AC-003, AC-004)
- `ops/qa/test_data_machine_coordinator.py`::`CoordinatorTest.test_plan_is_immutable` (AC-006)
- `ops/qa/test_data_machine_coordinator.py`::`SourceCleanupTest.test_source_cleanup_removes_only_authorized_source_hour` (AC-005)
- `ops/qa/test_data_machine_coordinator.py`::`SafetyTest.test_complete_sources_authorize_shared_cleanup` (AC-002)
- `ops/qa/test_data_machine_coordinator.py`::`SourceCleanupTest.test_symlink_hour_never_deletes_outside_source` (AC-008)
- `ops/qa/test_coordinator_host.py`::`RunnerTest.test_a_unreachable_does_not_block_b_archive_and_never_dispatches_cleanup` (AC-007)
- `ops/qa/test_coordinator_host.py`::`RunnerTest.test_retired_source_stays_in_its_historical_hour` (AC-010)
- `ops/qa/test_coordinator_host.py`::`RunnerTest.test_expired_hours_are_delegated_to_independent_retention_not_rearchived` (AC-014)
- `ops/qa/test_coordinator_host.py`::`RunnerTest.test_completed_archive_is_not_repeated_and_membership_change_reopens_it` (AC-014)
- `ops/qa/test_coordinator_host.py`::`RunnerTest.test_no_automatic_gap_policy_can_be_silently_enabled` (AC-003, AC-010)
- `ops/qa/test_coordinator_host.py`::`InstallerTest.test_plan_does_not_write_units_or_enable_timers` (AC-010)
- `ops/qa/test_coordinator_host.py`::`InstallerTest.test_independent_retention_units_for_both_roles_without_activation` (AC-010)
- `ops/qa/test_source_worker.py`::`SourceWorkerTest.test_changed_capture_seal_cannot_authorize_cleanup` (AC-008)
- `ops/qa/test_source_worker.py`::`SourceWorkerTest.test_legacy_owner_prevents_archive_authorization` (AC-008)
- `ops/qa/test_source_worker.py`::`SourceWorkerTest.test_expired_hour_and_archive_crossing_deadline_cannot_claim_success` (AC-008)
- `ops/qa/test_force_retention.py`::`RetentionTest.test_one_day_boundary_dry_run_and_offline_cleanup` (AC-011, AC-012)
- `ops/qa/test_force_retention.py`::`RetentionTest.test_boundary_one_second_before_deadline_keeps_entire_hour` (AC-011)
- `ops/qa/test_force_retention.py`::`RetentionTest.test_symlink_does_not_escape_and_does_not_block_other_hours` (AC-012)
- `ops/qa/test_force_retention.py`::`RetentionTest.test_full_disk_audit_failure_does_not_block_reclamation` (AC-012)
- `ops/qa/test_force_retention.py`::`RetentionTest.test_data_retention_uses_native_expiry_without_archive_or_coordinator_lock` (AC-013)
- `ops/qa/test_local_maintenance_integration.py`::`LocalCleanupIntegration.test_independent_shared_retention_drops_without_sources_and_preserves_recent_and_business_data` (AC-013)
- `ops/qa/test_local_maintenance_integration.py`::`LocalCleanupIntegration.test_shared_cleanup_requires_all_sources_and_never_marks_application_disks_clean` (AC-009)
- `ops/observability/test_host_https_watch.py`::`CoveragePlanTest.test_coordinator_receipt_reports_failure_staleness_and_held_hours` (AC-007)
- Run: `python3 -m unittest ops.qa.test_data_machine_coordinator`
- Run: `python3 -m unittest ops.qa.test_coordinator_host ops.qa.test_source_worker ops.observability.test_host_https_watch`
- Run: `make -C backend test-qa-local`

## Status

- InTest
