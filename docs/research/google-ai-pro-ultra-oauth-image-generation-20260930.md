# Google AI Pro / Ultra OAuth 生图方案调研与推荐

调研日期：2026-09-30。范围：用户提供的开源日报项目；重点检查 Gemini 图片输出、个人订阅 OAuth、对外接口及参数转换。本文是选型材料，不是上线批准或实号可用性报告。

**实测补充（同日）：** 已在 edge 的 Pro / Ultra OAuth 账号上直连 `daily-cloudcode-pa.googleapis.com`，成功生成并完整解码 1024×1024 JPEG；Pro 的流式调用也成功。同一 Ultra 账号同为 `generateContent`，prod host 返回 429、daily 成功。详见[实测记录、请求参数与图片样例](google-ai-pro-ultra-oauth-image-live-probe-20260930.md)。同日随后已完成[公网文本与生图全栈验证](antigravity-fullstack-validation-20260930.md)：四种生成协议通过，并发现“省略尺寸实际 1K、默认计费 2K”的缺口；本地修复通过测试，尚未部署。

**后续对比：** [daily/prod 历史 PR 与社区经验](antigravity-endpoint-history-20260930.md)解释当前按档位选址的来源；[Antigravity 与 Gemini Web 参数实测](antigravity-vs-gemini-web-image-parameters-20260930.md)已补充五种共同比例、2K/4K、21:9、参考图编辑和多候选限制。

## 1. 推荐结论

**个人 Google AI Pro / Ultra 订阅生图，优先验证 Antigravity OAuth → Gemini 原生接口这条路径。现有 TokenKey 优先复用自己的 Antigravity 通道；独立验证选 CLIProxyAPI；明确需要 OpenAI Images 接口时，TokenKey 已有本地适配实现待发布；独立网关可评估 OmniRoute。**

依据是这些实现已有 Antigravity 登录、项目发现、图片模型及 `v1internal` 图片请求处理；同日补测已验证所测 Pro / Ultra 的 daily 图片路径，但不能推广为任意账号、模型和分辨率都可用。

| 使用场景 | 推荐 | 原因与前置条件 |
|---|---|---|
| 现有 TokenKey 提供 OAuth 生图 | 复用本地 Antigravity 原生 Gemini 路径 | 本地已有 `image_gen`、图片输出模态补全及统一调度；公网四协议已验证；显式传 2K，默认尺寸修复待部署，无需另建账号池 |
| 独立判断账号是否能通过 OAuth 生图 | CLIProxyAPI | 原生 Gemini 请求保留图片参数，较容易隔离协议转换因素；只绑定 Antigravity 账号/唯一模型别名 |
| 客户端只会调用 `/v1/images/generations` | TokenKey 本地适配（待部署）；独立网关可选 OmniRoute | TokenKey 复用 AG 原生 Gemini，支持单张 b64_json、1K/2K/4K 和比例；契约与本地测试见[适配说明](../approved/antigravity-images-generations-adapter.md)。OmniRoute 仍需单独验收空响应和参数 |
| 个人编码工具偶尔生成图片 | opencodex | 有 Antigravity 图片兜底，但固定模型、单图、参数较少，并受 OpenAI 凭证优先路径影响 |
| 要官方开发者 API、清晰项目计费 | Gemini API / Vertex，配 LiteLLM 或 Bifrost | 与个人订阅权益分开管理；OAuth / ADC 是鉴权方式，不能据此认定调用消耗 Pro / Ultra 额度 |

**不建议**直接把 Gemini CLI 的 `google_one` / `code_assist` OAuth 当作 Antigravity 替代品，也不建议因网关模型列表出现 `image` 就认定账号可生图。Google 官方 Antigravity 套餐说明给出了 Pro / Ultra 不同额度等级，但没有给出可用于本方案承诺的固定“每天多少张图片”。[O2]、[O3]

## 2. 证据边界

- 核心项目检查了实际登录、请求构造、路由和响应处理源码；其余项目以 README 初筛，不能将“未确认”解读为“不支持”。附录列出全部日报项目及固定提交链接。
- 核心对比固定到抓取时的 Git SHA，避免后续 `main` 变化覆盖结论。官方网页为本次访问版本，不是固定提交。
- 本地 TokenKey 基线为 `a38cf0ff830f8d9e607d77a8f32e11dccc27bf4e`；与下载的上游 Sub2API 分开比较。
- 初稿阶段仅做源码研究；随后按用户授权补做了 edge 实号上游直测，结果单列于上面的实测报告。本文件的跨项目比较仍以源码为依据，未逐一部署这些项目；公网模板、归因与计费的后续实测范围见全栈报告；token 刷新生命周期仍未专项验收。
- 日报中的星数及增长数据不参与能力评级，本文未重新核验这些统计。

## 3. 先区分账号产品、OAuth 和上游

| 路线 | 凭据 / OAuth 选项 | 上游及计费含义 | 对本目标的判断 |
|---|---|---|---|
| Antigravity | Google 授权码、access / refresh token，配套 Antigravity OAuth client，发现或 onboarding 项目 | `cloudcode-pa.googleapis.com` / `daily-cloudcode-pa.googleapis.com` 的 `/v1internal:*` | 个人 Pro / Ultra 优先验证路线；项目、模型权限和额度必须逐账号核实 |
| Gemini CLI / Code Assist / `google_one` | 另一套内置 OAuth client；同样可能取得 Google Bearer token | Code Assist 通道；账号标签和 tier 不构成图片模型权限 | 不与 Antigravity token/client/模型权益混用 |
| Gemini API / AI Studio | API key；也有自建 Google OAuth client 选项 | `generativelanguage.googleapis.com`，开发者项目和 Cloud Billing | 可作为独立预算的官方 API 方案；不是仅凭订阅自动获得的 API 图片额度 |
| Vertex AI | ADC、服务账号或用户 OAuth，加 Cloud project / location / IAM | Vertex AI 项目 API | 云项目路线；“支持 OAuth”不等于“支持个人订阅转换” |
| Gemini 个人网页版 | 浏览器 Cookie / Web session | Gemini Web 协议 | 本地另有通道，但不属于 OAuth 方案 |
| Gemini Business | Business Cookie、会话派生 JWT | `business.gemini.google` | 与个人 Pro / Ultra 产品及凭据均不同 |

