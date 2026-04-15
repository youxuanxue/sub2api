# Antigravity 平台网关完整流转图

> 从管理员新增 Antigravity 账号，到终端用户通过 Claude Code 或 Gemini CLI 访问 Google Code Assist API 的全链路。
> Antigravity 本质上是对 Google Code Assist（cloudcode-pa.googleapis.com）的客户端模拟。

## 一、管理端配置阶段

### 1.1 Antigravity 是什么

```
Antigravity = Google Code Assist / Cloud Code PA API 的客户端模拟

  ┌────────────────────────────────────────────────────────┐
  │  目标上游：                                            │
  │  ├─ Prod: https://cloudcode-pa.googleapis.com         │
  │  └─ Daily: https://daily-cloudcode-pa.sandbox...      │
  │                                                        │
  │  协议：Google v1internal RPC 风格                      │
  │  认证：Google OAuth (cloud-platform scope)             │
  │  User-Agent：模拟 Antigravity 桌面客户端               │
  │  请求格式：包装为 {project, model, request: <body>}    │
  └────────────────────────────────────────────────────────┘

支持两种模式：
  ├─ OAuth 模式：通过 Google OAuth 获取令牌，调用 v1internal API
  └─ Upstream 模式：直接代理到自定义 Anthropic 兼容上游（AccountTypeUpstream）
```

### 1.2 新增 Antigravity 账号

```
管理员 UI / API
  │
  ▼
方式 1：OAuth 账号（主流）
  │
  ├─ POST /api/v1/admin/antigravity/oauth/auth-url
  │    └─ AntigravityOAuthHandler.GenerateAuthURL()
  │         └─ 生成 Google OAuth URL（固定 ClientID，PKCE，cloud-platform scope）
  │
  ├─ 用户完成 Google 授权
  │
  ├─ POST /api/v1/admin/antigravity/oauth/exchange-code
  │    └─ AntigravityOAuthHandler.ExchangeCode()
  │         ├─ 兑换授权码 → access_token + refresh_token
  │         └─ 获取用户 profile / project 信息
  │
  └─ admin/account_handler.go :: Create()
       └─ admin_service.go :: CreateAccount()
            ├─ Account{Platform: "antigravity", Type: "oauth"}
            ├─ 异步 EnsureAntigravityPrivacy()
            └─ 自动绑定 "antigravity-default" 分组

方式 2：Upstream 账号（自定义上游）
  │
  └─ admin/account_handler.go :: Create()
       └─ Account{Platform: "antigravity", Type: "upstream"}
            ├─ credentials = {"base_url": "https://...", "api_key": "sk-..."}
            └─ 直接代理到上游 /v1/messages（标准 Anthropic 协议）
```

### 1.3 关键配置项


| 配置项                            | 位置            | 作用                                                 |
| ------------------------------ | ------------- | -------------------------------------------------- |
| `type`                         | Account       | `oauth`（Google OAuth）/ `upstream`（自定义上游）/ `apikey` |
| `privacy_mode`                 | Account.Extra | 隐私模式（Antigravity 特定值 `AntigravityPrivacySet`）      |
| `mixed_scheduling`             | Account.Extra | 允许该账号参与 Anthropic/Gemini 分组的混合调度                   |
| `antigravity_credits_overages` | Account.Extra | 信用额度超额配置                                           |
| `model_mapping`                | Account       | 模型映射（默认使用 `DefaultAntigravityModelMapping`）        |
| `image_price_1k/2k/4k`         | Group         | 图片生成计费                                             |
| `fallback_model_antigravity`   | Settings      | 全局回退模型                                             |


### 1.4 创建/配置分组

```
admin_service.go :: CreateGroup()
  │  ├─ platform = "antigravity"
  │  ├─ require_oauth_only（过滤非 OAuth 账号）
  │  └─ fallback_group_id_on_invalid_request
```

### 1.5 混合调度（Mixed Scheduling）

```
Antigravity 账号可以"混入"其他平台的分组：

  Anthropic 分组 ──┐
                   ├─ selectAccountWithMixedScheduling()
  Gemini 分组   ──┘     └─ 同时考虑原生账号 + Antigravity 账号
                              （Antigravity 账号需启用 mixed_scheduling）

  Antigravity 分组 ──► 只使用 Antigravity 账号（不混合）
  
  ForcePlatform 路由（/antigravity/v1/*）──► 仅 Antigravity 账号
```

---

## 二、请求流转阶段

### 2.1 路由总览

```
Anthropic 兼容（Claude 格式）：
  POST /v1/messages ──────────────────► GatewayHandler.Messages
       （通过混合调度选中 Antigravity 账号时）

Antigravity 专属路由（强制平台）：
  POST /antigravity/v1/messages ──────► GatewayHandler.Messages（ForcePlatform）
  POST /antigravity/v1/messages/count_tokens ► GatewayHandler.CountTokens
  GET  /antigravity/v1/models ────────► GatewayHandler.AntigravityModels
  GET  /antigravity/v1/usage ─────────► GatewayHandler.Usage

Gemini v1beta 兼容（Gemini CLI 格式）：
  POST /antigravity/v1beta/models/* ─► GeminiV1BetaModels（ForcePlatform）
  GET  /antigravity/v1beta/models ───► GeminiV1BetaListModels

  GET  /antigravity/models ──────────► GatewayHandler.AntigravityModels
```

