---
title: 定了价才能上 — serving 准入处的运行期价格闸
status: approved
approved_by: "xuejiao (design directive, 2026-06-27)"
approved_at: 2026-06-27
authors: [agent]
created: 2026-06-26
revised_at: 2026-09-01
revision_note: >
  Slim to gate contract only; narrative history removed. Delivery formula stays in
  pricing-serving-single-source-of-truth.md.
related_prs: [1016]
related_commits: []
related_stories: []
related_design: docs/approved/pricing-registry-hot-reload.md, docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/pricing-availability-source-of-truth.md, docs/approved/channel-pricing-refund-gate-and-runtime-pricing.md, docs/approved/newapi-served-models-reconciler.md
supersedes: none
---

# 定了价才能上 — serving 准入处的运行期价格闸

本文**只**拥有运行期价格闸：发往上游前必须能解析结算价。交付公式不在本文，见
`docs/approved/pricing-serving-single-source-of-truth.md`。

**不变量：** 真价 > 家族 floor > 拒。有价（含 floor）放行，绝不 `$0`；连 floor 都没有 →
`404` + 内部子码 `model_not_priced`。走 floor 时发 `served_at_fallback`。闸默认全平台开
（`SettingKeyPricedServingGateEnabled='*'`，迁移 `tk_047`）；按平台移出启用集即回滚。

本闸**不**写 `model_mapping`、**不**写价格、**不**拥有 CatalogPolicy / RequestPlan。

## 1. 为什么需要闸

native 空 `model_mapping` 仍可能 catch-all 透传未定价 id；billing 在
`ErrModelPricingUnavailable` 时 fail-open 记 `$0`。CI A1（`catalog-serving-drift.py`）只保护
已上架 id。闸是运行期对应：转发前用与 billing **同一两个价源、同一键**判定。

## 2. 闸点

在 billing model id 解析后、上游首字节前调用 `tkCheckPricedServingGate`（companion：
`gateway_priced_serving_gate_tk.go` + `gateway_priced_serving_gate_wiring_tk.go`）。

价源（闸 ⟺ billing，无影子谓词）：

1. `BillingService.GetModelPricing`（active registry direct owner + registry family alias / floor）
2. `resolveChannelPricing` / `channel_model_pricing`（基础价 miss 时）

两源都解不出价（含 floor）且平台在启用集内 → 拒。键：native gemini/anthropic 用
`originalModel`；openai native 用 mapped `billingModel`。

**B1：** 只问 `GetModelPricing` 会漏渠道价 → 误拒「渠道有价、基础价缺」；改
`billing_model_source` 须经 admin 确认闸（`channel_handler_tk_billing_source_guard.go`）。

拒绝形：真实 `404`，body 按客户端协议对齐（anthropic `not_found_error` / gemini
`googleError` NOT_FOUND / openai `invalid_request_error`+`model_not_priced`）。不用 `403`。

无降级 canary：已删除；family owner + embedded LKG 即 glitch 防护。无 floor 的 newapi 缺价应拒。

## 3. 家族 floor

主流家族（claude / gpt / gemini）无真价时落到 registry **家族中位** floor，立刻服务 +
`served_at_fallback`。无 floor 的厂商/系列（多厂商 newapi、国产、OpenAI **o 系列**等）缺真价即拒。

`IsServedViaFamilyFloor`：有 floor 家族的未知 id → true；无 floor 厂商 → false。

## 4. 机械化强制

- Sentinel：`scripts/sentinels/priced-serving-gate.json`（闸 helper、各路线 hook、设置键、
  `tk_047` `'*'` seed、channel 确认闸）。
- 测试：`pricing_predicate_consistency_tk_test.go`（闸 ⟺ billing / floor）、
  `gateway_priced_serving_gate_*_test.go`（开/关、404、告警文案）、启用集 `*` / 空集回滚。

## 5. 已知残留（不阻断默认 ON）

- AG / Kiro / embeddings 路线闸 hook 已接线；`countTokens` 豁免。
- OpenAI o 系列无 gpt catch-all floor → 缺真价 reject（有意 backstop）。
- `billing_model_source=channel_mapped` 且渠道改名时，闸键与 billing 键可能偏斜——改名前先确认
  （见 §2 B1）。
- 闸 404 外形与部分网关既有上游 404 处理仍有细微分裂；不致鉴权/重试误判。
