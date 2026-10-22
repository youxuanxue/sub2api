# US-065-kiro-payload-overflow

- ID: US-065
- Title: Kiro oversized requests preserve the current turn and fail clearly
- Priority: P0
- As a / I want / So that:
  As a TokenKey Kiro caller, I want oversized translated requests to preserve
  my current turn and return a protocol-compatible client error, so that the
  gateway never silently changes my request or misreports it as provider failure.
- Trace: `docs/approved/kiro-payload-overflow.md`
- Risk Focus:
  - 逻辑错误: history screenshots and turns go first; tool-result text and current screenshots are reduced only while the payload is still over 900 KiB. Current user prose and tool schemas stay intact.
  - 行为回归: Messages and Chat Completions return 413 `request_too_large`, not a generic 502.
  - 安全问题: the client error reports byte counts but never echoes request content.
  - 运行时: local rejection makes zero upstream calls and triggers no failover or account-health penalty.

## Acceptance Criteria

1. **AC-001 (positive)**: Given an oversized request with removable history, when translation runs, then oldest history is removed and the final payload is at most 900 KiB while the latest UTF-8 message remains unchanged.
2. **AC-002 (negative)**: Given fixed current-turn data that remains over 900 KiB, when the Kiro gateway validates the translated payload, then it returns a typed 413 before issuing an upstream request.
3. **AC-003 (multi-entrypoint)**: Given the typed overflow rejection, when it exits through Messages or Chat Completions, then each response uses HTTP 413 and `request_too_large` in its native JSON envelope.
4. **AC-004 (regression)**: Given the overflow rejection, when shared failover handling inspects it, then it is not an `UpstreamFailoverError` and no alternate account or account penalty is selected.
5. **AC-005 (positive)**: Given a current tool result that alone exceeds 900 KiB, when translation runs, then the result keeps its head, tail, and tool id when paired, history that fits beside the clipped result is kept, and the current user prose remains.
6. **AC-006 (negative)**: Given a tool result already under 900 KiB, when translation runs, then that result is unchanged. Given a tool schema that alone exceeds 900 KiB, the gateway still returns 413.

## Assertions

- Final serialized size is checked after history truncation.
- Current message content remains byte-for-byte valid UTF-8.
- The error contains size metadata but no request body content.
- Kiro upstream recorder remains untouched on local overflow.
- Messages and Chat Completions agree on status and error type.

## Linked Tests

- `backend/internal/integration/kiro/tool_history_test.go`::`TestClaudeToKiro_HistoryTruncationPreservesCurrentUserContent`
- `backend/internal/integration/kiro/tool_history_test.go`::`TestClaudeToKiro_OversizedFixedPayloadPreservesCurrentUserContent`
- `backend/internal/integration/kiro/tool_history_test.go`::`TestTruncatePayload_ClipsOversizedCurrentToolResult`
- `backend/internal/integration/kiro/tool_history_test.go`::`TestTruncatePayload_LeavesFittingToolOutputUntouched`
- `backend/internal/integration/kiro/tool_history_test.go`::`TestClaudeToKiro_ClipsOrphanToolResultAlreadyNarrated`
- `backend/internal/integration/kiro/tool_history_test.go`::`TestTruncatePayload_StripsOldestHistoryImagesBeforeDroppingTurns`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_OversizedPayloadRejectedBeforeUpstream`
- `backend/internal/service/gateway_forward_as_chat_completions_test.go`::`TestUS065_ForwardAsChatCompletions_KiroOversizedPayloadReturns413WithoutUpstream`
- `backend/internal/handler/gateway_handler_tk_kiro_non_failover_test.go`::`TestTkHandleMessagesNonFailoverForwardError_Matrix`

Run:

```bash
cd backend && go test -tags=unit ./internal/integration/kiro ./internal/service ./internal/handler -run 'Payload|Oversized|US065|MessagesNonFailover' -count=1
```

## Evidence

The approved baseline records the direct same-account size probes. Automated
tests verify the local contract without sending paid or production traffic.

## Status

- [x] InTest - implementation and focused regression coverage are present; PR CI and human merge approval remain.