Antigravity 实现常用 scope：`cloud-platform`、`userinfo.email`、`userinfo.profile`、`cclog`、`experimentsandconfigs`（完整 scope 使用 Google 的 `https://www.googleapis.com/auth/` 前缀）。这些 scope 不单独授予某个付费图片模型的使用权。[S1]、[S3]、[S5]

上游 Sub2API 中，`code_assist` / `google_one` 使用内置 Gemini CLI client，scope 为 `cloud-platform`、`userinfo.email`、`userinfo.profile`；`ai_studio` 使用自定义 client，scope 包括 `cloud-platform` 与 `generative-language.retriever`。`google_one` 的实际实现也会进行项目发现，不能依据旧注释理解为完全不需要项目。其 `GoogleOneModels` 保守列表不含图片模型；该列表是项目策略证据，不是 Google 官方权益公告。[S5]、[O4]

## 4. 调用端口、路由和授权回调

下面的端口是仓库默认值或示例部署值，可由配置、Docker 映射、反向代理改变。**业务监听端口、登录回调端口、Google 上游 HTTPS 443 是三回事。** 客户端传网关发放的 key；Google refresh token 留在服务端。

| 项目 | 本地业务端口 | 与 Gemini 生图相关的入口 | OAuth 回调 / 登录差异 |
|---|---:|---|---|
| CLIProxyAPI | `8317` | `/v1beta/models/{model}:generateContent`；流式 `:streamGenerateContent?alt=sse` | `-antigravity-login`；默认 `http://localhost:51121/oauth-callback`。本版不要照搬旧教程假定 Gemini CLI `--login` 仍为内置入口 |
| OmniRoute | `20128` | `/v1/images/generations`；模型明确指定 `antigravity/gemini-3.1-flash-image` | Dashboard Google OAuth / `/callback`；远端可用 `omniroute login antigravity` 辅助。回调地址取决于实际部署，不套用其他项目的 51121 |
| opencodex | `10100` | `/v1/images/generations` 的 Antigravity 兜底 | `ocx login google-antigravity`；PKCE；首选 `http://127.0.0.1:51121/callback` |
| Sub2API / TokenKey | `8080` | `/antigravity/v1beta/models/{model}:generateContent`；通用 `/v1beta/...` 还受分组、调度配置影响 | Antigravity 默认 `http://localhost:8085/callback`，支持复制回调信息；Gemini CLI 为 `https://codeassist.google.com/authcode`；自定义 AI Studio 为 `http://localhost:1455/auth/callback` |
| LiteLLM | `4000` | OpenAI Images 适配器，按配置选择 Gemini 图片模型或 Imagen | Gemini API key / Vertex 项目凭据；未发现本次目标所需的个人 Antigravity 登录链路 |
| Bifrost | `8080` | OpenAI 集成前缀 `/openai`；Gemini 集成前缀 `/genai`，具体业务路径按其 API 配置 | Gemini key / Vertex 配置；未确认个人订阅 OAuth |
| new-api | `3000` | Gemini 原生 `/v1beta/models/...`；Images 转换器有 Imagen 限制，见下节 | Gemini key / Vertex 项目凭据；未确认个人订阅 OAuth |
| gemini-balance | `8000` | 原生 `(/gemini)/v1beta/...`；兼容聊天 `(/hf)/v1/...` | Gemini API key 池 |
| gemini-business2api | `7860` | `/v1/images/generations`、`/v1/images/edits`、聊天 | Business Cookie，不是 Google refresh-token OAuth |
| claude-relay-service | `3000` | `/gemini` 作为 Gemini 转发 base | 有 Gemini CLI / Antigravity 相关接入，但本次未确认完整图片专用链路 |
| antigravity-claude-proxy | `8080` | `/v1/messages` | Antigravity OAuth / 导入本地凭据；回调端口 `51121` 可配置 |
| james-6-23/codex2api | `2004`（文档示例） | 实验性原生 `/v1beta/models/...` | 已有 Antigravity OAuth；值得补验，尚不作为已验证图片方案 |

CLIProxyAPI 与 opencodex 的 OAuth 回调常占用同一个端口，且路径不同；顺序完成登录或按各工具支持的配置调整。不要把它们的回调 URL 互换。CLIProxyAPI 的本次 `BuildAuthURL` 实现未设置 PKCE 参数；opencodex 明确设置 PKCE，因此不应笼统称所有项目使用相同 OAuth 流程。[S1]、[S3]

## 5. 核心方案的源码差异

### 5.1 CLIProxyAPI：优先用于 OAuth 原生验证

原生 Gemini 转换器把调用方 JSON 包在 `request` 中；执行器补 `project`、模型与 `userAgent`。图片模型默认选 `requestType: image_gen`，请求 ID 形如 `image_gen/<毫秒>/<UUID>/12`。`generationConfig` 走原生路径，适合验证 `responseModalities` 和 `imageConfig`。[S1]

这不是逐字节透传：执行器会删除 `request.safetySettings`，并规范部分工具字段。`gemini-3.1-flash-image` 的非流式实现还会走流式结果聚合分支，因此客户端非流式不意味着上游一定调用 `:generateContent`。

本版确有 Images 路由，但相关媒体路径主要覆盖 Codex / xAI / OpenAI-compatible。**不能仅凭同一个服务存在 `/v1/images/generations`，就推定它能把 OpenAI Images 转成 Antigravity Gemini 生图。** 本次推荐原生 Gemini 入口。

