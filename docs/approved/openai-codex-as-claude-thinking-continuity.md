---
title: OpenAI Codex 伪装 Claude — Thinking 多轮连续性修复
status: shipped
approved_by: xuejiao (design review 2026-04-22)
approved_at: 2026-04-22
authors: [agent]
created: 2026-04-22
related_prs: ["#31"]
related_commits: [922dd169]
related_stories: [US-027]
revised: 2026-11-20（删 prod 实证/实施清单散文；保留契约）
---

# OpenAI Codex 伪装 Claude — Thinking 多轮连续性

`status: shipped`（#31）。US-027 Done。实现锚点：`anthropic_to_responses.go`、Messages 流式护栏。

## 决策

当 Anthropic Messages 被路由到 OpenAI Codex 账号（`platform=openai` + `channel_type=0`）时：

- **Part B（根治）**：`AnthropicToResponses` **不**设置 `Include=["reasoning.encrypted_content"]`。在 `Store=false` 下请求加密 reasoning 却无法 roundtrip，上游对含 `tool_result` / thinking 历史的多轮会静默返回 0-token 空消息。
- **Part A（护栏）**：流式 Responses→Anthropic 路径保证 terminal 前至少发出一个 content block（emit flag + 空 text schema firewall）；不采用早期 BufferedResponseAccumulator 流式方案。

## 范围 / 非目标

- **只**修「OpenAI 平台账号承接 Anthropic Messages」复合路径。
- **不**改原生 Anthropic、Chat Completions compat、`previous_response_id` 状态化、`thinking.signature` roundtrip、空响应自动换号重试、WSv2 forwarder。
- 无新 config flag。

## 对外契约

| 维度 | 行为 |
| --- | --- |
| `POST /v1/messages`（OpenAI 路由）多轮空响应 | 不再因 Include+Store=false 触发 |
| thinking `signature` | 仍不注入 |
| 流式 SSE 形状 | 同；仅在「deltas 有内容但零 content block」时补块 |

验收以 US-027 Linked Tests 为准；历史实测表与同类项目调研散文已删。
