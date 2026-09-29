---
title: Response usage.cost（推理响应带回实扣费用）
status: pending
approved_by: pending
created: 2026-09-29
authors: [agent]
risk: high
---

# Response usage.cost（推理响应带回实扣费用）

## Intent

Universal Key / 直连 Key 的推理响应在已有 `usage` token 字段上，追加本笔对客户的实扣金额
`usage.cost`（USD number），对齐 OpenRouter 事实标准，便于客户侧实时对账。

## Contract

- **字段**：在已有 `usage` 对象上增加 `cost`（float64，USD）。语义 = 用量账本
  `actual_cost`（含分组倍率 / 高峰因子 / 订阅折算后的客户实扣），**不是**上游原价、也不是
  标准价 `total_cost`。
- **非流式**：整包 JSON 的 `usage.cost`。
- **流式**：
  - OpenAI Chat Completions：含 `usage` 的 chunk（通常为末帧）写入 `usage.cost`。
  - Anthropic Messages：在 `message_delta` 的 `usage` 上写入（此时已具备完整用量）；
    `message_start` 不写（输入未齐）。
- **缺省**：无 `usage`、无法算定（缺鉴权快照 / 非 token 计价媒体等）时 **不新增**
  `cost` 字段，不发明 0。
- **一致性**：响应里的 `cost` 与随后落库的 `usage_logs.actual_cost` 同源计算；响应侧
  预计算的结果挂在 ForwardResult 上供 RecordUsage 复用，避免二次漂移。

## Out of scope（v1 不做）

- OpenAI Responses / 非 raw Chat Completions 写出路径（后续追加）。
- 新响应头、HTTP trailer、新查询 endpoint。
- `cost_details` / upstream 成本拆分。
- 改写客户端可见的 token 数字。
- 强制媒体 / 按次计费路径在响应里返回 cost。
- v1 覆盖面：**Claude Messages** + **OpenAI Chat Completions raw** 的 token 路径。

## Owners

| 关注点 | Owner |
| --- | --- |
| JSON/SSE 注入 | `backend/internal/service/response_usage_cost_tk.go` |
| Claude Messages 写出 | `gateway_upstream_response.go` |
| OpenAI Chat Completions raw 写出 | `openai_gateway_chat_completions_raw.go` |
| 计费复用 | `ForwardResult` / `OpenAIForwardResult` 的预计算 cost + RecordUsage |

## Validation

- 非流式 chat/messages：响应 `usage.cost` 与同请求落库 `actual_cost` 一致（容差 1e-12）。
- 流式：末帧 / `message_delta` 含 `usage.cost`；无 usage 时不出现 cost。
- 既有无 usage 的错误/中断路径行为不变。
