# Antigravity OAuth 文本与生图：TokenKey 全栈验证

验证日期：2026-09-30，主要请求窗口 02:48–02:56 UTC。线上版本 `1.8.266`；代码基线 `a38cf0ff830f8d9e607d77a8f32e11dccc27bf4e`。本文承接[方案推荐](google-ai-pro-ultra-oauth-image-generation-20260930.md)、[参数对比](antigravity-vs-gemini-web-image-parameters-20260930.md)及[daily/prod 历史](antigravity-endpoint-history-20260930.md)。

## 结论与交付状态

**可以复用现有 Antigravity OAuth 账号提供文本和图片 API。已实测公网入口 → prod relay → edge OAuth → Google 的服务链路，四种生成协议均有成功记录。** 当前方案应以 Antigravity 为首选、原生 Gemini 为参数最完整的接口；普通 `nano-2` 请求本次也实际选中了 Antigravity。

但“上游能够生图”不能直接等同于“现有默认行为已经可以完整替代 Web”：本次发现 **不传 imageSize 时，上游返回 1024×1024，TokenKey 却按默认 2K 计费**。显式 2K 则返回 2048×2048。已在本地准备默认 2K 的最小修复并通过回归测试，**尚未部署**。部署前，调用方应显式发送 `imageSize: "2K"`。

| 交付项 | 状态 |
|---|---|
| Pro / Ultra raw upstream daily 生图 | 前轮通过；本轮复用证据 |
| 公网原生 Gemini、Chat、Responses、Messages 文本 | 通过 |
| 上述四种入口的显式 2K 生图 | 通过，均完整解码图片并核对用量 |
| Universal Key、模型列表、工具往返、图片理解 | 通过所列样本 |
| 省略分辨率时的输出与默认收费一致性 | 线上复现缺口；本地修复通过测试，待部署 |
| `/v1/images/generations` → Antigravity | 当前不支持，实测 404 |
| 更改全局供应源优先级、移除 Web、部署代码 | 本轮未执行 |
| Studio 浏览器交互 | 未执行；本轮为 API 集成测试，不称作 UI e2e |

## 1. 方法与账号归因

- 通过现有 `run-probe.sh` 经 SSM 执行，使用 canonical 单账号 probe 及现有管理员测试 Key；OAuth token 留在 edge。
- 单账号测试使用保留的 exclusive/direct `__tk_probe_*` 资源；公网地址为 `https://api.tokenkey.dev`，不是仅测容器 localhost。
- 先对 us5 OAuth #21 单独验证文本；随后锁定 prod #154（配置上游 `https://api-us5.tokenkey.dev`）测试四种协议。edge 用量记录实际落在 us5 #25，不能把锁定 prod relay 误写成锁定 edge #21。
- Universal 使用既有 `TK_FULLTEST_KEY` #334；强制平台路径用于证明 AG 候选，另有一次不带平台前缀的 `nano-2` 生图。Universal 选择了 prod #61/us3 和 #154/us5；对应 edge 样本为 us3 #25/#26、us5 #25。
- prod 用响应 `X-Request-ID` 精确查 usage；跨 prod/edge 的 `upstream_request_id` 当前为空，因此按上游模型、输入/输出 token、图片数、结算时间相差小于 2 秒关联。所列成功请求均唯一匹配，但这是组合证据，**不是贯通的跨跳 request ID**。
- 全部生图串行执行；没有对未知结果自动重试。未启用原本不可调度的 Ultra 账号，未修改业务 mapping、账号优先级或正式分组。
- probe 图片校验在 prod 的临时独立 Python venv 使用 Pillow 完整 verify/load；依赖环境已删除。结束后 prod/us5 的保留 probe 活跃组、Key、账号绑定均为 0。

[脱敏逐请求证据、账单与清理结果](assets/antigravity-fullstack-20260930/evidence.json)保留请求 ID、SSM ID（canonical 样本）、哈希与跨跳关联方法，不含凭据或图片 Base64。

## 2. 文本能力

| 请求模型 | 入口 | 模式 | prod 耗时 | 实际上游模型 | 结果 |
|---|---|---|---:|---|---|
| `gemini-3.8-flash` | 原生 `generateContent` | 非流式 | 3.462s | `gemini-3.8-flash-high` | 200，文本及 token 计费 |
| `gemini-3.8-flash` | `/v1/chat/completions` | 非流式 | 2.384s | 同上 | `AGY_OK`，finish=stop |
| `gemini-3.8-flash` | `/v1/responses` | SSE | 1.559s | 同上 | 200，文本 delta 及用量归因 |
| `gemini-3.8-flash` | `/v1/messages` | SSE | 7.187s | 同上 | `AGY_OK`，message_stop |
| `gemini-3.6-flash` | 原生 `generateContent` | 非流式 | 1.754s | `gemini-3.6-flash-tiered` | 200，文本及 token 计费 |
| `gemini-3.7-flash` | `/v1/chat/completions` | SSE | 1.494s | `gemini-3.7-flash-high` | `AGY_OK`，stop + DONE |

