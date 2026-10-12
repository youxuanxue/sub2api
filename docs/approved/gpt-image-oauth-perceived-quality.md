---
title: GPT Image OAuth perceived quality (reasoning effort + prompt scaffold)
status: approved
approved_by: "user (2026-10-12 cloud task: port VelmoAI/codex-imagen reasoning_effort and structured prompt cues; no upscale; no API-key routing)"
approved_at: 2026-10-12
created: 2026-10-12
authors: [cursor]
risk: medium
---

# GPT Image OAuth perceived quality

> 状态：approved。范围只覆盖 OAuth / Setup-Token 的 Codex Responses 生图请求
> （`buildOpenAIImagesResponsesRequestWithMainModel`）。
> 这是观感提示，不是分辨率契约。像素仍由
> [strict canvas](gpt-image-strict-canvas-postprocess.md) 的本地 pad + CatmullRom 负责。

## 研究（源码，不是 README）

对照仓库：

- [VelmoAI/codex-imagen](https://github.com/VelmoAI/codex-imagen) `src/codex_imagen/_prompts.py`、`core.py`、`_bridge.py`
- 真正拼 Responses JSON 的依赖 [smturtle2/codex-image-gen](https://github.com/smturtle2/codex-image-gen) `src/codex_image_gen/_client.py` `_build_payload`

Velmo 自己不写 HTTP body。`_bridge.generate` 把 `prompt`、`instructions`、`reasoning_effort` 交给 `codex_image_gen.generate_image`。载荷字段以 `_build_payload` 为准。

| 位置 | Velmo / codex-image-gen 的实际做法 |
| --- | --- |
| `reasoning.effort` | 仅在调用方传入时写入。允许 `none` / `minimal` / `low` / `medium` / `high` / `xhigh`。Velmo 模式映射：`raw→none`，`medium→medium`，`high→high`，`max→xhigh`。`auto`：纯字符串且无 skill → `raw`；有引号 verbatim → `high`；有 skill / 透明底 / extra instructions → `medium`。 |
| `reasoning.summary` | 仅在传入时写入。Velmo 不默认带 `auto`。 |
| `instructions` | 库默认是一句 “Use the image_generation tool…”。Velmo `raw` 换成 anti-refinement 全文；`medium` / `high` / `max` 换成同一段 Codex labeled-spec preamble（让主模型把用户句子改写成 14 字段后再调工具）。三种质量模式的 instruction 正文相同，差别只在 effort。 |
| 用户 `input` | 字符串 prompt **不**预拆字段，原样进 `input_text`。只有 dict prompt 才在用户消息里渲染 `Primary request:` 等标签。 |
| 工具 | `type=image_generation`，`model=gpt-image-2`，`size`，`output_format`，`background`。不传 `quality`、`action`、`n`。`background=transparent` 在库内直接拒绝。 |
| 非目标（Velmo 自己也标了） | 不 upscale、不 outpaint。透明底走色键，不走上游 `transparent`。 |

TokenKey 现状（改前）：`instructions` 固定 `""`（codex2api，避免主模型改写用户 prompt），`reasoning.effort` 写死 `medium`，`summary=auto`，工具仍带 `action` / `quality` / `input_fidelity`。`usesCodexDirectImages=false`，OAuth 只走 Responses。

## 移植 / 不移植

移植：

- `reasoning.effort` 可配。未设置 `SUB2API_IMAGES_REASONING_EFFORT` 时：普通 prompt 为 `medium`（对齐当前行为，也对齐 Velmo 有增强时的档）；用户 prompt 含引号或中文书名号时升到 `high`（对齐 Velmo verbatim → high）。显式环境变量覆盖自动档，合法值与库一致。非法值忽略，回退自动档。
- 结构化观感脚手架写在 **用户 `input_text`**，使用 Velmo 的标签名：`Primary request`、`Composition/framing`、`Lighting/mood`、`Materials/textures`、`Constraints`、`Avoid`；有引号时加一行 `Text (verbatim)`。编辑请求的 Constraints 要求保留未点名区域。
- `instructions` 保持空字符串。

不移植：

- 不把 Velmo 的 labeled-spec preamble 放进 `instructions`。那段文字的作用是让主模型改写 prompt，和 codex2api 的空 `instructions` 相反。
- 不把默认 effort 降到 `none`（Velmo 对纯字符串 `auto` 会这样做），否则观感会比现在的 `medium` 更差。
- 不改工具 schema：仍传 `action`、`quality`、`input_fidelity`、mask。不删 `summary=auto`、`parallel_tool_calls`、`include`。
- 不接色键、skill 文件、batch/chain、`text.verbosity`。
- 不接 Real-ESRGAN 或任何放大模型。
- 不改 API-key Images 路由，也不把脚手架套到 API-key prompt 上。
- 不承诺像素、`4k` / `8k` 或 quality 档。上游仍可能把尺寸软化到约 1K；硬画布仍走本地 pad。

`SUB2API_IMAGES_PROMPT_SCAFFOLD=off|false|0|no` 关掉脚手架，effort 逻辑仍在。

## Owners

| 关注点 | Owner |
| --- | --- |
| effort 解析与 prompt 脚手架 | `backend/internal/service/openai_images_perceived_quality_tk.go` |
| OAuth Responses 接线（空 instructions、style 之前、AR marker 仍在末尾） | `backend/internal/service/openai_images_responses.go` |
| 像素 / 格式 | `openai_images_strict_canvas_tk.go`、`openai_images_upscale_tk.go`（本变更不改） |
