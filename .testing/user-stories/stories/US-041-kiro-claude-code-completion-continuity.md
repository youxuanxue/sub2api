# US-041-kiro-claude-code-completion-continuity

- ID: US-041
- Title: Kiro Claude Code completion continuity preserves unfinished work
- Priority: P0 (核心 Agent 完成态语义)
- As a / I want / So that:
  作为 **通过 TokenKey Kiro 节点使用 Claude Code 的开发者**，我希望 **Claude Code 收到真实的终止原因，并在实现或验证未完成时继续使用工具**，**以便** 终端不会把截断或未知结果显示成正常完成，也不需要我反复输入“继续”。
- Trace:
  - 设计锚点：`docs/approved/kiro-claude-code-completion-continuity.md`
  - Prompt 轴线：Claude Code system prompt 经共享 completion guard owner 幂等注入到 Kiro system priming。
  - 协议轴线：Kiro `metadataEvent.stopReason` 显式映射到 Anthropic Messages JSON/SSE stop reason。
  - 可观测轴线：成功映射记录 `gateway.kiro_stop_reason`；未知值 fail closed，不再伪造 `end_turn`。
- Risk Focus:
  - 逻辑错误：`MAX_TOKENS`、tool use 或未知终止态被错误合成为 `end_turn`。
  - 行为回归：普通非 Claude Code prompt、Kiro 内容过滤契约和 OpenAI-compatible Messages 既有 guard 检测必须保持不变。
  - 安全问题：日志仅记录截断后的 stop reason、模型和账号 ID，不记录 prompt、响应正文或凭证。
  - 运行时：流式响应已经产生内容后遇到未知终止态时必须发 error，不能再发成功的 `message_delta/message_stop`。

## Acceptance Criteria

1. **AC-001（完成连续性）**：Given Claude Code system prompt，When 构建 Kiro payload，Then completion guard 位于 system priming 且只出现一次；已有 marker 时仍不重复。
2. **AC-002（范围守卫）**：Given 普通 API system prompt，When 构建 Kiro system prompt，Then 不注入 Claude Code completion guard。
3. **AC-003（截断语义）**：Given Kiro 返回 `MAX_TOKENS`，When 流式或非流式 gateway 完成转换，Then Anthropic stop reason 为 `max_tokens`，不得出现 `end_turn`。
4. **AC-004（未知值 fail closed）**：Given Kiro 返回未知或空 stop reason，When gateway 处理，Then 非流式进入 502 failover；流式发送 `unsupported_stop_reason` error 且不发送成功终止事件。
5. **AC-005（既有契约回归）**：Given `END_TURN`、tool use 或有输出的 `CONTENT_FILTERED`，When gateway 转换，Then分别保持 `end_turn`、`tool_use` 与既有可见拒答成功语义。

## Assertions

- 共享 guard 的 marker 保持 `<sub2api-claude-code-todo-guard>`，重复调用 owner 结果字节不变。
- 完整 Kiro payload 的 history 与 current message 合计只有一个 guard marker。
- `mapKiroStopReason` 对已知值显式映射，对空值和未知值返回 typed error。
- JSON 与 SSE wire 分别保留 `max_tokens`；未知流式终止态不包含 `message_stop`。
- sentinel 锚定共享 owner、translator 注入点、stop-reason mapper、结构化日志与核心回归测试。

## Linked Tests

- `backend/internal/pkg/claude/claude_code_completion_guard_test.go`::`TestEnsureClaudeCodeCompletionGuard_AppendsExactlyOnce`
- `backend/internal/integration/kiro/prompt_filter_test.go`::`TestUS041_ClaudeToKiro_CompletionGuardAppearsOnceInSystemPriming`
- `backend/internal/integration/kiro/prompt_filter_test.go`::`TestBuildClaudeSystemPrompt_NonClaudeCodeDoesNotAddCompletionGuard`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestMapKiroStopReason_PreservesTerminalOutcome`
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
