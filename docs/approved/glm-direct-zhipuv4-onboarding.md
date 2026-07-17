---
title: GLM Direct ZhipuV4 Onboarding
status: approved
approved_by: "xuejiao (operator directive, 2026-06-22)"
approved_at: 2026-06-22
authors: [agent]
created: 2026-06-22
related_prs: []
related_commits: []
---

# GLM Direct ZhipuV4 Onboarding

> Superseded operationally: GLM serving intent now rides Alibaba DashScope/Qwen
> pool (`channel_type=17`; live account membership is runtime DB/admin config,
> not this historical plan). BigModel `https://bigmodel.cn/pricing` is used only
> as the official pricing source (CNY/USD=6.7 plus TokenKey's default 1.06
> base-tax); do not use this historical plan to restore a BigModel/Zhipu direct
> serving path without a new approval.

## Intent

Move the GLM direct account from legacy Zhipu v3 (`channel_type=16`) to the
OpenAI-compatible ZhipuV4 adaptor (`channel_type=26`) and expose only paid,
officially-priced GLM chat SKUs.

## Scope

- Account 67 (`name='GLM'`, `platform='newapi'`) is the only account touched.
- `base_url` is normalized to `https://open.bigmodel.cn`; the adaptor appends
  `/api/paas/v4/...`.
- `credentials.model_mapping` receives identity mappings for the paid GLM SKUs
  listed in `tk_served_models.json`.
- Free GLM rows such as `glm-4.7-flash` and `glm-4.5-flash` stay excluded so no
  visible chat model bills at zero.

## Gates

- Durable repo gate: `tk_044_glm_direct_zhipuv4_model_mapping.sql`,
  `tk_pricing_overlay.json`, and `tk_served_models.json` must pass
  `scripts/checks/catalog-serving-drift.py`.
- Runtime canary gate: completed on 2026-06-22. Prod account 67 was switched
  to `channel_type=26`, `base_url=https://open.bigmodel.cn`, and the 12 paid
  GLM mappings; a GLM-group smoke key was added for `user_id=1`, with
  `user_allowed_groups` authorization because group 26 is an exclusive standard
  group. `ZHIPU_CHAT_MODELS=glm-4.7` returned `200 servable` through
  `ops/pricing/probe-servable-models.sh`.
- Billing canary: `usage_logs.id=2919744` recorded `requested_model=glm-4.7`,
  `account_id=67`, `group_id=26`, `input_tokens=6`, `output_tokens=224`,
  `total_cost=0.0004964000`, and `actual_cost=0.0005261840`.
- Rollback is to restore `channel_type=16`, `base_url=https://open.bigmodel.cn/api/paas/v4`,
  remove the GLM direct `model_mapping`, and enqueue `scheduler_outbox account_changed`.
