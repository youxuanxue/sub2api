---
title: Response usage.cost（推理响应带回标准官方价）
status: approved
approved_by: "user (2026-09-30 conversation: cost 应为标准官方价格而非 actual_cost)"
approved_at: 2026-09-30
created: 2026-09-29
authors: [agent]
risk: high
related_prs: ["#2384"]
---

# Response usage.cost（推理响应带回标准官方价）

## Intent

Universal Key / 直连 Key 的推理响应在已有 `usage` token 字段上，追加本笔用量的
**标准官方价** `usage.cost`（USD number），便于客户侧按牌价对账；客户实扣
（分组倍率 / 高峰因子等）仍只落在账本 `actual_cost`，不放进响应。

## Contract

- **字段**：在已有 `usage` 对象上增加 `cost`（float64，USD）。语义 = 用量账本
  `total_cost`（倍率前标准官方价 / 价格表结算价），**不是**客户实扣 `actual_cost`，
  也不是上游原价拆分。
- **非流式**：整包 JSON 的 `usage.cost`。
- **流式**：
  - OpenAI Chat Completions：含 `usage` 的 chunk（通常为末帧）写入 `usage.cost`。
  - Anthropic Messages：在 `message_delta` 的 `usage` 上写入（此时已具备完整用量）；
    `message_start` 不写（输入未齐）。
- **缺省**：无 `usage`、无法算定（缺鉴权快照 / 非 token 计价媒体等）时 **不新增**
  `cost` 字段，不发明 0。
- **一致性**：响应里的 `cost` 与随后落库的 `usage_logs.total_cost` 同源结算；响应侧
  预计算的完整 `CostBreakdown` 挂在 ForwardResult 上供 RecordUsage 复用（实扣仍走
  `ActualCost`）。粘性会话 `ForceCacheBilling`（input→cache_read）必须在响应预览侧
  同样应用后再 settle。

## Out of scope（v1 不做）

- OpenAI Responses / 非 raw Chat Completions 写出路径（后续追加）。
- 新响应头、HTTP trailer、新查询 endpoint。
- `cost_details` / upstream 成本拆分。
- 改写客户端可见的 token 数字。
- 强制媒体 / 按次计费路径在响应里返回 cost。
- 在响应里暴露 `actual_cost` / 分组倍率后的实扣。
- v1 覆盖面：**Claude Messages** + **OpenAI Chat Completions raw** 的 token 路径。

## Owners

| 关注点 | Owner |
| --- | --- |
| 契约（本文件） | `docs/approved/response-usage-cost.md` |
| JSON/SSE 注入 | `backend/internal/service/response_usage_cost_tk.go`（`InjectUsageCost*`；入参为 `TotalCost`） |
| Claude 客户实扣结算 | `settleClaudeCustomerFacingCost`（`gateway_usage_billing.go`）— RecordUsage 与响应预览共用 |
| OpenAI 客户实扣结算 | `settleOpenAICustomerFacingCost`（`openai_gateway_usage.go`）— 含 response_model / Free Fast；RecordUsage 与响应预览共用 |
| Claude Messages 写出 | `gateway_upstream_response.go`（注入 `TotalCost`） |
| OpenAI Chat Completions raw 写出 | `openai_gateway_chat_completions_raw.go`（注入 `TotalCost`） |
| 同请求防漂移 | 响应侧结算结果挂 `PrecomputedCost`，`RecordUsage` 复用，禁止二次编排 |

禁止在 preview 路径再镜像一份 response_model / Free Fast / 倍率编排；新增结算规则只改 `settle*`。
响应写出必须传 `CostBreakdown.TotalCost`，禁止再传 `ActualCost`。

## Validation

- 非流式 chat/messages：响应 `usage.cost` 与同请求落库 `total_cost` 一致（容差 1e-12）；
  当分组倍率 ≠ 1 时，`usage.cost` ≠ `actual_cost`。
- 流式：末帧 / `message_delta` 含 `usage.cost`；无 usage 时不出现 cost。
- 既有无 usage 的错误/中断路径行为不变。
