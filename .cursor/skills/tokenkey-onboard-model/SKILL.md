---
name: tokenkey-onboard-model
description: >-
  TokenKey newapi long-tail onboarding — write account mapping and price owners. Use when adding or pricing Qwen, DeepSeek, Moonshot/Kimi, GLM, or VolcEngine models on curated newapi accounts, or debugging priced-but-empty-pool 429/503 drift.
---

# TokenKey：上架一个模型（newapi 长尾）

经 hub **`tokenkey-modelops-planner` 分支 C** 进入。本 skill 只写账号 `model_mapping` 与价格
owner；一次请求能不能交付仍看 `docs/approved/pricing-serving-single-source-of-truth.md`。

命令、probe 族、hotfix、activate 确认词：**只维护在 `ops/pricing/README.md`**，此处不复述。

**意图源** = `backend/internal/service/tk_served_models.json`（manifest）。它断言三方一致，不替代：

1. 账号 `credentials.model_mapping`（RequestPlan 输入；新 floor 经 `modelops activate` 写入）
2. `tk_pricing_overlay.json` / complete registry（价格 owner）

测试样本必须从 manifest / overlay / allowlist owner 派生；禁止手写会随上架漂移的清单。

## 范围

| 账号 | 名称 | channel_type | group | 上游 |
| --- | --- | --- | --- | --- |
| 60 | Qwen | 17 DashScope | 18 | `dashscope.aliyuncs.com` |
| 39 | ds-官 | 43 DeepSeek | 11 | `api.deepseek.com` |
| 83 | kimi | 25 Moonshot | 19 | `api.moonshot.cn`（国内 RMB÷6.7） |

**不含** litellm 全目录、四原生平台（走 refresh）、grok（第七平台，不经上述 mapping）。

## 真判断（脚本之外）

- 官方价与阶梯/思考双档（禁臆造；`source` URL+抓取日）
- `inconclusive`（非空池 429/5xx）是组无账号还是模型不可用
- 是否 catch-all（见 refresh skill 安全闸）
- 合并授权（人）

## 流程

不确定缺口 → 先 hub **分支 A** `modelops.py plan`。

1. **probe 可服务性** — DashScope 用 `DASHSCOPE_CHAT_MODELS`（思考+非思考）；Kimi/单账号用
   `probe_account_model`。命令见 README / `tokenkey-account-model-probe`。须
   `verdict=servable` 且 `usage_match.account_id` 命中目标账号。
2. **写 manifest** — `tk_served_models.json` 加 `newapi/<id>`；新 floor 的 `notes` 必须含
   `served-via-modelops-activation`；`display` / `price_source` 语义见 manifest `_schema`。
3. **价 + bundle** — 官方核价后 `apply-pricing-hotfix.py stage-overlay`（或 registry PR）；
   `go run ./cmd/account-model-mapping bundle` 生成 target artifact（禁手改 JSON）。
4. **activate** — 独立 probe/pricing evidence + `modelops.py activate` dry-run → 人审 →
   `--confirm yes-activate-model-surface`。只写 prod；generic deploy/rollback 不代替。
   契约：`docs/approved/model-surface-activation-contract.md`。
5. **livefire** — 同步骤 1 probe 族对 model_id 再打真 200（定价就绪 ≠ prod 可服务）。
6. **计费核对** — `usage_logs` 用 `requested_model`；思考档费率在 overlay
   `thinking_output_cost_per_token`，结果仍进 `output_cost`。

## 门禁

`scripts/checks/catalog-serving-drift.py`（preflight）：A0 parse / A1 price / A2 display⇒owner /
A3 served_on⇒mapping path / A4 enumeration WARN。新 floor 缺
`served-via-modelops-activation` 会硬失败（#812）。

prod live 对账：`manage-account-model-mapping-runtime.py check-accounts --json --bundle …`。

## 坑

- 大陆价基准 RMB÷6.7（Moonshot 国内表；禁国际 USD 表给 `api.moonshot.cn`）
- 新 floor 一律 activation；`served-via-admin-ui` 仅历史种子态
- 零计费高量：计费键是 `requested_model`，查 mapping chain
- **合并等人授权**

## 姊妹

- hub：`tokenkey-modelops-planner`
- catalog：`tokenkey-servable-model-refresh`
- 单账号诊断：`tokenkey-account-model-probe`
