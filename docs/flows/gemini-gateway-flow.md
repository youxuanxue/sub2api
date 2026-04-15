# Gemini 平台网关完整流转图

> 从管理员新增 Gemini 账号，到终端用户通过 Claude Code 或 Gemini CLI 访问 Google AI 模型的全链路。
> 支持两种接入方式：`/v1/messages`（Anthropic 兼容层）和 `/v1beta/models/*`（Gemini 原生）。

## 一、管理端配置阶段

### 1.1 新增 Gemini 账号

```
管理员 UI / API
  │
  ▼
admin/account_handler.go :: Create()
  │  绑定 CreateAccountRequest JSON
  │
  ▼
admin_service.go :: CreateAccount()
  │  ├─ 构建 Account{Platform: "gemini", Type: "oauth"|"apikey"}
  │  ├─ Schedulable 默认 true
  │  ├─ 自动绑定 "gemini-default" 分组（若未指定 GroupIDs）
  │  └─ accountRepo.Create() → BindGroups()
  │
  ▼
数据库：accounts 表
  ├─ platform = "gemini"
  ├─ type = "oauth"（Google OAuth）/ "apikey"（AI Studio API Key）
  └─ credentials = {"access_token": "...", "refresh_token": "...", "project_id": "..."}
                 或 {"api_key": "AIza..."}
```

### 1.2 OAuth 账号创建流程（Google OAuth）

```
管理员 UI
  │
  ├─ GET /api/v1/admin/gemini/capabilities
  │    └─ GeminiOAuthHandler.GetCapabilities()
  │
  ├─ POST /api/v1/admin/gemini/oauth/auth-url
  │    └─ GeminiOAuthHandler.GenerateAuthURL()
  │         └─ 生成 Google OAuth 授权 URL（带 PKCE）
  │
  └─ POST /api/v1/admin/gemini/oauth/exchange-code
       └─ GeminiOAuthHandler.ExchangeCode()
            ├─ 兑换授权码 → access_token + refresh_token
            └─ 获取 project_id（Code Assist 路径需要）
```

### 1.3 账号类型说明

| 类型 | 认证方式 | 上游 URL | 场景 |
|------|----------|----------|------|
| `apikey` | x-goog-api-key | `generativelanguage.googleapis.com` | AI Studio API Key |
| `oauth` (无 project_id) | Bearer Token | `generativelanguage.googleapis.com` | AI Studio OAuth |
| `oauth` (有 project_id) | Bearer Token | `cloudcode-pa.googleapis.com` | Code Assist (v1internal) |

### 1.4 关键配置项

| 配置项 | 位置 | 作用 |
|--------|------|------|
| `type` | Account | `oauth` / `apikey` |
| `project_id` | Account.Credentials | Code Assist 项目 ID（影响上游 URL 和请求格式）|
| `base_url` | Account.Credentials | 自定义上游地址（覆盖默认）|
| `model_mapping` | Account | 模型名映射 |
| `image_price_1k/2k/4k` | Group | 图片生成计费配置 |

---

## 二、请求流转阶段

### 2.1 路由总览

```
Anthropic 兼容层（Claude Code 客户端）：
  POST /v1/messages ──────────► GatewayHandler.Messages → GeminiMessagesCompatService.Forward

Gemini 原生（Gemini CLI / SDK）：
  GET  /v1beta/models ────────► GeminiV1BetaListModels
  GET  /v1beta/models/:model ─► GeminiV1BetaGetModel
  POST /v1beta/models/*       ► GeminiV1BetaModels
       例：/v1beta/models/gemini-2.0-flash:generateContent
       例：/v1beta/models/gemini-2.0-flash:streamGenerateContent
       例：/v1beta/models/gemini-2.0-flash:countTokens

Antigravity 套壳（共用 Gemini 处理器）：
  POST /antigravity/v1beta/models/* ► GeminiV1BetaModels（ForcePlatform=antigravity）
```

### 2.2 `/v1/messages`（Anthropic 兼容层）主流程

