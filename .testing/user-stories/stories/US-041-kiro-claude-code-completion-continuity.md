# US-041-kiro-claude-code-completion-continuity

- ID: US-041
- Title: Kiro preserves model turn boundaries and tool execution evidence
- Priority: P0
- As a / I want / So that:
  作为 Kiro Claude Code 用户，我希望网关忠实传输当前轮次和真实工具结果，
  以免状态问答被网关改成新的执行任务。
- Trace: `docs/approved/kiro-turn-boundary.md` 替代旧私有完成协议。
- Risk Focus:
  - 逻辑错误：正常轮次结束被误判为任务未完成并强制续跑。
  - 行为回归：工具结果、异常流、计费和 thinking side channel 必须保真。

## Acceptance Criteria

1. **AC-001**：识别为 Claude Code 也不注入完成 guard 或私有工具；保留客户端原有指令与工具。
2. **AC-002**：NO + END_TURN 在 JSON/SSE 均只调用一次上游，不追加 Read/Edit 或新 user 指令。
3. **AC-003**：普通工具立即返回客户端；同名客户端工具不得被隐藏。后续客户端请求保留结构化调用、失败结果与历史。
4. **AC-004**：MAX_TOKENS、上下文耗尽、refusal 保留真实终止态；未知/malformed/截断流不伪造成功结束。
5. **AC-005**：允许未提交输出时的传输重试；已输出内容后的断连不能重放。
6. **AC-006**：用量只对应本次模型轮次；缓存分账与 thinking side channel 保持有效。
7. **AC-007**：仅说“准备执行”的合法 END_TURN 仍返回客户端，不作为任务成功证据，也不触发网关强制业务续跑。

## Assertions

- JSON/SSE、Claude Code/普通请求均只发起一次正常模型轮次。
- 客户端原始 no-tools 指令保留；第二次受控上游 Read 响应不得被读取或返回。
- 合法进度文本的 END_TURN 保留，不能伪造工具或任务完成。
- 工具失败与结构化历史在客户端主动发起的后续轮次中保持原样。

## Linked Tests

- 运行命令: `cd backend && go test -tags=unit ./internal/integration/kiro ./internal/service -run 'Kiro|ClaudeToKiro' -count=1`

- `backend/internal/integration/kiro/prompt_filter_test.go`::`TestClaudeToKiro_NoPrivateCompletionProtocol`
- `backend/internal/integration/kiro/tool_result_status_test.go`::`TestClaudeToKiro_PreservesToolResultFailure`
- `backend/internal/integration/kiro/tool_history_test.go`::`TestClaudeToKiro_CLIToolHistory`
- `backend/internal/service/kiro_turn_boundary_test.go`::`TestKiroGatewayService_EndTurnDoesNotContinue`
- `backend/internal/service/kiro_turn_boundary_test.go`::`TestKiroGatewayService_ClaudeCodeTerminalOutcomes`
- `backend/internal/service/kiro_gateway_tool_history_test.go`::`TestKiroGatewayService_ClientTurnsPreserveToolHistory`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestUS041_KiroGatewayService_ClaudeCodeOrdinaryToolUseReturnsImmediately`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestMapKiroStopReason_PreservesTerminalOutcome`
- `backend/internal/service/kiro_gateway_service_test.go`::`TestKiroGatewayService_Forward_Streaming_UnknownStopReasonFailsClosed`

## Evidence

复现测试在旧行为中仅 Claude Code 两组失败（第二次调用），修复后四组均只调用一次。
其余测试与真实上游工具循环结果见本 PR 验证记录；不把 API-only 测试称为 UI e2e。
旧“提前总结”不能用伪造 user 续跑来保证，需发版后持续验证真实客户端行为。

## Status

- [x] InTest — 本地实现与回归；生产仍使用已发布版本。
