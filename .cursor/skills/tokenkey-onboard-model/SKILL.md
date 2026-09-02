---
name: tokenkey-onboard-model
description: >-
  TokenKey newapi curated-model onboarding through manifest, pricing owner, generated
  bundle, and evidence-backed activation. Use when adding or pricing Qwen, DeepSeek,
  Moonshot/Kimi, GLM, or VolcEngine models, or debugging priced-but-empty-pool drift.
---

# TokenKey：上架一个模型（newapi 策展模型面）

经 hub **`tokenkey-modelops-planner` 的 curated onboarding 路由**进入。本 skill 准备 manifest、价格 owner、
生成 bundle 与激活证据；一次请求能不能交付仍看
`docs/approved/pricing-serving-single-source-of-truth.md`。

命令与参数由 argparse parser 拥有，并机械生成到 `docs/agent_integration.md` § CLI。
本 skill 只保留上架判断与安全边界。

**意图源** = `backend/internal/service/tk_served_models.json`（manifest）。它断言三方一致，不替代：

1. 账号 `credentials.model_mapping`（RequestPlan 输入；新 floor 经 `modelops activate` 写入）
2. `tk_pricing_overlay.json` / complete registry（价格 owner）

测试样本必须从 manifest / overlay / allowlist owner 派生；禁止手写会随上架漂移的清单。

## 范围

范围由 `tk_served_models.json` 的策展 intent、生成 bundle 与 live account snapshot
共同表达。不要在 skill 中保存账号 ID、group ID 或当前账号名称。

不含完整 provider/LiteLLM 目录、原生平台 empirical allowlist 或协议路由。

## 真判断（脚本之外）

- 官方价与阶梯/思考双档（禁臆造；`source` URL+抓取日）
- `inconclusive`（非空池 429/5xx）是组无账号还是模型不可用
- 是否 catch-all（见 refresh skill 安全闸）
- 合并授权（人）

## 流程

不确定缺口 → 先走 hub 的 read-only planning 路由：`modelops.py plan`。

1. **probe 可服务性** — DashScope 用 `DASHSCOPE_CHAT_MODELS`（思考+非思考）；Kimi/单账号用
   `probe_account_model`。命令见 README / `tokenkey-account-model-probe`。须
   `verdict=servable` 且 `usage_match.account_id` 命中目标账号。
2. **写 manifest** — `tk_served_models.json` 加 `<id>`，只声明当前 policy：通用
   `channel_type`、必要的 base-URL `scopes`、非同名 `price_owner` 和 `display`。
   账号 ID、probe 日期、迁移/PR 和说明文字不进入 manifest。
3. **价 + bundle** — 官方核价后修改 complete registry（或明确 scoped channel price）；
   `go run ./cmd/account-model-mapping bundle` 生成 target artifact（禁手改 JSON）。
4. **activate** — 独立 probe/pricing evidence + `modelops.py activate` dry-run → 人审 →
   `--confirm yes-activate-model-surface`。只写 prod；generic deploy/rollback 不代替。
   契约：`docs/approved/model-surface-activation-contract.md`。
5. **livefire** — 同步骤 1 probe 族对 model_id 再打真 200（定价就绪 ≠ prod 可服务）。
6. **计费核对** — `usage_logs` 用 `requested_model`；思考档费率在 overlay
   `thinking_output_cost_per_token`，结果仍进 `output_cost`。

## 门禁

`scripts/checks/catalog-serving-drift.py`（preflight）校验 schema、scope、价格 owner
和 display 解析。生成 bundle 的 drift gate 校验 manifest 到运行时 floor 的投影。

prod live 对账：`manage-account-model-mapping-runtime.py check-accounts --json --bundle …`。

## 坑

- 大陆价基准 RMB÷6.7（Moonshot 国内表；禁国际 USD 表给 `api.moonshot.cn`）
- 新 floor 一律 activation；不得把 activation 记录写回 manifest
- 零计费高量：计费键是 `requested_model`，查 mapping chain
- **合并等人授权**

## 姊妹

- hub：`tokenkey-modelops-planner`
- catalog：`tokenkey-servable-model-refresh`
- 单账号诊断：`tokenkey-account-model-probe`
