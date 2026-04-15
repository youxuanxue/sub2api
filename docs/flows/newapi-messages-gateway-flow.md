# NewAPI 平台 `/v1/messages` 网关完整流转图

> 从管理员新增 NewAPI 账号，到终端用户通过 Claude Code（CC Switch）成功调用模型的全链路。

## 一、管理端配置阶段

### 1.1 新增 NewAPI 账号

```
管理员 UI / API
  │
  ▼
admin/account_handler.go :: Create()
  │  绑定 CreateAccountRequest JSON
  │
  ▼
account_handler_tk_newapi_validate.go :: tkValidateNewAPIAccountCreate()
  │  校验：
  │  ├─ platform == "newapi"
  │  ├─ channel_type > 0（必须，决定上游适配器类型，如 VolcEngine=45）
  │  └─ credentials["base_url"] 非空（上游 API 地址）
  │
  ▼
admin_service.go :: CreateAccount()
  │  ├─ 构建 Account{Platform: "newapi", ChannelType: 45, Credentials: {...}}
  │  ├─ Schedulable 默认 true
  │  ├─ 解析 groupIDs（显式指定 或 自动绑定 "newapi-default" 分组）
  │  ├─ checkMixedChannelRisk()：检查同分组混用不同 channel_type 的风险
  │  └─ accountRepo.Create() → BindGroups()
  │
  ▼
数据库：accounts 表
  ├─ platform = "newapi"
  ├─ channel_type = 45 (VolcEngine)
  ├─ credentials = {"base_url": "https://ark.cn-beijing.volces.com", "api_key": "***"}
  └─ model_mapping = {"claude-3-5-sonnet-20241022": "doubao-seed-2-0-mini-260215"}
```

### 1.2 创建/配置 NewAPI 分组

```
管理员 UI / API
  │
  ▼
admin/group_handler.go :: Create() / Update()
  │
  ▼
admin_service.go :: CreateGroup()
  │  ├─ platform = "newapi"
  │  ├─ allow_messages_dispatch = true（newapi 平台自动开启）
  │  └─ 绑定 API Keys 到该分组
  │
  ▼
数据库：groups 表
  ├─ platform = "newapi"
  └─ allow_messages_dispatch = true
```

### 1.3 关键配置项


| 配置项                       | 位置                  | 作用                                |
| ------------------------- | ------------------- | --------------------------------- |
| `channel_type`            | Account             | 决定 New API 适配器类型（VolcEngine=45 等） |
| `model_mapping`           | Account             | 将客户端请求的模型名映射为上游实际模型名              |
| `base_url`                | Account.Credentials | 上游 API 的基础地址                      |
| `api_key`                 | Account.Credentials | 上游 API 密钥                         |
| `allow_messages_dispatch` | Group               | 是否允许 `/v1/messages` 路由到该分组        |
| `newapi_bridge_enabled`   | Settings            | 全局开关，控制是否启用 New API 桥接适配器         |


---

## 二、请求流转阶段

### 2.1 全链路时序图

```
┌──────────┐    ┌─────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
│CC Switch │    │ Gateway  │    │ Handler  │    │ Service  │    │ Bridge   │
│(Claude   │    │ Routes + │    │ OpenAI   │    │ OpenAI   │    │ New API  │
│ Code)    │    │Middleware│    │ Gateway  │    │ Gateway  │    │ Adaptor  │
└────┬─────┘    └────┬─────┘    └────┬─────┘    └────┬─────┘    └────┬─────┘
     │               │               │               │               │
     │ POST /v1/     │               │               │               │
     │ messages      │               │               │               │
     │ (Anthropic    │               │               │               │
     │  格式)        │               │               │               │
     │──────────────►│               │               │               │
     │               │               │               │               │
     │               │ ①中间件链     │               │               │
     │               │──────────────►│               │               │
     │               │               │               │               │
     │               │               │ ②账号选择     │               │
     │               │               │──────────────►│               │               
     │               │               │               │               │
     │               │               │ ③ForwardAs    │               │
     │               │               │ Anthropic     │               │
     │               │               │ Dispatched    │               │
     │               │               │──────────────►│               │
     │               │               │               │               │
     │               │               │               │ ④Bridge       │
     │               │               │               │ Dispatch      │
     │               │               │               │──────────────►│
     │               │               │               │               │
     │               │               │               │               │──► 上游 API
     │               │               │               │               │    (VolcEngine
     │               │               │               │               │     etc.)
     │               │               │               │               │◄──
     │               │               │               │               │
     │               │               │               │ ⑤响应转换     │
     │               │               │               │◄──────────────│
     │               │               │               │               │
     │               │               │ ⑥Anthropic    │               │
     │               │               │  格式响应     │               │
     │               │               │◄──────────────│               │
     │               │               │               │               │
     │  Anthropic    │               │               │               │
     │  格式响应     │               │               │               │
     │◄──────────────│               │               │               │
     │               │               │               │               │
```

