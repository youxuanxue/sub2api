# Google AI Pro / Ultra：edge Antigravity OAuth 生图直测

测试日期：2026-09-30。调用与额度观测窗口约为 **01:56–02:03 UTC / 09:56–10:03 Asia/Shanghai**。这是[方案调研](google-ai-pro-ultra-oauth-image-generation-20260930.md)的实号验证补充。

后续同日完成了[Antigravity 与 Gemini Web 的参数矩阵对比](antigravity-vs-gemini-web-image-parameters-20260930.md)，并整理了[daily/prod 历史及社区讨论](antigravity-endpoint-history-20260930.md)。下文保留本轮 1K 探测的原始范围。 后续[公网全栈验证](antigravity-fullstack-validation-20260930.md)已补齐文本、生图、路由与计费证据；下文“尚未证明公网”的描述限定于此次 raw upstream 轮次。

## 结论

**Pro 和 Ultra 均已通过 edge 上的 Antigravity OAuth 凭据，在 `daily-cloudcode-pa.googleapis.com` 成功生成图片。模型为 `gemini-3.1-flash-image`，验证参数为 `TEXT + IMAGE`、`1:1`、`1K`。输出已完整解码为 1024×1024 JPEG。**

本次最重要的差异是 **生成请求的 upstream host**：同一 Ultra 账号、相同模型和参数、同为非流式 `generateContent`，daily 成功，`cloudcode-pa.googleapis.com` 返回 `429 RESOURCE_EXHAUSTED`。Pro 的 daily 非流式和流式均成功。因此 prod 返回的 429 不能直接解释为该个人订阅账号没有生图权限或额度耗尽。

现有 TokenKey 可以继续复用 Antigravity 原生 Gemini 路径，尤其是现有付费账号选择 daily 的逻辑。**这次证明了上游账号能力；尚未证明公网网关准入、路由归因和计费端到端通过。**

## 1. 测试环境与方法

- 使用 `ops/observability/run-probe.sh` 经 SSM 将临时 probe 投递到 edge；没有手工 SSH，也没有从本机携带 Google token 直连。
- 探查 us3、us4、us5、us6 的账号与部署；应用镜像均为 `ghcr.io/youxuanxue/sub2api:1.8.266`。
- OAuth access token / project 仅在对应 edge 的进程内读取与使用；未将凭据输出、拉回本机或写入材料。请求直接去 Google，不经过 TokenKey 公网入口、账号调度或模型白名单。
- 传输为 Python urllib HTTPS，关闭代理和重定向；请求头使用线上设置对应的 `User-Agent: antigravity/cli/1.2.12 darwin/arm64`，以及 Bearer、JSON Content-Type。没有声称该传输与 TokenKey 的 TLS 指纹完全相同。
- 没有改账号 mapping、schedulable、分组、环境配置、部署或额外 credits 设置；没有主动 refresh 或重新授权。us4 的 Ultra 测试账号原本 `schedulable=false`，仍保持该策略；直接能力探测不等于将它加入服务池。
- 先调用 `loadCodeAssist` 确认真实 `paidTier`，再查 `fetchAvailableModels`，最后发送图片请求。所有失败请求均保留上游状态，不做隐式 fallback 或循环重试。

## 2. 订阅等级与图片模型证据

| Edge / 账号 ID | 账号名 | Google `paidTier.id` | 上游返回的图片模型 |
|---|---|---|---|
| us3 / 3 | antigravity-oh1-ls-b | `g1-pro-tier` | `gemini-3.1-flash-image` |
| us4 / 5 | antigravity-491 | `g1-pro-tier` | 同上；本账号仅做发现，没有生成测试 |
| us5 / 21 | anti-109 | `g1-pro-tier` | 同上 |
| us6 / 27 | anti-167 | `g1-pro-tier` | 同上 |
| us4 / 33 | antigravity-or1-ls-b-2 | `g1-ultra-tier` | 同上 |

`loadCodeAssist` 在部分 Pro / Ultra 账号上同时返回 `currentTier.id=free-tier` 与非空 `paidTier`。**不能只读取 currentTier 就把这些账号归为免费账号。** 本表的订阅判断来自 `paidTier`，不是账号名字或管理界面标签。