### 5.2 OmniRoute：Images 转换明确，但需验证边界

Antigravity image registry 明确指向 `daily-cloudcode-pa.googleapis.com/v1internal:generateContent`，实际模型 `gemini-3.1-flash-image`；另有 preview 名称到该模型的别名。[S2]

| 调用方字段 | Antigravity 转换行为 |
|---|---|
| `model` | 使用 `antigravity/` 前缀选择 provider，再取实际模型 |
| `prompt` | 转成单个用户文本 part |
| `n` | 有限正数经 `Math.floor` 写入 `generationConfig.candidateCount`，否则回落 1；调用方宜只传正整数，这不证明上游接受多图 |
| `aspect_ratio` / `size` | 显式比例优先；否则由尺寸归一化得到 `imageConfig.aspectRatio` |
| `image_size` | 归一化为 `1K` / `2K` / `4K`；非法值钳到 `1K`，未传则不写入 |
| `responseModalities` | 该函数注释提到 `TEXT, IMAGE`，但实际构造体没有设置；以可执行代码为准 |
| 参考图片 / 编辑 | 该 Antigravity handler 只构造文本 parts；不能推定通用字段会被带到上游 |

请求外层使用 `requestType: image_gen`，必须有 OAuth 关联的 project。响应从候选 `inlineData` 变成 OpenAI `data[].b64_json`；该 handler 在没有图片时仍可能返回空 `data`，调用方需验证真实图像，不能只检查 HTTP 200。image registry 中列出的尺寸也较保守，不能代替实际参数能力测试。

### 5.3 opencodex：单图、固定模型的兜底

当 Images 路由没有可用 OpenAI 凭据、且 Antigravity provider 启用时，转入 CCA 图片路径。**不是任意请求写一个 Gemini 模型名就必定选中 Antigravity。**[S3]

- 实际模型固定为 `gemini-3.1-flash-image`，不由请求 `model` 自由选择。
- 仅 generations；不提供该兜底的 edits；`n` 必须为 1。
- 上游只带 prompt，并设置 `responseModalities: ["TEXT", "IMAGE"]`；调用方的 size、quality、imageConfig 没有用于构造这一请求。
- 固定使用 daily Cloud Code endpoint；外层仍为 `requestType: agent` / `agent-UUID`，与前两家的 `image_gen` 不同。
- 实现包含 Base64 / 图片魔数验证、空结果及安全阻断错误处理，以及 100 MiB 响应上限和默认 300 秒总超时（可配置）。

因此它适合“编码时偶尔画一张”，不适合作为承诺比例、分辨率、编辑、多图的统一图片服务。

### 5.4 Sub2API 上游与本地 TokenKey：必须分开看

| 层面 | 上游 `a60a29549f48` | 本地 TokenKey `a38cf0ff830f` |
|---|---|---|
| Gemini 原生请求 | 将原始 JSON 放入 wrapper，保留原生图片参数 | 同样走原生请求路径，另有本地 wire 处理 |
| request type / ID | wrapper 固定 `agent` / `agent-UUID` | 按模型与请求内容解析；图片使用 `image_gen/<毫秒>/<UUID>/12` |
| 输出模态 | 原生调用方传入的 `responseModalities` 可保留 | 额外调用 `EnsureImageResponseModalities` 补齐图片模型输出模态 |
| 上游传输 | 原生 gateway 使用 `streamGenerateContent`，非流式聚合 | 应结合本地 native wire owner 验证最终请求 |
| 判断依据 | 阅读 gateway / wrapper / 相关测试源码 | 阅读本地 wrapper、request-type / native 测试；未在本次运行这些测试 |

上游 typed `GeminiGenerationConfig` 没有列出某字段，不代表原生 raw JSON 路径会丢失该字段；本次按真正执行路径判断。[S4]、[L1]

现有 TokenKey 推荐沿用 [candidate eligibility SSOT](../approved/candidate-eligibility-ssot.md) 的 `protocolrouter.Plan`、实际账号选择和既有计量流程。此次研究不增加平行 scheduler，不靠计费组平台猜账号能力。上线前仍须核对部署版本、账号授权、模型映射、调度准入及图片计量；本地实现不代表已部署。

### 5.5 开发者 key 网关与其他 OAuth 候选

| 项目 | 本次确认的生图实现 | 与 Pro / Ultra OAuth 目标的差距 |
|---|---|---|
| LiteLLM | Gemini image 使用 `generateContent`；Imagen 使用 `predict`。有 `n`、`size` 及 Gemini 专用 `imageConfig` 等转换 | Gemini adapter 用 `GEMINI_API_KEY` / `x-goog-api-key`；Vertex 是项目凭据路线，非个人订阅转换 [S6] |
| Bifrost | Gemini image generation / edit 走 `generateContent`；Imagen 单独处理；该 provider 的图片流式方法不支持 | Gemini key / Vertex auth；没有据此确认个人订阅 OAuth [S7] |
| new-api | 当前 `ConvertImageRequest` 只接受 `imagen` 前缀；Gemini Nano Banana 应评估原生路径或聊天转换 | 不应给 Gemini 图片模型照抄 Imagen `/v1/images/generations` 示例；模型 catalog 不等于 converter 支持 [S8] |
| gemini-balance | key 轮询、原生 Gemini、聊天图片模态处理 | 原生 Pydantic GenerationConfig 有 `responseModalities`，未声明 `imageConfig`，存在额外字段被忽略的风险；单独 HF/OpenAI Images 路由不是 Gemini OAuth 证据 [S9] |
| gemini-business2api | Images generations / edits、Base64 / URL 输出 | 依赖 `secure_c_ses`、`csesidx`、`config_id`，用 Business Cookie 派生 JWT；不是个人 Google AI OAuth [S10] |
| antigravity-claude-proxy | Antigravity OAuth，响应 converter 可把 `inlineData` 转成 Anthropic image block | 没确认完整 Images 或 Gemini 原生图片参数入口；“能转图片 block”不等于完整图片服务 [S11] |
| claude-relay-service | 有 Gemini / Antigravity relay | 本次未找到足以证明专用图片入口、比例/分辨率处理的完整链路，需再做定向验证 [S12] |
| james-6-23/codex2api | 实验性 Antigravity OAuth + Gemini 原生路由 | 原生路径值得候选验证；文档说明 Responses 的 image_generation tool 被忽略、Imagen predict 未实现，不能用这些入口替代原生生图 [S13] |

