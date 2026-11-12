---
title: NewAPI 生图图片 token 计费（output_tokens_details 丢失修复）
status: pending
approved_by: 待审
created: 2026-11-11
authors: [agent]
risk: high
related_design: docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/gpt-image-native-cutover.md
---

# NewAPI 生图：图片 token 计费丢失修复

## 问题（现网实测）

newapi 第五平台账号走 `/v1/images/generations`，上游返回 200 和完整图片，
但 TokenKey 不给这一单计费，日志里每次都出现：

```text
openai_images_generations.record_usage_failed
error: "image output tokens unavailable for token-priced image owner: model=gpt-image-2.5-flare"
```

`usage_logs` 里查不到对应行，`billing_usage_entries` 也没有扣款。等于**白送**。

2026-11-11 现场：账号 `454`（`tokensea/default · 企业号转生图`，
`https://agent.tokensea.ai`，channel_type 1）被绑到 `image-oai-0.7`（group 1656），
优先级调到 1 后开始承接 `image-oai-0.7` 的生图流量。4 小时内 10 次
`record_usage_failed`，全部无 `usage_logs` 落行。

## 根因

`usage.output_tokens_details.image_tokens` 在桥接层被丢掉，链路有三段：

1. 上游 tokensea 返回的是 OpenAI Images 形状，用量在
   `usage.output_tokens_details.image_tokens`（本次实测为 196）。
2. TokenKey 经 new-api 适配器取用量，拿到的是 new-api 的 `dto.Usage`。
   该结构**没有 `output_tokens_details` 字段**（只有
   `prompt_tokens_details` / `completion_tokens_details` 和
   `input_tokens_details`），`relay/channel/openai/relay_image.go`
   的 `normalizeOpenAIUsage` 也只映射 `InputTokensDetails`。
3. `backend/internal/service/newapi_bridge_usage.go` 的
   `openAIUsageFromNewAPIDTO` 于是只能给出 `ImageOutputTokens: 0`。

而 token 计价的生图 owner（`gpt-image-2` / `2.5-*` / `2.6-*`）在
`billing_service_tk_image_token_settlement.go` 里是**失败关闭**的：

```go
if tokens.ImageOutputTokens <= 0 {
    return nil, fmt.Errorf("%w: model=%s", ErrImageUsageTokensUnavailable, model)
}
```

这个失败关闭是正确设计（宁可不发也不许 $0 结算），不能放宽。要修的是第 2 段：
把上游的图片 token 明细**在 TokenKey 侧补回来**。

## 决策

在 bridge 层观察适配器写给客户端的响应，从中取回
`output_tokens_details.image_tokens`，经 `DispatchOutcome` 交给
`OpenAIForwardResult.Usage`，不修改 new-api 结构、不改 `dto.Usage`。

- `backend/internal/relay/bridge/image_usage_observer_tk.go`：
  `imageUsageObserver` 包装 gin writer，只观察不改变转发字节。
  非流式保留头部与滚动尾部（上限 64 KiB）；流式解析完整 `data:` 行取最后一次 usage。
  大 base64 载荷不整体驻留，内存有界。
- `RunImageRelayWithUsage`：`RunImageRelay` 的增量入口，装观察器并返回明细；
  `RunImageRelay` 行为不变（观察器为 nil，返回空明细）。
- `DispatchImageGenerations` 把明细放进 `DispatchOutcome.ImageUsage`。
- `mergeBridgeImageUsage` 把明细并入 `OpenAIUsage`，**只填空值，不覆盖**
  适配器已给出的正数。

## 不做什么

- 不放宽 `ErrImageUsageTokensUnavailable` 的失败关闭。
- 不给 unpriced / 未知模型补兜底价（那是另一条门禁）。
- 不改 `../new-api`（跨仓依赖按 CLAUDE.md §4 只在 pin 上同步）。
- 不回填历史未计费的单子。

## 验收

- 单测：观察器覆盖非流式 / 分块 / 流式 / 大载荷有界内存 / 无 usage / 错误体。
- 端到端：`DispatchImageGenerations` 打 tokensea 形状的假上游，必须给出
  `ImageUsage.ImageOutputTokens == 196`，且客户端仍收到完整图片。
- 结算：`TkCalculateImageTokenCost("gpt-image-2.5-flare", ...)` 在 merge 前
  返回 `ErrImageUsageTokensUnavailable`，merge 后返回正的 `TotalCost` 与
  `BillingMode=image`。
- 线上验收（合入发版后）：用 `image-oai-0.7` 的 key 打一次生图，确认
  `usage_logs` 出现该 `account_id` 的记录、`total_cost > 0`、
  `record_usage_failed` 不再出现。

## 已知边界

- 观察的是**客户端可见响应**。若某个适配器在服务端改写用量而不写进
  响应体，本次修复取不到，仍会失败关闭并记 ops 错误（不会静默 $0）。
- 流式只在 `image_generation.completed` 事件带 usage 时有效；上游不带 usage 的
  流式实现会走同一失败关闭。