## 3. 实际生成矩阵

下表 prod 指 `cloudcode-pa.googleapis.com`，daily 指 `daily-cloudcode-pa.googleapis.com`；均使用 HTTPS 443。`stream` 指 `/v1internal:streamGenerateContent?alt=sse`，`nonstream` 指 `/v1internal:generateContent`。

| Edge / 账号 | 等级 | Host | 调用方式 | HTTP / 实际结果 | 上游请求耗时 |
|---|---|---|---|---|---:|
| us3 / 3 | Pro | prod | stream | 429 `RESOURCE_EXHAUSTED`，无图片 | 1.027 s |
| us5 / 21 | Pro | prod | stream | 429 `RESOURCE_EXHAUSTED`，无图片 | 0.089 s |
| us6 / 27 | Pro | prod | stream | 429 `RESOURCE_EXHAUSTED`，无图片 | 0.264 s |
| **us5 / 21** | **Pro** | **daily** | **nonstream** | **200，JPEG 1024×1024，454,888 bytes** | **10.443 s** |
| **us4 / 33** | **Ultra** | **daily** | **nonstream** | **200，JPEG 1024×1024，449,232 bytes** | **14.402 s** |
| **us5 / 21** | **Pro** | **daily** | **stream** | **200，JPEG 1024×1024，467,805 bytes** | **8.907 s** |
| us4 / 33 | Ultra | prod | nonstream | 429 `RESOURCE_EXHAUSTED`，无图片 | 0.153 s |

耗时是该请求从发出到读取完响应的实测值，不含 SSM 投递时间，也不代表 Pro / Ultra 性能排名。us3 / us6 没有继续补测 daily，不能依据它们的 prod 结果认定账号不可用。

文本控制组：us3 / 3、us6 / 27 调用 prod 的 `gemini-3.8-flash-high`，同样返回 429，耗时分别 0.320 s / 0.257 s。它支持“prod 的生成调用问题不限于图片”的判断；本次未定位 Google 内部配额或路由为何如此，也不将它归因为已证实的 IP 封禁。

全部拒绝响应均为：

```json
{
  "error": {
    "code": 429,
    "message": "Resource has been exhausted (e.g. check quota).",
    "status": "RESOURCE_EXHAUSTED"
  }
}
```

## 4. 已成功的请求参数

以下是脱敏后的请求结构。服务端在 edge 内填入真实 project 和 OAuth access token；调用 TokenKey 公共接口的客户端不需要自行构造该外层。

```http
POST https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent
Authorization: Bearer <edge-local-access-token>
Content-Type: application/json
User-Agent: antigravity/cli/1.2.12 darwin/arm64
```

```json
{
  "project": "<account-project-id>",
  "model": "gemini-3.1-flash-image",
  "userAgent": "antigravity",
  "requestType": "image_gen",
  "requestId": "image_gen/<milliseconds>/<uuid>/12",
  "request": {
    "contents": [{
      "role": "user",
      "parts": [{
        "text": "Generate one image of a blue ceramic cup on a plain white background, minimalist product photograph, no text."
      }]
    }],
    "generationConfig": {
      "responseModalities": ["TEXT", "IMAGE"],
      "imageConfig": {"aspectRatio": "1:1", "imageSize": "1K"}
    }
  }
}
```

相同 body 的 daily `streamGenerateContent?alt=sse` 已在 Pro 成功。图片位于 `response.candidates[].content.parts[].inlineData`；不能只拼接 `text`。这几次成功响应都没有可见文字，但有真实图片，`finishReason=STOP`。

## 5. 图片验证与样例

对生成的原始 JPEG 做了 Base64 解码、SHA-256 记录，并在 edge 既有 Python 容器中用 Pillow `Image.load()` **完整解码**；三张均为 RGB、1024×1024。随后制作缩略图带回本地，抽查 Pro / Ultra 样例确为白底蓝色陶瓷杯。

