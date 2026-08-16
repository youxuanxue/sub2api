# US-044-qa-lifecycle-single-owner-and-export-contract

- ID: US-044
- Title: QA S3 Bundle 用户面与单一生命周期 owner
- Priority: P0
- As a / I want / So that: 作为 TokenKey 用户与生产运维者，我希望 QA 用户面只读取 S3 Bundle，且 hot lifecycle 只由 maintenance 拥有，以便 prod 数据库、旧 export worker 和固定时龄清理不能成为回退路径。
- Trace: `docs/approved/design-prod-qa-24h-s3-lifecycle.md`
- Risk Focus:
  - 逻辑错误：prod fallback 或第二 lifecycle owner 被重新引入。
  - 安全问题：Bundle entitlement 或 API-key ownership 被绕过。

## Acceptance Criteria

1. S3 Bundle list/detail/export 重复校验 entitlement、API-key ownership 与 committed artifact。
2. Immutable S3 `spec.json` 是 Bundle/ZIP job registry；篡改失败、过期即不存在，且不创建或读取数据库 job row。
3. prod trajectory/self-export worker、gateway download proxy 与 `qa_exports_tmp` orphan cleaner 不存在。
4. lifecycle sentinel 拒绝 prod fallback、第二目标 deletion owner、transition 之外的固定时龄 DROP 与 export-orphan runtime。
5. Bundle infra readiness 真实回读 bucket CORS/AES256 encryption/job-surface lifecycle、queue/DLQ、
   Fargate capacity 与 worker image；raw-S3-to-Fargate canary 是发布/日常健康门禁；Worker 以单记录/单页有界内存
   从 verified segments 发布 Bundle，ZIP immutable 校验只读对象流。
6. rollout 将 repository readiness 与 `single_owner_not_activated` observed state 分开记录。
7. single-owner activation 在锁内拒绝最近 24 个已完成小时及当前到未来 72 小时的 catalog 缺口和非精确 UTC-hour bounds；
   首次 Bundle 部署所需 IAM bootstrap 有唯一运维入口且 app image 切换前 fail closed。

## Linked Tests

- `backend/internal/handler/qa_handler_bundle_test.go`::`TestQABundleHandlersCreateAndReadScopedPendingJob`
- `backend/internal/observability/qa/service_bundle_test.go`::`TestUS044_QABundleReadsCommittedManifestAndRejectsCrossUser`
- `backend/internal/observability/qa/service_bundle_test.go`::`TestGetUserBundleRejectsTamperedS3RegistrySpec`
- `backend/internal/observability/qa/service_bundle_test.go`::`TestGetUserBundleTreatsExpiredS3RegistrySpecAsMissing`
- `backend/internal/observability/qa/service_bundle_test.go`::`TestCreateUserBundleExportRequiresReadyBundleAndSignsWorkerArtifact`
- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_qa_bundle_infra_verifier_checks_live_capacity_image_and_dlq`
- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_qa_bundle_canary_ssm_wrapper_requires_canonical_receipt`
- `ops/qa/test_qa_phase_ops.py`::`TestQAPhaseOps.test_qa_lifecycle_ssot_check_passes`
- `backend/cmd/server/qa_single_owner_activation_test.go`::`TestQASingleOwnerActivationPlanRejectsMissingRecentCompletedHour`
- `backend/cmd/server/qa_single_owner_activation_test.go`::`TestQASingleOwnerActivationPlanRejectsMissingCurrentOrFutureHour`
- `backend/cmd/server/qa_single_owner_activation_test.go`::`TestQASingleOwnerActivationPlanRejectsMalformedCurrentOrFutureHour`
- `backend/internal/observability/qa/bundle/projector_test.go`::`TestPublishVerifiedCommitsStreamsPagesBeforeLateProjectionFailure`
- `backend/internal/observability/qa/bundle/projector_test.go`::`TestVisitVerifiedSegmentsMergesDeterministically`
- `backend/internal/observability/qa/bundle/publisher_test.go`::`TestBuildExportZipReadsOnlyCommittedBundlePages`
- `deploy/aws/cloudformation/test_stage0_qa_bundle_contract.py`::`Stage0QABundleContractTest.test_qa_cloudformation_service_role_covers_managed_resource_lifecycles`
- `deploy/aws/cloudformation/test_stage0_qa_bundle_contract.py`::`Stage0QABundleContractTest.test_qa_bundle_verifier_roles_have_scoped_bucket_readback`
- `frontend/e2e/qa-bundle.e2e.ts`::`QA Bundle list, detail, watermark and ZIP export stay on Bundle/S3 paths`
- `frontend/e2e/qa-bundle.e2e.ts`::`QA Bundle entitlement denial removes the entry and never starts a job`
- `frontend/e2e/qa-bundle.e2e.ts`::`temporary unavailability is recoverable from the visible retry action`

运行命令：

```bash
cd backend && go test -tags=unit ./internal/handler ./internal/observability/qa
cd .. && python3 scripts/checks/qa-lifecycle-ssot.py
cd frontend && pnpm exec playwright test e2e/qa-bundle.e2e.ts --project=chromium
```

## Assertions

- User QA never falls back to prod DB/export workers.
- Maintenance is the only target deletion owner.
- Bundle projection and ZIP verification stay bounded by one record/page or an object stream, never one full 24-hour result.

## Evidence

- Repository: S3 Bundle contract and retired prod export code are present in this worktree.
- Local verification (2026-08-15): repository preflight passed and the real Chromium QA Bundle journey passed all 3 scenarios.
- Observed live: transitional; no deployment or activation is claimed.

## Status

- [x] Done — repository contract, focused behavior tests, real-browser journey, and full preflight are complete; production rollout remains a separate approved operation.
