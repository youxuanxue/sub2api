---
title: Kiro Claude Code Completion Continuity
status: approved
approved_by: "feng (对话审批 2026-07-28)"
approved_at: 2026-07-28
authors: [agent]
created: 2026-07-28
related_prs: []
related_commits: []
---

# Kiro Claude Code Completion Continuity

## Problem

Claude Code 经 TokenKey 的 Kiro 节点调用 Claude Opus 时，模型可能在实现或验证尚未完成时返回文本。网关此前无条件把 Kiro 的终止结果合成为 Anthropic `end_turn`；即使真实原因是 token 上限或未知终止态，Claude Code 终端也会把这一轮显示成正常结束，用户只能反复输入“继续”。

#1488 修复了第一层问题：Kiro 模型终止原因不再被统一伪造成
`end_turn`。但 Kiro 的 `END_TURN` 只表示模型自然结束当前生成，不能证明
Claude Code 的多步骤 Agent 任务已经完成。Claude Code 对合法 `end_turn` 会直接
交还控制权，因此单纯保真 stop reason 仍会复现“实现未完成却需要用户输入继续”。

## Stop Reason Contract

当前 Kiro 客户端把 `metadataEvent.stopReason` 定义为开放字符串，并原样保留到
Agent turn metadata；其 AWS runtime 标准词汇及本桥接处理如下：

| Kiro value | 官方语义 | Anthropic / gateway 处理 |
| --- | --- | --- |
| `END_TURN` | 模型自然结束当前 turn | 普通请求映射 `end_turn`；Claude Code 还需完成握手 |
| `TOOL_USE` | 模型请求调用工具 | 有客户端工具时映射 `tool_use`；私有完成工具由网关消费 |
| `MAX_TOKENS` | 达到本次生成 token 上限 | 映射 `max_tokens` |
| `STOP_SEQUENCE` | 命中调用方配置的 stop sequence | Kiro 未回传实际命中串，不能构造合法 Anthropic 响应，fail closed |
| `GUARDRAIL_INTERVENED` | AWS guardrail 介入 | 有可见拒答时映射 `refusal`；空响应走客户端内容过滤错误 |
| `CONTENT_FILTERED` | 内容过滤器拒绝生成 | 有可见拒答时映射 `refusal`；空响应走客户端内容过滤错误 |
| `MALFORMED_MODEL_OUTPUT` | 模型输出结构无效 | 无安全等价 stop reason，fail closed |
| `MALFORMED_TOOL_USE` | 模型生成的工具调用无效 | 不伪造成合法 `tool_use`，fail closed |
| `MODEL_CONTEXT_WINDOW_EXCEEDED` | 模型上下文窗口耗尽 | 映射 `model_context_window_exceeded` |

Claude Code 对 Anthropic stop reason 的消费语义不同：`end_turn`、
`stop_sequence` 与 `pause_turn` 都会结束当前 CLI turn；`max_tokens` 与
`model_context_window_exceeded` 会触发客户端恢复；`tool_use` 只有同时存在合法
工具块才会继续；`refusal` 会作为 API error 结束。因此禁止用 `pause_turn`、空
`tool_use` 或虚构的 `max_tokens` 代替任务完成判定。

## Approved Behavior

