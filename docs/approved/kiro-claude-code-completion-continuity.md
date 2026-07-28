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

## Approved Behavior

- Claude Code system prompt 由共享 owner 注入完成连续性守卫：实现与验证尚未完成时继续调用工具，多步骤任务保持 task/todo 状态真实，只有需要用户输入的真实 blocker 才能提前停止。
- Kiro system priming 对已识别的 Claude Code prompt 只注入一次共享守卫；普通 API prompt 不注入。
- Kiro `END_TURN`、`TOOL_USE`、`MAX_TOKENS`、`STOP_SEQUENCE` 映射到对应 Anthropic Messages stop reason。响应包含 tool use 时以 `tool_use` 为准。
- `CONTENT_FILTERED` 无输出时沿用既有客户端错误契约；已有可见拒答文本时沿用既有成功响应契约。
- 空值或未知 stop reason 不得伪造成 `end_turn`。非流式请求进入 502 failover；已开始的流式响应发送 SSE error，并且不发送成功终止事件。
- 每个成功映射记录脱敏结构化 marker `gateway.kiro_stop_reason`，用于确认原始与 Anthropic stop reason 的关系。

## Ownership and Flow

```text
Claude Code system prompt
  -> shared completion guard owner
  -> Kiro system priming (exactly once)
  -> Kiro EventStream metadataEvent.stopReason
  -> fail-closed stop-reason mapper
  -> Anthropic JSON/SSE terminal outcome
  -> Claude Code continues or finishes according to the real outcome
```

共享守卫 owner 位于 `backend/internal/pkg/claude/claude_code_completion_guard.go`。OpenAI-compatible Messages 与 Kiro translator 只调用 owner，不复制策略文本。Kiro stop reason 的 wire owner 位于 `backend/internal/service/kiro_gateway_service.go`，SSE encoder 只接收已经验证过的结果。

## Risk Boundaries

- 不改变路由、认证、计费、模型目录、账号调度或持久化数据。
- 不在服务端持久化 Claude Code 的 task/todo 状态；任务完成判断仍由客户端 Agent 执行。
- 不部署、不重启、不修改 kiro-us3/4/5/6 节点配置；上线由独立 release/rollout 审批处理。
- 行为变化仅限已识别的 Claude Code prompt 注入，以及 Kiro Messages 终止态的真实映射或 fail-closed 处理。

## Verification

- 共享守卫幂等，并保留旧 marker 兼容既有 bridge 检测。
- 完整 `ClaudeToKiro` payload 的 system priming 中守卫只出现一次，普通 prompt 不受影响。
- 流式与非流式 `MAX_TOKENS` 均返回 `max_tokens`，不再伪造成 `end_turn`。
- 未知 stop reason 在流式与非流式路径都 fail closed，且成功终止事件不会泄漏。
- Kiro、共享 Claude package 与相关 service 单元测试，以及 sentinel/preflight 门禁全部通过后才允许提交和推送。