### 2.2 详细调用链

```
POST /v1/messages
  │  请求体：Anthropic Messages 格式
  │  Header: x-api-key: sk-xxx 或 Authorization: Bearer sk-xxx
  │
  ▼
═══════════════════════════════════════════════════════════
  ① 中间件链（routes/gateway.go :: RegisterGatewayRoutes）
═══════════════════════════════════════════════════════════
  │
  ├─ RequestBodyLimit          请求体大小限制
  ├─ ClientRequestID           注入 X-Request-Id
  ├─ OpsErrorLogger            运维错误日志记录
  ├─ InboundEndpointMiddleware 标准化入站端点
  ├─ apiKeyAuth                API Key 认证
  │    └─ middleware/api_key_auth.go
  │         ├─ 验证 API Key 有效性 + 订阅状态
  │         ├─ 设置 Gin Context: APIKey, AuthSubject
  │         └─ setGroupContext(c, apiKey.Group)
  │              └─ context.WithValue(ctx, ctxkey.Group, group)
  │                   ↑ 这一步将 Group 注入 ctx，后续
  │                     resolveOpenAICompatPlatform 读取
  │
  └─ requireGroupAnthropic     要求 Key 必须绑定分组
  │
  ▼
═══════════════════════════════════════════════════════════
  ① 路由分发（routes/gateway_tk_openai_compat_handlers.go）
═══════════════════════════════════════════════════════════
  │
  tkOpenAICompatMessagesPOST(h)
  │  ├─ getGroupPlatform(c) → 读 apiKey.Group.Platform
  │  └─ isOpenAICompatPlatform(platform)
  │       └─ platform == "openai" || platform == "newapi"
  │            │
  │            ├─ true  → h.OpenAIGateway.Messages(c)  ← NewAPI 走这里
  │            └─ false → h.Gateway.Messages(c)         ← 原生 Anthropic
  │
  ▼
═══════════════════════════════════════════════════════════
  ② 处理器（handler/openai_gateway_handler.go :: Messages）
═══════════════════════════════════════════════════════════
  │
  ├─ 检查 AllowMessagesDispatch（分组级别开关）
  ├─ 解析请求体 → reqModel, isStream
  ├─ 模型映射 → effectiveMappedModel（group.ModelMapping）
  │
  ▼
═══════════════════════════════════════════════════════════
  ③ 账号调度（service/openai_gateway_service.go）
═══════════════════════════════════════════════════════════
  │
  SelectAccountWithScheduler(ctx, groupID, ..., routingModel, excludedIDs, transport)
  │
  ├─ listSchedulableAccounts(ctx, groupID)
  │    │
  │    ├─ resolveOpenAICompatPlatform(ctx)
  │    │    └─ 从 ctx 读 ctxkey.Group
  │    │         ├─ Group.Platform == "newapi" → return "newapi"
  │    │         └─ 其他 → return "openai"
  │    │
  │    └─ 按 platform 过滤账号
  │         ├─ schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, "newapi", false)
  │         └─ 或 accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, "newapi")
  │
  ├─ filterAndRank（openai_account_scheduler.go）
  │    ├─ isOpenAICompatAccount(account)
  │    │    └─ account.Platform == "openai" || account.Platform == "newapi"
  │    ├─ account.IsModelSupported(requestedModel)
  │    │    └─ 检查 model_mapping 中是否包含请求的模型
  │    ├─ isAccountTransportCompatible(account, transport)
  │    └─ 负载感知 + 粘性会话选择
  │
  └─ 返回 AccountSelectionResult{Account, MappedModel, ...}
  │
  ▼
═══════════════════════════════════════════════════════════
  ④ 转发决策 + 桥接分发
═══════════════════════════════════════════════════════════
  │
  ForwardAsAnthropicDispatched(ctx, c, account, body, cacheKey, defaultModel)
  │  (service/openai_gateway_bridge_dispatch_tk_anthropic.go)
  │
  ├─ ShouldDispatchToNewAPIBridge(account, "chat_completions")
  │    └─ accountUsesNewAPIAdaptorBridge(settings, account, endpoint)
  │         ├─ account.ChannelType > 0 ?            ← 必须
  │         ├─ settings.IsNewAPIBridgeEnabled(ctx) ? ← 全局开关
  │         └─ endpoint ∈ {chat_completions, responses, embeddings, images} ?
  │
  │  ┌───────────────────────────────────────────────────────┐
  │  │ false → ForwardAsAnthropic(...)                       │
  │  │          直接转发到上游 Anthropic 兼容 API             │
  │  └───────────────────────────────────────────────────────┘
  │
  │  ┌───────────────────────────────────────────────────────┐
  │  │ true → 进入 New API 桥接路径 ↓                        │
  │  └───────────────────────────────────────────────────────┘
  │
  ├─ 1. 解析 Anthropic Messages 请求体
  ├─ 2. 模型解析：originalModel / upstreamModel / billingModel
  │       └─ upstreamModel = account.MapModel(originalModel)
  ├─ 3. anthropicToChatCompletionsBody()
  │       └─ Anthropic Messages → OpenAI Chat Completions JSON
  ├─ 4. 构建桥接输入
  │       ├─ newAPIBridgeChannelInput(account)
  │       │    └─ ChannelContextInput{ChannelType, BaseURL, APIKey, Model...}
  │       └─ bridgeAuthFromGin(c) → 获取认证信息
  │
  ▼
═══════════════════════════════════════════════════════════
  ⑤ Bridge 分发（relay/bridge/dispatch.go）
═══════════════════════════════════════════════════════════
  │
  ensureNewAPIDeps()           ← sync.Once 保证只初始化一次
  │  ├─ service.InitHttpClient()      初始化 New API 全局 HTTP Client
  │  ├─ StreamingTimeout = 300        设置流式超时（秒）
  │  └─ StreamScannerMaxBufferMB = 128  流式扫描缓冲区上限
  │
  DispatchChatCompletions(ctx, c, in, chatBody)
  │
  ├─ installBodyStorage(c, chatBody) → 将请求体注入 gin.Context
  ├─ json.Unmarshal → dto.GeneralOpenAIRequest
  ├─ PopulateContextKeys(c, in)
  │    └─ 设置 channel_type, base_url, api_key 等到 gin context
  ├─ GenRelayInfo(c)
  │    └─ 根据 channel_type + URL path 确定 relay mode
  │         例如 /v1/chat/completions → RelayModeTextCompletions
  ├─ RunOpenAITextRelay(c, relayInfo, apiType)
  │    └─ 选择对应 channel_type 的适配器（如 VolcEngine）
  │         ├─ 构建上游请求（重写 URL / Headers / Body）
  │         ├─ 发送 HTTP 请求到真实上游
  │         └─ 流式/非流式处理响应
  │
  └─ 返回 DispatchOutcome{Usage, Model, UpstreamModel, AdaptorRelayFmt, ...}
  │
  ▼
═══════════════════════════════════════════════════════════
  ⑥ 响应格式转换（回到 ForwardAsAnthropicDispatched）
═══════════════════════════════════════════════════════════
  │
  bridge 将上游响应写入 captureWriter（内存缓冲区）
  │
  ├─ 流式请求：convertBufferedChatCompletionsToAnthropicSSE()
  │    ├─ 逐行解析 SSE data: {...} 
  │    ├─ tkBridgeChatChunkToAnthropicEvents() 
  │    │    └─ Chat Completions chunk → Anthropic stream events
  │    │         ├─ message_start
  │    │         ├─ content_block_start / content_block_delta
  │    │         ├─ message_delta (stop_reason, usage)
  │    │         └─ message_stop
  │    └─ 写入真实 c.Writer（SSE 格式）
  │
  └─ 非流式请求：convertBufferedChatCompletionsToAnthropicJSON()
       ├─ 聚合所有 chunks 为完整文本/工具调用
       ├─ 构建 apicompat.AnthropicResponse
       └─ c.JSON(200, response)
  │
  ▼
  客户端（CC Switch / Claude Code）收到标准 Anthropic 格式响应
```

