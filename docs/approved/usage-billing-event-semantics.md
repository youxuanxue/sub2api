---
title: 用量计费事件语义：零图与失败搜索
status: approved
approved_by: "user (conversation approval: 2026-11-20)"
approved_at: 2026-11-20
created: 2026-11-20
authors: [agent]
risk: high
related_prs: ["#3346"]
related_issues: ["#3220", "#8949", "#8863"]
---

# 用量计费事件语义：零图与失败搜索

## 决策

TokenKey 的计费只对上游实际报告、且符合对应计量契约的成功用量事件收费。已知的零计量结果不得被统一计费入口回退成一次；失败的原生搜索调用不得进入搜索附加费。

本文件批准两个独立语义：

1. **图片计费**：当 Responses 用量结果明确包含 `ImageCount == 0`，且解析出的渠道价卡为 `BillingModeImage` 时，本次图片计量单位为 0，费用为 0。
2. **Grok 原生搜索**：只有未标记为失败、取消或错误的原生搜索输出项计入搜索调用数；`failed`、`cancelled`、`canceled`、`error` 状态不计费。

## 图片计费契约

- `ImageCount > 0` 时，继续使用既有图片/按张计费路径及图片倍率。
- `ImageCount == 0` 仅在计数已知且价卡明确为图片模式时按 0 张结算。
- 普通 token、按次、视频及未知计数场景保持原有兼容行为；未知计数不能被推断为零。
- 统一按次/图片计费入口必须区分“计数为零”和“没有提供计数”：旧调用方未声明计数已知时仍保留一次的兼容回退。
- 计费价卡仍由 `ModelPricingResolver` 解析，不能因为响应零图而绕过价格 owner 或改用另一套价格表。

## Grok 搜索计费契约

- JSON `output`、SSE `response.output_item.done` 和终态 `response.completed` / `response.done` 使用同一搜索输出项识别规则。
- 输出项类型仍限于既有原生搜索类型和服务器侧搜索 custom/function call 识别规则；本文件不扩大工具识别范围。
- 状态比较忽略大小写和首尾空白。状态为 `failed`、`cancelled`、`canceled` 或 `error` 的项不计数。
- 缺少状态的既有兼容格式继续按原识别规则计数；缺少状态不等于失败。
- 同一调用在 `output_item.done` 与终态 output 中只计一次；稳定 `call_id` / `id` 优先，缺少 ID 时沿用现有有序合成键规则。
- 上游终态 `usage.server_side_tool_usage_details.x_search_calls` 是外部证据；本地解析器必须与同一响应事件的成功调用集合一致，不凭字段文本出现次数计费。

## Owners

| 事实 | Owner |
| --- | --- |
| 本计费语义契约 | 本文件 |
| OpenAI Responses 图片用量结算 | `backend/internal/service/openai_gateway_usage.go` |
| 统一按次/图片计费的已知计数语义 | `backend/internal/service/billing_service.go` |
| Grok 原生搜索输出识别与去重 | `backend/internal/service/grok_search_count.go` |
| 图片零计量回归 | `backend/internal/service/image_count_billing_switch_test.go` |
| 失败搜索回归 | `backend/internal/service/grok_search_count_test.go` |

## 验收

- 图片价卡 + 明确零图：`TotalCost == 0` 且 `ActualCost == 0`。
- 图片价卡 + 正数图片：按图片数量和图片倍率正常结算。
- 既有普通按次计费：未声明计数已知时，缺省计一次的兼容测试继续通过。
- Grok JSON 与 SSE 混合成功/失败输出：只计成功调用，并对 `output_item.done` 与终态输出去重。
- 固定上游 SSE 样本逐事件核对：失败的 X Search 不计费，终态 `x_search_calls == 1` 与本地计数一致。

## 边界

本文件只定义用量事件如何进入计费，不改变路由、候选准入、上游工具权限、价格 owner、退款、历史账单回写或生产发布流程。生产部署和合并仍是独立动作。