## 6. 参数契约：不要混用三个 API 家族

| 含义 | Gemini `generateContent`（本次 OAuth 主路径） | OpenAI Images（取决于网关 adapter） | 官方 Interactions（当前官方文档） |
|---|---|---|---|
| 模型 | URL 中 `{model}`；网关写入内部 wrapper | body `model`，可能需要 provider 前缀 | body `model` |
| 提示词 | `contents[].parts[].text` | `prompt` | `input` |
| 图片输出 | `generationConfig.responseModalities` 含 `IMAGE` | 路由及 adapter 决定 | `response_format.type: image` |
| 比例 | `generationConfig.imageConfig.aspectRatio` | OmniRoute 的 `aspect_ratio` 扩展或 `size` 映射 | `response_format.aspect_ratio` |
| 分辨率档位 | `generationConfig.imageConfig.imageSize` | OmniRoute 的 `image_size` 扩展；不是所有网关都有 | `response_format.image_size` |
| 数量 | `candidateCount` 不应直接视为保证产出 n 张 | `n` 受 adapter / 上游限制 | 按该 API 契约，不复用 `candidateCount` |
| 参考图 | parts 中 `inlineData: {mimeType, data}` 等模型支持形式 | 常为 edits multipart；不等于 generations JSON 带图有效 | image input；多轮可用 `previous_interaction_id` |
| 图片结果 | 候选 content parts 中 `inlineData`；流式需正确聚合 | `data[].b64_json` 或 URL，依实现 | interaction 的图片输出 / steps，按对应 SDK 或 REST schema 解析 |

`imageSize` 是模型档位，不是任意像素尺寸；支持的比例、分辨率、参考图和多轮能力应以**实际 provider + model + account**测试为准。`gemini-3.1-flash-image` 在本次核心 OAuth 实现中有直接证据；官方新列出的其他图片型号不能仅改模型字符串就认定 Antigravity 也已开放。

`Imagen :predict` 是另一条调用与响应契约，不与 Gemini `generateContent` 混用。官方本次图片文档已给出 Interactions 示例，也不能据此把 Antigravity `/v1internal` 改成 `/interactions`。[O1]

## 7. 最小调用模板

以下模板不执行账号导入或付费调用。先取得自己网关的 API key，并确认服务仅路由到待测 Antigravity 账号。模型名在这些固定版本有源码证据，但请求成功及账号资格仍待验证。

### 7.1 CLIProxyAPI 原生；TokenKey 复用相同请求体

保存为 `gemini-image.json`：

```json
{
  "contents": [
    {"role": "user", "parts": [{"text": "生成一张极简白底产品摄影：蓝色陶瓷杯，不要文字。"}]}
  ],
  "generationConfig": {
    "responseModalities": ["TEXT", "IMAGE"],
    "imageConfig": {"aspectRatio": "1:1", "imageSize": "1K"}
  }
}
```

调用 CLIProxyAPI（`GATEWAY_API_KEY` 是本地网关 key）：

```bash
curl --fail-with-body --max-time 300 \
  'http://127.0.0.1:8317/v1beta/models/gemini-3.1-flash-image:generateContent' \
  -H "x-goog-api-key: ${GATEWAY_API_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary @gemini-image.json \
  -o gemini-image-response.json
```

TokenKey / Sub2API 改用其 key 和专用 URL：

```bash
curl --fail-with-body --max-time 300 \
  'http://127.0.0.1:8080/antigravity/v1beta/models/gemini-3.1-flash-image:generateContent' \
  -H "x-goog-api-key: ${TOKENKEY_API_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary @gemini-image.json \
  -o gemini-image-response.json
```

若模型已配置别名或 key 的分组限制，使用该部署明确暴露的名称并核对实际命中账号。首次可先省略 `imageConfig` 测基本图片输出，再逐项增加比例、1K/2K/4K；不要同时改变账号、模型、转换器及所有参数。

客户端不需要手写上游 `project`、`requestId`、`userAgent` 或 `requestType`，这些由网关凭据和请求 owner 维护。修改它们不是获得模型权益的方法。

### 7.2 OmniRoute 的 OpenAI Images 入口

```bash
curl --fail-with-body --max-time 300 \
  'http://127.0.0.1:20128/v1/images/generations' \
  -H "Authorization: Bearer ${OMNIROUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary '{
    "model": "antigravity/gemini-3.1-flash-image",
    "prompt": "生成一张极简白底产品摄影：蓝色陶瓷杯，不要文字。",
    "n": 1,
    "aspect_ratio": "1:1",
    "image_size": "1K"
  }' \
  -o omniroute-image-response.json
```

这里的 `aspect_ratio` / `image_size` 是该实现的扩展字段，不能无差别复制到 opencodex 或其他 OpenAI-compatible 网关。验收检查 `data` 非空、Base64 可解码、文件是图片，以及实际尺寸；不要将空数组当生图成功。

### 7.3 官方开发者 API 对照：Interactions

这是**单独的开发者项目路线**，不是上面 OAuth 请求的替换 URL。以下采用官方当前文档的字段：[O1]

