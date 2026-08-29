# US-045-qa-phase2-production-integrity

- ID: US-045
- Title: QA single-owner 迁移边界
- Priority: P0
- As a / I want / So that: 作为生产运维者，我希望当前 QA lifecycle 只由 maintenance 执行 provision/archive/restore/seal/DROP/hot cleanup，已退休的 cutover 命令不能再当 owner。
- Trace: `docs/approved/design-prod-qa-24h-s3-lifecycle.md`
- Risk Focus:
  - 行为回归：历史双 timer/fixed-age 事实被误当目标，或 activation 后 boundary 复活。
  - 运行时问题：DROP 后 cleanup 丢失、无限重试或隐藏单小时失败。

## Acceptance Criteria

1. Retired cutover/inventory/finalize commands must not reappear as current lifecycle owners.
2. Before `single_owner_activate`, the single boundary timer path provisions future children and preserves the existing 24-hour whole-partition cleanup; after receipt existence it fails closed.
3. Boundary sync success and rollback both force boundary disabled/inactive after activation.
4. Maintenance resumes a bounded list of exact already-dropped Blob/DLQ hours idempotently; per-hour failure remains visible without undoing DROP.
5. `retention_until` is legacy schema compatibility only and cannot authorize deletion.
6. Read-only seal validation works on the mounted ledger；DROP/cleanup failure receipts preserve every already-committed deletion and resumed cleanup fact.

## Linked Tests

- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_qa_boundary_sync_activation_receipt_forces_disabled_on_success_and_rollback`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestResumePendingHotCleanupsCleansExactDroppedHoursAndIsIdempotent`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestResumePendingHotCleanupsKeepsPerHourFailureVisibleAfterDrop`
- `backend/cmd/server/qa_maintenance_boundary_test.go`::`TestQABoundaryCommandRejectsRetiredCutoverModes`
- `backend/cmd/server/qa_maintenance_boundary_test.go`::`TestQABoundaryRunsTransitionCleanupBeforeSingleOwnerActivation`
- `backend/cmd/server/qa_maintenance_boundary_test.go`::`TestQABoundaryCommandRefusesAfterSingleOwnerActivation`
- `backend/internal/observability/qa/captureledger/ledger_test.go`::`TestValidateHourSealWorksWithReadOnlyLedger`
- `backend/cmd/server/qa_maintenance_phase2_test.go`::`TestUS045_QAMaintenanceCommandDropFailureHeartbeatPreservesCommittedDeletion`
- `backend/cmd/server/qa_maintenance_test.go`::`TestQAMaintenanceDropPhasePreservesCommittedNormalDropOnCleanupError`
- `ops/qa/test_qa_maintenance_phase2_runtime.py`::`QAPhase2RunnerTest.test_phase3_runner_preserves_committed_drop_when_child_fails`

运行命令：

```bash
cd backend && go test -tags=unit ./internal/observability/qa/lifecycle ./cmd/server
cd .. && python3 -m unittest ops.qa.test_qa_phase_ops.TestQAPhaseOps.test_qa_boundary_sync_activation_receipt_forces_disabled_on_success_and_rollback
```

## Assertions

- Retired cutover/inventory/finalize commands do not authorize current deletion.
- Activation receipt existence permanently retires boundary ownership.

## Evidence

- Repository: single-owner implementation is ready for later approved rollout.
- Local verification (2026-08-15): linked lifecycle/server unit tests, boundary-sync regression, lifecycle sentinel/self-test, and repository preflight passed.
- Observed live: `single_owner_not_activated`; no deployment, DDL, timer activation, or production mutation occurred.

## Status

- [x] Done — repository transition guards and recovery behavior are verified; production activation remains a separate approved operation.