### 2.2 Claude 格式请求流程（/v1/messages）

```
POST /v1/messages 或 /antigravity/v1/messages
  │  请求体：Anthropic Messages 格式
  │
  ▼
═══════════════════════════════════════════════════════════
  ① 处理器（handler/gateway_handler.go :: Messages）
═══════════════════════════════════════════════════════════
  │
  GatewayHandler.Messages(c)
  │  ├─ 解析请求体 → ParseGatewayRequest()
  │  ├─ SelectAccountWithLoadAwareness()
  │  │    └─ 混合调度 或 ForcePlatform 直接选择
  │  │
  │  └─ account.Platform == PlatformAntigravity && !APIKey ?
  │       └─ antigravityGatewayService.Forward()
  │
  ▼
═══════════════════════════════════════════════════════════
  ② 转发（service/antigravity_gateway_service.go :: Forward）
═══════════════════════════════════════════════════════════
  │
  Forward(ctx, c, account, body, isStickySession)
  │
  ├─ Upstream 账号类型？
  │    └─ ForwardUpstream()
  │         ├─ URL: {base_url}/v1/messages
  │         ├─ Header: Authorization: Bearer <api_key>
  │         ├─ Header: x-api-key: <api_key>
  │         └─ 直接发送原始 Claude 请求体
  │
  └─ OAuth 账号（主路径）：
       │
       ├─ 1. 解析 Claude 请求体
       │    └─ antigravity.ClaudeRequest 解析
       │
       ├─ 2. 模型映射
       │    └─ account.MapModel() 或 DefaultAntigravityModelMapping
       │         └─ 例: claude-3-5-sonnet → gemini-2.0-flash
       │
       ├─ 3. 获取 Token
       │    └─ AntigravityTokenProvider.GetAccessToken(ctx, account)
       │         └─ 自动刷新（15 分钟窗口）
       │
       ├─ 4. 格式转换
       │    └─ antigravity.TransformClaudeToGeminiWithOptions()
       │         ├─ Anthropic Messages → Gemini GenerateContent
       │         ├─ 思维模式后缀处理
       │         └─ 工具 / 系统消息转换
       │
       ├─ 5. 构建上游请求
       │    └─ antigravity.NewAPIRequestWithURL()
       │         ├─ URL: {base}/v1internal:streamGenerateContent[?alt=sse]
       │         ├─ Header: Authorization: Bearer <access_token>
       │         ├─ Header: User-Agent: antigravity/<version> windows/amd64
       │         └─ Content-Type: application/json
       │
       ├─ 6. 重试循环
       │    └─ antigravityRetryLoop()
       │         ├─ HTTP 发送（带代理）
       │         ├─ 429 → 重试 + URL 回退（prod/daily 交替）
       │         ├─ 503 → 重试
       │         ├─ 特定 400 → stripThinkingFromClaudeRequest() 重试
       │         ├─ 内部 500 惩罚 → antigravity_internal500_penalty
       │         └─ 不可恢复 → AntigravityAccountSwitchError（触发换号）
       │
       └─ 7. 响应转换
            ├─ 流式：Gemini SSE → Claude SSE
            │    └─ handleClaudeStreamingResponse()
            └─ 非流式：handleClaudeStreamToNonStreaming()
                 └─ 聚合流式块 → Claude JSON
  │
  ▼
  客户端收到 Anthropic Messages 格式响应
```

### 2.3 Gemini 格式请求流程（/antigravity/v1beta/models/*）

```
POST /antigravity/v1beta/models/gemini-2.0-flash:streamGenerateContent
  │  请求体：Gemini GenerateContent 格式
  │
  ▼
═══════════════════════════════════════════════════════════
  ① 处理器（handler/gemini_v1beta_handler.go :: GeminiV1BetaModels）
═══════════════════════════════════════════════════════════
  │
  GeminiV1BetaModels(c)
  │  ├─ ForcePlatform == PlatformAntigravity
  │  ├─ parseGeminiModelAction() → model + action
  │  ├─ SelectAccountWithLoadAwareness()
  │  └─ antigravityGatewayService.ForwardGemini()
  │
  ▼
═══════════════════════════════════════════════════════════
  ② 转发（service/antigravity_gateway_service.go :: ForwardGemini）
═══════════════════════════════════════════════════════════
  │
  ForwardGemini(ctx, c, account, model, action, stream, body)
  │
  ├─ 身份补丁注入
  ├─ 可选 Schema 清理
  ├─ wrapV1InternalRequest()
  │    └─ 包装请求体：
  │         {
  │           "project": "<project_id>",
  │           "requestId": "<uuid>",
  │           "userAgent": "antigravity",
  │           "requestType": "agent",
  │           "model": "<mapped_model>",
  │           "request": <原始 Gemini 请求体>
  │         }
  │
  ├─ antigravity.NewAPIRequestWithURL()
  │    └─ URL: {base}/v1internal:streamGenerateContent[?alt=sse]
  │
  ├─ antigravityRetryLoop()
  │
  └─ 响应处理
       ├─ unwrapV1InternalResponse()
       │    └─ 剥离外层包装，返回内部 response
       ├─ 流式：handleGeminiStreamingResponse()
       └─ 非流式：handleGeminiNonStreamingResponse()
  │
  ▼
  客户端收到 Gemini 原生格式响应
```

