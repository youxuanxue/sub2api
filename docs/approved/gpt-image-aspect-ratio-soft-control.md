---
title: GPT Image aspect_ratio soft control + floor identity
status: approved
approved_by: "user (2026-09-28 conversation on PR #2368: approved push after xj-review; live OAuth probes confirmed marker AR ratio control)"
approved_at: 2026-09-28
created: 2026-09-28
authors: [cursor]
risk: high
related_prs: [2368]
---

# GPT Image aspect_ratio soft control + floor identity

> 状态：approved。范围：`gpt-image-*` 的 TokenKey-only `aspect_ratio` 准入、出站
> `marker AR=` 软控、exact contract 的 `output_format` jpeg 放行，以及原生 OpenAI
> floor 上的 `gpt-image-2` / `gpt-image-2.5-{flare,sunburst}` identity。
> 实现细节以代码为准；本文只锁契约与边界。

## 背景

官方 OpenAI Images JSON **没有** `aspect_ratio` 字段；文档只认 `size=WIDTHxHEIGHT`。
ChatGPT OAuth / Codex `image_generation` 可通过 prompt 文本 `marker AR=<ratio>` 软控画布比例。
TokenKey 对外承认可选 `aspect_ratio`，在出站 prompt 注入该 marker，并把字段在转发前剥离，
避免 API-key 上游因未知参数 400。

## 契约

### 1. `aspect_ratio` 准入（公共）

- 仅 `gpt-image-*` 模型允许显式 `aspect_ratio`；其它模型 → 错误。
- 允许值（归一化后）：`1:1`、`3:2`、`2:3`、`16:9`、`9:16`。非法值 → 400。
- 类型必须是 string（或 JSON null）；非 string → 400。
- 字段是 TokenKey-only：转发前从 JSON / multipart 删除，**永不**作为官方 Images 参数发送。

### 2. 出站 `marker AR=` 注入

| 路径 | 行为 |
| --- | --- |
| OAuth / Responses / Direct soft builders | `deriveFromSize=true`：显式 `aspect_ratio` 优先；否则从已知官方 `size` 派生比例并注入 |
| API-key Images | `deriveFromSize=false`：仅显式 `aspect_ratio` 且**未**同时带 `size` 时注入；已有硬 `size` 时不注入，避免与官方 WxH 冲突 |

已有客户端 `marker AR=...` 时幂等，不再追加。

### 3. 实测边界（edge OAuth，非契约保证）

- `marker AR=16:9` / `9:16`（及 `marker AR=16:9,4k` 合并写法）：比例命中观测像素。
- `size=1k|2k|4k` / `marker size=1k|2k|4k`：**不能**拉出稳定 1K/2K/4K 分辨率阶梯。
- tools JSON `size=` 在 OAuth Codex 路径上仍常落 `auto`；硬控分辨率以官方 API-key `size` 为准。

### 4. `tk_image_contract=exact`

- `output_format` 放行：`png` \| `jpeg` \| `auto`（edge OAuth 实测 jpeg/png 魔数匹配）。

### 5. Floor / allowlist

原生 OpenAI catalog / floor 保留：

- `gpt-image-2` identity（默认 Images 模型，避免 floor 替换空 mapping fail-open 后拒请求）
- `gpt-image-2.5-flare` / `gpt-image-2.5-sunburst` identity
- `gpt-image-2.5` → `gpt-image-2.5-flare`

营销后缀（plus/pro/vip）**不**进 floor。`gpt-image-2` 从 advertised_dead 提升为 allowlisted，
会出现在 priced ∩ allowlist 的 `/v1/models` fallback 中。

## Owners

| 关注点 | Owner |
| --- | --- |
| 比例 allowlist / marker / resolve | `backend/internal/service/openai_images_aspect_ratio_tk.go` |
| Parse / strip / API-key rewrite | `backend/internal/service/openai_images.go` |
| OAuth Direct / Responses 注入 | `openai_images_direct.go` / `openai_images_responses.go` |
| Floor / catalog membership | `pricing_catalog_supported_models_tk.go` + `account_model_mapping_ssot_tk.go` |
| Sentinel | `scripts/sentinels/gateway-tk.json`（aspect_ratio companion + 注入点） |
| 回归测试 | `openai_images_aspect_ratio_tk_test.go` 等 |

## 非目标

- 不把 `1k/2k/4k` 当作网关契约。
- 本 PR 不改 Studio Web UI：`GPT_IMAGE_SIZES` 仍可仅暴露官方 WxH；`no-web-impact`。
