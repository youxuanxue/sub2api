---
title: Kimi 请求错误修复与请求体边界
status: approved
approved_by: "feng (2026-10-16 conversation: 把上一轮排查的需要我们修复的问题，修复了，开 pr。需要客户端改的问题，列个简要清单和改法。)"
approved_at: 2026-10-16
created: 2026-10-16
authors: [codex]
risk: high
---

# Kimi 请求错误修复与请求体边界

用户授权代码修复、验证和提交 PR；合并、部署另行执行。

## 已确认的问题

2026-10-16 cn-prod 的 Kimi 请求中，火山明确拒绝 `input_audio`；
共享能力门的供应范围、证据和行为由
[多模态供应能力 SSOT](multimodal-supply-capability-ssot.md) 拥有。

同次排查的本地错误记录 139、141 在进入上游前收到 413。Universal Chat
规范化传入 limit=0，触发了与路由配置不同的默认 64 MiB 限制。
本地真实函数复现 64 MiB + 1 被拒绝，显式传配置上限则通过。
历史日志没有保存正文尺寸，不能断言历史请求究竟多大，也不能据此判断视频超过供应限制。

## 请求体契约与 owner

- 路由仍从 `config.GatewayConfig` 取得上限，由 `middleware.RequestBodyLimit`
  限制线上读取并向 request context 传递同一个值。无新增配置或扩大现网配置。
- `httputil.NormalizeLenientJSONRequestBodyForRequest` 统一读取该路由上限；
  Universal 选号准备和 Direct/Universal handler 的宽松 JSON 规范化使用同一规则。
  没有路由上下文的独立调用保留原显式 fallback / 安全默认值。
- 原始正文和规范化后的正文均不得超过该路由上限，包含控制字符转义膨胀。
- HTTP gzip/deflate/zstd 解压仍独立受 64 MiB 安全上限约束。完整读取超过上限返回
  `MaxBytesError`，不能把截断后的前缀当作完整请求。观测专用 prefix API 仍允许截断。
- Universal 413 保留现有协议错误封装，message 增加 `stage=read|decode|normalize`
  和 `limit_bytes`。结构化日志只记阶段、上限、阶段输入字节数和 Content-Length；
  读取失败时阶段输入尺寸记 -1（未知），Content-Length 是客户端声明值，不代表实际读取量。
  不新增正文、媒体内容或密钥日志。
- 网关允许完整请求通过不代表上游允许该媒体：Caddy 上限、上游整请求体上限、
  单个视频文件上限分别生效。100 MB 的本地合成测试不是上游媒体成功承诺。

## 验证

- `TestUniversalBodyConfiguredLimitParity`：Direct/Universal 的 65 MiB 与 100 MB
  合成媒体请求在 100 MB 配置下保持原字节通过；上限 + 1 返回 413。
- `TestUniversalBodyNormalizationExpansionLimit`：原正文未超限，控制字符转义后超限，
  在选号前以 normalize 阶段 413 拒绝。
- `TestDecodeContentEncodedBodyRejectsOversizeWithoutTruncation`：gzip/deflate/zstd
  解压恰好 64 MiB 成功，64 MiB + 1 拒绝；观测前缀读取语义不变。
- 多模态门测试见其 SSOT，包含 Direct/Universal、Chat/Responses、混合输入和未知供应回归。

## 客户端改法

| 错误字段 | 调整 |
|---|---|
| `input_audio` | 火山 Kimi K3 不接受原生音频；先转写成文字或改用明确支持音频的模型。Moonshot 当前官方 Chat API 也未提供该类型，不能作为已验证的音频替代路径。 |
| `stop` | 最多四项；去掉不需要的停止词或省略该字段。 |
| `tools` | Chat 工具使用 `type:function`、合法且非空的函数名，`function.parameters` 为 JSON Schema 对象。 |
| `tool_choice.allowed_tools` | 引用项使用 function、函数名非空且唯一，并对应 tools 中的定义。 |
| `thinking` | 使用 boolean 或 `{ "type": "enabled|disabled|auto" }` 中一个合法值；多个开关不得冲突。 |
| `messages` | role 非空且合法；content 为字符串或合法内容数组，不能是数字。 |
| 请求尺寸 | 计算序列化后的完整 JSON 字节数（base64 约膨胀 4/3）；超过任何一层限制就缩小媒体、减少历史或使用供应支持的公网 URL。 |
| 重试 | 400/413 修改输入后再试；429 遵循 Retry-After，临时 5xx 使用有上限的退避。 |

通用 `InvalidParameter`（上游未返回具体字段）仍需要脱敏参数结构和 Request ID 定位，
不能凭错误码自动删参数或声称已经修复。未知数值参数应按对应供应文档范围修正。
