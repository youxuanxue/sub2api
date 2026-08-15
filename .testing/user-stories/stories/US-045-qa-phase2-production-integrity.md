# US-045-qa-phase2-production-integrity

- ID: US-045
- Title: QA Phase 2 production integrity closeout
- Priority: P0（不可逆归档控制状态、生产恢复与数据生命周期）
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **QA Phase 2 归档只按已批准状态机运行，并由唯一不可变 cutover、同一受限 runner、可验证恢复、最小 IAM 与 default-free 小时 boundary 共同守卫**，**以便** 缺失或损坏的归档不能伪装成成功，历史 repair 不能重开，保留期清理也不能获得额外删除授权。
- Trace:
  - 长期设计：`docs/approved/design-prod-qa-24h-s3-lifecycle.md`
  - Phase 2 收口：`docs/approved/design-qa-phase2-archive-closeout.md`

## Scope Boundary

- 本 Story 只记录已实现的双 timer 固定时龄删除基线及其生产证据。
- 未来 archive-gated DROP 与 single maintenance owner 只由主 QA 设计定义；本 Story 的 AC/test 不得覆盖它。

- Risk Focus:
  - 逻辑错误：cutover 指向非 `committed`、未 restore-verified 或非批准小时；normal 失败后仍补偿；保留期后无 source 的小时被写成空成功。
  - 行为回归：任意窗口 `repair-apply`、历史 backfill、move/unset cutover、第二 catchup owner 被重新引入，或 Phase 2 固定时龄删除被误写成未来目标。
  - 安全问题：共享 EC2 role 被误报为进程隔离；app 获得 raw bucket 无界 list/read；恢复正文绕过显式隐私确认或从 prod 回源。
  - 运行时问题：timer/operator 使用不同 image、UID/GID、mount、scratch 或资源限制；host receipt、DB heartbeat 与 control state 矛盾时仍报告健康；export 临时文件清理发生竞态或计划漂移；`qa_records` UTC 小时分区缺口、过期分区未 DROP、DEFAULT 稳态残留或 hot-file 清理未完成。

## Acceptance Criteria

