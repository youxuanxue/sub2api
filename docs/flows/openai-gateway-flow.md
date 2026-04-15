# OpenAI 平台网关完整流转图

> 从管理员新增 OpenAI 账号，到终端用户通过客户端访问 OpenAI 兼容模型的全链路。
> 覆盖 `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/embeddings`、`/v1/images/generations` 全部端点。

## 一、管理端配置阶段

### 1.1 新增 OpenAI 账号

```
管理员 UI / API
  │
  ▼
admin/account_handler.go :: Create()
  │  绑定 CreateAccountRequest JSON
  │  OpenAI 账号无特殊前置校验（不同于 NewAPI 需 channel_type > 0）
  │
  ▼
admin_service.go :: CreateAccount()
  │  ├─ 构建 Account{Platform: "openai", ChannelType: 0, Type: "oauth"|"apikey"|...}
  │  ├─ Schedulable 默认 true
  │  ├─ 自动绑定 "openai-default" 分组（若未指定 GroupIDs）
  │  ├─ OAuth 账号：异步 goroutine → EnsureOpenAIPrivacy()
  │  └─ accountRepo.Create() → BindGroups()
  │
  ▼
数据库：accounts 表
  ├─ platform = "openai"
  ├─ channel_type = 0（直连，不走 New API bridge）
  ├─ type = "oauth" / "apikey"
  └─ credentials = {"access_token": "...", "refresh_token": "..."} 或 {"api_key": "sk-..."}
```

### 1.2 创建/配置 OpenAI 分组

```
管理员 UI / API
  │
  ▼
admin/group_handler.go :: Create()
  │
  ▼
admin_service.go :: CreateGroup()
  │  ├─ platform = "openai"
  │  ├─ allow_messages_dispatch（可选，控制是否允许 /v1/messages 路由）
  │  ├─ default_mapped_model（/v1/messages 默认映射模型）
  │  ├─ require_oauth_only（是否只允许 OAuth 账号）
  │  ├─ require_privacy_set（是否要求隐私已设置）
  │  └─ messages_dispatch_model_config（/v1/messages 模型调度配置）
  │
  ▼
数据库：groups 表
  ├─ platform = "openai"
  └─ allow_messages_dispatch = true/false
```

### 1.3 关键配置项


| 配置项                                            | 位置            | 作用                                                        |
| ---------------------------------------------- | ------------- | --------------------------------------------------------- |
| `type`                                         | Account       | `oauth`（Codex OAuth）/ `apikey`（API Key 直连）                |
| `channel_type`                                 | Account       | `0` = 直连 OpenAI；`> 0` = 走 New API bridge（此时行为同 NewAPI 平台） |
| `model_mapping`                                | Account       | 模型名映射                                                     |
| `openai_passthrough`                           | Account.Extra | 启用透传模式（最小化请求改写）                                           |
| `openai_oauth_responses_websockets_v2_enabled` | Account.Extra | 启用 WebSocket v2 通道                                        |
| `allow_messages_dispatch`                      | Group         | 是否允许 `/v1/messages` 路由到该分组                                |
| `require_privacy_set`                          | Group         | 是否要求账号隐私模式已设置                                             |
| `default_mapped_model`                         | Group         | `/v1/messages` 的默认映射模型                                    |


---

## 二、请求流转阶段

### 2.1 全端点路由总览

```
POST /v1/chat/completions ─────► OpenAIGatewayHandler.ChatCompletions
POST /v1/responses          ────► OpenAIGatewayHandler.Responses
POST /v1/responses/*subpath ────► OpenAIGatewayHandler.Responses
GET  /v1/responses          ────► OpenAIGatewayHandler.ResponsesGet
POST /v1/messages           ────► OpenAIGatewayHandler.Messages     ← 需 AllowMessagesDispatch
POST /v1/embeddings         ────► OpenAIGatewayHandler.Embeddings
POST /v1/images/generations ────► OpenAIGatewayHandler.ImageGenerations
WS   /v1/responses          ────► OpenAIGatewayHandler.ResponsesWebSocket

所有端点共享路由判断：
  tkOpenAICompatMessagesPOST / tkOpenAICompat* 系列
  └─ isOpenAICompatPlatform(group.Platform)
       └─ "openai" || "newapi" → OpenAIGateway 处理器
```

### 2.2 `/v1/chat/completions` 主流程（最典型路径）

