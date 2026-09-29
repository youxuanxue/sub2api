---
title: Shared image generation controls in Studio and Quickstart
status: approved
approved_by: "用户（本会话：同意。请完整实现。）"
authors: [codex]
---

# 生图参数与接入示例

## Contract

沿用现有 Studio「图片」布局和 Quickstart cURL/Python、模型及密钥选择器。
本会话已批准先前设计；此实现不部署、不发起真实付费生图。

现有 `/me/api-keys/:id/capabilities` 为每个模型增加可选 `image_generation`，
每个条目描述独立获准的请求配置：`endpoint`、`aspect_ratios`、`counts`、
`input_image`、`soft_aspect_ratio`。配置来自已通过授权、候选路径、计费归属选择的
账号/分组；参数通过原有 `CandidateRequest`/`protocolrouter.Plan` 与 Images
输入校验、账号能力判断复核。只做元数据计算，不请求供应商、不预留余额/并发。
配置不含账号、凭据或上游地址，不将不同路径的独立选项任意合并。

Gemini Universal 示例走 Chat 的 `extra_body.google.image_config.aspect_ratio`；
已支持的 Gemini 专用客户端保留 native `generateContent` 示例。
Gemini Web 的比例限制由原有协议路由裁决。GPT Image Studio/Quickstart 默认发送
顶层 `size=WxH`（`GPT_IMAGE_SIZES`，由网关 Go
`openAIImagesKnownSizeTable` 生成，与
`openAIImagesAspectRatioFromSize` 同源）；网关本地精确画布兑现最终像素，
不需要 `tk_image_contract`。仅发 `aspect_ratio` 仍是合法 API 构图偏好。
硬 `size` 与软 `aspect_ratio` 同传时以 size 为准。Seedream/Wan/Imagen 的既有
size 映射保留。未知能力只构造最小请求；不以计费字段决定路由、输入图支持、
数量或高级选项。

模型/密钥切换、历史复用、导航恢复与发送复用同一个参数归一化函数。
兼容选项保留，不支持的比例/数量退回合法默认值，不支持的输入图清除。
Quickstart 的示例与 Studio/BakeOff 共用请求构造；Python 保存 URL、base64、
Gemini chat/native 返回图片，cURL 保存原始响应。生图提示词不复用 Hello/ping。

生图模型的「验证密钥」只调用鉴权模型列表，明确显示生图未验证。
实际生图由用户在 Studio 明确点击。Studio 链接包含模型、合法比例/数量和密钥 ID，
不含密钥内容、提示词或输入图；登录回跳不自动生成。匿名预览使用占位密钥与
示例模型，不发现私有能力，不创建密钥，也不生成。

## Owners

| Responsibility | Owner |
| --- | --- |
| Authorized image request profile projection | See Candidate Eligibility SSOT Implementation/Owners |
| Frontend profile selection, normalization, request and Studio link | `frontend/src/utils/imageGeneration.tk.ts` |
| GPT Image exact-canvas size chips (WxH; Go SSOT → generated FE) | Go `openAIImagesKnownSizeTable` via `cmd/gpt-image-sizes-ssot` → `frontend/src/constants/gptImageSizes.generated.tk.ts` (re-exported by `studioMediaPresentations.tk.ts`) |
| Shared parameter controls | `frontend/src/components/keys/ImageGenerationParameters.vue` |
| cURL/Python serialization and saving results | `frontend/src/utils/imageGenerationExamples.tk.ts` |
| Gateway transport | existing `frontend/src/api/playground.ts` |
| Key verification state | existing `frontend/src/composables/useTkUseKey.ts` |

候选资格及公开接入其他 owner 继续遵循
[candidate-eligibility-ssot.md](candidate-eligibility-ssot.md) 和
[public-quickstart-registration-offer.md](public-quickstart-registration-offer.md)。

## Validation

验收与可运行证据登记在 US-058。测试覆盖 Web/混合供给、Direct 授权边界、
GPT 比例、未知能力、参数恢复、示例请求与结果保存、匿名访问和真实 UI 导航。
浏览器使用确定性 API 夹具，不能代替真实供应商出图与最终尺寸验收。