1. **AC-001（正向 cutover）**：Given `2026-08-07 21:00 UTC` shard 已 `committed` 且 `restore_verified_at` 非空，When 执行唯一 exact cutover operation，Then 只标记该 row，重复执行幂等，读取返回同一 valid cutover。
2. **AC-002（负向 cutover）**：Given shard 缺失、状态非法、未 restore-verified、window 不精确、已有不同 marker 或 control 数据损坏，When 读取或设置 cutover，Then fail closed；schema 阻止第二 marker、invalid marker、move、unset 与删除，CLI 不接受 generic cutover window 或 move/unset action。
3. **AC-003（状态机）**：Given 每轮 normal sealed hour，When normal 成功且 restore-verified，Then 只从 cutover 后的 UTC timeline 选择至多一个 oldest retryable candidate；normal 失败不补偿，补偿失败保留 normal 成功但整轮非零，terminal failure 可见且不阻塞后续小时。
4. **AC-004（完整性）**：Given timely zero-row hour、source retention 后首次发现的未知小时、late identity 或 missing/corrupt evidence，When reconcile，Then 分别产生可恢复空 base、`source_unavailable_after_retention`、单一 delta 或 failed/blocked control；不得合成缺失历史、授权删除或覆盖 immutable commit。
5. **AC-005（runtime）**：Given timer、operator、self-test 与 health probe，When执行或发生 pre-app/child failure，Then它们共用唯一 host runner 与批准的 image/user/mount/scratch/resource contract，原子 receipt 始终推进且四类运行事实矛盾时 fail closed。
6. **AC-006（IAM 与 recovery）**：Given app archive role 与 ops recovery role，When渲染 policy 或从 workstation inspect/verify/restore，Then app 只有所需 suffix-scoped artifact 权限且无无界 list/partial read，recovery 不经过 prod 主机/API/数据库，正文恢复要求 privacy confirmation，并如实保留 shared-role 风险。
7. **AC-007（cutover 排空与 export orphan）**：Given legacy stale cleanup 仅在 no-move cutover 排空期间运行，且 steady-state boundary 处理 export temp crash orphan，When plan/apply，Then只处理 effective mount 内 age/type/open-handle 全部合格且 exact plan-hash 未漂移的文件；durable same-T0 finalize receipt 原子切换 owner 后，legacy cleanup 必须停用，已知历史坏小时与 `qa_export_jobs` 保持不变。
8. **AC-008（qa_records UTC 小时生命周期）**：Given `qa_records` 已切换为 UTC 小时 RANGE 分区且 archive/boundary 共用 `QAMA` 锁，When `*:00` boundary owner 运行，Then 每条 pooled connection 继承短 DDL timeout，仅对可识别锁竞争有界重试，并在任何 DROP 前完成 `[current_hour, current_hour+72h)` catalog coverage；之后仅基于 catalog bound DROP `upper_bound <= retention_boundary` 的规范子分区，并在同一事务内完成 terminal gap 分类 + DROP + `source_dropped_at`；hot blob/DLQ 小时目录在 DROP 后幂等清理；稳态禁止 rehome/staging/copy/move 与逐行 QA retention；非锁错误、取消、重试耗尽、`archive_failed`、分区缺口、DEFAULT 稳态残留与 hot-file 残留必须 fail closed/health failed。
9. **AC-009（过期 gap 批量决策）**：Given 已过 24 小时、源行数为零、无 commit-ready segment、非 committed/terminal 且 recovery role 确认无 `commit.json` 的精确小时，When 生成 batch plan，Then hash 绑定 DB UTC anchor/cutoff/cutover/latest-normal、精确窗口、control window/update/segment fingerprint/commit-ready fact、bucket/role/recovery-run 与 S3 absence；When 使用精确 hash 与 approver apply，Then 在 `QAMA` 同一事务中重验证全部事实、通过 `qa_archive_shards` owner 写入 `failed/source_unavailable_after_retention` 并追加不可变 receipt；任一事实漂移整批拒绝，且不写 S3、不删/搬数据、不切 timer。

## Assertions

- `forward_cutover` 的 schema owner 是 `tk_072_qa_archive_forward_cutover.sql`；两个既有 `tk_071` migration 均先于它，且不改名。
- 唯一 write API 不接收 window 或 boolean；批准小时由 archive package 持有，operator 只做固定确认与接线。
- `cleanup_eligible=false` 与 `deletion_authorized=false` 在整个 Phase 2 保持不变。
- `2026-08-07 01:00 UTC` 保持 `commit_mismatch`，`2026-08-04 04:00 UTC` 保持 `missing_evidence`，不得修改两者 S3 commit。
- `production_recloseout_state: production_recloseout_verified`：2026-08-14 的只读生产复核已确认 archive、maintenance timer、boundary、小时分区 owner、raw archive IAM、same-T0 finalize、owner switch 与真实 partition DROP 均已落地；历史 terminal gaps 仍按 `accepted_terminal` 保持 degraded。
- `retry_release_observation: pending`：本 PR 新增的 bounded lock retry 与 receipt/heartbeat 尝试字段尚未合并发版；当前生产旧格式仅由双边全旧 rolling-compatibility 规则接受，不能被表述为新重试已在线验证。

## Linked Tests

