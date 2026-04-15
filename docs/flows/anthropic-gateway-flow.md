# Anthropic 平台网关完整流转图

> 从管理员新增 Anthropic 账号，到终端用户通过 Claude Code 访问原生 Anthropic API 的全链路。
> 平台标识为 `anthropic`（非 `claude`），处理器为 `GatewayHandler`。

## 一、管理端配置阶段

### 1.1 新增 Anthropic 账号

```
管理员 UI / API
  │
  ▼
admin/account_handler.go :: Create()
  │  绑定 CreateAccountRequest JSON
  │  Anthropic 账号无特殊前置校验
  │
  ▼
admin_service.go :: CreateAccount()
  │  ├─ 构建 Account{Platform: "anthropic", Type: "oauth"|"apikey"|"setup-token"|"bedrock"}
  │  ├─ Schedulable 默认 true
  │  ├─ 自动绑定 "anthropic-default" 分组（若未指定 GroupIDs）
  │  └─ accountRepo.Create() → BindGroups()
  │
  ▼
数据库：accounts 表
  ├─ platform = "anthropic"
  ├─ type = "oauth"（Claude OAuth）/ "apikey"（API Key）/ "setup-token" / "bedrock"
  └─ credentials = {"access_token": "...", "refresh_token": "..."} 或 {"api_key": "sk-ant-..."}
```

### 1.2 账号类型说明


| 类型            | 认证方式         | 上游 URL                            | 特殊行为                              |
| ------------- | ------------ | --------------------------------- | --------------------------------- |
| `oauth`       | Bearer Token | `api.anthropic.com`               | 自动刷新 Token、mimic Claude Code、指纹注入 |
| `apikey`      | x-api-key    | `api.anthropic.com` 或自定义 base_url | 可启用 API Key 透传模式                  |
| `setup-token` | Bearer Token | `api.anthropic.com`               | 类似 OAuth，不同初始化流程                  |
| `bedrock`     | AWS SigV4    | AWS Bedrock endpoint              | 走独立 Bedrock 转发路径                  |


### 1.3 创建/配置分组

```
管理员 UI / API
  │
  ▼
admin_service.go :: CreateGroup()
  │  ├─ platform 默认值为 "anthropic"（未指定时）
  │  ├─ model_routing（模型路由配置）
  │  ├─ claude_code_only（是否仅允许 Claude Code 客户端）
  │  ├─ fallback_group_id_on_invalid_request（无效请求回退分组）
  │  └─ require_oauth_only（是否仅允许 OAuth 账号）
```

### 1.4 关键配置项


| 配置项                                    | 位置            | 作用                                      |
| -------------------------------------- | ------------- | --------------------------------------- |
| `type`                                 | Account       | 认证类型 (oauth/apikey/setup-token/bedrock) |
| `privacy_mode`                         | Account.Extra | OAuth 隐私模式（`training_off`）              |
| `model_mapping`                        | Account       | 模型名映射                                   |
| `anthropic_apikey_passthrough`         | Account.Extra | API Key 透传模式                            |
| `anthropic_cache_ttl_override`         | Account.Extra | 缓存 TTL 覆盖                               |
| `claude_code_only`                     | Group         | 仅允许 Claude Code 客户端                     |
| `model_routing`                        | Group         | 按模型路由到不同账号子集                            |
| `fallback_group_id_on_invalid_request` | Group         | 无效请求回退分组                                |


---

## 二、请求流转阶段

### 2.1 全链路时序图

```
┌──────────┐    ┌─────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
│Claude    │    │ Gateway  │    │ Gateway  │    │ Gateway  │    │Anthropic │
│Code /    │    │ Routes + │    │ Handler  │    │ Service  │    │ API      │
│Client    │    │Middleware│    │          │    │          │    │          │
└────┬─────┘    └────┬─────┘    └────┬─────┘    └────┬─────┘    └────┬─────┘
     │               │               │               │               │
     │ POST /v1/     │               │               │               │
     │ messages      │               │               │               │
     │ (Anthropic)   │               │               │               │
     │──────────────►│               │               │               │
     │               │               │               │               │
     │               │ ①中间件链     │               │               │
     │               │──────────────►│               │               │
     │               │               │               │               │
     │               │               │ ②路由判断     │               │
     │               │               │ platform !=   │               │
     │               │               │ openai/newapi  │               │
     │               │               │               │               │
     │               │               │ ③账号选择     │               │
     │               │               │──────────────►│               │
     │               │               │               │               │
     │               │               │ ④Forward      │               │
     │               │               │──────────────►│               │
     │               │               │               │──────────────►│
     │               │               │               │   Anthropic   │
     │               │               │               │   Messages    │
     │               │               │               │   API         │
     │               │               │               │◄──────────────│
     │               │               │               │               │
     │               │               │◄──────────────│               │
     │  Anthropic    │               │               │               │
     │  原生响应     │               │               │               │
     │◄──────────────│               │               │               │
```

### 2.2 详细调用链

