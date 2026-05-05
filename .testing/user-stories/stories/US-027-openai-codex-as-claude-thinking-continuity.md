# US-027-openai-codex-as-claude-thinking-continuity

- ID: US-027
- Title: OpenAI Codex 伪装 Claude — Thinking 多轮空响应根治 + 流式空内容护栏
- Version: V1.4.x (hot-fix)
- Priority: P0 (用户路径硬伤：Claude Code CLI 多轮 thinking + tool 调用一直被"截断"且每次"继续"也只回一两句)
- As a / I want / So that:
  作为 **TokenKey 的 Claude Code CLI 用户**，我希望 **当我用 `ANTHROPIC_BASE_URL=https://api.tokenkey.dev` + `model=opus` 走多轮工具调用（含 thinking）时，gateway 不再返回空响应让 CC 显示「答完了」**，**以便** 我不必反复手动「继续」、且哪怕上游真的吐了 0 token 也不会让 CC 客户端把会话 JSONL 写坏（`anthropics/claude-code#24662`）。

- Trace:
  - 防御需求轴线：`AnthropicToResponses` 同时设了 `Include=["reasoning.encrypted_content"]` + `Store=false`，这是 Responses API 的「无状态思考续接」契约 —— 要求**下一轮把上一轮 reasoning item 原样回放**；但我们的下行路径 (`anthropicAssistantToResponses`) 直接 ignore Anthropic 的 `thinking` 块，导致上游 Codex 见到「有 `function_call_output` 但缺对应 `reasoning` item」的输入按规范静默返回 0 token。这是「对外契约一致性」的反退化边界。
  - 实体生命周期轴线：Anthropic Messages 流式生命周期 = `message_start → (content_block_start → content_block_delta* → content_block_stop)+ → message_delta → message_stop`。本故事补齐 **content_block 数 = 0** 这条历史上未被覆盖的迁移边——即「上游真的没吐内容」时仍必须发出 ≥1 个 content_block 否则 CC 客户端崩。
  - 系统事件轴线：每次 `POST /v1/messages` 走 `platform=openai + channel_type=0`（Codex 直连）路径；尤其是 CC 多轮工具调用的第 2..N 轮。

- Risk Focus:
  - 逻辑错误：
    - Part B 必须**完全**摘掉 `Include=["reasoning.encrypted_content"]`，不能只在某些分支摘（否则只要还有一处带 include + Store=false 就继续触发空响应）；
    - Part A 的 `EmittedAnyContentBlock` 计数必须覆盖 **三类** content block（text / thinking / tool_use），任何一处忘记 `state.EmittedAnyContentBlock = true` 都会在正常多 block 流里多发一个空 block，破坏客户端 index 序列。
  - 行为回归：
    - `AnthropicToResponses` 的对外签名 0 变更（仍返回 `*ResponsesRequest`）；
    - `ResponsesEventToAnthropicEvents` / `FinalizeResponsesAnthropicStream` / `ResponsesEventToAnthropicState` 的对外签名 0 变更（仅新增内部字段 `EmittedAnyContentBlock` + 内部 helper `ensureContentBlockEmittedAsEmptyText`）；
    - 正常文本流（有 deltas）和正常工具流（有 function_call）的 SSE 事件序列字节级一致。
  - 安全问题：
    - 不引入新的会话级状态存储，不持久化用户 prompt / reasoning，所以无新增 PII / 凭证泄露面；
    - 不绕过任何鉴权 / 计费路径——`InputTokens`/`OutputTokens` 仍按上游 `response.completed.usage` 透传。
  - 运行时问题：
    - 流式路径的护栏不能引入额外内存累积（Part A 早期设计用 `BufferedResponseAccumulator` 兜底，但因为 ordering 问题已经撤销，最终方案只用一个 bool flag + 1 个空 content block，常数空间）；
    - 上游真的吐 0 token 时，护栏发出的空 content block 必须**保留** `Usage.InputTokens` 透传以免计费失账。

## Acceptance Criteria

