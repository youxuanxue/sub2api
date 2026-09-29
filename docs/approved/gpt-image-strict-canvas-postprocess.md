---
title: GPT Image strict canvas + format post-process (codex2api parity)
status: pending
approved_by: pending
approved_at: pending
created: 2026-09-29
authors: [cursor]
risk: high
related_prs: []
---

# GPT Image strict canvas + format post-process

> 状态：`pending`（待人工审批）。对齐 [codex2api](https://github.com/james-6-23/codex2api)
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

结论：format/background 在 Responses 路径明显好于 Direct；**字面 size 仍需本地 pad/cover**；
quality/compression 仍需本地体积可区分后处理。`3840x2176` 继续本地 400（总像素超 `8294400`）。

## 决策（已确认）

**继续走本地精确画布**，与 codex2api PR #519 / Image Studio `strict_size` 同路径：

1. tools 参数形状已对齐；上游仍把 `tools[].size` 软化为 `auto` 并返回漂移像素 —— **不是传参错误**。
2. 显式 `size=WxH`：上游发 ceil-16 合法尺寸；响应经本地 **pad（默认）** 落到请求画布。
3. 显式 `output_format` / `quality` / `output_compression`：本地 coerce 兜底（Responses 上 jpeg/webp 多数已原生兑现）。
4. 非目标：不把字面 size 押在上游 Responses 行为上；不引入 `-2k/-4k` 超分别名（可后续独立 PR）。
5. 废弃 `tk_image_contract`：本地精确画布/format 后处理为默认行为；字段若传入则忽略并在转发前剥离。
6. Studio/Quickstart：GPT Image 芯片发送 `size=WxH`（`GPT_IMAGE_SIZES`），与本契约共用尺寸表。

## 契约（审批后生效）

### 0. 上游传输（codex2api 对齐）

OAuth / Setup-Token 生图**一律**走：

`POST https://chatgpt.com/backend-api/codex/responses`

请求体为 Responses + `tools[{type:image_generation,...}]` + `tool_choice=image_generation`，
驱动模型默认 `gpt-5.6-luna`，图像模型在 `tools[0].model`。
**不再**默认调用 `/backend-api/codex/images/generations` Direct 端点。

### 1. 尺寸准入（OAuth Responses 生图）

对显式 `size=WIDTHxHEIGHT`（非 `auto`）：

- 宽高为正整数；总像素 ≤ `8294400`；长边/短边 ≤ 3。
- 否则 **400** `invalid_request_error`（对齐超限用例，不等待上游）。
- 发往上游的 size：各边 **向上取整到 16 的倍数**（例：`1920x1080` → `1920x1088`）。
- 返回给客户的画布：本地 **pad（默认）** 或可选 `cover` 精确还原请求的 `WxH`。
- 响应 `data[].size` / 顶层 `size` = **最终画布**；usage 计费档仍按最终像素 reconcile。

### 2. `output_format` 后处理

显式 `png` / `jpeg` / `webp`：

- 上游容器不符时，网关解码后重编码；魔数必须与声明一致（fail-closed）。
- `webp` 使用纯 Go lossless 编码器（`CGO_ENABLED=0` 兼容）。
- `output_compression`（0–100）与 `quality` 映射到 JPEG quality / WebP
  compression effort，使质量/压缩档在文件体积上可区分（客户判定阈值 >10%）。

### 3. `background`

- 继续透传上游 `background`。
- 本地 pad 时：`transparent` + png/webp → 透明边；`opaque` / jpeg → 不透明垫底。
- **不**承诺主体抠图；真 alpha 仍依赖上游。矩阵「透明」用例以格式+尺寸+容器为准，
  并在有上游 alpha 时保留。

### 4. 非目标

- 不引入 `-2k/-4k` 模型别名超分（可后续独立 PR）。
- 不改变 API-key 官方 Images 硬 `size` 语义（仅 OAuth/Codex 路径做 ceil+本地精确画布）。
- 不把色键去背景当作默认透明实现。

## Owners

| 关注点 | Owner |
| --- | --- |
| 尺寸校验 / ceil-16 / pad-cover | `openai_images_strict_canvas_tk.go` |
| format + compression coerce | `openai_images_output_format_tk.go` |
| Direct / multi / Responses 接线 | `openai_images_direct.go`（`usesCodexDirectImages=false`）/ `*_codex_direct_multi_tk.go` / `openai_images_responses.go` |
| Studio/Quickstart GPT size 芯片 | `frontend/src/constants/studioMediaPresentations.tk.ts`（`GPT_IMAGE_SIZES`）+ `imageGeneration.tk.ts` |
| 回归 | `openai_images_strict_canvas_tk_test.go` 等 |

## Validation

- 单元：ceil-16、超限 400、pad 精确画布、jpeg/webp 魔数、compression 体积差。
- edge-us3 OAuth：复跑客户 01–10 矩阵（`gpt-image-2` / `2.5-sunburst` / `2.5-flare`）。
- 探针：`ops/stage0/probe_openai_upstream_image.sh`；矩阵
  `ops/stage0/probe_openai_image_fidelity_matrix.sh`；AR/07/09
  `ops/stage0/probe_openai_image_ar_07_09.sh`。