```
POST /v1/messages
  │  请求体：Anthropic Messages 格式（原生）
  │  Header: x-api-key: sk-xxx 或 Authorization: Bearer sk-xxx
  │
  ▼
═══════════════════════════════════════════════════════════
  ① 中间件链（routes/gateway.go :: RegisterGatewayRoutes）
═══════════════════════════════════════════════════════════
  │
  ├─ RequestBodyLimit
  ├─ ClientRequestID
  ├─ OpsErrorLogger
  ├─ InboundEndpointMiddleware
  ├─ apiKeyAuth
  │    └─ 验证 API Key → 设置 AuthSubject + Group 到 ctx
  └─ requireGroupAnthropic
  │
  ▼
═══════════════════════════════════════════════════════════
  ② 路由分发（routes/gateway_tk_openai_compat_handlers.go）
═══════════════════════════════════════════════════════════
  │
  tkOpenAICompatMessagesPOST(h)
  │  ├─ getGroupPlatform(c) → "anthropic"
  │  └─ isOpenAICompatPlatform("anthropic") → false
  │       └─ h.Gateway.Messages(c)  ← Anthropic 走这里（非 OpenAI 路径）
  │
  ▼
═══════════════════════════════════════════════════════════
  ③ 处理器（handler/gateway_handler.go :: Messages）
═══════════════════════════════════════════════════════════
  │
  GatewayHandler.Messages(c)
  │
  ├─ 读取 + 解析请求体
  │    └─ ParseGatewayRequest(body, PlatformAnthropic)
  │
  ├─ Claude Code 检测
  │    ├─ SetClaudeCodeClientContext(c, body)
  │    ├─ checkClaudeCodeVersion(c, apiKey)
  │    └─ Claude Code Only 分组限制
  │
  ├─ 计费 / 并发控制
  │    ├─ 用户级并发限制
  │    ├─ 账号级并发限制
  │    └─ billing header 签名
  │
  ├─ 会话粘性
  │    ├─ GenerateSessionHash(body)
  │    ├─ GetCachedSessionAccountID(sessionHash)
  │    └─ BindStickySession() → 成功后绑定
  │
  ├─ ⚡ 账号选择（失败重试循环）
  │    │
  │    └─ SelectAccountWithLoadAwareness()
  │         ├─ checkClaudeCodeRestriction() → 可能切换分组
  │         ├─ selectAccountWithMixedScheduling()
  │         │    └─ 允许混入 Antigravity 账号（若启用 mixed_scheduling）
  │         ├─ 负载感知 + 优先级排序
  │         ├─ RequirePrivacySet 过滤
  │         └─ 并发槽获取 + 等待
  │
  └─ ⚡ 转发
       │
       ├─ Antigravity 账号且非 API Key：
       │    └─ antigravityGatewayService.Forward()
       │
       └─ Anthropic 账号：
            └─ gatewayService.Forward(ctx, c, account, parsedReq)
  │
  ▼
═══════════════════════════════════════════════════════════
  ④ 转发（service/gateway_service.go :: Forward）
═══════════════════════════════════════════════════════════
  │
  Forward(ctx, c, account, parsedRequest)
  │
  ├─ API Key 透传模式？
  │    └─ account.IsAnthropicAPIKeyPassthroughEnabled()
  │         └─ forwardAnthropicAPIKeyPassthroughWithInput()
  │              ├─ buildUpstreamRequestAnthropicAPIKeyPassthrough()
  │              │    ├─ URL: api.anthropic.com/v1/messages?beta=true
  │              │    ├─ Header: x-api-key: <api_key>
  │              │    ├─ Header: anthropic-version: 2023-06-01
  │              │    └─ 最小化请求改写
  │              └─ httpUpstream.Do()
  │
  ├─ Bedrock 账号？
  │    └─ forwardBedrock()
  │         └─ AWS SigV4 签名 → Bedrock endpoint
  │
  └─ 标准 OAuth / API Key 路径：
       │
       ├─ evaluateBetaPolicy()
       │    └─ 检查 beta 特性策略 → 可能阻断
       │
       ├─ OAuth 请求规范化
       │    ├─ normalizeClaudeOAuthRequestBody()
       │    │    └─ 清理请求体、系统消息规范化
       │    ├─ enforceCacheControlLimit()
       │    │    └─ 限制 cache_control 块数量 ≤ 4
       │    └─ claude.NormalizeModelID()
       │         └─ 模型 ID 标准化
       │
       ├─ GetAccessToken(ctx, account)
       │    └─ OAuth: 从缓存或刷新获取 Bearer Token
       │    └─ API Key: 直接返回 api_key
       │
       ├─ buildUpstreamRequest()
       │    ├─ URL: https://api.anthropic.com/v1/messages?beta=true
       │    ├─ OAuth → Authorization: Bearer <token>
       │    ├─ API Key → x-api-key: <key>
       │    ├─ anthropic-version: 2023-06-01
       │    ├─ anthropic-beta: 按策略合并
       │    ├─ OAuth 指纹注入 ApplyFingerprint()
       │    └─ 白名单 headers 透传
       │
       ├─ httpUpstream.DoWithTLS()
       │    └─ 支持代理 + TLS 指纹
       │
       ├─ 重试逻辑
       │    ├─ 签名错误重试 shouldRectifySignatureError()
       │    ├─ 预算/思维块重试 FilterThinkingBlocksForRetry()
       │    └─ HTTP 级别重试
       │
       └─ 响应处理
            ├─ 流式：handleStreamingResponse()
            │    ├─ SSE 逐行扫描
            │    ├─ processSSEEvent()
            │    │    ├─ usage 解析 parseSSEUsage()
            │    │    ├─ 缓存 TTL 覆盖
            │    │    ├─ 模型名替换 replaceModelInSSELine()
            │    │    └─ 终止检测 anthropicStreamEventIsTerminal()
            │    └─ keepalive + 超时
            │
            └─ 非流式：handleNonStreamingResponse()
                 └─ JSON 响应体处理
  │
  ▼
═══════════════════════════════════════════════════════════
  ⑤ 用量记录
═══════════════════════════════════════════════════════════
  │
  gatewayService.RecordUsage(ctx, forwardResult)
  │  ├─ 配额扣减
  │  ├─ RPM 增量（OAuth/setup-token 账号）
  │  └─ Codex 用量快照更新（OAuth 账号）
  │
  ▼
  客户端收到 Anthropic 原生 Messages 格式响应
```

