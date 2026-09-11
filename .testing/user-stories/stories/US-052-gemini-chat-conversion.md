# US-052 Gemini 请求通过合法 Chat Target 执行

- ID: US-052
- Title: Gemini generateContent 使用通用 Chat 转换边
- Priority: P1
- As a / I want / So that: As a Gemini API client, I want compatible authorized Chat accounts to serve my request, so that protocol shape does not exclude usable supply.
- Trace: docs/approved/gemini-chat-conversion.md; user approval 2026-09-11
- Risk Focus:
  - 逻辑错误：原始 Gemini 字段被静默丢失、映射与计费模型混淆。
  - 行为回归：原生 Gemini 签名和多模态被转换门禁影响。
  - 安全问题：未知字段、越过授权及端点权限、供应源错误泄漏。
  - 运行时：流式缓冲、断流伪造成功、开始输出后的重放及部分用量丢失。

## Acceptance Criteria

1. AC-001 (positive): Given supported Gemini text/system/generation parameters When planning and executing Then Chat receives the mapped model and text, while the client receives Gemini candidates and usage.
2. AC-002 (negative): Given unsupported Gemini media/tools/thinking/signature/cache or unknown fields When planning a Chat target Then no legal conversion exists; native identity retains its existing behavior.
3. AC-003 (regression): Given authorized native and Chat candidates When Direct or Universal selects Then shared priority and exclusion rules apply; native identity does not outrank a better Chat account solely by protocol.
4. AC-004 (runtime): Given a Chat SSE response When text arrives Then Gemini text is emitted before completion; heartbeats/usage alone do not commit output, and truncated responses never synthesize STOP.
5. AC-005 (negative/runtime): Given an upstream pre-output retryable error or a partial response When forwarding Then retry remains possible only before output; known partial usage survives with an observable error and sanitized Google error body.

## Assertions

AC-001: exact upstream HTTP path, bearer, mapped model, converted request and client model; Gemini JSON/SSE fields; prompt/cache/output usage.
AC-002: real Gemini body negatives through Plan, native identity positive, no new supplier or model allowlist.
AC-003: Direct/Universal selection with Chat and native peers, priority and failed-account exclusion.
AC-004: recorder receives content and flush before finish, no false terminal on EOF.
AC-005: retry classification before output; result retains usage after truncation; no provider body leakage or false STOP.

## Linked Tests

- `backend/internal/pkg/apicompat/gemini_chat_tk_test.go`::`TestGeminiToChatRequest`
- `backend/internal/pkg/apicompat/gemini_chat_tk_test.go`::`TestGeminiToChatRejectsUnsupportedSemantics`
- `backend/internal/pkg/apicompat/gemini_chat_tk_test.go`::`TestChatToGeminiResponse`
- `backend/internal/engine/protocolrouter/gemini_chat_conversion_tk_test.go`::`TestGeminiToChatPlanChecksActualBody`
- `backend/internal/service/gemini_chat_forward_tk_test.go`::`TestGeminiChatCandidateUsesSharedScopeAndPriority`
- `backend/internal/service/gemini_chat_forward_tk_test.go`::`TestGeminiChatWriterStreamsBeforeCompletion`
- `backend/internal/service/gemini_chat_forward_tk_test.go`::`TestGeminiChatForwardFailureAndPartialUsage`
- `backend/internal/handler/gemini_chat_conversion_tk_test.go`::`TestUS052_GeminiChatSelectedHTTPTransport`
- `backend/internal/handler/gemini_v1beta_handler_tk_execute_test.go`::`TestGeminiSelectedProtocolRepairsTransportSignature`

Run command: `cd backend && go test -tags=unit ./internal/pkg/apicompat ./internal/engine/protocolrouter ./internal/service ./internal/handler -run 'Test(GeminiToChat|ChatToGemini|GeminiChat|US052|GeminiSelectedProtocol)' -count=1`

## Evidence

Local focused tests exercise OpenAI raw and NewAPI adaptor forwarding against real HTTP fixtures.
These are unit/HTTP integration checks, not UI e2e or live supplier evidence.
No production mapping, capability, catalog or pricing activation is included.

## Status

- InTest