- Claude Code system prompt 由共享 owner 注入完成连续性守卫：实现与验证尚未完成时继续调用工具，多步骤任务保持 task/todo 状态真实，只有需要用户输入的真实 blocker 才能提前停止。
- Kiro system priming 对已识别的 Claude Code prompt 只注入一次共享守卫，并注入 transport-private completion tool；普通 API prompt 不启用该协议。客户端已声明同名工具时禁用私有协议，绝不遮蔽客户端工具。
- 模型必须通过私有工具显式报告 `complete` 或 `blocked`，并提供非空的最终答复或 blocker 问题。该工具及其参数不暴露给 Claude Code；首轮完成信号仍可补齐尚未输出的最终答复。
- 未收到有效完成信号的 Claude Code `END_TURN` 或只有无效私有工具的 `TOOL_USE`，在同一客户端 HTTP 请求内进入有界 Kiro 续跑；续跑上限由 service 常量统一控制，禁止无限 Agent loop。
- 第二轮及之后属于 transport-only completion repair：默认对用户零可见文本；缺少完成信号的普通 assistant 文本只进入续跑历史与计费。仅三类例外可出门：普通 client 工具（含 tool 前必要短文本）、非 completion 终止结果（如 `MAX_TOKENS` / policy refusal）、以及去重后仍剩下的**单段短 blocker 问句**。
- 已有首轮可见文本时，后续 `complete` 的普通文本与私有 message 都只作为内部完成证据；后续 `blocked` 若为多段 recap、长段散文或非问句形态，整段当作 transport 握手丢弃。
- 普通 Claude Code 工具调用立即以 `tool_use` 返回客户端。私有完成信号与普通工具同时出现时，普通工具优先；`MAX_TOKENS` 等非完成终止原因也始终优先。
- `CONTENT_FILTERED` 与 `GUARDRAIL_INTERVENED` 无输出时沿用客户端内容过滤错误契约；已有可见拒答文本时返回 Anthropic `refusal`。
- 空值或未知 stop reason 不得伪造成 `end_turn`。非流式请求进入 502 failover；已开始的流式响应发送 SSE error，并且不发送成功终止事件。
- 隐藏续跑的额外输入与所有隐藏工具输出计入 Kiro 估算用量。每个成功映射记录脱敏结构化 marker `gateway.kiro_stop_reason`；完成协议动作记录 `gateway.kiro_completion_protocol`。

## Ownership and Flow

```text
Claude Code system prompt
  -> shared completion guard owner
  -> Kiro system priming + private completion tool (exactly once)
  -> Kiro EventStream metadataEvent.stopReason
  -> valid completion signal? -- no --> bounded internal continuation
                             -- yes -> strip private tool
  -> fail-closed stop-reason mapper / explicit completion
  -> Anthropic JSON/SSE terminal outcome
  -> Claude Code continues or finishes according to the real outcome
```

共享守卫与私有工具名的 owner 位于 `backend/internal/pkg/claude/claude_code_completion_guard.go`；Kiro 私有协议、tool schema 与 continuation payload owner 位于 `backend/internal/integration/kiro/claude_code_completion.go`。OpenAI-compatible Messages 与 Kiro translator 只调用共享 owner，不复制策略文本。Kiro stop reason 与有界续跑的 wire owner 位于 `backend/internal/service/kiro_gateway_service.go`，SSE encoder 只接收已经验证过的结果。

## Risk Boundaries

- 不改变路由、认证、计费、模型目录、账号调度或持久化数据。
- 不在服务端持久化 Claude Code 的 task/todo 状态；完成判定由同一模型通过私有协议显式声明，网关只验证信号形状与终止态。
- 不部署、不重启、不修改 kiro-us3/4/5/6 节点配置；上线由独立 release/rollout 审批处理。
- 行为变化仅限已识别的 Claude Code prompt、私有完成协议，以及 Kiro Messages 终止态的真实映射或 fail-closed 处理。
- 单个客户端请求可能产生额外 Kiro 调用、延迟与估算费用，但由确定性上限约束；达到上限后不再内部续跑。

## Verification

- 共享守卫幂等，并保留旧 marker 兼容既有 bridge 检测。
- 完整 `ClaudeToKiro` payload 的 system priming 与私有工具只出现一次；普通 prompt 及同名客户端工具不受影响。
- 流式与非流式 Claude Code 请求在首次 `END_TURN` 缺少完成信号时内部续跑；收到隐藏 `complete` 后只保留此前已展示的答复与 `end_turn`，隐藏 recap、语义改写、tool output 摘要及 completion message 均不得追加。
- 隐藏 `blocked` 在去重后必须仍是单段短问句才可见；长段散文或与已可见文本重叠的 recap 必须整段丢弃。隐藏普通工具必须保留 text-before-tool 顺序；没有首轮可见文本时允许 completion message 作为唯一兜底答复。
- 普通客户端工具立即返回；普通工具与完成信号并存时普通工具优先；空完成消息不能结束任务。
- 流式与非流式 `MAX_TOKENS` 均返回 `max_tokens`；context window、policy refusal 与不支持的官方值按上表处理。
- 未知 stop reason 在流式与非流式路径都 fail closed，且成功终止事件不会泄漏；续跑达到上限时确定性停止。
- Kiro、共享 Claude package 与相关 service 单元测试，以及 sentinel/preflight 门禁全部通过后才允许提交和推送。
