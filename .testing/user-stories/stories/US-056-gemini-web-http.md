# US-056 Gemini Web Cookie HTTP gateway

- ID: US-056
- Priority: P1
- Title: Gemini Web Cookie HTTP gateway
- As a / I want / So that: 运营复用两个独立 Web 会话，经 us4 提供官方 Gemini 格式的文本和原图。
- Trace: [授权与契约](../../../docs/approved/gemini-web-channel.md)
- Risk Focus:
  - 逻辑错误：位置编码帧解析、官方响应、原图与预览区分。
  - 行为回归：Messages/native 入口均传网关选中账号；普通 API 账号不受影响。
  - 安全问题：edge 管理员鉴权、Worker 账号密钥、Cookie 脱敏和复制清理。
  - 运行时问题：数据库租约与 CAS、Cookie 删除、重启、下载失败不重复生图。

## Acceptance Criteria

1. AC-001 正向：完成候选转换为官方 text/inlineData；不得虚构 usage/thoughts。
2. AC-002 负向：无效鉴权、越界请求、非法图片 URL 在上游副作用前被拒绝。
3. AC-003 隔离：一号 Cookie 更新持久化后重启可读，另一号 Cookie 不变；同号重入被拒绝。
4. AC-004 回归：图片下载失败不再次发起生成，也不退回预览；过期会话暂停状态跨重启保存。
5. AC-005 安全：普通 DTO/审计不含 Cookie；复制不保留会话且不可调度；控制接口拒绝普通用户。
6. AC-006 并发：竞争 owner 与过期租约不能执行/写回；自身保存不触发错误重载。
7. AC-007 部署影响：空闲维护不写库、旧编辑不覆盖新导入；过载明确拒绝且健康检查可用；排空完成在途请求；超预算图片在解码前拒绝；不兼容控制 API 和暂停账号不能通过部署检查。
8. AC-008 请求能力准入：不兼容 system、历史、tools、controls 的请求在选账号前跳过 Web 账号；万能 key 可选择其他授权账号，Direct 不越权换组；原生单轮文本与生图仍可用，等待后刷新重验能力声明。
9. AC-009 导入并发：显式导入成功必须实际落库；竞争导入只有一个成功，Worker 更新或活跃租约使旧导入冲突；保留并发更新的 API Key、模型映射和暂停状态，旧编辑不覆盖新会话。
10. AC-010 初始化：复制 Worker 账号保留空 `gemini_web` 声明；首次导入以版本 1 原子初始化并保持调度关闭，普通 Gemini API Key 和 prod 中继仍拒绝。
11. AC-011 导入安全：仅声明本地 Worker 能力的账号可导入，中继/普通账号拒绝；错误和响应不含 Cookie；文件上限、Cookie 类型、版本、域范围与 CDP 会话有效期均按契约处理。
12. AC-012 UI 生命周期：真实编辑页面可选文件导入并显示提交摘要；成功导入保留未保存的表单输入；畸形/超限文件不发送；读文件中关闭/切换或迟到 HTTP 响应不串号、不更新新弹窗。

## Assertions

断言官方候选内容和原图字节一致、拒绝时无上游调用、不同账号 Cookie 不串写、失败不重生图。

## Linked Tests

