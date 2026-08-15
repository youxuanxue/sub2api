# US-045-qa-phase2-production-integrity

- ID: US-045
- Title: QA Phase 2 历史证据与 single-owner 迁移边界
- Priority: P0
- As a / I want / So that: 作为生产运维者，我希望保留 Phase 2 已验证的 archive/recovery 历史证据，同时让当前目标只由 maintenance 执行 provision/archive/restore/seal/DROP/hot cleanup。
- Trace: `docs/approved/design-qa-phase2-archive-closeout.md`（historical/superseded runtime）与 `docs/approved/design-prod-qa-24h-s3-lifecycle.md`（current target）。
- Risk Focus:
  - 行为回归：历史双 timer/fixed-age 事实被误当目标，或 activation 后 boundary 复活。
  - 运行时问题：DROP 后 cleanup 丢失、无限重试或隐藏单小时失败。

## Acceptance Criteria

1. Phase 2 production recloseout remains historical evidence and does not authorize current/future deletion.
2. Before `single_owner_activate`, boundary can only provision hourly children; after receipt existence it fails closed.
3. Boundary sync success and rollback both force boundary disabled/inactive after activation.
4. Maintenance resumes a bounded list of exact already-dropped Blob/DLQ hours idempotently; per-hour failure remains visible without undoing DROP.
5. `retention_until` is legacy schema compatibility only and cannot authorize deletion.

## Linked Tests

- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_qa_boundary_sync_activation_receipt_forces_disabled_on_success_and_rollback`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestResumePendingHotCleanupsCleansExactDroppedHoursAndIsIdempotent`
- `backend/internal/observability/qa/lifecycle/boundary_execution_test.go`::`TestResumePendingHotCleanupsKeepsPerHourFailureVisibleAfterDrop`
- `backend/cmd/server/qa_maintenance_boundary_test.go`::`TestQABoundaryCommandRejectsRetiredCutoverModes`
- `backend/cmd/server/qa_maintenance_boundary_test.go`::`TestQABoundaryProvisionOnlyRefusesAfterSingleOwnerActivation`
- `backend/cmd/server/qa_maintenance_boundary_test.go`::`TestQABoundaryCommandRefusesAfterSingleOwnerActivation`

运行命令：

```bash
cd backend && go test -tags=unit ./internal/observability/qa/lifecycle ./cmd/server
cd .. && python3 -m unittest ops.qa.test_qa_phase_ops.TestQAPhaseOps.test_qa_boundary_sync_activation_receipt_forces_disabled_on_success_and_rollback
```

## Assertions

- Historical Phase 2 evidence does not authorize target-state deletion.
- Activation receipt existence permanently retires boundary ownership.

## Evidence

- Historical: `production_recloseout_verified` remains evidence of the superseded Phase 2 runtime only.
- Repository: single-owner implementation is ready for later approved rollout.
- Local verification (2026-08-15): linked lifecycle/server unit tests, boundary-sync regression, lifecycle sentinel/self-test, and repository preflight passed.
- Observed live: `single_owner_not_activated`; no deployment, DDL, timer activation, or production mutation occurred.

## Status

- [x] Done — repository transition guards and recovery behavior are verified; production activation remains a separate approved operation.
