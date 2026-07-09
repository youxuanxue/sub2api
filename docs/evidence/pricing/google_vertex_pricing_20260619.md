# Google Vertex AI（Gemini Enterprise Agent Platform）媒体生成官方定价

> 来源：<https://cloud.google.com/gemini-enterprise-agent-platform/generative-ai/pricing>
> （旧路径 <https://cloud.google.com/vertex-ai/generative-ai/pricing> 重定向至此）
> 抓取日期：2026-06-19。价格会变，以官方页为准；本文件只是当时快照，用于计费对账与代码注释引用。
> 仅收录与 TokenKey 相关的**媒体生成**条目（Imagen 图片、Veo 视频、Gemini 原生图）。

## Imagen（图片生成）— 按张 flat 计费，**无 1K/2K/4K 分辨率档**

| 模型 ID（billing key） | 档位 | 官方价 |
| --- | --- | --- |
| `imagen-4.0-ultra-generate-001` | Imagen 4 Ultra | **$0.06 / 张** |
| `imagen-4.0-generate-001` | Imagen 4（Standard） | **$0.04 / 张** |
| `imagen-4.0-fast-generate-001` | Imagen 4 Fast | **$0.02 / 张** |
| `imagen-3.0-generate-002` / `-001` | Imagen 3 | $0.04 / 张 |
| `imagen-3.0-fast-generate-001` | Imagen 3 Fast | $0.02 / 张 |
| Imagen 2 / Imagen 1 | 生成 / 编辑 | $0.020 / 张 |
| Imagen 4 / Imagen 3 — Upscaling | 提分辨率到 2K/3K/4K（**独立功能**，非生成档） | $0.06 / 张 |
| Imagen 1 — Upscaling | 提分辨率到 2k/4k | $0.003 / 张 |

**关键事实**：官方对 Imagen 一律「$X **per image**」，价格只随**质量档（Fast/Standard/Ultra）**变，**不随输出分辨率变**。页面中 2K/3K/4K 只出现在单独的 *Upscaling* 功能行，不是生成定价的分档维度。

## Veo（视频生成）— 按秒计费

| 模型 | 档位 | 官方价 |
| --- | --- | --- |
| Veo 3.1 | Video（+Audio 同价） | $0.20 / 秒 |
| Veo 3.1 Fast | Video | $0.08 / 秒 |
| Veo 3.1 Lite | Video | $0.03 / 秒 |
| Veo 3 | Video | $0.20 / 秒 |
| Veo 3 Fast | Video | $0.08 / 秒 |
| Veo 2 | Video | $0.50 / 秒 |

## Gemini 原生图（Nano Banana 家族）— **token 计费，按分辨率分档**

> 这才是 1K/2K/4K 分辨率档位定价的真正归属，**与 Imagen 无关**。TokenKey 后端 `getDefaultImagePrice` 的硬编码 fallback `$0.134` 即来自这里（Gemini 3 Pro Image 的 1K/2K 输出价）。

| 模型 | 输出图分辨率 | 官方价（每张输出图，token 折算） |
| --- | --- | --- |
| Gemini 3 Pro Image | 1K / 2K（~1MP / ~4MP） | 1120 tokens ≈ **$0.134** |
| Gemini 3 Pro Image | 4K（~16MP） | 2000 tokens ≈ **$0.24** |
| Gemini 3.1 Flash Image | 512（~0.25MP） | 747 tokens ≈ $0.045 |
| Gemini 3.1 Flash Image | 1K（~1MP） | 1120 tokens ≈ $0.067 |

## TokenKey 计费含义

- **Imagen 按官方 flat 价结算**：TK overlay（`tk_pricing_overlay.json`）存的 imagen 底价与官方逐条一致（0.06/0.04/0.02）。后端的 `2K→×1.5 / 4K→×2` 尺寸乘数（`getDefaultImagePrice`，上游 commit `d1b684b78` 引入）是为**真有像素档位**的模型（Seedream 送 WxH）设计的；Imagen 请求只带比例码、无 size，会默认归 `2K` 档而被误乘 ×1.5。`tkIsFlatPerImageModel`（`billing_service_tk_flat_image.go`）按 `imagen-` 前缀豁免这条乘数，使 Imagen 按官方 flat 价结算。详见 PR #867。
- **1K/2K/4K 档位**只对 Gemini 原生图成立（且 Gemini 原生图走 `/v1/chat/completions` 的 token 计费路径，本就不经 `getDefaultImagePrice`）。
- **Veo 视频**按秒计费（`output_cost_per_second`），与图片的按张/按档无关。
