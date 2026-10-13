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

- `reasoning.effort` 可配。未设置 `SUB2API_IMAGES_REASONING_EFFORT` 时默认 `medium`（对齐当前行为，也对齐 Velmo 有增强时的档）。Velmo 只在 dict 的 `Text (verbatim)` 字段有内容时升到 `high`；自由字符串本身不会升档。TokenKey 只把短的成对 ASCII `"label"`（1–48 个字符、非纯空白、开引号前不是数字）当作这个字段的替身并升到 `high`。中文引号、书名号、不成对引号、英寸记号（`12"`、`12" to 24"`）保持 `medium`。成对的短中文引号仍会在脚手架里加一行 `Text (verbatim)`，但不升 effort。
- 显式环境变量覆盖自动档。合法值 `none` / `minimal` / `low` / `medium` / `high` / `xhigh`。别名 `x-high`、`extra_high`、`extrahigh` 以及 Velmo 模式名 `max` 归一成 `xhigh`（Codex 允许列表里没有 effort `max`）。`auto` 和其他无法识别的值忽略，回退自动档。留空（compose 默认空字符串）等同未设置。
- 结构化观感脚手架写在 **用户 `input_text`**，使用 Velmo 的标签名：`Primary request`、`Composition/framing`、`Lighting/mood`、`Materials/textures`、`Constraints`、`Avoid`。生成请求的构图/光线/材质是条件句（请求没写才补，不覆盖已写明的构图或媒介）。编辑请求改为保留输入图的构图、光线和表面，只改请求点名的部分，不再套用「清晰主体 / 新光线」那套生成提示。有短引号跨度时加一行 `Text (verbatim)`。
- `instructions` 保持空字符串。

不移植：

- 不把 Velmo 的 labeled-spec preamble 放进 `instructions`。那段文字的作用是让主模型改写 prompt，和 codex2api 的空 `instructions` 相反。
- 不把默认 effort 降到 `none`（Velmo 对纯字符串 `auto` 会这样做），否则观感会比现在的 `medium` 更差。
- 仍传 `action`、`quality`、mask。不删 `summary=auto`、`parallel_tool_calls`、`include`。
- `input_fidelity` 只在 Codex 软模型上省略，见下方 2026-10-13 修订。API-key Images 与其它工具模型继续透传。
- 不接色键、skill 文件、batch/chain、`text.verbosity`。
- 不接 Real-ESRGAN 或任何放大模型。
- 不改 API-key Images 路由，也不把脚手架套到 API-key prompt 上。
- 不承诺像素、`4k` / `8k` 或 quality 档。上游仍可能把尺寸软化到约 1K；硬画布仍走本地 pad。

`SUB2API_IMAGES_PROMPT_SCAFFOLD=off|false|0|no` 关掉脚手架，effort 逻辑仍在。

Stage0 蓝绿模板 `ops/stage0/deploy_via_ssm_bluegreen.sh` 在 blue 和 green 的 `environment` 里保留 `${SUB2API_IMAGES_REASONING_EFFORT:-}` 与 `${SUB2API_IMAGES_PROMPT_SCAFFOLD:-}`。`docker compose --env-file .env` 创建容器时读取宿主机 `.env`。键缺失或值为空时，容器里对应值是空字符串：effort 走自动档，脚手架保持开启（与 `deploy/docker-compose.yml` 里脚手架默认 `on` 的应用行为相同）。部署器不 `env_set` / `env_default` 这两个键，也不把它们放进 SSM 启动前缀，避免部署机上的空值盖住宿主机已有配置。

2026-10-13 edge-uk1 A/B（medium / high / xhigh，2048 与一发 4K）没有稳定观感增益。默认档保持 `medium`（短 ASCII `"label"` 仍升到 `high`）。不要为了这次观感把默认改成 high 或 xhigh。

## 2026-10-13 edge-uk1：可观测与 input_fidelity

证据：blue `1.8.289`，`model=gpt-image-2`，账号落到 OAuth Responses，上游软模型 `gpt-image-2-codex`。

### 可观测

当时 `usage_logs.reasoning_effort` 与 `requested_reasoning_effort` 全是 NULL；`main_model` 与脚手架没有列，路由日志也没有这三项。

OAuth Responses 生图（`forwardOpenAIImagesOAuth` 的非 Direct 分支）现在：

- 结构化事件 `openai_images.oauth_dispatch`，以及同一条 `[OpenAI] Images request routing` 文本，带上本次实际的 `reasoning_effort`、`effort_source`（`env` 或 `auto`）、`main_model`、`main_model_fallbacks`（当前驱动之后仍可重试的链）、`scaffold_on`、`tool_model`、`input_fidelity_omitted`。
- `OpenAIForwardResult.ReasoningEffort` 写成即将发往上游的 `reasoning.effort`。`RecordUsage` 把它落入 `usage_logs.reasoning_effort`；Images API 没有客户端 effort 字段，`requested_reasoning_effort` 由既有 coalesce 记成同一个值。
- `main_model` 与 `scaffold_on` 没有 usage_logs 列，不新增 schema，只出现在上述日志。
- API-key Images 不走这套 Responses 驱动，不写 effort 列。

分组若为该模型配置了 reasoning effort 倍率，OAuth 生图会开始按这个实际档计费。未配置时倍率仍是 1，默认价格不变。

### input_fidelity

同一轮 edits：`input_fidelity=high` 与 `low` 都是 HTTP 400，`invalid_input_fidelity_model`，模型 `gpt-image-2-codex` 不支持该字段。省略字段则 200。

`buildOpenAIImagesResponsesRequestWithMainModel` 在工具模型是 `gpt-image-2`、`gpt-image-2-codex`，或以 `gpt-image-2-codex-` 开头时不写 `tools[].input_fidelity`。`gpt-image-2` 是上游软成 `gpt-image-2-codex` 的工具名。`gpt-image-1`、`gpt-image-1.5`、`gpt-image-2.5-*` 以及 API-key Images 原文透传继续保留该字段。Direct Images 仍关闭，不在本修订里重新打开。

## Owners

| 关注点 | Owner |
| --- | --- |
| effort 解析与 prompt 脚手架 | `backend/internal/service/openai_images_perceived_quality_tk.go` |
| OAuth 生图 dispatch 日志、usage effort、Codex `input_fidelity` 省略 | `backend/internal/service/openai_images_oauth_dispatch_tk.go` |
| OAuth Responses 接线（空 instructions、style 之前、AR marker 仍在末尾） | `backend/internal/service/openai_images_responses.go` |
| Stage0 蓝绿宿主机 `.env` 透传 | `ops/stage0/deploy_via_ssm_bluegreen.sh` |
| 像素 / 格式 | `openai_images_strict_canvas_tk.go`、`openai_images_upscale_tk.go`（本变更不改） |