---

## 三、调试历程中发现的关键问题

### 3.1 问题时间线

```
问题 #1: 503 Service temporarily unavailable
  │  原因：listSchedulableAccounts 只查 platform="openai"，
  │        newapi 账号被过滤掉，无可用账号
  │  修复：引入 resolveOpenAICompatPlatform(ctx)，
  │        根据 Group.Platform 动态决定查询平台
  │
  ▼
问题 #2: 502 Upstream request failed (api_key not found)
  │  原因：选到 newapi 账号后走了原生 ForwardAsAnthropic，
  │        直接用 Anthropic 协议访问非 Anthropic 上游
  │  修复：实现 ForwardAsAnthropicDispatched，
  │        newapi 账号走 bridge dispatch 路径
  │
  ▼
问题 #3: 编译错误 (hash.Hash64 类型)
  │  原因：account.go 中 fnv.Hash64a 类型签名不匹配
  │  修复：改为 hash.Hash64 接口类型
  │
  ▼
问题 #4: 502 Bridge dispatch panicked (nil pointer)
  │  原因：New API 的全局 http.Client 未初始化
  │        sub2api 不运行 new-api 的 main()，
  │        service.InitHttpClient() 从未被调用
  │  修复：bridge/dispatch.go 中用 sync.Once 懒初始化 http.Client
  │
  ▼
问题 #5: 400 model does not exist
  │  原因：VolcEngine 需要用正确的模型标识符
  │        (doubao-seed-2-0-mini-260215)，不是显示名
  │  修复：用户更新 model_mapping 为正确的模型名
  │
  ▼
问题 #6: 502 Bridge dispatch panicked (non-positive interval for NewTicker)
  │  原因：New API 的 constant.StreamingTimeout 未初始化（值为 0），
  │        time.NewTicker(0) panic
  │  修复：ensureNewAPIDeps() 中增加 StreamingTimeout
  │        和 StreamScannerMaxBufferMB 的默认值初始化
  │
  ▼
✅ 成功：CC Switch 通过 /v1/messages 访问 VolcEngine 模型
```