- `backend/internal/observability/qa/archive/control_schema_test.go`::`TestUS045_QAArchiveForwardCutoverMigrationFollowsBothTK071Migrations`
- `backend/internal/observability/qa/archive/control_schema_test.go`::`TestUS045_QAArchiveForwardCutoverMigrationIsAdditiveValidAndImmutable`
- `backend/internal/observability/qa/archive/cutover_test.go`::`TestUS045_SQLControlStoreSetsOnlyApprovedForwardCutover`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_SetForwardCutover*`
- `backend/internal/observability/qa/archive/timeline_selector_test.go`::`TestUS045_TimelineCompensation*`
- `backend/internal/observability/qa/archive/timeline_selector_integration_test.go`::`TestUS045_SQLTimelineSelectorPersistsTerminalAndFindsUncoveredIdentity`
- `backend/internal/observability/qa/archive/reconciler_test.go`::`TestUS045_ReconcilerTimelyZeroRowCommitsRestorableBase`
- `backend/internal/observability/qa/archive/reconciler_test.go`::`TestUS045_ReconcilerExpiredZeroRowBecomesSourceUnavailableFailure`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_DefaultPlanEnsuresNormalControlBeforeSourceInspection`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_NormalFirstBoundedCompensationSkipsSelectionAfterNormalFailure`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_NormalFirstBoundedCompensationStopsWhenNoCandidateExists`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_NormalFirstBoundedCompensationRunsExactlyOneOldestCandidate`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_NormalFirstBoundedCompensationKeepsNormalSuccessWhenCatchupFails`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_NormalFirstBoundedCompensationReportsSelectionAndTerminalFailures`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_QAMaintenanceCommandReportsCommittedNormalAndCompensationFacts`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_QAMaintenanceCommandFailureHeartbeatPreservesNormalSuccess`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_QAMaintenanceCommandUnlockFailureHeartbeatPreservesNormalSuccess`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_RepairApplyIsUnavailableBeforeDependencies`
- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_us045_qa_archive_closeout_rejects_repair_apply_before_aws`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2RunnerTest.test_us045_selftest_uses_real_image_user_and_mount_for_create_read_remove`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2RunnerTest.test_us045_runner_success_writes_atomic_correlated_receipt`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2RunnerTest.test_us045_runner_rejects_zero_exit_without_correlated_child_receipt`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2RunnerTest.test_us045_runner_records_every_pre_app_failure`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2OperatorAndHealthTest.test_us045_timer_and_operators_invoke_the_single_host_runner`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2OperatorAndHealthTest.test_us045_correlated_health_accepts_only_matching_fresh_success`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2OperatorAndHealthTest.test_us045_correlated_health_rejects_missing_stale_and_contradictory_facts`
- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_qa_maintenance_sync_emits_disabled_timer_command_by_default`
- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_historical_closeout_has_fixed_targets_and_safety_guards`
- `ops/qa/test_prod_qa_stale_cleanup.py`::`ProdQAStaleCleanupTest.test_us045_export_orphan_plan_is_exact_and_revalidated`
- `ops/qa/test_prod_qa_stale_cleanup.py`::`ProdQAStaleCleanupTest.test_scheduled_export_cleanup_waits_for_exact_first_activation`
- `ops/qa/test_prod_qa_stale_cleanup.py`::`ProdQAStaleCleanupTest.test_export_orphan_apply_rejects_plan_drift_before_removal`
- `ops/qa/test_prod_qa_stale_cleanup.py`::`ProdQAStaleCleanupTest.test_export_tmp_override_resolves_its_effective_bind`
- `ops/qa/test_prod_qa_stale_cleanup.py`::`ProdQAStaleCleanupTest.test_age_retention_first_apply_does_not_depend_on_maintenance_timer`
- `ops/qa/test_prod_qa_stale_cleanup.py`::`ProdQAStaleCleanupTest.test_dedicated_operator_plan_cannot_depend_on_generic_retention`
- `ops/qa/test_prod_qa_stale_cleanup.py`::`ProdQAStaleCleanupTest.test_legacy_cleanup_rejects_every_plan_after_finalize`
- `ops/qa/test_prod_qa_stale_cleanup.py`::`ProdQAStaleCleanupTest.test_generic_data_retention_plan_cannot_drive_qa_cleanup`
- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_qa_stale_timer_enable_requires_first_apply_receipt_before_aws`
- `deploy/aws/stage0/test_build_cfn.py`::`BuildCfnSizeTest.test_qa_orphan_helper_is_distributed_within_ssm_standard_limits`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_WorkstationRecoveryInspectUsesDirectS3WithoutAppConfigOrDatabase`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_WorkstationRecoveryInspectReportsMissingAndCorruptEvidenceWithoutDependencies`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_WorkstationRecoveryVerifyUsesDirectS3WithoutAppConfigOrDatabase`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_WorkstationRecoveryRestoreUsesExplicitLocalRootAndSecureModes`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_WorkstationRecoveryRestoreRejectsMissingPrivacyConfirmationBeforeDependencies`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_WorkstationRecoveryRequiresAllDirectS3ParametersBeforeDependencies`
- `deploy/aws/cloudformation/test_stage0_qa_raw_archive_contract.py`::`Stage0QARawArchiveContractTest.test_us045_app_role_has_suffix_scoped_access_without_list_or_partial_reads`
- `deploy/aws/cloudformation/test_stage0_qa_raw_archive_contract.py`::`Stage0QARawArchiveContractTest.test_us045_recovery_role_is_nonempty_read_only_and_audited`
- `deploy/aws/cloudformation/test_stage0_qa_raw_archive_contract.py`::`Stage0QARawArchiveContractTest.test_us045_structured_contract_rejects_broadened_actions_resources_and_removed_conditions`
- `deploy/aws/cloudformation/test_stage0_qa_raw_archive_contract.py`::`Stage0QARawArchiveContractTest.test_us045_deploy_renders_exact_security_binding_and_shared_role_boundary`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_break_glass_script_is_retired`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_missing_recovery_evidence_rejects_without_authorizing_transition`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_mismatched_recovery_evidence_rejects_without_authorizing_transition`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_verified_synthetic_evidence_authorizes_only_planned_transition`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_relabeled_synthetic_evidence_cannot_claim_production_success`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_copied_command_receipts_cannot_be_production_evidence`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_production_evidence_requires_hash_bound_human_approval`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_stale_production_receipts_cannot_authorize_retirement`
- `backend/internal/pkg/pgpartition/hourly_test.go`::`TestRetentionBoundaryUsesDatabaseHourSemantics`
- `backend/internal/pkg/pgpartition/hourly_test.go`::`TestHourlyTargetRangesCrossMonthYear`
- `backend/internal/observability/qa/lifecycle/boundary_test.go`::`TestRetentionUntilForHourUsesHourStartPlus25Hours`
- `backend/internal/observability/qa/lifecycle/boundary_test.go`::`TestBuildCutoverFinalizePlanRequiresDrainAndT0Plus25Hours`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestRunBoundaryStopsBeforeExpiryWhenProvisioningFails`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestDropExpiredHourLocksChildBeforeInspectingArchiveCoverage`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestDropExpiredHourRejectsCatalogBoundDriftAfterLock`
- `backend/internal/observability/qa/lifecycle/cutover_apply_test.go`::`TestApplyCutoverFinalizeRequiresMatchingActivationReceipt`
- `backend/internal/observability/qa/lifecycle/cutover_apply_test.go`::`TestApplyCutoverFinalizeDropsEmptyDefaultAndPersistsReceipt`
- `backend/cmd/server/qa_maintenance_boundary_test.go`::`TestQABoundaryLockContentionAdvancesFailureHeartbeatAndReceipt`
- `backend/cmd/server/qa_maintenance_boundary_test.go`::`TestOpenQABoundaryDBAppliesTimeoutsToEveryPooledConnection`
- `backend/cmd/server/qa_maintenance_boundary_integration_test.go`::`TestUS045_QABoundaryPooledConnectionsApplyLockTimeoutAndRetry`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestRunProvisionRetriesOnlyLockContentionBeforeCoverageCheck`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestRunProvisionDoesNotRetryNearMatchLockTimeoutText`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestRunBoundaryLockRetryExhaustionRemainsFailClosed`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestRunProvisionContextCancellationStopsLockRetry`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2OperatorAndHealthTest.test_boundary_correlated_nonzero_retry_facts_remain_healthy`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2OperatorAndHealthTest.test_boundary_legacy_retry_shape_is_accepted_during_rollout`
- `backend/internal/repository/migrations_schema_integration_test.go`::`TestQAHourlyCutoverFinalizeReceiptRequiresMatchingActivationT0`
- `ops/qa/test_qa_boundary_runtime.py`::`QABoundaryRunnerTest.test_finalize_operator_requires_fresh_successful_archive_receipt`
- `ops/qa/test_qa_boundary_runtime.py`::`QABoundaryDeploymentTest.test_ssm_sync_installs_runtime_and_switches_cleanup_owner_atomically`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2OperatorAndHealthTest.test_us045_catalog_health_rejects_finalize_without_same_t0_activation`
- `backend/internal/observability/qa/lifecycle/hot_paths_test.go`::`TestValidateHourDirRejectsEscape`
- `backend/internal/repository/pgpartition_integration_test.go`::`TestPgPartition_EnsureHourlyCoversFutureHorizon`
- `backend/internal/repository/pgpartition_integration_test.go`::`TestPgPartition_HourlyWriteRoutesToChildPartition`
- `backend/internal/repository/pgpartition_integration_test.go`::`TestPgPartition_DropExpiredHourlyUsesCatalogUpperBound`
- `backend/internal/observability/qa/archive/gap_decision_test.go`::`TestUS045_BuildGapDecisionPlanSelectsOnlyExpiredEmptyNonterminalHours`
- `backend/internal/observability/qa/archive/gap_decision_test.go`::`TestUS045_CompleteGapDecisionPlanFromRecoveryStoreBindsEveryHeadResult`
- `backend/internal/observability/qa/archive/gap_decision_test.go`::`TestUS045_ApplyGapDecisionRejectsSourceOrControlDriftAtomically`
- `backend/internal/observability/qa/archive/gap_decision_test.go`::`TestUS045_ApplyGapDecisionRejectsSegmentFingerprintDrift`
- `backend/internal/observability/qa/archive/gap_decision_test.go`::`TestUS045_ApplyGapDecisionPersistsTerminalRowsAndApprovalReceiptInOneTransaction`
- `backend/internal/observability/qa/archive/gap_decision_integration_test.go`::`TestUS045_GapDecisionPlanJSONApplyIsAtomicAndIdempotentInPostgreSQL`
- `backend/internal/observability/qa/archive/control_schema_test.go`::`TestUS045_QAArchiveGapDecisionReceiptsAreAppendOnlyAndNotASecondStateMachine`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_GapDecisionS3PlanUsesOnlyWorkstationRecoveryStore`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_GapDecisionApplyRequiresHashBoundConfirmationBeforeDependencies`
- `backend/cmd/qa-archive/main_test.go`::`TestUS045_GapDecisionPlanTransportRejectsOversizedExpansion`
- `ops/qa/test_prod_qa_archive_closeout.py`::`ProdQAGapDecisionOperatorTest.test_gap_plan_binds_db_recovery_facts_and_writes_secure_atomic_plan`
- `ops/qa/test_prod_qa_archive_closeout.py`::`ProdQAGapDecisionOperatorTest.test_gap_apply_rejects_wrong_confirmation_before_remote_execution`
- `ops/qa/test_prod_qa_archive_closeout.py`::`ProdQAGapDecisionOperatorTest.test_gap_db_plan_decodes_bounded_gzip_receipt`
- `ops/qa/test_prod_qa_archive_closeout.py`::`ProdQAGapDecisionOperatorTest.test_gap_apply_refuses_oversized_ssm_transport_before_remote_call`