---

## 三、平台特有功能

### 3.1 Claude Code 特性

```
Claude Code 客户端特殊处理：
  │
  ├─ 客户端检测
  │    └─ SetClaudeCodeClientContext() → 解析 metadata.user_id
  │
  ├─ 版本检查
  │    └─ checkClaudeCodeVersion() → 可拒绝过旧版本
  │
  ├─ 分组限制
  │    └─ ClaudeCodeOnly 分组 → 非 Claude Code 请求被拒
  │
  ├─ 会话管理
  │    └─ X-Claude-Code-Session-Id 同步
  │
  ├─ OAuth Mimic
  │    ├─ applyClaudeCodeMimicHeaders()
  │    ├─ rewriteSystemForNonClaudeCode()
  │    └─ 模拟 Claude Code 客户端行为
  │
  └─ 计费
       └─ syncBillingHeaderVersion()
       └─ signBillingHeaderCCH()
```

### 3.2 Beta 策略

```
evaluateBetaPolicy()
  │
  ├─ 检查请求所需的 beta 特性
  │    └─ requestNeedsBetaFeatures()
  │
  ├─ 策略过滤
  │    └─ getBetaPolicyFilterSet()
  │         └─ 部分 beta 特性可被策略阻断 → BetaBlockedError
  │
  └─ Header 合并
       ├─ OAuth: applyClaudeOAuthHeaderDefaults()
       └─ API Key: defaultAPIKeyBetaHeader + InjectBetaForAPIKey
```

### 3.3 Prompt Cache

```
缓存控制：
  │
  ├─ enforceCacheControlLimit()
  │    └─ 限制 cache_control 块 ≤ 4（避免过度缓存）
  │
  ├─ SSE 事件中缓存 TTL 覆盖
  │    └─ account.IsCacheTTLOverrideEnabled()
  │
  └─ reconcileCachedTokens()
       └─ 调和缓存命中/未命中 token 计数
```

---

## 四、关键文件索引


| 阶段          | 文件                                                   | 核心函数/类型                                                              |
| ----------- | ---------------------------------------------------- | -------------------------------------------------------------------- |
| 路由注册        | `server/routes/gateway.go`                           | `RegisterGatewayRoutes`                                              |
| 路由分发        | `server/routes/gateway_tk_openai_compat_handlers.go` | `tkOpenAICompatMessagesPOST`                                         |
| 认证中间件       | `server/middleware/api_key_auth.go`                  | `apiKeyAuth`                                                         |
| 处理器         | `handler/gateway_handler.go`                         | `Messages`, `CountTokens`, `Models`                                  |
| 账号调度        | `service/gateway_service.go`                         | `SelectAccountWithLoadAwareness`, `selectAccountWithMixedScheduling` |
| 转发（标准）      | `service/gateway_service.go`                         | `Forward`, `buildUpstreamRequest`                                    |
| 转发（透传）      | `service/gateway_service.go`                         | `forwardAnthropicAPIKeyPassthroughWithInput`                         |
| 转发（Bedrock） | `service/gateway_service.go`                         | `forwardBedrock`                                                     |
| 流式处理        | `service/gateway_service.go`                         | `handleStreamingResponse`, `processSSEEvent`                         |
| Beta 策略     | `service/gateway_service.go`                         | `evaluateBetaPolicy`                                                 |
| Claude Code | `handler/gateway_handler.go`                         | `SetClaudeCodeClientContext`, `checkClaudeCodeVersion`               |
| 模型映射        | `pkg/claude/models.go`                               | `NormalizeModelID`                                                   |
| 平台常量        | `domain/constants.go`                                | `PlatformAnthropic`                                                  |