```bash
curl --fail-with-body --max-time 300 \
  'https://generativelanguage.googleapis.com/v1beta/interactions' \
  -H "x-goog-api-key: ${GEMINI_API_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary '{
    "model": "gemini-3.1-flash-image",
    "input": "生成一张极简白底产品摄影：蓝色陶瓷杯，不要文字。",
    "response_format": {
      "type": "image",
      "aspect_ratio": "1:1",
      "image_size": "2K"
    }
  }' \
  -o official-interaction-response.json
```

不使用 Gemini candidates 解析器解析 Interactions；也不把 Interactions 的 `response_format` 放入 `generationConfig`。

## 8. Pro / Ultra 方案落地与验收建议

### 阶段 A：逐账号证明基本能力

Pro、Ultra 分别选择明确授权的测试账号，保持模型、提示词、网关版本、出口和参数一致，记录下面的结果。同日补测已完成账号 paidTier、模型发现及 daily 1K 原生图片输出验证；其余范围以[实测报告](google-ai-pro-ultra-oauth-image-live-probe-20260930.md)标注为准。

| 验收项 | 应取得的证据 | 不足以作为通过的情况 |
|---|---|---|
| OAuth 和项目 | 实际 provider/client、脱敏账号标识、项目发现、token 刷新结果 | 仅管理界面标注 Pro / Ultra |
| 模型可用性 | 账号对应 catalog / 权益响应，以及实际图片请求 | 只有网关静态模型列表 |
| 文本控制组与图片 | 同账号文本请求结果，随后图片模型成功且图片可解码 | 文本可用就认定图片可用 |
| 输出质量与尺寸 | MIME、解码后尺寸、比例、是否真实输出图片 | HTTP 200、文字描述“我画好了”、空 `data` |
| 参数生效 | 默认、1:1 / 16:9、1K / 2K / 4K 分步验证 | 上游忽略字段，但网关仍原样回显请求参数 |
| 图生图 / 多轮 | 原生参考图片、后续编辑实际改变图片，保留模型要求的 thought signature / 上下文 | 支持上传图片或文本视觉理解就认定能编辑输出 |
| 错误与额度 | 权限、模型不存在、429、内容阻断、超时的区别；额度消耗与重置观测 | 所有失败都归因于免费账号或统一无限重试 |
| 持续运行 | token 过期后刷新，账号实际归因，重试是否重复计量 | 单次调用成功 |

Google 的 Antigravity 套餐页面区分 Pro 更高额度和 Ultra 最高额度，并涉及五小时窗口、周上限与额外 AI credits 设置；额度与工作量相关。**采购和容量判断应基于同一工作负载下的实测成功图片数、耗时和额度消耗，不能把套餐名转换成固定图片配额。** 是否启用超额 AI credits 是独立成本选择，不作为默认补救。[O3]

### 阶段 B：接入现有 TokenKey

1. 首先走已存在的 Antigravity 原生接口，核对实际部署与本地源码差异，复用既有账号导入/授权流程。
2. 保持 `protocolrouter.Plan` 为模型、converter 和候选合法性裁决者；图片能力证据与账号绑定，不能只按 tier 放行所有图片模型。
3. 对非流式及流式聚合都检查真实图片输出；采集模型、实际账号、耗时、图片尺寸/数量、usage 与错误类型，避免记录 token 和图片 Base64 正文。
4. 核对本地图片计量与上游 usage 含义；未返回图片、安全阻断、重试及超时分别验收，避免“请求成功”与“产出成功”混淆。
5. 原生链路验收通过后，再按客户端需要评估共享 Images converter。若新增公共接口，应按项目研发规则另立实现与验收范围，本次材料不批准新增接口。

### 阶段 C：独立预算的回退

若账号无图片权限或额度不足，可选择官方 Gemini API / Vertex，明确暴露凭据来源和计费策略。不要静默从订阅 OAuth 切换到付费项目；不同 API 家族应由明确 adapter 转换。

现有本地 [Gemini Web 通道方案](../approved/gemini-web-channel.md) 是 Cookie 路线：文档描述的当前范围以单轮文本提示生图为主，比例有限，拒绝参考图、复杂历史和未支持参数。它可另行评估网页权益，但不能用于证明 OAuth 方案成功，也不能承诺相同的分辨率或多轮编辑能力。

## 9. 日报项目全量筛选

“深查”表示检查了本题相关实现；“初筛”主要来自该固定版本 README。以下链接均固定到本次抓取 SHA；缺少本题证据的项目保留为待确认，而不是断言不支持。

