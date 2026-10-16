---
title: GPT Image strict canvas + format post-process (codex2api parity)
status: shipped
approved_by: "youxuanxue (merged PR #2385 on 2026-09-29)"
approved_at: 2026-09-29
created: 2026-09-29
authors: [cursor]
risk: high
related_prs: ["#2385", "#2402"]
---

# GPT Image strict canvas + format post-process

> 状态：`shipped`（已由 youxuanxue 合并 [PR #2385](https://github.com/youxuanxue/sub2api/pull/2385)，
> 合并记录作为审批基线；此状态表示已合并，不表示已部署）。[PR #2402](https://github.com/youxuanxue/sub2api/pull/2402)
> 增量：尺寸分隔符/像素下限/单边 3840、style→Style guidance、Responses 主模型 fallback。
> 对齐 [codex2api](https://github.com/james-6-23/codex2api)
> Image Studio / PR #519：上游 ChatGPT OAuth 不保证字面 `size` / `output_format` /
> `background=transparent`；网关在显式请求时用本地后处理兑现客户契约。

## 背景

edge-us3 OAuth 实测（2026-09-29，account `gpt125` / `gpt-image-2`）：

**Direct `/codex/images/*`（现网 1.8.265 默认路径，已废弃）**

- `output_format=jpeg`：网关已重编码为 JPEG；尺寸常被上游改写。
- `size` / `webp`：上游忽略；webp 现网返回 PNG。

**Responses + `image_generation` tool（本 PR 目标路径，上游直连实测）**

| 请求 | HTTP | 容器 | 像素（实测） | 备注 |
| --- | --- | --- | --- | --- |
| `size=1024x1024` `output_format=jpeg` `quality=medium` | 200 | jpeg | `1145x1374` | format 兑现；size 不兑现 |
| `size=1024x640` `output_format=webp` `quality=high` | 200 | webp | `1312x1199` | **webp 上游原生兑现**；size/quality 软 |
| `size=1024x1024` `background=transparent` `output_format=png` | 200 | png | `1024x1536` | background 透传；size 不兑现 |
| `size=1024x1024` `output_format=jpeg` `quality=high` | 200 | jpeg | `1254x1254` | call_meta.quality=`low`（请求 high）；size 不兑现 |

结论：format/background 在 Responses 路径明显好于 Direct；**字面 size 仍需本地 pad**；
此前将 quality/compression 都用于本地后处理；quality 的处理已由下方 2026-10-16 修订替代。`3840x2176` 继续本地 400（总像素超 `8294400`）。

## 决策（已确认）

**继续走本地精确画布**，与 codex2api PR #519 / Image Studio `strict_size` 同路径：

1. tools 参数形状已对齐；上游仍把 `tools[].size` 软化为 `auto` 并返回漂移像素 —— **不是传参错误**。
2. 显式 `size=WxH`：上游发 ceil-16 合法尺寸；响应经本地 **pad** 落到请求画布（cover/crop 非目标）。
   内容需要放大时：本地 **渐进 CatmullRom**（每跳 ≤2×），减轻大画布一次拉满发糊。
   **不**引入 `-2k/-4k` 模型别名；**不**接 Real-ESRGAN 外挂（可后续独立评估）。
3. 显式 `output_format` 不符或指定 `output_compression` 时本地 coerce；`quality` 只影响上游生成，不单独触发重编码（见 2026-10-16 修订）。
4. 非目标：不把字面 size 押在上游 Responses 行为上；不引入 `-2k/-4k` 超分别名。
5. 废弃 `tk_image_contract`：本地精确画布/format 后处理为默认行为；字段若传入则忽略并在转发前剥离。
6. Studio/Quickstart：GPT Image 芯片发送 `size=WxH`（由 Go `openAIImagesKnownSizeTable` 的 `StudioChip` 行生成 `GPT_IMAGE_SIZES`），与本契约共用同一尺寸表。

## 契约

### 0. 上游传输（codex2api 对齐）

OAuth / Setup-Token 生图**一律**走：

`POST https://chatgpt.com/backend-api/codex/responses`

请求体为 Responses + `tools[{type:image_generation,...}]` + `tool_choice=image_generation`，
驱动模型默认 `gpt-5.6-luna`，图像模型在 `tools[0].model`。
**不再**默认调用 `/backend-api/codex/images/generations` Direct 端点。

### 1. 尺寸准入（OAuth Responses 生图）

对显式 `size=WIDTHxHEIGHT`（非 `auto`）：

- 分隔符归一：接受 `x` / `X` / `*` / `×`，解析后统一为小写 `WIDTHxHEIGHT`。
- 宽高为正整数；**单边 ≤ `3840`**；总像素 ∈ `[655360, 8294400]`；长边/短边 ≤ 3。
- 否则 **400** `invalid_request_error`（对齐超限用例，不等待上游）。
- 发往上游的 size：各边 **向上取整到 16 的倍数**（例：`1920x1080` → `1920x1088`）。
- 返回给客户的画布：本地 **pad** 精确还原请求的 `WxH`（内容等比装入，边距填充；不 crop）。
- 响应顶层 `size` = **最终画布**；usage 计费档仍按最终像素 reconcile。

### 1b. Responses 驱动主模型（main model）与 style

- 驱动模型默认 `gpt-5.6-luna`（`SUB2API_IMAGES_MAIN_MODEL` 可覆盖）；图像模型仍在 `tools[0].model`。
- 上游以 plan-gated 400 拒绝当前驱动时，**同账号**按
  `gpt-6.1-sol` → `gpt-5.6-terra` → `gpt-5.6-sol` → `gpt-6-astra` 重试
  （`SUB2API_IMAGES_MAIN_MODEL_FALLBACKS` 可覆盖整条链），不冷却生图容量。
- 客户端 `style` **不写** `tools[].style`；折入 prompt 文本
  `Style guidance: <style>`（在 `marker AR=...` 之前），与 codex2api 一致。

### 2. `output_format` 后处理

显式 `png` / `jpeg` / `webp`：

- 上游容器不符时，网关解码后重编码；魔数必须与声明一致（fail-closed）。
- `webp` 使用纯 Go lossless 编码器（`CGO_ENABLED=0` 兼容）。
- `output_compression`（0–100）继续映射到 JPEG quality / WebP compression effort。
- `quality` 是生成质量，继续传给上游；不再映射到本地编码参数。
- 格式和尺寸已满足请求、没有显式压缩时，返回原始图片字节，保留上游附带的 C2PA 等元数据。
- 格式转换、尺寸调整或显式压缩会生成衍生图片；不复制原图签名到变更后的字节，也不伪造 OpenAI 签名。

### 3. `background`

- 继续透传上游 `background`。
- 本地 pad 时：`transparent` + png/webp → 透明边；`opaque` / jpeg → 不透明垫底。
- **不**承诺主体抠图；真 alpha 仍依赖上游。矩阵「透明」用例以格式+尺寸+容器为准，
  并在有上游 alpha 时保留。

### 4. 非目标

- 不引入 `-2k/-4k` 模型别名超分；大画布清晰度靠显式 `size` + 本地渐进放大（暂不接 Real-ESRGAN）。
- 不改变 API-key 官方 Images 硬 `size` 语义（仅 OAuth/Codex 路径做 ceil+本地精确画布）。
- 不把色键去背景当作默认透明实现。


## 2026-10-16：图片凭据保留与原生响应契约修复

审批依据：本会话用户在收到「定位 fidelity/revised_prompt 路径，避免不必要重编码」建议后明确回复：
「同意，1 已经让 tokensea 去查了。2 和 3 请继续排查、核实和修复。」
该授权覆盖本节实现；合并和部署仍走项目出口门禁。本节状态为实现待合并，不表示已上线。

- GPT Image 的原生 API-key JSON / SSE 和 Codex Direct 响应复用字段清理 owner；
  移除 `revised_prompt`，保留图像字节、usage、provider 扩展字段。DALL-E 的该字段保持原义。
- `gpt-image-2` 的 `input_fidelity` 继续在统一请求解析处拒绝；覆盖 multipart edits，
  不扩大到 `gpt-image-1.5`。本轮线上 JSON 与 multipart 探针都已返回对应 400。
- SSE 按完整帧处理，支持多行 data，保留 event/id/retry/comment 和 `[DONE]`；
  读取阶段按现有 upstream response read limit 限制单帧；无换行的超长行及客户端断开后的读取同样受限。
- 不新增凭据签发系统。原样字节测试仅证明元数据不被网关删除，不证明上游签名存在或可信。
- Web impact: none；没有 UI 工件。验证是服务单元/集成测试及生产 HTTP 拒绝探针，不称 UI e2e。

### 验收与风险覆盖

| 场景 | 行为证据 |
| --- | --- |
| quality-only / 同格式同尺寸 | `TestImagesPostprocessPreservesUpstreamBytesForGenerationQuality`：带 caBX 的 PNG 字节完全一致 |
| 必需格式/尺寸/压缩变换 | `TestImagesPostprocessStillTransformsRequestedFormatSizeAndCompression`：正确格式与像素、无旧凭据冒充 |
| 原生 JSON / URL 回填 | `TestOpenAIGatewayServiceForwardImages_APIKeyBackfillsB64JSONFromURL`、`TestImagesNativeResponseSanitizerPreservesOtherModelsAndPayloads` |
| 原生流、多行 data、DALL-E 回归 | `TestImagesNativeStreamingResponseStripsMultilineRevisedPrompt` |
| SSE 超长行、帧边界、断开后的用量统计 | `TestImagesNativeStreamFrameLimitBoundsUpstreamReads`、`TestImagesNativeStreamFrameBudgetResetsAndAllowsExactLimit` |
| multipart fidelity 正负向 | `TestParseOpenAIImagesRequest_MultipartInputFidelity` |

逻辑与回归风险由以上测试覆盖；鉴权、租户与计费策略不变；运行时风险为 SSE 单帧缓冲，
以既有读限制拒绝超大帧。原报告具体失败请求尚未取得服务端关联 ID，不能据此宣称客户全链路已通过。

## Owners

| 关注点 | Owner |
| --- | --- |
| 尺寸校验 / ceil-16 / pad / 本地渐进放大 | `openai_images_strict_canvas_tk.go` + `openai_images_upscale_tk.go` |
| style → prompt Style guidance | `openai_images_style_tk.go` |
| Responses 驱动主模型 fallback | `openai_images_main_model_tk.go` |
| format + compression coerce | `openai_images_output_format_tk.go` |
| Direct / multi / Responses 接线 | `openai_images_direct.go`（`usesCodexDirectImages=false`）/ `*_codex_direct_multi_tk.go` / `openai_images_responses.go` |
| Studio/Quickstart GPT size 芯片 | 生成自 Go `openAIImagesKnownSizeTable` → `frontend/src/constants/gptImageSizes.generated.tk.ts`（`studioMediaPresentations.tk.ts` re-export）+ `imageGeneration.tk.ts` |
| 回归 | `openai_images_strict_canvas_tk_test.go`、`openai_images_main_model_tk_test.go` 等 |

## Validation

- 单元：ceil-16、分隔符归一、单边/像素上下限 400、pad 精确画布、jpeg/webp 魔数、compression 体积差、style→Style guidance、main-model fallback 同账号重试。
- edge-us3 OAuth：复跑客户 01–10 矩阵（`gpt-image-2` / `2.5-sunburst` / `2.5-flare`）。
- 探针：`ops/stage0/probe_openai_upstream_image.sh`；矩阵
  `ops/stage0/probe_openai_image_fidelity_matrix.sh`；AR/07/09
  `ops/stage0/probe_openai_image_ar_07_09.sh`。