运行命令：

```bash
cd backend
go test -tags=unit -count=1 -run 'TestUS045_' ./cmd/qa-archive
go test -tags=unit -count=1 -run 'TestUS045_' ./cmd/server
go test -tags=integration -count=1 -run 'TestUS045_' ./internal/observability/qa/archive ./cmd/server
go test -tags=unit -count=1 -run 'TestHourly|TestRetention|TestBuildCutover|TestRunBoundary|TestDropExpiredHour|TestApplyCutover|TestValidateHourDir' ./internal/pkg/pgpartition/... ./internal/observability/qa/lifecycle/...
go test -tags=integration -count=1 -run 'TestQAHourlyCutover|TestPgPartition_EnsureHourly|TestPgPartition_Hourly|TestPgPartition_DropExpiredHourly' ./internal/repository/
go test -tags=unit -count=1 -run 'TestUS045_.*GapDecision' ./internal/observability/qa/archive ./cmd/qa-archive
go test -tags=integration -count=1 -run 'TestUS045_.*GapDecision' ./internal/observability/qa/archive
cd ..
python3 -m unittest ops.qa.test_qa_boundary_runtime ops.observability.test_probe_qa_phase2_live_health ops.qa.test_prod_phase2_live_health ops.qa.test_qa_maintenance_phase2_runtime ops.qa.test_qa_phase_ops
python3 -m unittest ops.qa.test_prod_qa_archive_closeout
python3 -m unittest ops.qa.test_prod_qa_stale_cleanup
python3 scripts/checks/qa-lifecycle-ssot.py --self-test
python3 -m unittest deploy.aws.cloudformation.test_stage0_qa_raw_archive_contract ops.qa.test_qa_archive_recovery_gate
python3 .testing/user-stories/verify_quality.py
```