```
POST /v1/chat/completions
  │  请求体：OpenAI Chat Completions 格式
  │  Header: Authorization: Bearer sk-xxx
  │
  ▼
═══════════════════════════════════════════════════════════
  ① 中间件链（routes/gateway.go）
═══════════════════════════════════════════════════════════
  │
  ├─ RequestBodyLimit
  ├─ ClientRequestID
  ├─ OpsErrorLogger
  ├─ InboundEndpointMiddleware
  ├─ apiKeyAuth → 验证 Key + 注入 Group 到 ctx
  └─ requireGroupAnthropic
  │
  ▼
═══════════════════════════════════════════════════════════
  ② 处理器（handler/openai_chat_completions.go）
═══════════════════════════════════════════════════════════
  │
  OpenAIGatewayHandler.ChatCompletions(c)
  │  ├─ 解析请求体 → model, stream
  │  ├─ 模型映射（Group.ModelMapping）
  │  ├─ SelectAccountWithScheduler()
  │  │    ├─ 粘性会话（session hash）
  │  │    ├─ 负载感知选择
  │  │    └─ 模型白名单过滤
  │  │
  │  └─ ForwardAsChatCompletionsDispatched()
  │
  ▼
═══════════════════════════════════════════════════════════
  ③ 转发决策（service/openai_gateway_bridge_dispatch.go）
═══════════════════════════════════════════════════════════
  │
  ForwardAsChatCompletionsDispatched()
  │
  ├─ ShouldDispatchToNewAPIBridge(account, "chat_completions")
  │    └─ channel_type > 0 && bridge_enabled ?
  │
  │  ┌───────────────────────────────────────────────────┐
  │  │ true  → bridge.DispatchChatCompletions()          │
  │  │          （走 New API 适配器，参见 newapi 文档）    │
  │  └───────────────────────────────────────────────────┘
  │
  │  ┌───────────────────────────────────────────────────┐
  │  │ false → ForwardAsChatCompletions()  ← 直连路径    │
  │  └───────────────────────────────────────────────────┘
  │
  ▼
═══════════════════════════════════════════════════════════
  ④ 直连转发（service/openai_gateway_chat_completions.go）
═══════════════════════════════════════════════════════════
  │
  ForwardAsChatCompletions()
  │  ├─ ChatCompletionsToResponses()
  │  │    └─ 将 Chat Completions 请求体转换为 Responses API 格式
  │  ├─ GetAccessToken(ctx, account)
  │  ├─ buildUpstreamRequest()
  │  │    ├─ URL: account.GetOpenAIBaseURL() + "/v1/responses"
  │  │    ├─ Auth: Authorization: Bearer <token>
  │  │    └─ 保留白名单 headers
  │  ├─ httpUpstream.Do()
  │  │    └─ 支持代理（account.Proxy）
  │  └─ 响应处理
  │       ├─ 流式：handleStreamingResponse()
  │       └─ 非流式：handleNonStreamingResponse()
  │
  ▼
  客户端收到 OpenAI Chat Completions 格式响应
```

### 2.3 `/v1/responses` 流程

```
POST /v1/responses
  │
  ▼
OpenAIGatewayHandler.Responses(c)
  │  ├─ 解析请求体 → model, stream, previous_response_id
  │  ├─ previous_response_id 粘性会话
  │  ├─ SelectAccountWithScheduler()
  │  └─ ForwardAsResponsesDispatched()
  │       │
  │       ├─ bridge → bridge.DispatchResponses()
  │       └─ 直连 → Forward()
  │            │
  │            ├─ Passthrough 模式：
  │            │    └─ forwardOpenAIPassthrough()
  │            │         └─ buildUpstreamRequestOpenAIPassthrough()
  │            │              └─ 最小化请求改写，直传上游
  │            │
  │            ├─ WebSocket v2：
  │            │    └─ forwardResponsesWebSocketV2()
  │            │
  │            └─ 标准模式：
  │                 ├─ 可选 Codex 模型检测
  │                 ├─ buildUpstreamRequest()
  │                 └─ httpUpstream.DoWithTLS()
  │
  ▼
  客户端收到 OpenAI Responses API 格式响应
```

### 2.4 `/v1/messages`（Anthropic 兼容层）

```
POST /v1/messages（OpenAI 分组）
  │  请求体：Anthropic Messages 格式
  │
  ▼
OpenAIGatewayHandler.Messages(c)
  │  ├─ 检查 AllowMessagesDispatch
  │  ├─ 解析 Anthropic 请求 → model, stream
  │  ├─ SelectAccountWithScheduler()
  │  └─ ForwardAsAnthropicDispatched()
  │       │
  │       ├─ bridge → 走 ForwardAsAnthropicDispatched 的桥接路径
  │       └─ 直连 → ForwardAsAnthropic()
  │            │
  │            ├─ AnthropicToResponses()
  │            │    └─ 将 Anthropic Messages → OpenAI Responses 格式
  │            ├─ buildUpstreamRequest()
  │            │    └─ URL: /v1/responses
  │            ├─ httpUpstream.Do()
  │            └─ 响应转换
  │                 ├─ 流式：Responses SSE → Anthropic SSE
  │                 └─ 非流式：Responses JSON → Anthropic JSON
  │
  ▼
  客户端收到 Anthropic Messages 格式响应
```

### 2.5 Embeddings / Images