Universal 扩展验证：

- `/antigravity/v1/models` 返回当前可服务列表，包含上述三个文本系列、`gemini-3.1-flash-image`、`nano-2`。
- 原生 `streamGenerateContent?alt=sse` 返回 `AGY_STREAM_OK`、合法 SSE 终止；本次 2.484s。
- Gemini 原生 `functionDeclarations` + `toolConfig.functionCallingConfig.mode=ANY` 正确调用 `get_weather(city=Paris)`；保留模型返回的 content/签名并回传 `functionResponse` 后，回答 `22°C`。没有真的调用天气外部服务，工具结果是受控测试输入。
- 将刚生成的红杯图片作为 `inlineData` 输入文本模型，得到 `Red ceramic mug.`，验证图像输入与文本模型的多模态能力。
- 原生响应包含 `thoughtsTokenCount`，用量中的 output_tokens 包含候选输出和思考 token；例如工具请求 16 + 28 = 44。未穷举不同 thinking 档位。

当前 AG public floor 未列出 Gemini Pro 文本或 Claude 模型；不能把 Flash 成功扩写为所有 Google/Claude 型号可用。`-high` 请求在部分 Google 响应中回报基础模型名，日志因严格字符串比较标记 `upstream_model_mismatch=true`；本轮不据此判断发生了跨供应源替换。

## 3. 生图协议与计费

前四行均使用模型 `gemini-3.1-flash-image`、`TEXT+IMAGE`、`16:9`、显式 `2K`，锁定 prod #154。

| 入口 | 模式 | 结果形式 | prod 耗时 | 图片数 / total_cost |
|---|---|---|---:|---|
| `/v1beta/models/{model}:generateContent` | 非流式 | Gemini `inlineData` | 19.705s | 1 / $0.1008 |
| `/v1/chat/completions` | 非流式 | 文本字段中的 Markdown data URL | 16.198s | 1 / $0.1008 |
| `/v1/responses` | SSE | output_text 中的 Markdown data URL | 14.201s | 1 / $0.1008 |
| `/v1/messages` | SSE | text block 中的 Markdown data URL | 14.741s | 1 / $0.1008 |
| `/v1/images/generations` | 非流式 | 404，平台组不支持该接口 | — | 无 usage 行 |

四种成功响应中的图片均经过 Base64 严格解析、MIME 校验、完整图片解码及 SHA-256 记录；流式兼容图片另外检查协议终止状态。不能将兼容输出称为 OpenAI Images 的 `data[].b64_json`，也不能将 Responses 的文本图片称为原生 `image_generation_call`。

Universal 原生生图 SSE 使用 `3:4 + 2K`，真实输出 **1792×2400**，19.318s；无平台前缀的 `nano-2 + 1:1 + 2K` 输出 **2048×2048**，20.094s，选中 Antigravity #154。这证明正常路由可以选择 AG，不证明任意时间、任意 Key 都保证 AG 优先。

账单核对：

- 成功图片 `image_count=1`、`billing_mode=image`；文本请求 `image_count=0`、`billing_mode=token`。部分文本行仍有历史默认 `image_size=2K`，不能仅据此判断为生图收费。
- 保留 Direct probe 的倍率为 1，`total_cost=actual_cost=$0.1008`。
- Universal 测试用户有 0.01 倍率，图片 `total_cost=$0.1008`、`actual_cost=$0.001008`；这是现有测试折扣，不是公开售价。计费组可能显示 Google-Vertex，但实际账号平台仍是 Antigravity，符合账号与计费组分离的契约。
- 所测图片记录 `image_output_size=null`；显式尺寸时 `image_size_source=input`，省略时为 default。记录的尺寸档位不是从图片像素独立测出的；不能以账单标记替代图片解码。
- 本轮 prod 成功请求的 total_cost 合计 $0.710649，actual_cost 合计 $0.4086482625；edge 单账号先导请求另计 $0.0003495。prod 与 edge 是两层内部记录，不相加冒充用户双重扣款，也不是 Google 上游现金成本。

## 4. 默认分辨率缺口与本地修复

线上对照：

| 请求 | 实际解码 | 账单尺寸来源 | total_cost |
|---|---|---|---:|
| `imageConfig: {aspectRatio: "1:1"}` | 1024×1024 | `2K / default` | $0.1008 |
| `imageConfig: {aspectRatio: "1:1", imageSize: "2K"}` | 2048×2048 | `2K / input` | $0.1008 |

