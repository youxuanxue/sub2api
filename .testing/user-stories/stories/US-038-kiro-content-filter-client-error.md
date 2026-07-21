# US-038-kiro-content-filter-client-error

- ID: US-038
- Title: Kiro CONTENT_FILTERED returns a client-owned 400 without failover
- Priority: P0 (production 502 root-cause correction)
- As a / I want / So that:
  作为 **TokenKey API 调用方与运维者**，我希望 **Kiro 的 `metadataEvent.stopReason=CONTENT_FILTERED` 被识别为最终客户端请求错误**，**以便** 调用方收到稳定的 400 契约，同时该结果不再触发跨账号/edge failover、账号惩罚或 provider/platform SLA 告警。
- Trace:
  - 根因轴线：Kiro 返回 HTTP 200 EventStream，只有 `metadataEvent(CONTENT_FILTERED)`、context usage 和 metering，没有 `assistantResponseEvent`；旧实现忽略 metadata 并把结果归为普通空响应 502。
  - 对外契约轴线：`/v1/messages`、`/v1/chat/completions`、`/v1/responses` 分别返回既有协议兼容的 400 错误 envelope。
  - 可观测轴线：最终归因固定为 `phase=request`、`error_owner=client`、`error_source=client_request`；此前账号尝试留下的 upstream evidence 可以保留，但不得覆盖最终归因。
- Risk Focus:
  - 逻辑错误：必须消费结构化 stop reason，且仅在没有 text/thinking/tool output 时转为内容过滤错误。
  - 行为回归：普通未知空 EventStream 继续走 502 failover；已经产生拒答输出时继续返回成功响应。
  - 安全问题：prod 只信任配置为 Kiro mirror stub 的固定 outcome header，不解析可变错误文案，也不在 header 中传递 prompt、响应正文或凭证。
  - 运行时：内容过滤不得调用账号 cooldown、token refresh 或跨账号 retry；前序 failover context 不得把最终结果重新归为 provider/platform。

## Acceptance Criteria

1. **AC-001（根因复现）**：Given 真实帧序列包含 `metadataEvent.stopReason=CONTENT_FILTERED`、`contextUsageEvent`、`meteringEvent` 且无 assistant 输出，When parser 与 Kiro gateway 消费该流，Then 返回 typed content-filter error，不包装为 `UpstreamFailoverError`。
2. **AC-002（多入口契约）**：Given 同一 typed outcome，When 从 Messages、Chat Completions、Responses 入口返回，Then 分别得到 400 `invalid_request_error`、400 `content_filter_error`、400 `content_filter`。
3. **AC-003（无账号副作用）**：Given native Kiro 或可信 Kiro mirror 返回内容过滤，When gateway 处理结果，Then 不重试其他账号、不调用 rate-limit/account-health 惩罚逻辑，且 outcome 自身不创建 upstream error context。
4. **AC-004（最终客户端归因）**：Given 当前请求已有一次 502 failover evidence，随后 Kiro 返回内容过滤，When ops logger 分类最终 400，Then 仍为 `request/client/client_request`，严重度为 P3；此前 evidence 可保留用于诊断。
5. **AC-005（普通空响应回归）**：Given EventStream 没有输出且没有 `CONTENT_FILTERED`，When Kiro gateway 完成读取，Then 保持现有 502 failover 行为并记录 `empty_response`。
6. **AC-006（已有输出回归）**：Given Kiro 先产生 assistant text 再携带相同 stop reason，When gateway 完成读取，Then 保留输出并返回成功，不标记客户端内容过滤错误。

## Assertions

- parser 的 `OnStopReason` 收到精确值 `CONTENT_FILTERED`。
- typed error 与 `UpstreamFailoverError` 互斥，三入口 wire status 与 error type/code 符合 AC-002。
- native/mirror 内容过滤路径设置结构化 ops marker，且 clean path 不产生 `OpsUpstreamErrorsKey` 或 upstream status/message。
- 预置 502 upstream context 后，最终 content-filter marker 覆盖 SLA owner/source 分类但不删除既有 evidence。
- ordinary empty response 仍记录 `response_error/empty_response`；有 assistant text 时不设置 content-filter marker。

## Linked Tests

- `backend/internal/integration/kiro/eventstream_test.go`::`TestParseEventStream_MetadataStopReason`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_NonStreaming_ContentFilteredIsNotFailover`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_Streaming_ContentFilteredIsNotFailover`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_ContentFilteredWithAssistantTextRemainsSuccess`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_EmptyResponseTriggersFailover`
- `backend/internal/service/gateway_forward_kiro_content_filter_test.go`::`TestForwardAsChatCompletions_KiroMirrorContentFilteredReturns400WithoutFailover`
- `backend/internal/service/gateway_forward_kiro_content_filter_test.go`::`TestForwardAsResponses_KiroMirrorContentFilteredReturns400WithoutFailover`
- `backend/internal/handler/gateway_handler_kiro_content_filter_test.go`::`TestGatewayHandler_HandleKiroContentFilteredError`
- `backend/internal/handler/ops_error_logger_tk_kiro_content_filter_test.go`::`TestClassifyOpsKiroContentFilterAfterPriorFailoverStillOwnedByClient`

运行命令：

```bash
cd backend
go test -tags=unit ./internal/integration/kiro ./internal/service ./internal/handler -count=1
```

## Evidence

- 聚焦测试与项目 preflight 由 PR review 闭环执行并记录；线上原始帧根因与契约边界见 `docs/approved/kiro-content-filter-outcome.md`。

## Status

- [x] InTest — 实现与自动化回归已完成，等待 PR CI 与人工合并审批。
