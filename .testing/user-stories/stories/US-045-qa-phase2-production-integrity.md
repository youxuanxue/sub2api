# US-045-qa-phase2-production-integrity

- ID: US-045
- Title: QA Phase 2 production integrity closeout
- Priority: P0（不可逆归档控制状态、生产恢复与数据生命周期）
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **QA Phase 2 归档只按已批准状态机运行，并由唯一不可变 cutover、同一受限 runner、可验证恢复、最小 IAM 与独立 stale cleanup 共同守卫**，**以便** 缺失或损坏的归档不能伪装成成功，历史 repair 不能重开，保留期清理也不能获得额外删除授权。
- Trace:
  - 长期设计：`docs/approved/design-prod-qa-24h-s3-lifecycle.md`
  - Phase 2 收口：`docs/approved/design-qa-phase2-archive-closeout.md`
- Risk Focus:
  - 逻辑错误：cutover 指向非 `committed`、未 restore-verified 或非批准小时；normal 失败后仍补偿；保留期后无 source 的小时被写成空成功。
  - 行为回归：任意窗口 `repair-apply`、历史 backfill、move/unset cutover、第二 catchup owner 或 archive-gated stale cleanup 被重新引入。
  - 安全问题：共享 EC2 role 被误报为进程隔离；app 获得 raw bucket 无界 list/read；恢复正文绕过显式隐私确认或从 prod 回源。
  - 运行时问题：timer/operator 使用不同 image、UID/GID、mount、scratch 或资源限制；host receipt、DB heartbeat 与 control state 矛盾时仍报告健康；export 临时文件清理发生竞态或计划漂移。

## Acceptance Criteria

1. **AC-001（正向 cutover）**：Given `2026-08-07 21:00 UTC` shard 已 `committed` 且 `restore_verified_at` 非空，When 执行唯一 exact cutover operation，Then 只标记该 row，重复执行幂等，读取返回同一 valid cutover。
2. **AC-002（负向 cutover）**：Given shard 缺失、状态非法、未 restore-verified、window 不精确、已有不同 marker 或 control 数据损坏，When 读取或设置 cutover，Then fail closed；schema 阻止第二 marker、invalid marker、move、unset 与删除，CLI 不接受 generic cutover window 或 move/unset action。
3. **AC-003（状态机）**：Given 每轮 normal sealed hour，When normal 成功且 restore-verified，Then 只从 cutover 后的 UTC timeline 选择至多一个 oldest retryable candidate；normal 失败不补偿，补偿失败保留 normal 成功但整轮非零，terminal failure 可见且不阻塞后续小时。
4. **AC-004（完整性）**：Given timely zero-row hour、source retention 后首次发现的未知小时、late identity 或 missing/corrupt evidence，When reconcile，Then 分别产生可恢复空 base、`source_unavailable_after_retention`、单一 delta 或 failed/blocked control；不得合成缺失历史、授权删除或覆盖 immutable commit。
5. **AC-005（runtime）**：Given timer、operator、self-test 与 health probe，When执行或发生 pre-app/child failure，Then它们共用唯一 host runner 与批准的 image/user/mount/scratch/resource contract，原子 receipt 始终推进且四类运行事实矛盾时 fail closed。
6. **AC-006（IAM 与 recovery）**：Given app archive role 与 ops recovery role，When渲染 policy 或从 workstation inspect/verify/restore，Then app 只有所需 suffix-scoped artifact 权限且无无界 list/partial read，recovery 不经过 prod 主机/API/数据库，正文恢复要求 privacy confirmation，并如实保留 shared-role 风险。
7. **AC-007（stale cleanup 与回归）**：Given独立 24 小时 stale cleanup 与 export temp crash orphan，When plan/apply，Then只处理 effective mount 内 age/type/open-handle 全部合格且 exact plan-hash 未漂移的文件；archive completeness、cutover 或 maintenance health 不改变其 owner、窗口或删除权限，已知历史坏小时与 `qa_export_jobs` 保持不变。

## Assertions

- `forward_cutover` 的 schema owner 是 `tk_072_qa_archive_forward_cutover.sql`；两个既有 `tk_071` migration 均先于它，且不改名。
- 唯一 write API 不接收 window 或 boolean；批准小时由 archive package 持有，operator 只做固定确认与接线。
- `cleanup_eligible=false` 与 `deletion_authorized=false` 在整个 Phase 2 保持不变。
- `2026-08-07 01:00 UTC` 保持 `commit_mismatch`，`2026-08-04 04:00 UTC` 保持 `missing_evidence`，不得修改两者 S3 commit。
- 本 Story 的 InTest 只表示仓库实现正在闭环；不表示生产 schema、IAM、timer、恢复或清理已执行。

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
- `deploy/aws/stage0/test_build_cfn.py`::`BuildCfnSizeTest.test_qa_orphan_helper_is_distributed_within_ssm_standard_limits`
- `ops/archive/test_data_layer_retention_activation.py`::`RetentionActivationTest.test_qa_cleanup_readiness_is_independent_of_archive_and_maintenance`
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
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_missing_recovery_evidence_preserves_break_glass_state`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_mismatched_recovery_evidence_preserves_break_glass_state`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_verified_synthetic_evidence_authorizes_only_planned_transition`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_relabeled_synthetic_evidence_cannot_claim_production_success`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_copied_command_receipts_cannot_be_production_evidence`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_production_evidence_requires_hash_bound_human_approval`
- `ops/qa/test_qa_archive_recovery_gate.py`::`QAArchiveRecoveryGateTest.test_us045_stale_production_receipts_cannot_authorize_retirement`

运行命令：

```bash
cd backend
go test -tags=unit -count=1 -run 'TestUS045_' ./cmd/qa-archive
go test -tags=unit -count=1 -run 'TestUS045_' ./cmd/server
go test -tags=integration -count=1 -run 'TestUS045_' ./internal/observability/qa/archive
cd ..
python3 -m unittest ops.qa.test_qa_maintenance_phase2_runtime ops.qa.test_qa_phase_ops
python3 -m unittest ops.qa.test_prod_qa_stale_cleanup ops.archive.test_data_layer_retention_activation
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

## Status

- [x] Done — Phase 2 production integrity closeout 已完成：prod re-closeout、age retention、export orphan、workstation recovery evidence、break-glass 退役、deploy inject flip 均已在 prod/仓库 SSOT 对齐。
