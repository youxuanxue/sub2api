---
title: Gemini Messages conversion and capability failure follow-up
status: approved
approved_by: "feng (conversation approval: 按推荐顺序推进, 2026-09-13)"
created: 2026-09-13
---

# Gemini → Messages 与历史失败修复

用户授权按算术校验、Edge 能力冲突、Gemini→Messages、视觉代表、历史失败复测的顺序修复。
授权覆盖实现、本地验证及低并发定向实测；本变更没有部署或切流动作。

## 协议契约

Gemini generation 在现有 Chat 转换之后增加 Messages target。Plan 与 Execute 共用
`apicompat.GeminiToMessagesRequest` 严格解析实际 body；原生 Gemini 不继承转换限制。
账号、模型、凭据、端点、授权、兼容性排序和计费继续由现有 owner 决定。

支持文本/systemInstruction、单候选、temperature 0–1、topP、输出上限及停止串；
内联 PNG/JPEG/GIF/WebP；functionDeclarations、调用/结果历史与 AUTO/NONE/ANY
（allowedFunctionNames 仅支持单个强制函数）。缺省输出上限为 4096。
Gemini Schema 只转换已定义的公共子集；parametersJsonSchema 原样保留为 JSON Schema。
函数 id 原样续接；无 id 的调用生成确定性关联 id，按名称关联结果仅在无歧义时允许。

思考支持显式 thinkingBudget=0，或 ≥1024 且小于输出上限、includeThoughts=true。
开启思考时不允许强制工具。Messages 的签名包装为带来源标识的 base64 thoughtSignature，
仅允许本转换器来源的签名回传；Gemini 原生签名、动态预算、thinkingLevel、文件 URI、
音视频、缓存引用、安全配置及其他不能保留的字段拒绝该转换边。

返回 candidates、函数调用、thought parts 和 usageMetadata。文本 SSE 即到即发；
工具参数和签名思考在 content_block_stop 后输出，单块上限 4 MiB。
必须收到 message_stop 才发布完成标志；错误安全化，输出后不重试，已知部分用量仍结算。
Messages 没有独立 reasoning token 计数时不捏造 thoughtsTokenCount。

## Edge 与测试修复

`routingSupportedProtocols` 是已有路由投影 owner：当 Antigravity 镜像已验证原生 Gemini
通道时，Plan 使用该通道，在 prod 完成可保留语义的转换，不再依赖 Edge 兼容入口或放宽
中转 key 的转换权限。未验证原生通道的历史 Chat 镜像以及外部供应商保持原声明。
原始探测声明保留，不写账号配置；不支持的思考/图片转换仍需选择其他合法账号。

算术 fixture 已要求“仅最终整数”，判定改为完整答案匹配，拒绝否定句和多答案。
视觉分支按实际验证的模型家族选代表，保留旧失败证据。NVIDIA Build 的有效
max_completion_tokens 转成等值 max_tokens；两者同时出现时沿用 Chat converter 的
max_completion_tokens 优先规则，不增加预算或调用重试，不影响其他供应端。

## Owners 与验证

- `backend/internal/pkg/apicompat/gemini_messages_{request,response}_tk.go`：严格双向转换与流状态。
- `protocolrouter/registry.go`、`service/protocol_execution.go`：唯一选路及绑定执行。
- `service/gemini_messages_forward_tk.go`、`handler/gemini_v1beta_handler_tk_execute.go`：复用实际账号所属的 Messages transport；handler 保留部分用量结算。
- `service/gemini_chat_forward_tk.go`：共享 Gemini 输出 framing、字节上限与安全错误。
- `service/account_supported_protocols.go`：原生通道的 Plan 投影。
- `service/newapi_nvidia_chat_tk.go`、`openai_gateway_bridge_dispatch.go`：NVIDIA 请求字段兼容。
- `ops/stage0/gateway_capability_check.py`、`gateway-account-supply.json`：判定与代表模型。

测试覆盖实际 Plan 正负准入、原生回归、工具/签名续接、未知字段、模型与端点、账号认证、
JSON/SSE、断流与部分用量。HTTP 集成测试不称 UI e2e，也不证明候选代码已在线上运行。
