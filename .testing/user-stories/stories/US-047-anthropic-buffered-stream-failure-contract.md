# US-047-anthropic-buffered-stream-failure-contract

- ID: US-047
- Title: Anthropic 缓冲流失败进入 failover，部分内容诚实标记截断
- Priority: P0
- As a / I want / So that: 作为 OpenAI-compatible API 调用方，我希望 Anthropic SSE 缓冲组装失败时得到可恢复且真实的结果，从而避免空 200、错误重试和重复计费。
- Trace: `docs/approved/anthropic-buffered-stream-failure-contract.md`
- Risk Focus:
  - 逻辑错误：区分只有 `message_start`、已有可用 content block、完整终止三种状态。
  - 行为回归：无内容失败走既有 failover；已有内容失败保留 HTTP 200。
  - 安全问题：错误 detail 继续受 `log_upstream_error_body` 控制，账号身份只进入既有 Ops 结构。
  - 运行时问题：覆盖 terminal error、未完整 EOF、scanner read error 和 native idle timeout。

## Acceptance Criteria

1. AC-001 (正向): Given 完整 Anthropic SSE When 缓冲组装完成 Then 返回正常 HTTP 200，且没有 upstream failure 标记。
2. AC-002 (负向): Given 尚无 content block When 收到 terminal `error`、超时、读错误或未完整 EOF Then service 返回 `UpstreamFailoverError`，未提前写客户端响应。
3. AC-003 (负向): Given 只有 `message_start` When 随后收到 `error` Then 不得返回空 HTTP 200。
4. AC-004 (回归): Given 已有 content block When 随后失败 Then 保留部分 HTTP 200，不 failover，并以 `stream_truncated` 计入 SLA。
5. AC-005 (契约): Given `not_found_error` 或 `request_too_large` When 最终对客 Then 分别使用 `not_found_error` 或 `request_too_large`，不得使用 `server_error`。
6. AC-006 (可观测性): Given 任一 buffered upstream failure When 记录 Ops event Then event 包含 platform、account ID 和 account name。

## Assertions

- 返回错误可通过 `errors.As(err, *UpstreamFailoverError)` 识别。
- failover error 的 `ClientStatusCode`、`ClientErrorType`、`ClientMessage` 固定最终对客契约。
- 部分内容路径 `GetOpsStreamErrors` 只有一个 `CountTowardsSLA=true` 的标记。
- 完整路径没有 `OpsUpstreamErrorsKey` 和 stream failure。

## Linked Tests

- `backend/internal/service/gateway_anthropic_buffered_error_tk_test.go`::`TestUS047_MessageStartThenErrorReturnsFailover`
- `backend/internal/service/gateway_anthropic_buffered_error_tk_test.go`::`TestUS047_PartialContentThenErrorPreservesResponse`
- `backend/internal/service/gateway_anthropic_buffered_error_tk_test.go`::`TestUS047_IncompleteEOFRoutesByContentAvailability`
- `backend/internal/service/gateway_anthropic_buffered_error_tk_test.go`::`TestUS047_ScannerReadErrorRoutesByContentAvailability`
- `backend/internal/service/gateway_anthropic_buffered_error_tk_test.go`::`TestUS047_CompletionBeforeReadErrorRemainsSuccessful`
- `backend/internal/service/openai_gateway_anthropic_native_pump_test.go`::`TestUS047_NativeBufferedIdleRoutesByContentAvailability`
- `backend/internal/service/gateway_anthropic_buffered_error_tk_test.go`::`TestUS047_ClientErrorTypeMapping`
- `backend/internal/handler/gateway_anthropic_buffered_failover_tk_test.go`::`TestUS047_GatewayFailoverExhaustedPreservesClientErrorType`
- `backend/internal/handler/gateway_anthropic_buffered_failover_tk_test.go`::`TestUS047_ResponsesFailoverExhaustedPreservesClientErrorType`
- `backend/internal/handler/gateway_anthropic_buffered_failover_tk_test.go`::`TestUS047_OpenAIFailoverExhaustedPreservesClientErrorType`
- `backend/internal/handler/gateway_anthropic_buffered_failover_tk_test.go`::`TestUS047_EmptyClientErrorTypeKeepsLegacyDefaults`
- Run:

```bash
cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/service -run 'TestUS047_' -count=1
cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/handler -run 'TestUS047_' -count=1
cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/service -count=1
python3 scripts/sentinels/check-gateway-tk.py
```

## Evidence

- `TestUS047_` service tests: PASS (`ok github.com/Wei-Shaw/sub2api/internal/service 3.211s`).
- `TestUS047_` handler tests: PASS (`ok github.com/Wei-Shaw/sub2api/internal/handler 1.161s`).
- Full `internal/service` unit package: PASS.
- Gateway TK sentinel registry: PASS (`742/742 intact`).

## Status

- [x] Done
