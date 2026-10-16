# US-064-oauth-consolidation-gateway-safety

- ID: US-064
- Priority: P0
- Title: 集中后的 OAuth 保持出口与请求错误语义
- As a / I want / So that: 作为网关用户和运营，我希望集中后的账号保持配置出口、正确拒绝不支持的请求并报告真实流式失败，从而避免身份出口漂移和错误重试。
- Trace: `docs/approved/oauth-consolidation-gateway-safety.md`
- Risk Focus:
  - 逻辑错误：外厂模型能力与请求校验错误归属。
  - 行为回归：显式模型映射、API-key 和显式直连保持。
  - 安全问题：指定代理失效时不能发送凭据到直接出口。
  - 运行时问题：调度快照变旧、代理查库失败、重复重试、语义错误采样缺失。

## Acceptance Criteria

1. AC-001 (正向): Given 可用代理或显式无代理 When 刷新 OAuth Then 使用指定出口或有意直连；API-key 就绪策略不变。
2. AC-002 (负向): Given 配置代理失效或查库失败 When 调度、权威发送、刷新、管理测试或主动配额查询 Then 拒绝且不调用上游；共享 DB 故障不能隔离账号凭据；缓存用量仍可读取。
3. AC-003 (回归): Given Qwen 小数版本或显式映射 When 检查 Codex 能力 Then 拒绝外厂原始模型，保留映射到可用模型的行为。
4. AC-004 (负向): Given Kiro 精确图片格式 400 When 流式/非流式执行 Then 保留 400、只调用一个端点、不先写响应；同文案 503/echo 保留供应故障归属。
5. AC-005 (回归): Given SSE 最终 5xx、HTTP 5xx 或缺失元数据 When 巡检 Then 最终失败去重且不能 double count，缺证据为 unknown/yellow。

## Assertions

观察实际 executor/HTTP factory 调用次数、错误类型、选中出口、HTTP 响应写入和巡检计数与 verdict；不以字符串存在作为行为证据。

## Linked Tests

- `backend/internal/service/oauth_proxy_admin_test.go`::`TestOAuthAntigravityProxyFailureDoesNotClearCredentialError` (AC-002)
- `backend/internal/service/oauth_proxy_admin_test.go`::`TestOAuthModelsRejectsUnavailableProxyBeforeNetwork` (AC-002)

- `backend/internal/service/oauth_proxy_admin_test.go`::`TestOAuthAdminTestRejectsUnavailableProxyBeforeNetwork` (AC-002)
- `backend/internal/service/oauth_proxy_admin_test.go`::`TestOAuthAnthropicUsageRejectsUnavailableProxyBeforeNetwork` (AC-002)
- `backend/internal/service/oauth_proxy_admin_test.go`::`TestOAuthQuotaRejectsUnavailableProxyBeforeNetwork` (AC-002)
- `backend/internal/service/oauth_proxy_admin_test.go`::`TestOAuthUnavailableProxyKeepsCachedUsageReadable` (AC-002)

- `backend/internal/service/oauth_proxy_binding_test.go`::`TestOAuthProxyBindingScheduling` (AC-001/002)
- `backend/internal/service/oauth_proxy_binding_test.go`::`TestOAuthProviderRefreshRejectsUnavailableProxyBeforeNetwork` (AC-002)
- `backend/internal/service/oauth_proxy_binding_test.go`::`TestOAuthRefreshSendsConfiguredRouteAndExplicitDirect` (AC-001)
- `backend/internal/service/oauth_proxy_binding_test.go`::`TestOAuthRefreshRereadsProxyBeforeExecutor` (AC-002)
- `backend/internal/service/oauth_proxy_binding_test.go`::`TestExecuteSelectedProtocolRejectsLostOAuthProxyBeforeExecutor` (AC-002)
- `backend/internal/service/oauth_proxy_binding_test.go`::`TestOAuthProxyFailureScopePreservesSharedInfrastructure` (AC-002)
- `backend/internal/service/openai_oauth_model_support_test.go`::`TestIsModelSupported_OpenAIOAuthEmptyMapping_RejectsForeignModels` (AC-003)
- `backend/internal/service/openai_oauth_model_support_test.go`::`TestIsModelSupported_OpenAIOAuthExplicitMappingUnchanged` (AC-003)
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_ImageFormatRejectionIsClientError` (AC-004)
- `backend/internal/service/kiro_gateway_service_test.go`::`TestClassifyKiroImageValidationDoesNotMaskProviderFailure` (AC-004)
- `ops/observability/test_probe_post_release_tick.py`::`ProbePostReleaseTickTest.test_sse_final_failure_counts_once_and_unrelated_request_does_not` (AC-005)
- `ops/observability/test_probe_post_release_tick.py`::`ProbePostReleaseTickTest.test_wire_failure_is_not_double_counted_with_ops_final_failure` (AC-005)
- `ops/observability/test_probe_post_release_tick.py`::`ProbePostReleaseTickTest.test_missing_or_naive_correlation_metadata_cannot_report_healthy` (AC-005)

- Run command:

```bash
go test -C backend -tags=unit ./internal/service ./internal/integration/kiro ./internal/handler
python3 -m unittest ops/observability/test_probe_post_release_tick.py scripts/test_release_post_check.py
```

## Evidence

Kiro 图片 400 与 Qwen3.8 误投已先复现失败，修复后聚焦测试通过。发布巡检的假 Docker/SQL 返回值测试通过；真实生产发送及代理故障注入未执行，不能当作已发布验收。

## Status

- InTest — 代码与回归测试，部署及生产验收独立。