---

## 三、协议转换概览

```
┌──────────────────────────────────────────────────────────────┐
│                    客户端请求                                 │
│                                                              │
│  Claude Code ───► Anthropic Messages ───┐                    │
│                                         │                    │
│  Gemini CLI ──► Gemini GenerateContent ─┤                    │
│                                         ▼                    │
│                              ┌─────────────────┐             │
│                              │  Gateway 处理器  │             │
│                              └────────┬────────┘             │
│                                       │                      │
│                         ┌─────────────┼─────────────┐        │
│                         │             │             │        │
│                         ▼             ▼             ▼        │
│                   ┌──────────┐  ┌──────────┐  ┌──────────┐   │
│                   │ Claude→  │  │ Gemini→  │  │Upstream  │   │
│                   │ Gemini   │  │ v1internal│  │直接代理  │   │
│                   │ 转换     │  │ 包装     │  │          │   │
│                   └────┬─────┘  └────┬─────┘  └────┬─────┘   │
│                        │             │             │         │
│                        ▼             ▼             ▼         │
│                  ┌─────────────────────────────────────┐     │
│                  │  Google v1internal API               │     │
│                  │  cloudcode-pa.googleapis.com         │     │
│                  │  或 自定义 upstream (/v1/messages)    │     │
│                  └─────────────────────────────────────┘     │
│                                                              │
│                    ┌──────────────────┐                      │
│                    │  响应转换回客户端  │                      │
│                    │  格式（Claude/    │                      │
│                    │  Gemini）         │                      │
│                    └──────────────────┘                      │
└──────────────────────────────────────────────────────────────┘
```

---

## 四、Token 管理

```
AntigravityTokenProvider / AntigravityTokenRefresher

  GetAccessToken(ctx, account)
  │
  ├─ Token 有效？→ 返回缓存 Token
  │
  └─ Token 过期或即将过期（15 分钟窗口）？
       └─ RefreshAccountToken()
            ├─ 使用 refresh_token 向 Google OAuth 刷新
            ├─ 更新 account.credentials
            └─ 更新缓存
```

---

## 五、关键文件索引


| 阶段         | 文件                                           | 核心函数/类型                                       |
| ---------- | -------------------------------------------- | --------------------------------------------- |
| OAuth 管理   | `handler/admin/antigravity_oauth_handler.go` | `GenerateAuthURL`, `ExchangeCode`             |
| OAuth 服务   | `service/antigravity_oauth_service.go`       | Token 兑换、刷新、验证                                |
| 路由注册       | `server/routes/gateway.go`                   | `/antigravity/v1`, `/antigravity/v1beta`      |
| Claude 处理器 | `handler/gateway_handler.go`                 | `Messages`（Antigravity 分支）                    |
| Gemini 处理器 | `handler/gemini_v1beta_handler.go`           | `GeminiV1BetaModels`（Antigravity 分支）          |
| 核心转发       | `service/antigravity_gateway_service.go`     | `Forward`, `ForwardGemini`, `ForwardUpstream` |
| 重试逻辑       | `service/antigravity_gateway_service.go`     | `antigravityRetryLoop`                        |
| 格式转换       | `pkg/antigravity/request_transformer.go`     | `TransformClaudeToGeminiWithOptions`          |
| 响应转换       | `pkg/antigravity/response_transformer.go`    | Gemini → Claude 流转换                           |
| HTTP 客户端   | `pkg/antigravity/client.go`                  | `NewAPIRequestWithURL`                        |
| OAuth 配置   | `pkg/antigravity/oauth.go`                   | ClientID, Scopes, BaseURLs                    |
| 账号调度       | `service/gateway_service.go`                 | `selectAccountWithMixedScheduling`            |
| Token 刷新   | `service/antigravity_token_refresher.go`     | `AntigravityTokenRefresher`                   |
| 配额管理       | `service/antigravity_credits_overages.go`    | 信用额度超额处理                                      |
| 内部惩罚       | `service/antigravity_internal500_penalty.go` | 500 错误惩罚逻辑                                    |
| 默认模型映射     | `domain/constants.go`                        | `DefaultAntigravityModelMapping`              |
| 平台常量       | `domain/constants.go`                        | `PlatformAntigravity`                         |