| Pro：daily 非流式 | Ultra：daily 非流式 |
|---|---|
| ![Pro 实测生成的蓝色陶瓷杯](assets/gemini-oauth-probe-20260930/pro-generate.jpg) | ![Ultra 实测生成的蓝色陶瓷杯](assets/gemini-oauth-probe-20260930/ultra-generate.jpg) |

以上是 256×256 缩略图；原图尺寸以完整解码结果为准。[Pro 流式缩略图](assets/gemini-oauth-probe-20260930/pro-stream.jpg)另存。

| 样例 | 原图 SHA-256 |
|---|---|
| Pro / nonstream | `014308f31f0627c91342cfe63bc28ec3afdafaa61fe92c5a3f3d3ed8ae67a2ed` |
| Ultra / nonstream | `581748bd4c32736e8843bc95fb8ebc3333f0393c6755ba468b67dc4619fe7702` |
| Pro / stream | `b652aadf09859d8d8a00a43f94d9efd37dc84795eca8baef7441b2b2cef49b20` |

原图保存在各 edge 的受限临时目录，路径记录在证据 JSON 中；本材料仅归档脱敏摘要和缩略图，不承诺远端 `/tmp` 长期保留。

## 6. Usage、额度和成本能说明什么

| 成功请求 | promptTokenCount | candidatesTokenCount | totalTokenCount |
|---|---:|---:|---:|
| Pro / nonstream | 22 | 1561 | 1583 |
| Ultra / nonstream | 22 | 1542 | 1564 |
| Pro / stream | 22 | 1519 | 1541 |

以上为 Google 返回的 usage 元数据，不能直接换算个人订阅实际扣款。流式响应重复携带相同 usage，统计时不能把两个事件累加成两次费用。

prod 上的 `fetchAvailableModels` 在测试前后均给 us5 Pro / us4 Ultra 的图片模型返回 `remainingFraction=1`；生成后的 daily 复查也为 1，且 resetTime 随复查时间变化。因此不能将这份表面额度视为“无需扣额度”或用它推算每张图消耗。原始摘要见证据文件。

本次没有执行 TokenKey 公网请求，因而不验证其 usage_logs / 用户计费归因；没有调整额外 AI credits 设置，也没有账单证据来承诺零成本或无限量。

## 7. 对现有 TokenKey 的具体建议

1. **保留现有原生 Gemini 通道，并让已确认的 Pro / Ultra 使用 daily。** 本地 [resolveAntigravityForwardBaseURL](../../backend/internal/service/antigravity_gateway_retry.go) 已按保存的 `plan_type` 选择 daily；不是给所有账号全局强制 daily，也不是使用带 `.sandbox` 的另一域名。
2. us4 / us5 当前应用均无 `GATEWAY_ANTIGRAVITY_FORWARD_BASE_URL` 强制覆盖；本地选择逻辑与本次实测方向一致。镜像版本和配置检查不等于已经验证公网请求实际命中该路径。
3. 保持 `requestType=image_gen`、`responseModalities=[TEXT,IMAGE]` 和 Gemini 原生图片 parts 解析；本次有直接成功证据，无需新增 OmniRoute 等中间服务才能拿到图片。
4. 下一项验收应是指定账号的 TokenKey 网关调用与计量归因，继续遵守 [candidate eligibility SSOT](../approved/candidate-eligibility-ssot.md)。本次没有修改映射来绕过网关准入，也没有启用原本不参与调度的 Ultra 账号。
5. 本轮只验证 1K；后续的 2K / 4K、非方形和参考图结果见参数矩阵报告。多轮编辑、刷新后持续可用性和长期容量仍未验证，不能从单图结果扩展承诺。

## 8. 可审计证据

[脱敏探测记录与图像验证结果](assets/gemini-oauth-probe-20260930/evidence.json)包含各次时间、SSM CommandId、账号 ID、host、action、HTTP 状态、请求 ID、耗时、响应摘要哈希、usage 和图片哈希。未包含 access / refresh token、邮箱、真实 project 或图片 Base64。

结论强度：**“所测 Pro / Ultra 在 daily 可生成 1K 图片”已实测；“prod / daily 行为不同”有同账号、同 action 对照；“TokenKey 公网服务与计费已就绪”未验证。**