```
POST /v1/embeddings
  │
  ▼
OpenAIGatewayHandler.Embeddings(c)
  └─ ForwardAsEmbeddingsDispatched()
       ├─ bridge → bridge.DispatchEmbeddings()
       └─ 直连 → ForwardAsEmbeddings()
            └─ forwardOpenAIV1JSON()
                 ├─ buildOpenAIV1TargetURL(base, "embeddings")
                 └─ httpUpstream.Do()

POST /v1/images/generations
  │
  ▼
OpenAIGatewayHandler.ImageGenerations(c)
  └─ ForwardAsImageGenerationsDispatched()
       ├─ bridge → bridge.DispatchImageGenerations()
       └─ 直连 → ForwardAsImageGenerations()
            └─ forwardOpenAIV1JSON()
                 ├─ buildOpenAIV1TargetURL(base, "images/generations")
                 └─ httpUpstream.Do()
```

---

## 三、账号调度详细流程

```
SelectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, model, excludedIDs, transport)
  │
  ├─ resolveOpenAICompatPlatform(ctx)
  │    └─ Group.Platform == "openai" → return "openai"
  │
  ├─ 尝试粘性会话
  │    ├─ previousResponseID → 强绑定同一账号
  │    └─ sessionHash → GetCachedSessionAccountID → 优先同一账号
  │
  ├─ scheduler.Select() （若存在 OpenAIAccountScheduler）
  │    │
  │    └─ filterAndRank()
  │         ├─ isOpenAICompatAccount(a) → a.Platform ∈ {"openai", "newapi"}
  │         ├─ a.IsSchedulable()
  │         ├─ a.IsModelSupported(model)
  │         ├─ isAccountTransportCompatible(a, transport)
  │         ├─ RequirePrivacySet → a.IsPrivacySet()
  │         └─ 负载感知排序（并发数/优先级/最近使用）
  │
  └─ 回退：SelectAccountWithLoadAwareness()
       └─ selectBestAccount() → 并发槽获取 + 等待
```

---

## 四、与其他平台的关键差异


| 维度                | OpenAI（直连）                        | NewAPI（bridge）                                   | Anthropic          | Gemini                   |
| ----------------- | --------------------------------- | ------------------------------------------------ | ------------------ | ------------------------ |
| 处理器               | OpenAIGatewayHandler              | OpenAIGatewayHandler                             | GatewayHandler     | GatewayHandler           |
| channel_type      | 0                                 | > 0                                              | N/A                | N/A                      |
| 上游协议              | OpenAI Responses API              | 按 channel_type 适配                                | Anthropic Messages | Gemini GenerateContent   |
| `/v1/messages` 转换 | Anthropic → Responses → Anthropic | Anthropic → ChatCompletions → bridge → Anthropic | 原生                 | Claude → Gemini → Claude |
| 认证                | Bearer (OAuth) / API Key          | 由 bridge 处理                                      | x-api-key / Bearer | x-goog-api-key / Bearer  |
| Passthrough       | 支持（最小改写）                          | 不支持                                              | 支持（API Key 透传）     | 不支持                      |


---

## 五、关键文件索引


| 阶段                | 文件                                                   | 核心函数/类型                                            |
| ----------------- | ---------------------------------------------------- | -------------------------------------------------- |
| 账号创建              | `handler/admin/account_handler.go`                   | `Create`                                           |
| 分组创建              | `service/admin_service.go`                           | `CreateGroup`                                      |
| 路由注册              | `server/routes/gateway.go`                           | `RegisterGatewayRoutes`                            |
| 路由分发              | `server/routes/gateway_tk_openai_compat_handlers.go` | `tkOpenAICompat*`                                  |
| Chat Completions  | `handler/openai_chat_completions.go`                 | `ChatCompletions`                                  |
| Responses         | `handler/openai_gateway_handler.go`                  | `Responses`                                        |
| Messages          | `handler/openai_gateway_handler.go`                  | `Messages`                                         |
| Embeddings/Images | `handler/openai_gateway_embeddings_images.go`        | `Embeddings`, `ImageGenerations`                   |
| 平台识别              | `service/openai_gateway_service_tk_platform.go`      | `resolveOpenAICompatPlatform`                      |
| 账号调度              | `service/openai_account_scheduler.go`                | `SelectAccountWithScheduler`                       |
| Chat转发            | `service/openai_gateway_chat_completions.go`         | `ForwardAsChatCompletions`                         |
| Responses转发       | `service/openai_gateway_service.go`                  | `Forward`, `forwardOpenAIPassthrough`              |
| Messages转发        | `service/openai_gateway_messages.go`                 | `ForwardAsAnthropic`                               |
| V1转发              | `service/openai_gateway_v1_forward.go`               | `ForwardAsEmbeddings`, `ForwardAsImageGenerations` |
| Bridge 分发         | `service/openai_gateway_bridge_dispatch.go`          | `*Dispatched` 系列                                   |
| 上游构建              | `service/openai_gateway_service.go`                  | `buildUpstreamRequest`, `httpUpstream.Do`          |