## Evidence

- Task 1 已实跑 fixed CLI unit tests 与隔离 PostgreSQL migration/control integration tests；migration 只应用于 testcontainer。
- Task 2 已实跑 timeline selector、可恢复零行 base、retention 后终态和 late-identity membership 的 unit/integration tests；PostgreSQL 仅为本地 testcontainer。
- Task 3 已实跑 maintenance normal-first/单一补偿/失败 heartbeat、archive-disabled no-write 状态、CLI 与 Python operator 的 `repair-apply` 退役测试；archive PostgreSQL integration 只使用本地 Colima testcontainer。
- Task 4 已实跑唯一 host runner、真实 mount selftest、原子 receipt、全部 pre-app/child failure、timer/operator 收敛、Go run correlation、同步脚本与四源 health contradiction 测试；所有 Docker/AWS/systemd 边界均为本地 fake 或临时目录。
- Task 5 已实跑 effective export mount 解析、24 小时边界、regular-file/symlink/open-handle 选择、canonical plan hash、drift revalidation、首次 activation 与后续 scheduled cleanup 测试；helper 经实际 runner、CFN/SSM payload 与 timer-sync payload 分发验证。全部文件删除只发生在本地临时目录，`qa_export_jobs` 仅输出诊断。
- Task 6 已实跑结构化 IAM、部署渲染、workstation direct-S3 CLI 边界与 recovery retirement gate 测试；全部 AWS/DB/S3 边界使用本地 fake、memory store 或临时目录。
- 2026-08-10 production workstation recovery：`2026-08-07T21:00:00Z` cutover window 经 recovery role 完成 inspect/verify/restore；gate `plan-retirement` 在 `approved_by=feng` approval 下通过。生产 receipt/approval _bundle 由 operator 本地保管（不入库）；执行时证据目录示例 `/tmp/tk-qa-workstation-recovery-20260810T071805Z/`（含 `recovery-evidence.json`、`production-approval.json`）。
- break-glass prod QA dump 工具已退役；prod deploy 默认注入 `QA_ARCHIVE_ENABLED=true`。
- Task 7（qa_records UTC 小时生命周期）：已实跑 hourly bound/unit tests、lifecycle hot-path/boundary tests 与 PostgreSQL integration（EnsureHourly 覆盖、写入路由、catalog-bound DROP）；rehome/staging/copy/move 路径已删除并由 sentinel + qa-lifecycle-ssot 守卫。
- 2026-08-14 production recloseout：只读四源 evaluator 已关联 archive/boundary systemd、host receipt、DB heartbeat、archive control 与 `qa_records` catalog；same-T0 finalize、timer owner switch、真实 partition DROP/hot cleanup 和 raw archive IAM 均已验证，forward reasons 为空。历史 `source_unavailable_after_retention` 仍使总状态保持 degraded。

## Status

- [x] InTest — Phase 2 仓库实现、本地行为测试、PostgreSQL testcontainer integration 与 2026-08-14 production recloseout 已完成；本 Story 不声明未来 archive-gated lifecycle 已实现。本 PR 未写线上服务，新增 bounded lock retry 及其 receipt/heartbeat 字段仍须在合并发版后通过真实调度观测，届时再将 `retry_release_observation` 更新为 `verified`。
