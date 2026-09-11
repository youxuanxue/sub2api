---
title: Gemini generateContent to Chat conversion
status: approved
approved_by: "feng (conversation approval, 2026-09-11)"
created: 2026-09-11
---

# Gemini generateContent → Chat

用户在重新审视 #1922 后同意补充受能力契约约束的通用转换边，复用统一授权、
调度和计费。本文替代旧 PR 的供应商窄门及跨账号 identity-first 提议。
授权覆盖实现和本地验证；不代表已上线或已验证任何真实供应源。

## 契约

`generateContent` / `streamGenerateContent` 可通过合法 Chat Target 返回 Gemini
响应。账号仍声明真实 `supported_protocols`；账号模型准入、端点证据、授权范围、
端点权限和账单绑定继续由现有 owner 决定。供应商名和账号 ID 不新增选路权重。
同账号沿用 Plan 的原生优先；跨账号沿用 Candidate Eligibility SSOT 的支付层级、
兼容性、priority、容量和会话规则。Direct / Universal 共用此路径。

转换从原始 Gemini body 严格解析，不能用现有通用 profile 的 text 默认值作证明。
初始子集支持 user/model 文本 parts、systemInstruction，以及 generationConfig 中
temperature、topP、maxOutputTokens、stopSequences、candidateCount=1。
未列入的字段或非法类型拒绝该转换边，包括 tools、toolConfig、图片、音视频、
thoughtSignature、thinkingConfig、cachedContent、safetySettings、结构化输出。
这些限制属于此 converter，不能缩窄原生 Gemini identity。

上游 Chat 文本映射为 Gemini candidates；可观测的 reasoning 文本保留为 thought
parts，不伪造签名或保证签名续接。stop/length/content_filter 映射为对应 finishReason。
未知结束原因、工具输出及畸形响应不能伪装成功。流式逐事件输出，不缓存整段回答；
在真实结束前断流不补 STOP，开始输出后不重放。

usageMetadata 投影 Chat 的 prompt/completion/cache/reasoning 计数；内部计费继续消费
现有 Chat transport 的用量结果及选中路径。部分输出的已知用量仍需记录，错误仍可见。
不新增价格、模型映射、供应商能力或线上配置。CloudWise 的原生 Chat 供给证据属于
独立模型准入流程，不能由协议转换或旧 #113 mapping 推导。

## Owner 与实施范围

- `internal/pkg/apicompat/gemini_chat_tk.go`：原始请求验证及请求/响应字段转换。
- `protocolrouter` registry / preserves：新边只在共享 converter 接受原始 body 时合法。
- `service/protocol_execution.go` 和 Gemini handler：按不可变 Plan 接入执行回调。
- `service/gemini_chat_forward_tk.go`：复用 Chat dispatch/transport，将输出转换成 Gemini。
- 现有 candidate、计费、凭据、故障 owner 继续共享；兼容路径 Vertex 门和 countTokens
  不为此转换放宽。原生 Gemini 的 body 与签名修复语义保持原有路径。

## 验收

必须覆盖真实 body 的正负准入、模型/端点绑定、Direct/Universal 一致性、原生与转换
共同调度、非流式、实时 SSE、用量、失败前重试和部分输出不重放；现有 Gemini
签名/多模态 identity 回归测试必须通过。sentinel 锚住生产调用点和行为测试。
验证为本地单元/HTTP 集成测试，不称 UI e2e 或线上供给验收。