1. **AC-001 (正向 / 根因摘除)**：Given `AnthropicMessagesRequest{Thinking:{...}, Stream:true}`，When 调用 `AnthropicToResponses(req)`，Then 返回的 `ResponsesRequest.Include` 不包含 `"reasoning.encrypted_content"`，且 `*Store == false`（`store=false` 自身不是 bug，bug 是 store=false **同时** Include reasoning encrypted content）。
2. **AC-002 (正向 / 流式空响应护栏)**：Given 上游 SSE 仅含 `response.created` + `response.completed{output:[]}` + `[DONE]`（即真实 prod bug 复现），When 走 `handleAnthropicStreamingResponse`，Then 客户端收到的 SSE 流必然包含**至少一对** `content_block_start{type:text,text:""}` + `content_block_stop{index:0}`，且这对 block 严格出现在 `message_delta` 之前（保护 Anthropic 流协议关闭顺序）。
3. **AC-003 (正向 / 计费透传保持)**：Given AC-002 同样的输入，When 检查 `result.Usage.InputTokens`，Then 等于上游 `response.completed.usage.input_tokens`（=42 in test），不能因为护栏注入空 block 就把 usage 清零或漏算。
4. **AC-004 (回归 / 正常文本流不双发)**：Given 上游 SSE 含 `response.output_text.delta` 实文本，When 走 `handleAnthropicStreamingResponse`，Then 整条 SSE 中 `event: content_block_start` 出现次数 == 1（不是 2），证明护栏在已有 content block 时不重复注入；且文本 `Hello` / ` world` 出现在 `content_block_delta.delta.text` 而非合成 block 的 `content_block_start.text` 字段里。
5. **AC-005 (回归 / 正常工具流标记 emit)**：Given 上游 SSE 含 `response.output_item.added{type:function_call}` + `function_call_arguments.delta` + `output_item.done`，When 转换出 Anthropic 事件，Then `state.EmittedAnyContentBlock == true`（保护工具调用分支也被纳入 emit 计数）；且转换出的事件序列含 `content_block_start{type:tool_use}` + `content_block_delta{type:input_json_delta}` + `content_block_stop`。
6. **AC-006 (回归 / response.failed 也走护栏)**：Given 上游在没有任何 output 的情况下发 `response.failed{error:rate_limit_error}`，When 转换出 Anthropic 事件，Then 仍按 AC-002 的护栏发 `content_block_start{type:text,text:""}` + `content_block_stop` + `message_delta{stop_reason:end_turn}` + `message_stop`，**共 4 个事件**（修复前是 2 个），保证失败路径也不会让 CC 客户端持久化无 content block 的会话条目。
7. **AC-007 (回归 / 接口签名)**：Given 此 PR 落地，When 阅读 `apicompat.AnthropicToResponses` / `ResponsesEventToAnthropicEvents` / `FinalizeResponsesAnthropicStream` 的导出函数签名，Then 0 变更（新增的 `EmittedAnyContentBlock` 是 `ResponsesEventToAnthropicState` 上的导出字段但不是函数参数；`ensureContentBlockEmittedAsEmptyText` 是包内私有 helper）。
8. **AC-008 (回归 / 全量单元测试)**：Given 此 PR 落地，When 执行 `go test -tags=unit -count=1 ./internal/pkg/apicompat/... ./internal/service/...`，Then 全部包通过，无新增 FAIL。

## Assertions