### 3.2 核心设计洞察

**sub2api 将 new-api 作为库引入，而非独立服务。** new-api 的 `main()` 不会被执行，因此其全局初始化逻辑（HTTP Client、常量默认值等）不会自动运行。所有被 sub2api 使用的 new-api 全局状态，必须由 bridge 层通过 `ensureNewAPIDeps()` 显式初始化。

```
┌─────────────────────────────────────────────────────────────────┐
│                      sub2api 进程                               │
│                                                                 │
│  ┌───────────────────┐     ┌──────────────────────────────┐     │
│  │   sub2api 自身     │     │  new-api（作为 Go module）    │     │
│  │                   │     │                              │     │
│  │  handler/         │     │  relay/channel/*   适配器     │     │
│  │  service/         │     │  relay/helper/*    流处理     │     │
│  │  repository/      │     │  dto/*            请求结构    │     │
│  │  middleware/       │     │  service/         HTTP Client│     │
│  │  relay/bridge/ ───┼────►│  constant/        运行时常量  │     │
│  │                   │     │                              │     │
│  │  ensureNewAPIDeps │     │  ⚠ main() 不执行             │     │
│  │  负责初始化 ──────┼────►│  ⚠ 全局变量需外部初始化      │     │
│  └───────────────────┘     └──────────────────────────────┘     │
└─────────────────────────────────────────────────────────────────┘
```

---

## 四、关键文件索引


| 阶段           | 文件                                                       | 核心函数/类型                                                                               |
| ------------ | -------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| 账号创建         | `handler/admin/account_handler.go`                       | `Create`                                                                              |
| 账号校验         | `handler/admin/account_handler_tk_newapi_validate.go`    | `tkValidateNewAPIAccountCreate`                                                       |
| 分组创建         | `service/admin_service.go`                               | `CreateGroup`                                                                         |
| 路由注册         | `server/routes/gateway.go`                               | `RegisterGatewayRoutes`                                                               |
| 路由分发         | `server/routes/gateway_tk_openai_compat_handlers.go`     | `tkOpenAICompatMessagesPOST`                                                          |
| 认证中间件        | `server/middleware/api_key_auth.go`                      | `apiKeyAuth`                                                                          |
| 处理器入口        | `handler/openai_gateway_handler.go`                      | `Messages`                                                                            |
| 平台识别         | `service/openai_gateway_service_tk_platform.go`          | `resolveOpenAICompatPlatform`, `isOpenAICompatAccount`                                |
| 账号调度         | `service/openai_gateway_service.go`                      | `SelectAccountWithScheduler`, `listSchedulableAccounts`                               |
| 调度过滤         | `service/openai_account_scheduler.go`                    | `filterAndRank`                                                                       |
| 转发决策         | `service/openai_gateway_bridge_dispatch.go`              | `ShouldDispatchToNewAPIBridge`                                                        |
| 桥接门控         | `service/gateway_bridge_dispatch.go`                     | `accountUsesNewAPIAdaptorBridge`                                                      |
| Anthropic 桥接 | `service/openai_gateway_bridge_dispatch_tk_anthropic.go` | `ForwardAsAnthropicDispatched`                                                        |
| 格式转换         | 同上                                                       | `anthropicToChatCompletionsBody`, `convertBufferedChatCompletionsToAnthropicSSE/JSON` |
| Bridge 分发    | `relay/bridge/dispatch.go`                               | `ensureNewAPIDeps`, `DispatchChatCompletions`                                         |
| 直接转发         | `service/openai_gateway_messages.go`                     | `ForwardAsAnthropic`                                                                  |


