# US-055 — QA Bundle 保真会话导出

- ID: US-055
- Priority: P1
- Title: QA Bundle 保真会话导出
- As a / I want / So that: 作为有 QA 读取权限的用户，我要从已有导出入口下载可追溯会话，以便分析推理与完整工具链。
- Trace: `docs/approved/qa-bundle-session-export.md`
- Risk Focus:
  - 逻辑错误：前缀去重、工具关联、reasoning 密文与 sig-only 丢失。
  - 行为回归：逐请求证据不变、旧 ZIP 不误复用、原生字段保留。
  - 安全问题：user/API key/wire 边界与既有授权守卫。
  - 运行时问题：跨页和交错会话、有界磁盘暂存、checksum、取消与不可变重试。

## Acceptance Criteria

1. AC-001（正向）：同一会话的工具调用和结果跨页/交错出现，导出一个会话并关联原始 request_id。
2. AC-002（负向）：相同提示、重复请求、跨用户/密钥/协议或歧义前缀不能被猜测合并。
3. AC-003（正向）：summary+encrypted、sig-only、原生 item id/phase、sidecar 来源均保留；流式与非流式原生内容等价。
4. AC-004（负向）：缺证据、失败、截断、孤立工具结果必须显式记账；checksum 失败和取消不发布 ZIP。
5. AC-005（回归）：ZIP 包含原始记录、会话及来源清单；旧 job 格式不覆盖；重复执行产物确定。
6. AC-006（UI）：Playwright 驱动真实 QA 面板下载，检查生产 exporter 生成的 ZIP；无 entitlement 不出现入口且不创建 job。
7. AC-007（SSOT）：旧 projector/契约回流失败；会话 owner 变更必须触发 Worker 发布判定。

## Linked Tests

- `backend/internal/observability/trajectory/session_test.go`::`TestUS055_SessionsReconstructInterleavedToolChain`
- `backend/internal/observability/trajectory/session_test.go`::`TestUS055_SessionsNeverGuessAcrossScopeRetriesOrAmbiguity`
- `backend/internal/observability/trajectory/session_test.go`::`TestUS055_StableSessionRetainsRewrittenHistory`
- `backend/internal/observability/trajectory/session_test.go`::`TestUS055_ResponsesReasoningAndNativeFieldsSurvive`
- `backend/internal/observability/trajectory/session_test.go`::`TestUS055_EncryptedOnlyAndAnthropicSignatureOnlyStreams`
- `backend/internal/observability/trajectory/session_test.go`::`TestUS055_MissingTruncatedAndSidecarEvidenceIsAccounted`
- `backend/internal/observability/trajectory/session_test.go`::`TestUS055_ChatAndGeminiNativeToolPayloads`
- `backend/internal/observability/trajectory/session_test.go`::`TestUS055_SessionExportRejectsDuplicatesCancellationAndWriteFailure`
- `backend/internal/observability/qa/bundle/session_export_test.go`::`TestUS055_BundleSessionZip`
- `backend/internal/observability/qa/bundle/session_export_test.go`::`TestUS055_BundleSessionZipRejectsCorruptPageAndCancellation`
- `backend/internal/observability/qa/bundle/session_export_test.go`::`TestUS055_ExportVersionIsImmutableAndLegacyJobsRemainReadable`
- `backend/internal/observability/qa/service_bundle_test.go`::`TestUS044_QABundleReadsCommittedManifestAndRejectsCrossUser`
- `frontend/e2e/qa-bundle.e2e.ts`::`QA Bundle list, detail, watermark and ZIP export stay on Bundle/S3 paths`
- `frontend/e2e/qa-bundle.e2e.ts`::`QA Bundle entitlement denial removes the entry and never starts a job`
- `scripts/checks/test_traj_ssot.py`::`TrajectorySSOTTest.test_reintroduced_projection_and_duplicate_owner_are_rejected`
- `ops/qa/test_qa_bundle_release_surface.py`::`QABundleReleaseSurfaceTest.test_session_projector_change_requires_worker_rollout`

运行命令：

```bash
cd backend && go test -tags=unit ./internal/observability/trajectory ./internal/observability/qa/bundle ./internal/observability/qa
cd .. && python3 scripts/checks/test_traj_ssot.py
python3 -m unittest ops.qa.test_qa_bundle_release_surface
cd frontend && pnpm exec playwright test e2e/qa-bundle.e2e.ts --project=chromium
```

## Assertions

每条输入记录进入原始证据和一个会话 call；推理密文与原生字段、工具关联使用行为断言；
UI 下载检查真实 ZIP 内容。HTTP 服务使用隔离 fixture，未声明生产 S3/Fargate 验证。

## Evidence

- 2026-09-11：trajectory / QA / Bundle unit 测试通过，trajectory race 检查通过。
- Playwright：真实 Chromium QA 面板、权限拒绝、失败重试通过；下载 ZIP 的会话、工具引用与签名已断言。
- Go lint、前端 typecheck、QA API/composable 测试与 traj SSOT/Worker surface 检查通过。
- HTTP 服务使用隔离 fixture，ZIP 由生产 Bundle exporter 实际生成；未执行生产 S3/Fargate canary，未部署。

## Status

- [x] Done