```
POST /v1/messages（Gemini 分组）
  │  请求体：Anthropic Messages 格式
  │  Header: x-api-key: sk-xxx
  │
  ▼
═══════════════════════════════════════════════════════════
  ① 路由分发
═══════════════════════════════════════════════════════════
  │
  tkOpenAICompatMessagesPOST(h)
  │  ├─ getGroupPlatform(c) → "gemini"
  │  └─ isOpenAICompatPlatform("gemini") → false
  │       └─ h.Gateway.Messages(c)  ← 走 GatewayHandler
  │
  ▼
═══════════════════════════════════════════════════════════
  ② 处理器（handler/gateway_handler.go :: Messages）
═══════════════════════════════════════════════════════════
  │
  GatewayHandler.Messages(c)
  │  ├─ platform == PlatformGemini 分支
  │  ├─ 会话粘性：gemini: + sessionHash
  │  ├─ SelectAccountWithLoadAwareness()
  │  │    └─ selectAccountWithMixedScheduling()
  │  │         └─ 可混入 Antigravity 账号
  │  │
  │  ├─ account.Platform == PlatformAntigravity ?
  │  │    └─ antigravityGatewayService.ForwardGemini()
  │  │
  │  └─ geminiCompatService.Forward(ctx, c, account, body)
  │
  ▼
═══════════════════════════════════════════════════════════
  ③ Anthropic → Gemini 格式转换 + 转发
     （service/gemini_messages_compat_service.go :: Forward）
═══════════════════════════════════════════════════════════
  │
  Forward(ctx, c, account, body)
  │
  ├─ 1. 请求格式转换
  │    └─ convertClaudeMessagesToGeminiGenerateContent()
  │         ├─ Anthropic Messages → Gemini GenerateContent
  │         ├─ system prompt → systemInstruction
  │         ├─ 工具 → tools（cleanToolSchema 清理不支持的 JSON Schema 字段）
  │         ├─ thinking → thinkingConfig
  │         └─ convertClaudeGenerationConfig() → generationConfig
  │
  ├─ 2. 模型映射
  │    └─ account.MapModel(requestedModel)
  │
  ├─ 3. 构建上游请求
  │    │
  │    ├─ API Key 账号：
  │    │    ├─ URL: {base}/v1beta/models/{model}:generateContent
  │    │    │       或 :streamGenerateContent?alt=sse
  │    │    ├─ Header: x-goog-api-key: <api_key>
  │    │    └─ normalizeGeminiRequestForAIStudio()
  │    │         └─ googleSearch → google_search（AI Studio 命名适配）
  │    │
  │    ├─ OAuth 无 project_id（AI Studio OAuth）：
  │    │    ├─ URL: {base}/v1beta/models/{model}:generateContent
  │    │    ├─ Header: Authorization: Bearer <token>
  │    │    └─ normalizeGeminiRequestForAIStudio()
  │    │
  │    └─ OAuth 有 project_id（Code Assist）：
  │         ├─ URL: {GeminiCliBaseURL}/v1internal:generateContent
  │         │       或 :streamGenerateContent?alt=sse
  │         ├─ Header: Authorization: Bearer <token>
  │         ├─ Header: User-Agent: GeminiCLI/...
  │         └─ 请求体包装：{model, project, request: <gemini body>}
  │
  ├─ 4. httpUpstream.Do()
  │
  └─ 5. 响应转换
       │
       ├─ 流式：handleStreamingResponse()
       │    ├─ Gemini SSE → Claude SSE
       │    └─ convertGeminiToClaudeMessage()
       │
       └─ 非流式：handleNonStreamingResponse()
            └─ Gemini JSON → Claude JSON
  │
  ▼
  客户端（Claude Code）收到 Anthropic Messages 格式响应
```

### 2.3 `/v1beta/models/*`（Gemini 原生）主流程

```
POST /v1beta/models/gemini-2.0-flash:streamGenerateContent
  │  请求体：Gemini GenerateContent 格式
  │  Header: x-goog-api-key: xxx 或 Authorization: Bearer xxx
  │
  ▼
═══════════════════════════════════════════════════════════
  ① 中间件链（routes/gateway.go）
═══════════════════════════════════════════════════════════
  │
  ├─ RequestBodyLimit
  ├─ APIKeyAuthWithSubscriptionGoogle（Google 风格错误响应）
  └─ requireGroupGoogle
  │
  ▼
═══════════════════════════════════════════════════════════
  ② 处理器（handler/gemini_v1beta_handler.go :: GeminiV1BetaModels）
═══════════════════════════════════════════════════════════
  │
  GeminiV1BetaModels(c)
  │
  ├─ parseGeminiModelAction(c.Param("modelAction"))
  │    └─ 解析 "gemini-2.0-flash:streamGenerateContent"
  │         → model = "gemini-2.0-flash"
  │         → action = "streamGenerateContent"
  │
  ├─ 模型映射（channel mapping）
  │    └─ ResolveChannelMappingAndRestrict()
  │
  ├─ 会话粘性
  │    ├─ extractGeminiCLISessionHash()
  │    └─ GenerateSessionHash()
  │
  ├─ SelectAccountWithLoadAwareness()
  │
  ├─ account.Platform == PlatformAntigravity ?
  │    └─ antigravityGatewayService.ForwardGemini()
  │
  └─ ForwardNative(ctx, c, account, model, action, stream, body)
  │
  ▼
═══════════════════════════════════════════════════════════
  ③ 原生转发（service/gemini_messages_compat_service.go :: ForwardNative）
═══════════════════════════════════════════════════════════
  │
  ForwardNative()
  │
  ├─ 请求预处理
  │    ├─ 空 parts 过滤
  │    └─ ensureGeminiFunctionCallThoughtSignatures()
  │
  ├─ 构建上游请求（URL/Auth 同 Forward 的三种路径）
  │    ├─ API Key → x-goog-api-key
  │    ├─ OAuth AI Studio → Bearer
  │    └─ OAuth Code Assist → v1internal + 项目包装
  │
  ├─ httpUpstream.Do()
  │
  └─ 响应处理
       ├─ 流式：handleNativeStreamingResponse()
       │    └─ SSE 直传（Code Assist 需 unwrapV1InternalResponse）
       └─ 非流式：handleNativeNonStreamingResponse()
            └─ JSON 直传 / Code Assist unwrap
  │
  ▼
  客户端（Gemini CLI / SDK）收到 Gemini 原生格式响应
```