- AC-001: `ops/gemini-web/test_worker.py`::`WorkerTests.test_official_response_omits_unobserved_usage_and_thoughts`
- AC-001: `ops/gemini-web/test_worker.py`::`WorkerTests.test_original_rpc_and_text_url_hops_return_real_bytes`
- AC-002: `ops/gemini-web/test_worker.py`::`WorkerTests.test_http_auth_and_buffered_sse`
- AC-002: `ops/gemini-web/test_worker.py`::`WorkerTests.test_bad_input_has_no_upstream_side_effect`
- AC-002: `ops/gemini-web/test_worker.py`::`WorkerTests.test_untrusted_hops_rejected_before_request`
- AC-003: `ops/gemini-web/test_worker.py`::`WorkerTests.test_cookie_deletion_persistence_and_restart`
- AC-003: `backend/internal/service/gemini_web_tk_test.go`::`TestGeminiWebSelectedAccountHeaderBothEntryPoints`
- AC-004: `ops/gemini-web/test_worker.py`::`WorkerTests.test_download_failure_does_not_regenerate_or_return_preview`
- AC-004: `ops/gemini-web/test_worker.py`::`WorkerTests.test_auth_failure_persists_pause_and_transport_error_is_redacted`
- AC-005: `backend/internal/handler/gemini_web_session_handler_test.go`::`TestGeminiWebControlRejectsInvalidAccessAndConflicts`
- AC-005: `backend/internal/handler/dto/credentials_redact_test.go`::`TestRedactCredentials_StripsSensitiveKeysAndReportsStatus`
- AC-005: `backend/internal/service/admin_service_duplicate_account_test.go`::`TestDuplicateAccountRemovesGeminiWebSession`
- AC-006: `ops/gemini-web/test_worker.py`::`WorkerTests.test_control_lease_prevents_a_second_process_and_fences_stale_save`
- AC-006: `ops/gemini-web/test_worker.py`::`WorkerTests.test_control_owner_keeps_its_saved_version_and_reloads_operator_import`
- AC-006: `backend/internal/repository/account_gemini_web_tk_integration_test.go`::`AccountRepoSuite.TestGeminiWebLeaseAndCAS`
- AC-007: `backend/internal/repository/account_gemini_web_tk_integration_test.go`::`AccountRepoSuite.TestGeminiWebMaintenanceOnlyReturnsDueAccounts`
- AC-007: `backend/internal/repository/account_gemini_web_tk_integration_test.go`::`AccountRepoSuite.TestGeminiWebImportSurvivesPreBindingEditor`
- AC-007: `ops/gemini-web/test_worker.py`::`WorkerTests.test_idle_poll_is_read_only_and_paused_sessions_fail_deploy_check`
- AC-007: `ops/gemini-web/test_worker.py`::`WorkerTests.test_http_auth_and_buffered_sse`
- AC-007: `ops/gemini-web/test_worker.py`::`WorkerTests.test_drain_finishes_inflight_request_and_releases_lease`
- AC-007: `ops/gemini-web/test_worker.py`::`WorkerTests.test_image_pixel_limit_rejects_before_decode`
- AC-007: `ops/gemini-web/test_worker.py`::`WorkerTests.test_large_image_decode_and_buffer_fit_container_budget`
- AC-005: `ops/gemini-web/test_worker.py`::`WorkerTests.test_control_redirect_never_forwards_admin_key`
- AC-008: `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebCandidateRejectsProductionChatWithoutPoisoningPeer`
- AC-008: `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebCandidateNativeTextAndImageRemainAvailable`
- AC-008: `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebCandidateNativeActionCapability`
- AC-008: `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebCandidatePreservesLocalCountTokens`
- AC-008: `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebCandidateRechecksCapabilityAfterWait`
- AC-008: `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebAdmissionMatchesWorkerContractFixtures`
- AC-008: `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebChatAndResponsesDefaultLimitIsNotWorkerCapability`
- AC-008: `ops/gemini-web/test_worker.py`::`WorkerTests.test_scheduler_admission_contract_fixtures`
- AC-009: `backend/internal/repository/account_gemini_web_tk_integration_test.go`::`TestGeminiWebConcurrentImportsHaveOneWinner`
- AC-009: `backend/internal/repository/account_gemini_web_tk_integration_test.go`::`AccountRepoSuite.TestGeminiWebExplicitImportConflictsAndPreservesAccount`
- AC-009: `backend/internal/service/admin_gemini_web_import_test.go`::`TestGeminiWebImportServiceReportsCASConflict`
- AC-009: `backend/internal/handler/admin/account_handler_gemini_web_import_test.go`::`TestGeminiWebImportHandlerSuccessAndConflict`
- AC-010: `backend/internal/handler/admin/account_handler_gemini_web_import_test.go`::`TestGeminiWebImportHandlerInitializesCopiedWorker`
- AC-010: `backend/internal/repository/account_gemini_web_tk_integration_test.go`::`TestGeminiWebConcurrentInitializationsHaveOneWinner`
- AC-010: `backend/internal/repository/account_gemini_web_tk_integration_test.go`::`AccountRepoSuite.TestGeminiWebInitializationGuardsAndPreservesAccount`
- AC-010: `backend/internal/service/gemini_web_request_tk_test.go`::`TestCanImportGeminiWebSessionAcceptsCopiedDeclarationOnly`
- AC-011: `backend/internal/handler/admin/account_handler_gemini_web_import_test.go`::`TestGeminiWebImportHandlerRejectsInvalidInputAndNonWorker`
- AC-011: `backend/internal/handler/admin/account_handler_gemini_web_import_test.go`::`TestGeminiWebCookieScopeMatchesPythonOwner`
- AC-011: `ops/gemini-web/test_worker.py`::`WorkerTests.test_admin_cookie_scope_matches_worker_owner`
- AC-012: `frontend/src/components/account/__tests__/EditAccountModal.spec.ts`::`Gemini Web session import`
- AC-012: `frontend/e2e/gemini-web-import.e2e.ts`::`Worker import success, redacted errors, size limit and relay boundary`
- AC-012: `frontend/e2e/gemini-web-import.e2e.ts`::`switching accounts during file read cannot submit credentials to either account`
- AC-010: `frontend/e2e/gemini-web-import.e2e.ts`::`copy Worker and initialize its own session while scheduling stays disabled`

运行命令：

```sh
python3 -m pip install -r ops/gemini-web/requirements.txt
python3 -m unittest discover -s ops/gemini-web -v
python3 -m unittest discover -s ops/stage0 -p 'test_probe_account_model*.py'
cd backend
go test -tags=unit ./internal/handler/admin ./internal/handler/dto ./internal/service -run 'Test(GeminiWeb|CanImport|DuplicateAccount|RedactCredentials|ValidateGeminiWeb|NormalizeGeminiWeb)'
go test -tags=integration ./internal/repository -run 'TestGeminiWebConcurrent|TestAccountRepoSuite/TestGeminiWeb' -v
cd ../frontend
pnpm exec vitest run src/components/account/__tests__/EditAccountModal.spec.ts
pnpm exec playwright test --config playwright.gemini-import.config.ts
```

## Evidence

本地验证覆盖数据库模式的协议、HTTP、鉴权与会话生命周期。旧 canary 的在线证据见契约历史证据节；不能替代本次数据库实现的部署验收。
2026-09-23 补齐导入 Handler/Service、真实 PostgreSQL 并发与保留语义测试、编辑弹窗组件测试及
Playwright Chromium 真实 UI 文件上传验收。浏览器使用模拟后台与假凭证，截图输出
`frontend/e2e/artifacts/gemini-import-success.png` 和 `gemini-import-account-switch.png`。
本机 Docker 数据盘不足，数据库测试以临时 Go overlay 将 PostgreSQL 数据目录放入 tmpfs；
业务代码和 SQL 未替换，标准 CI 仍使用既有 Testcontainers 环境。

复制初始化扩展：新增 Chromium 复制 → 编辑 → 上传旅程，截图
`frontend/e2e/artifacts/gemini-import-initialize.png`。真实 PostgreSQL 验证首次初始化竞争、
调度/租约守卫、保留其他账号字段及旧编辑不会覆盖 runtime；仍不证明 Google 登录有效性。

## Status

- InTest