| 项目 / 固定版本 README | 深度 | 与 Gemini 生图 / 个人 OAuth 的关系 |
|---|---|---|
| [diegosouzapw/OmniRoute @ a1a2dce1a64a](https://github.com/diegosouzapw/OmniRoute/blob/a1a2dce1a64a4b123f80ec899fc88bb2aa34d1a4/README.md) | 深查 | Antigravity OAuth 图片适配器最明确；OpenAI Images 参数映射参考。 |
| [router-for-me/CLIProxyAPI @ a270e7b9e57a](https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/README.md) | 深查 | Antigravity OAuth + 原生 Gemini；推荐独立验证。 |
| [lidge-jun/opencodex @ 569e3e7dae48](https://github.com/lidge-jun/opencodex/blob/569e3e7dae48bafc54b8a1a7e3a85129befe2d98/README.md) | 深查 | Antigravity 图片兜底；固定模型、单图，参数有限。 |
| [BerriAI/litellm @ 6684256136c9](https://github.com/BerriAI/litellm/blob/6684256136c91cd30f3ea53c8c935712479959c2/README.md) | 深查 | Gemini / Imagen 开发者 API 生图；不等于个人订阅 OAuth。 |
| [Wei-Shaw/sub2api @ a60a29549f48](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/README.md) | 深查 | Antigravity 原生生图路径；上游与本地 TokenKey 有关键差异。 |
| [QuantumNous/new-api @ 789c970199ea](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/README.md) | 深查 | Gemini key / Vertex；Images converter 的 Imagen 限制须注意。 |
| [maximhq/bifrost @ 47748e0293c8](https://github.com/maximhq/bifrost/blob/47748e0293c87cf7cb3575579c34aba52e4e78d8/README.md) | 深查 | Gemini / Imagen 图片及编辑；开发者凭据路线。 |
| [songquanpeng/one-api @ 8df4a2670b98](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/README.md) | 初筛 | 传统 Gemini key 网关；未确认个人 Google AI OAuth 生图链路。 |
| [james-6-23/codex2api @ c47c1a669e39](https://github.com/james-6-23/codex2api/blob/c47c1a669e395d6fc15878556d6605554b06b67a/README.md) | 专题文档 | 已加入实验性 Antigravity OAuth / Gemini 原生入口；图片实测待补。 |
| [Wei-Shaw/claude-relay-service @ cf95ecc5e0ba](https://github.com/Wei-Shaw/claude-relay-service/blob/cf95ecc5e0ba846faaf1b11de574367de89fd00c/README.md) | 部分源码 | Gemini / Antigravity relay；未确认图片专用链路及参数完整性。 |
| [basketikun/chatgpt2api @ dc105e51bd48](https://github.com/basketikun/chatgpt2api/blob/dc105e51bd486bd75c8ef4f74be4bc4724bdfc33/README.md) | 初筛 | ChatGPT 网页图片来源，不是 Gemini OAuth。 |
| [snailyp/gemini-balance @ 5512f7ff218a](https://github.com/snailyp/gemini-balance/blob/5512f7ff218a85970ecb643e2d04e44e7de96efd/README.md) | 深查 | Gemini key 池；现代 imageConfig 字段保留需验证。 |
| [dwgx/WindsurfAPI @ 25e96107d339](https://github.com/dwgx/WindsurfAPI/blob/25e96107d33928de625e57f2cc4b25c96aea04fc/README.md) | 初筛 | Windsurf / Devin 登录与 Gemini 协议、图片输入；不是个人 Google AI 订阅证明。 |
| [modelbus/one-api-pro @ 26b8ff08a629](https://github.com/modelbus/one-api-pro/blob/26b8ff08a6296e29999f66022cc2f1bcda509076/README.md) | 初筛 | One API 增强版；未确认个人 Google AI OAuth 生图。 |
| [XxxXTeam/codex-proxy @ 83b7b5628224](https://github.com/XxxXTeam/codex-proxy/blob/83b7b5628224a0d1f8b4541dbfdc884c2f6892aa/README.md) | 初筛 | Codex / Claude 协议代理；本题图片能力取决于上游，未确认。 |
| [chenyme/grok2api @ 5e5ad75556b6](https://github.com/chenyme/grok2api/blob/5e5ad75556b61a2c4a8fcf344d83bfe7760f2b42/README.md) | 初筛 | Grok 图片通道，不是 Gemini。 |
| [BenedictKing/ccx @ 19f31d847563](https://github.com/BenedictKing/ccx/blob/19f31d8475632b90b25cbe6c2a4469c913938766/README.md) | 初筛 | 有 Images 与 Gemini 原生协议；未确认个人 Google OAuth 图片来源。 |
| [badrisnarayanan/antigravity-claude-proxy @ daa39d6c6239](https://github.com/badrisnarayanan/antigravity-claude-proxy/blob/daa39d6c6239ac078a4e69de85094dde35558ef6/README.md) | 部分源码 | Antigravity OAuth；可转换输出图片 block，未确认完整图片参数入口。 |
| [1rgs/claude-code-proxy @ 5e45ba683ded](https://github.com/1rgs/claude-code-proxy/blob/5e45ba683ded931c1832cfca6468a791c6855e45/README.md) | 初筛 | 经 LiteLLM 的 Gemini key / Vertex；非个人订阅 OAuth 专项方案。 |
| [fuergaosi233/claude-code-proxy @ 7ea4177a54a5](https://github.com/fuergaosi233/claude-code-proxy/blob/7ea4177a54a5ff7969a5f8ec76d9f80f2e0409e5/README.md) | 初筛 | Claude Code 到兼容上游；未确认本题链路。 |
| [icebear0828/codex-proxy @ e0967e6bc6f4](https://github.com/icebear0828/codex-proxy/blob/e0967e6bc6f4f1c1b914247ded79e54ed269ee38/README.md) | 初筛 | Codex / ChatGPT 图片来源，与 Gemini 区分。 |
| [yukkcat/gemini-business2api @ e716b8a6c316](https://github.com/yukkcat/gemini-business2api/blob/e716b8a6c3162eb8debbb8673320eb3d0fa1257a/README.md) | 深查 | Business Cookie / JWT 生图及编辑，非个人 Pro / Ultra OAuth。 |
| [yym68686/uni-api @ 2120b3f00f76](https://github.com/yym68686/uni-api/blob/2120b3f00f768677156e881f06dbb07b1efd2aa9/README.md) | 初筛 | 多后端 Gemini / 图片相关适配；未确认个人 Google OAuth。 |
| [caiwuu/web2api @ 0edb8c07b4a0](https://github.com/caiwuu/web2api/blob/0edb8c07b4a00ddf31babacffbd5f90635021c61/README.md) | 初筛 | 本次 README 主要为 Claude Web / 图片输入；非 Gemini OAuth 证据。 |
| [yushangxiao/claude2api @ c387010d9a10](https://github.com/yushangxiao/claude2api/blob/c387010d9a102754a3d64a8cf9dc0ae82e958d1e/README.md) | 初筛 | Claude 网页转 API；非 Gemini OAuth。 |
| [raine/claude-code-proxy @ 1e30e301a48c](https://github.com/raine/claude-code-proxy/blob/1e30e301a48c01a797308e2d24f6c66515363cbf/README.md) | 初筛 | Claude Code 连接 Codex 等上游；不能据此认定 Gemini OAuth 生图。 |
| [silasxbt/anti-api @ 7e20269161e0](https://github.com/silasxbt/anti-api/blob/7e20269161e08d0b7e4de1dd1f5524eb973c74e1/README.md) | 初筛 | Antigravity PKCE / 多模态输入；专用图片生成未确认，与下一项同 SHA。 |
| [ink1ing/anti-api @ 7e20269161e0](https://github.com/ink1ing/anti-api/blob/7e20269161e08d0b7e4de1dd1f5524eb973c74e1/README.md) | 初筛 | 与 silasxbt/anti-api 同 SHA，不计为独立实现证据。 |
| [FakeOAI/tokens @ 4862df998d1e](https://github.com/FakeOAI/tokens/blob/4862df998d1e372e0a434d3bdf8b548d92128342/README.md) | 初筛 | 号池与协议聚合；未确认图片完整实现及 OAuth 权益来源。 |
| [nielspeter/claude-code-proxy @ 2e82b12ed6aa](https://github.com/nielspeter/claude-code-proxy/blob/2e82b12ed6aaf5112d96175841f5ea72aa630f16/README.md) | 初筛 | Claude Code 到 OpenRouter / Ollama；未确认本题链路。 |
| [J1aDong/codexProxy @ dcd0241b5883](https://github.com/J1aDong/codexProxy/blob/dcd0241b5883a06eb863bc3f4aad6e1342239583/README.md) | 初筛 | Anthropic 到 Codex / Gemini 路由；未确认完整图片链路。 |
| [imroc/llm-proxy @ 07e9dd3ff869](https://github.com/imroc/llm-proxy/blob/07e9dd3ff8698f402598d3c514b88ed660ea31ce/README.md) | 初筛 | 本地代理与重试；图片能力依赖上游，未确认个人 OAuth。 |
| [zy-yyy/coding-proxy @ 403a471b9133](https://github.com/zy-yyy/coding-proxy/blob/403a471b9133bd99f6000997799d960050a8813b/README.md) | 初筛 | 含 Antigravity 相关登录 / 转发；未确认专用图片链路。 |
| [LYCaikano/codex-proxy @ 9c5057f09454](https://github.com/LYCaikano/codex-proxy/blob/9c5057f09454d6e2c66fe7784cb9c1ba8a747a77/README.md) | 初筛 | Codex 多协议代理；未确认本题链路。 |
| [liu5269/codex2api @ 169abcf13866](https://github.com/liu5269/codex2api/blob/169abcf138660928f208b4ce6936feec2a42c952/README.md) | 初筛 | Codex2API 变体；未确认 Gemini OAuth 生图。 |

## 10. 主要源码和官方资料

以下源码链接固定到调研提交；附录 README 链接同时给出其他项目的版本锚点。

[S1]: https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/internal/auth/antigravity/constants.go

- **S1 — router-for-me/CLIProxyAPI**：[internal/auth/antigravity/constants.go](https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/internal/auth/antigravity/constants.go)；[internal/auth/antigravity/auth.go](https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/internal/auth/antigravity/auth.go)；[internal/translator/antigravity/gemini/antigravity_gemini_request.go](https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/internal/translator/antigravity/gemini/antigravity_gemini_request.go)；[internal/runtime/executor/antigravity_executor_request.go](https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/internal/runtime/executor/antigravity_executor_request.go)；[internal/runtime/executor/antigravity_executor_execute.go](https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/internal/runtime/executor/antigravity_executor_execute.go)；[config.example.yaml](https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/config.example.yaml)。

[S2]: https://github.com/diegosouzapw/OmniRoute/blob/a1a2dce1a64a4b123f80ec899fc88bb2aa34d1a4/open-sse/config/imageRegistry.ts

- **S2 — diegosouzapw/OmniRoute**：[open-sse/config/imageRegistry.ts](https://github.com/diegosouzapw/OmniRoute/blob/a1a2dce1a64a4b123f80ec899fc88bb2aa34d1a4/open-sse/config/imageRegistry.ts)；[open-sse/handlers/imageGeneration.ts](https://github.com/diegosouzapw/OmniRoute/blob/a1a2dce1a64a4b123f80ec899fc88bb2aa34d1a4/open-sse/handlers/imageGeneration.ts)；[src/lib/oauth/constants/oauth.ts](https://github.com/diegosouzapw/OmniRoute/blob/a1a2dce1a64a4b123f80ec899fc88bb2aa34d1a4/src/lib/oauth/constants/oauth.ts)；[src/app/api/v1/images/generations/route.ts](https://github.com/diegosouzapw/OmniRoute/blob/a1a2dce1a64a4b123f80ec899fc88bb2aa34d1a4/src/app/api/v1/images/generations/route.ts)。

[S3]: https://github.com/lidge-jun/opencodex/blob/569e3e7dae48bafc54b8a1a7e3a85129befe2d98/src/oauth/google-antigravity.ts

- **S3 — lidge-jun/opencodex**：[src/oauth/google-antigravity.ts](https://github.com/lidge-jun/opencodex/blob/569e3e7dae48bafc54b8a1a7e3a85129befe2d98/src/oauth/google-antigravity.ts)；[src/server/images.ts](https://github.com/lidge-jun/opencodex/blob/569e3e7dae48bafc54b8a1a7e3a85129befe2d98/src/server/images.ts)。

[S4]: https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/service/antigravity_gateway_gemini.go

- **S4 — Wei-Shaw/sub2api**：[backend/internal/service/antigravity_gateway_gemini.go](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/service/antigravity_gateway_gemini.go)；[backend/internal/service/antigravity_gateway_service.go](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/service/antigravity_gateway_service.go)；[backend/internal/service/antigravity_gateway_service_test.go](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/service/antigravity_gateway_service_test.go)；[backend/internal/server/routes/gateway.go](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/server/routes/gateway.go)。

[S5]: https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/pkg/geminicli/constants.go

- **S5 — Wei-Shaw/sub2api**：[backend/internal/pkg/geminicli/constants.go](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/pkg/geminicli/constants.go)；[backend/internal/pkg/geminicli/models.go](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/pkg/geminicli/models.go)；[backend/internal/pkg/antigravity/oauth.go](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/pkg/antigravity/oauth.go)；[backend/internal/service/gemini_oauth_service.go](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/service/gemini_oauth_service.go)。

[S6]: https://github.com/BerriAI/litellm/blob/6684256136c91cd30f3ea53c8c935712479959c2/litellm/llms/gemini/image_generation/transformation.py

- **S6 — BerriAI/litellm**：[litellm/llms/gemini/image_generation/transformation.py](https://github.com/BerriAI/litellm/blob/6684256136c91cd30f3ea53c8c935712479959c2/litellm/llms/gemini/image_generation/transformation.py)。

[S7]: https://github.com/maximhq/bifrost/blob/47748e0293c87cf7cb3575579c34aba52e4e78d8/core/providers/gemini/gemini.go

- **S7 — maximhq/bifrost**：[core/providers/gemini/gemini.go](https://github.com/maximhq/bifrost/blob/47748e0293c87cf7cb3575579c34aba52e4e78d8/core/providers/gemini/gemini.go)。

[S8]: https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/relay/channel/gemini/adaptor.go

- **S8 — QuantumNous/new-api**：[relay/channel/gemini/adaptor.go](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/relay/channel/gemini/adaptor.go)；[docker-compose.yml](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/docker-compose.yml)。

[S9]: https://github.com/snailyp/gemini-balance/blob/5512f7ff218a85970ecb643e2d04e44e7de96efd/app/domain/gemini_models.py

- **S9 — snailyp/gemini-balance**：[app/domain/gemini_models.py](https://github.com/snailyp/gemini-balance/blob/5512f7ff218a85970ecb643e2d04e44e7de96efd/app/domain/gemini_models.py)；[README.md](https://github.com/snailyp/gemini-balance/blob/5512f7ff218a85970ecb643e2d04e44e7de96efd/README.md)。

[S10]: https://github.com/yukkcat/gemini-business2api/blob/e716b8a6c3162eb8debbb8673320eb3d0fa1257a/core/account.py

- **S10 — yukkcat/gemini-business2api**：[core/account.py](https://github.com/yukkcat/gemini-business2api/blob/e716b8a6c3162eb8debbb8673320eb3d0fa1257a/core/account.py)；[core/jwt.py](https://github.com/yukkcat/gemini-business2api/blob/e716b8a6c3162eb8debbb8673320eb3d0fa1257a/core/jwt.py)；[README.md](https://github.com/yukkcat/gemini-business2api/blob/e716b8a6c3162eb8debbb8673320eb3d0fa1257a/README.md)。

[S11]: https://github.com/badrisnarayanan/antigravity-claude-proxy/blob/daa39d6c6239ac078a4e69de85094dde35558ef6/src/format/response-converter.js

- **S11 — badrisnarayanan/antigravity-claude-proxy**：[src/format/response-converter.js](https://github.com/badrisnarayanan/antigravity-claude-proxy/blob/daa39d6c6239ac078a4e69de85094dde35558ef6/src/format/response-converter.js)；[README.md](https://github.com/badrisnarayanan/antigravity-claude-proxy/blob/daa39d6c6239ac078a4e69de85094dde35558ef6/README.md)。

[S12]: https://github.com/Wei-Shaw/claude-relay-service/blob/cf95ecc5e0ba846faaf1b11de574367de89fd00c/src/services/relay/geminiRelayService.js

- **S12 — Wei-Shaw/claude-relay-service**：[src/services/relay/geminiRelayService.js](https://github.com/Wei-Shaw/claude-relay-service/blob/cf95ecc5e0ba846faaf1b11de574367de89fd00c/src/services/relay/geminiRelayService.js)；[src/services/relay/antigravityRelayService.js](https://github.com/Wei-Shaw/claude-relay-service/blob/cf95ecc5e0ba846faaf1b11de574367de89fd00c/src/services/relay/antigravityRelayService.js)。

[S13]: https://github.com/james-6-23/codex2api/blob/c47c1a669e395d6fc15878556d6605554b06b67a/docs/ANTIGRAVITY.md

- **S13 — james-6-23/codex2api**：[docs/ANTIGRAVITY.md](https://github.com/james-6-23/codex2api/blob/c47c1a669e395d6fc15878556d6605554b06b67a/docs/ANTIGRAVITY.md)。

[L1]: ../../backend/internal/service/antigravity_gateway_service.go

- **L1 — 本地 TokenKey**：[wrapper](../../backend/internal/service/antigravity_gateway_service.go)、[request type 测试](../../backend/internal/service/antigravity_gateway_request_type_test.go)、[native 测试](../../backend/internal/service/antigravity_gateway_native_tk_test.go)、[native wire](../../backend/internal/service/antigravity_gateway_gemini_wire_tk.go)。以本文记录的本地 HEAD 为版本锚点。

[O1]: https://ai.google.dev/gemini-api/docs/image-generation
[O2]: https://ai.google.dev/gemini-api/docs/billing
[O3]: https://antigravity.google/docs/plans
[O4]: https://ai.google.dev/gemini-api/docs/oauth
[O5]: https://one.google.com/about/google-ai-plans/

- [O1 — Gemini 官方图片生成文档][O1]：当前 Interactions 示例、模型与图片参数。
- [O2 — Gemini API Billing][O2]：开发者项目计费与额度框架。
- [O3 — Antigravity Plans][O3]：个人订阅关联等级、使用窗口及额外 credits。
- [O4 — Gemini API OAuth][O4]：自建项目 OAuth 的鉴权流程。
- [O5 — Google AI Plans][O5]：个人套餐与产品范围；Workspace / Business 应另行区分。