- `AnthropicToResponses(reqWithThinking).Include` 不含 `"reasoning.encrypted_content"`（断言根因摘除，覆盖 AC-001）。
- `AnthropicToResponses(reqWithThinking).Store != nil && *Store == false`（断言 `Store=false` 仍保留——它本身是 Codex 直连账号的正确选择，bug 只在它和 Include 同时存在时触发）。
- 在 `TestStreamingEmptyResponse` 里 `events` 的事件类型序列严格为 `[content_block_start, content_block_stop, message_delta, message_stop]`（不是 `[message_delta, message_stop]`）；`events[0].ContentBlock.Type == "text"` 且 `events[0].ContentBlock.Text == ""`。
- 在 `TestStreamingFailedNoOutput` 里同样的 4 事件序列适用——证明 failed 分支也走护栏。
- 在 `TestUS027_StreamingNormalText_DoesNotDoubleEmit` 里 `strings.Count(body, "event: content_block_start") == 1`（断言无双发）。
- 在 `TestUS027_StreamingTextFlow_SetsEmittedFlag` / `TestUS027_StreamingFunctionCallFlow_SetsEmittedFlag` 里 `state.EmittedAnyContentBlock == true`（断言三类 block 都被覆盖到 flag 写入）。
- `TestUS027_StreamingEmptyResponse_SynthesizesEmptyTextBlock.result.Usage.InputTokens == 42`（断言计费透传，覆盖 AC-003）。
- `strings.Index(body, "event: content_block_start") < strings.Index(body, "event: message_delta")`（断言 ordering 不破坏 Anthropic 关闭顺序，覆盖 AC-002 的尾部约束）。

## Linked Tests

- `backend/internal/pkg/apicompat/anthropic_responses_test.go`::`TestUS027_AnthropicToResponses_NoEncryptedContentInclude` (AC-001)
- `backend/internal/pkg/apicompat/anthropic_responses_test.go`::`TestStreamingEmptyResponse` (AC-002 + AC-007 — 直接断言 4 事件序列 + 空 text block)
- `backend/internal/pkg/apicompat/anthropic_responses_test.go`::`TestStreamingFailedNoOutput` (AC-006)
- `backend/internal/pkg/apicompat/anthropic_responses_test.go`::`TestUS027_StreamingTextFlow_SetsEmittedFlag` (AC-005 文本分支)
- `backend/internal/pkg/apicompat/anthropic_responses_test.go`::`TestUS027_StreamingFunctionCallFlow_SetsEmittedFlag` (AC-005 工具分支)
- `backend/internal/service/us027_streaming_empty_safety_net_test.go`::`TestUS027_StreamingEmptyResponse_SynthesizesEmptyTextBlock` (AC-002 端到端 + AC-003)
- `backend/internal/service/us027_streaming_empty_safety_net_test.go`::`TestUS027_StreamingNormalText_DoesNotDoubleEmit` (AC-004)

运行命令：

```bash
# 仅本故事的回归 (秒级)
cd backend && go test -tags=unit -count=1 -v \
  -run 'TestUS027_|TestStreamingEmptyResponse|TestStreamingFailedNoOutput|TestAnthropicToResponses_Thinking' \
  ./internal/pkg/apicompat/... ./internal/service/...

# 涉及包全量回归 (~80s)
cd backend && go test -tags=unit -count=1 ./internal/pkg/apicompat/... ./internal/service/...
```

## Evidence

- 设计文档：`docs/approved/openai-codex-as-claude-thinking-continuity.md`（status=approved，2026-04-22）。
- Prod 实证（2026-04-22，1 小时窗口）：11 次相关请求中 7 次为「续接 + thinking 历史」形态，全部 `input_tokens=0 / output_tokens=0`、`stop_reason=end_turn`、`duration < 1.5s` —— 即上游空响应；首轮（无历史）正常返回 thinking + 7×tool_use。
- 本地 (worktree `/sub2api-wt-codex-thinking-continuity`) 自检：
  - `go build ./...` clean。
  - `go test -tags=unit -count=1 ./internal/pkg/apicompat/... ./internal/service/...` → `ok ... 0.526s` + `ok ... 80.163s`，全部 PASS。
  - `TestStreamingEmptyResponse` / `TestStreamingFailedNoOutput` 旧断言（2 事件序列）已同步更新为 4 事件序列并标注 US-027 schema firewall 注释，避免未来误把 4→2 当成 bug 修回去。
- Claude Code 客户端联动：CC #24662 issue 描述与本故事的 prod 现象同构（empty content block → session JSONL corruption → 必须 `--resume` 重开）；本护栏即使在 Part B 漏网时也能避免客户端崩溃。
- Prod 部署证据以后续 release/deploy smoke 输出为准，不在 story 中回填截图或运行日志。

## Status

- [ ] InTest — 实现已完成，preflight + PR 合并待跟进；shipped 后翻 Done 并补 Evidence。