问题来自两个默认值：Google 对所测模型采用 1K；TokenKey 计费默认 2K。Studio 的共享请求构造器目前只发 ratio，不显式发 image_size，因此存在同一路径风险；这是源码分析，未冒充浏览器实测。

按用户同意的默认 2K 方向，本地修复在 **已选中 Antigravity 后的共享 v1internal wrapper** 补足省略的 imageSize，默认值复用现有 `NormalizeImageBillingTierOrDefault("")`。四种入口落到同一 owner，显式 1K/2K/4K、比例和 IMAGE 模态均保留；文本模型不注入图片配置。泛用协议转换和 Web 准入不被改为接受 imageSize。显式非法参数仍交给原有校验，不在本修复中静默重写。

涉及文件：

- [AG wire owner](../../backend/internal/service/antigravity_gateway_gemini_wire_tk.go)及[共同 wrapper](../../backend/internal/service/antigravity_gateway_service.go)。
- [原生请求回归](../../backend/internal/service/antigravity_gateway_native_tk_test.go)、[实际转发与计费默认一致性断言](../../backend/internal/service/antigravity_gateway_service_test.go)、[Studio extra_body 转换回归](../../backend/internal/service/gemini_compat_image_options_test.go)。

已运行：

```sh
cd backend
go test -tags=unit ./internal/service -run 'Test(Antigravity|WrapNativeGeminiRequest|GeminiCompat)' -count=1
```

结果通过，另通过 gofmt / git diff --check。测试覆盖默认请求、已有 ratio/IMAGE 模态、显式 1K/4K、snake size、null/malformed 边界、文本请求和 generic converter 不变性。**这些是本地回归，不是修复部署后的线上验收。** 本轮不创建提交、PR 或部署，所以未执行发布 preflight。

## 5. 可直接使用的请求

公网 HTTPS 443；TokenKey 内部服务仍为 8080；上游 Google HTTPS 443。客户端只用 TokenKey Key，不发送 Google OAuth token。

推荐原生接口；Universal Key 可用 `/antigravity` 前缀限定供应平台，Direct AG Key 可用普通路径：

```sh
curl 'https://api.tokenkey.dev/antigravity/v1beta/models/gemini-3.1-flash-image:generateContent' \
  -H "Authorization: Bearer $TOKENKEY_API_KEY" \
  -H 'Content-Type: application/json' \
  --data '{
    "contents": [{"role":"user","parts":[{"text":"Generate one image of a blue ceramic cup on white."}]}],
    "generationConfig": {
      "responseModalities": ["TEXT","IMAGE"],
      "imageConfig": {"aspectRatio":"16:9","imageSize":"2K"}
    }
  }'
```

现有 Chat 客户端可发送下列 body 到 `/v1/chat/completions`，使用授权覆盖 AG 的 Key；该例采用本轮直接测通的 extension：

```json
{
  "model": "gemini-3.1-flash-image",
  "messages": [{"role":"user","content":"Generate one blue ceramic cup on white."}],
  "stream": false,
  "generationConfig": {
    "responseModalities": ["TEXT","IMAGE"],
    "imageConfig": {"aspectRatio":"16:9","imageSize":"2K"}
  }
}
```

Studio 风格的 `extra_body.google.image_config.{aspect_ratio,image_size}` 也有共享转换器与单元回归，但本轮公网矩阵使用上述 generationConfig，避免混淆实测与源码支持。`n/candidateCount > 1` 不承诺支持。

## 6. 推荐的落地边界

1. 现阶段直接通过 AG 原生或兼容生成入口提供能力，客户端显式 2K；需要固定 AG 时用平台前缀或 Direct AG Key。
2. 审查并发布默认分辨率修复后，再复测“省略尺寸 → 2048×2048 → 2K 单图费用”和显式 1K/4K 回归。无需因为本轮结果再切换 daily/prod 策略。
3. Web 保留为独立的受限候选；带 imageSize、参考图或额外比例的请求不降参后转 Web。此次未改变全局优先级或禁用 Web。
4. 长时额度、并发、所有 ratio×size、Ultra 全协议矩阵、多参考图、连续编辑及 Studio UI 仍是未完成范围；不以短时成功承诺这些能力。
5. 跨跳 request ID 贯通与按真实图片输出记录尺寸，是后续可观测性补强项。当前已有足够证据定位默认分辨率缺口，但不把组合关联写成精确 trace。

## 后续实现：Images 兼容入口（尚未部署）

用户批准“补一层协议”后，本地增加了 `/v1/images/generations` → AG 原生 Gemini
适配。实现契约、参数边界和示例见
[Antigravity Images 适配](../approved/antigravity-images-generations-adapter.md)。
上表 404 是部署版本实测结果，不能用本地代码或 mock 测试替换为线上已成功。
