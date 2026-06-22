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
- Free Z.AI rows such as `glm-4.7-flash` and `glm-4.5-flash` stay excluded so no
  visible chat model bills at zero.

## Gates

- Durable repo gate: `tk_044_glm_direct_zhipuv4_model_mapping.sql`,
  `tk_pricing_overlay.json`, and `tk_served_models.json` must pass
  `scripts/checks/catalog-serving-drift.py`.
- Runtime canary gate: prod apply requires a GLM-group test API key, then
  `ZHIPU_CHAT_MODELS=glm-4.7` livefire via `ops/pricing/probe-servable-models.sh`.
- Rollback is to restore `channel_type=16`, `base_url=https://open.bigmodel.cn/api/paas/v4`,
  remove the GLM direct `model_mapping`, and enqueue `scheduler_outbox account_changed`.