### 2.4 模型列表

```
GET /v1beta/models
  │
  ▼
GeminiV1BetaListModels(c)
  │  ├─ Antigravity 强制平台 → 返回 antigravity.FallbackGeminiModelsList()
  │  ├─ 有 Antigravity 账号但无原生 Gemini → 同上回退
  │  └─ SelectAccountForAIStudioEndpoints()
  │       └─ ForwardAIStudioGET(account, "/v1beta/models")
  │            └─ 代理上游模型列表
```

---

## 三、上游 URL 决策树

```
GetGeminiBaseURL(defaultBaseURL)
  │
  ├─ account.credentials.base_url 有值？
  │    └─ 使用自定义 base_url
  │
  └─ 使用默认值
       ├─ AI Studio: https://generativelanguage.googleapis.com
       └─ Code Assist: https://cloudcode-pa.googleapis.com

URL 模板：
  ┌──────────────────────────────────────────────────────────────┐
  │ API Key / OAuth (AI Studio)                                  │
  │   {base}/v1beta/models/{model}:{action}[?alt=sse]           │
  │   例: .../v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse │
  ├──────────────────────────────────────────────────────────────┤
  │ OAuth + project_id (Code Assist)                             │
  │   {GeminiCliBaseURL}/v1internal:{action}[?alt=sse]          │
  │   请求体: {model, project, request: <gemini body>}          │
  └──────────────────────────────────────────────────────────────┘
```

---

## 四、错误处理与重试

```
上游错误处理：
  │
  ├─ 429 Rate Limited
  │    └─ geminiCooldownForTier() → 按 tier 级别冷却
  │         └─ shouldRetryGeminiUpstreamError() → 可重试
  │
  ├─ 503 Service Unavailable
  │    └─ shouldFailoverGeminiUpstreamError() → 切换账号
  │
  ├─ Scope 错误（模型列表）
  │    └─ shouldFallbackGeminiModels() → 返回默认模型列表
  │
  └─ 其他错误
       └─ writeGeminiMappedError() / handleGeminiUpstreamError()
            └─ 映射为对应格式错误（Claude / Google 风格）
```

---

## 五、关键文件索引

| 阶段 | 文件 | 核心函数/类型 |
|------|------|---------------|
| OAuth 管理 | `handler/admin/gemini_oauth_handler.go` | `GenerateAuthURL`, `ExchangeCode` |
| OAuth 服务 | `service/gemini_oauth_service.go` | Token 刷新、tier 刷新 |
| 路由注册 | `server/routes/gateway.go` | `/v1beta` 路由组 |
| 原生处理器 | `handler/gemini_v1beta_handler.go` | `GeminiV1BetaModels`, `GeminiV1BetaListModels` |
| Messages 处理器 | `handler/gateway_handler.go` | `Messages`（Gemini 分支）|
| 兼容层服务 | `service/gemini_messages_compat_service.go` | `Forward`, `ForwardNative`, `ForwardAIStudioGET` |
| 格式转换 | `service/gemini_messages_compat_service.go` | `convertClaudeMessagesToGeminiGenerateContent` |
| 账号调度 | `service/gateway_service.go` | `SelectAccountWithLoadAwareness` |
| 默认常量 | `pkg/geminicli/constants.go` | `AIStudioBaseURL`, `GeminiCliBaseURL` |
| 模型列表 | `pkg/gemini/models.go` | `DefaultModels` |
| 平台常量 | `domain/constants.go` | `PlatformGemini` |
