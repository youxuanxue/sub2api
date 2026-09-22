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

运行命令：

```sh
python3 -m pip install -r ops/gemini-web/requirements.txt
python3 -m unittest discover -s ops/gemini-web -v
python3 -m unittest discover -s ops/stage0 -p 'test_probe_account_model*.py'
```

## Evidence

本地验证覆盖数据库模式的协议、HTTP、鉴权与会话生命周期。旧 canary 的在线证据见契约历史证据节；不能替代本次数据库实现的部署验收。
无 UI 工件；以上为协议/集成验证，不是 UI e2e。

## Status

- InTest
