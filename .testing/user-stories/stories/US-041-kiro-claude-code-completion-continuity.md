# US-041-kiro-claude-code-completion-continuity

- ID: US-041
- Title: Kiro Claude Code completion continuity preserves unfinished work
- Priority: P0 (核心 Agent 完成态语义)
- As a / I want / So that:
  作为 **通过 TokenKey Kiro 节点使用 Claude Code 的开发者**，我希望 **Claude Code 收到真实的终止原因，并在实现或验证未完成时继续使用工具**，**以便** 终端不会把截断或未知结果显示成正常完成，也不需要我反复输入“继续”。
- Trace:
  - 设计锚点：`docs/approved/kiro-claude-code-completion-continuity.md`
  - Prompt 轴线：Claude Code system prompt 经共享 completion guard owner 幂等注入到 Kiro system priming，并启用 transport-private completion tool。
  - 协议轴线：Kiro `metadataEvent.stopReason` 显式映射到 Anthropic Messages JSON/SSE stop reason；模型任务完成由私有工具显式确认。
  - 续跑轴线：缺少有效完成信号的 `END_TURN` 在同一客户端请求内有界续跑，普通工具调用仍交还 Claude Code。
  - 可观测轴线：成功映射记录 `gateway.kiro_stop_reason`，完成协议记录 `gateway.kiro_completion_protocol`；未知值 fail closed。
- Risk Focus:
  - 逻辑错误：模型级 `END_TURN` 被误认为 Agent 任务完成，或 `MAX_TOKENS`、tool use、policy/malformed 终止态被错误合成为 `end_turn`。
  - 行为回归：普通非 Claude Code prompt、同名客户端工具、普通 Claude Code 工具、Kiro 内容过滤契约和 OpenAI-compatible Messages 既有 guard 检测必须保持不变。
  - 安全问题：日志仅记录截断后的 stop reason、模型和账号 ID，不记录 prompt、响应正文或凭证。
  - 运行时：流式响应已经产生内容后遇到未知终止态时必须发 error，不能再发成功的 `message_delta/message_stop`。

## Acceptance Criteria

1. **AC-001（显式完成协议）**：Given Claude Code system prompt，When 构建 Kiro payload，Then completion guard 与 transport-private completion tool 各只出现一次；有效 `complete/blocked` 信号被消费并转换为 `end_turn`；首轮 completion message 可补齐最终答复，隐藏续跑的 `complete` 在已有可见文本时只作为内部确认，不追加普通文本或私有 message。
2. **AC-002（未完成续跑）**：Given Kiro 返回文本与 `END_TURN` 但无有效完成信号，When 流式或非流式 gateway 处理，Then 在同一客户端请求内继续 Kiro turn，直到收到完成信号或达到确定性上限；第二轮及之后 missing-signal 的 recap、语义改写及 tool output 摘要不得进入客户端输出。
3. **AC-003（范围与工具守卫）**：Given 普通 API 请求声明同名工具，When Kiro 调用该工具，Then 工具不得被吞；Given Claude Code 调用普通工具或同时产生普通工具与完成信号，Then 普通工具立即以 `tool_use` 返回并优先于完成信号。
4. **AC-004（截断语义）**：Given Kiro 返回 `MAX_TOKENS` 或 `MODEL_CONTEXT_WINDOW_EXCEEDED`，When gateway 转换，Then保留对应 Anthropic stop reason，即使同一响应含私有完成信号也不得覆盖。
5. **AC-005（policy 与 malformed 语义）**：Given Kiro 返回 policy refusal，When有可见文本时映射 `refusal`、无输出时返回客户端内容过滤错误；Given malformed/未知/空 stop reason，Then fail closed，不伪造成功结束。
6. **AC-006（有界与计费）**：Given 模型持续不发完成信号，When达到内部续跑上限，Then停止追加 Kiro 请求；隐藏续跑的输入与隐藏工具输出计入估算用量。

## Assertions

- 共享 guard 的 marker 保持 `<sub2api-claude-code-todo-guard>`，重复调用 owner 结果字节不变。
- 完整 Kiro payload 的 history 与 current message 合计只有一个 guard marker。
- `mapKiroStopReason` 对已知值显式映射，对空值和未知值返回 typed error。
- JSON 与 SSE wire 分别保留 `max_tokens`；未知流式终止态不包含 `message_stop`。
- 私有工具只在 `ClaudeCodeCompletionProtocol` 为真时被消费；空消息、普通工具或非完成 stop reason 不能提前结束。
- 流式与非流式输出在同轮不得重复已经可见的完整 completion message；隐藏续跑的 missing-signal/complete 文本是 transport-only，不得把 recap、语义改写或 tool output 摘要作为第二份答复。隐藏普通工具的 text-before-tool 与真实 `blocked` 问题仍须可见。
- sentinel 锚定共享 owner、私有协议 owner、有界续跑、范围门禁、stop metadata、结构化日志与核心回归测试。

## Linked Tests

- `backend/internal/pkg/claude/claude_code_completion_guard_test.go`::`TestEnsureClaudeCodeCompletionGuard_AppendsExactlyOnce`
- `backend/internal/integration/kiro/prompt_filter_test.go`::`TestUS041_ClaudeToKiro_CompletionGuardAppearsOnceInSystemPriming`
- `backend/internal/integration/kiro/prompt_filter_test.go`::`TestBuildClaudeSystemPrompt_NonClaudeCodeDoesNotAddCompletionGuard`
- `backend/internal/integration/kiro/claude_code_completion_test.go`::`TestConsumeClaudeCodeCompletionSignal_StripsOnlyPrivateTool`
- `backend/internal/integration/kiro/claude_code_completion_test.go`::`TestPrepareClaudeCodeCompletionContinuation_PreservesToolsAndMovesTurnToHistory`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestMapKiroStopReason_PreservesTerminalOutcome`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_ClaudeCodeEndTurnContinuesUntilExplicitCompletion`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_EmptyCompletionSignalDoesNotFinish`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_EmptyCompletionMessageWithAssistantTextDoesNotFinish`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_CompletionSignalMessageIsPreservedAfterText`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_CompletionSignalDoesNotRepeatVisibleFinalText`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_ContinuationCompletionDoesNotRepeatPriorFinalText`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_HiddenCompletionSuppressesSemanticRecapAndToolOutputSummary`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_HiddenBlockedMessageRemainsVisible`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_HiddenMaxTokensTextRemainsVisible`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_NonClaudeCodeCompletionNamedToolIsPreserved`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_ClaudeCodeOrdinaryToolUseReturnsImmediately`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_ClaudeCodeOrdinaryToolWinsOverCompletionSignal`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_MaxTokensOverridesCompletionSignal`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_ClaudeCodeCompletionLoopIsBounded`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_NonStreaming_PreservesMaxTokensStopReason`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_Streaming_PreservesMaxTokensStopReason`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_Streaming_UnknownStopReasonFailsClosed`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_NonStreaming_UnknownStopReasonFailsOver`

运行命令：

```bash
cd backend
go test -tags=unit -count=1 ./internal/pkg/claude ./internal/integration/kiro ./internal/service
```

## Evidence

- 聚焦回归、相关 package 单元测试、sentinel checker 与项目 preflight 由本 PR 执行并记录。
- 本变更无 Web surface；提交消息使用 `no-web-impact` 作为机械锚点。

## Status

- [x] InTest — 实现与自动化回归已完成，等待 PR CI 与人工合并审批。
