# 严重 Bug 深度审查（2026-04-22）

- 审查者：agent
- 范围：`backend/internal/{service,handler,repository}` + 第五平台 `newapi` 全栈
- 设计哲学：乔布斯 + OPC（One-Person Company）
- **强约束**：所有修复方案必须最小化与 sub2api / new-api upstream 的冲突面；TokenKey 第五平台、品牌相关改动优先于 upstream。

---

## 0. 总览（按严重度排序）

| #  | 标题 | 文件 | 严重度 | 上游冲突面 |
|----|------|------|--------|------------|
| B-1 | NewAPI bridge 上游错误从不喂给 `RateLimitService.HandleUpstreamError` —— 401/402/429/529 永远不会触发账号禁用 / 限流 / 过载状态 | `internal/service/openai_gateway_bridge_dispatch{,_tk_anthropic}.go` 等 | 🟥 P0 | 零（仅 TK 文件） |
| B-2 | Anthropic bridge 路径在 service 层直接写响应（OpenAI/Embeddings/Images 路径不在 service 写）→ handler 行为模型不一致；任何人将 `TkTryWriteNewAPIRelayErrorJSON` 加到 `Messages` handler 都会立刻 double-write | `internal/service/openai_gateway_bridge_dispatch_tk_anthropic.go::ForwardAsAnthropicDispatched` + `internal/handler/tk_newapi_relay_error.go` | 🟨 P3 (文档化) | 零（TK 文件） |
| B-3 | `ratelimit_service.handle401` token_invalidated/token_revoked/`detail:Unauthorized` 仅识别 `PlatformOpenAI`，`PlatformNewAPI`（OpenAI-shape 上游）漏判，账号继续被调度直到 5min 默认锁过期 | `internal/service/ratelimit_service.go:165-184` | 🟧 P1 | 零（仅修条件式） |
| B-4 | `BulkUpdateAccounts` 没有调用 `resolveNewAPIMoonshotBaseURLOnSave`，批量编辑 newapi/Moonshot api_key 后 `base_url` 区域不被重新探测 → 全量 401 | `internal/service/admin_service.go::BulkUpdateAccounts` | 🟧 P1 | 零（仅 TK 调用一处） |
| B-5 | `BatchUpdateCredentials` 把整个 `account.Credentials` 经 `UpdateAccount` 覆写，但只补一个字段 → 批量改 `account_uuid` 时会触发 `resolveNewAPIMoonshotBaseURLOnSave` 在所有 newapi/Moonshot 账号上额外做一次冷探，且 channel_type/base_url 不变也走完整 update 写回 | `internal/handler/admin/account_handler.go::BatchUpdateCredentials` | 🟧 P1 | 零（TK 路径） |
| B-6 | `applyStickyToNewAPIBridge` 对 OpenAI Responses bridge 路径错误地用 ChatCompletions 注入器（写入 `prompt_cache_key` 至 root）—— 当前 `InjectOpenAIChatCompletionsBody == InjectOpenAIResponsesBody` 巧合掩盖；任何注入器分叉立刻劣化 | `internal/service/openai_gateway_bridge_dispatch.go::ForwardAsResponsesDispatched` + `sticky_session_context.go::applyStickyToNewAPIBridge` | 🟨 P2 | 零（TK 文件） |
| B-7 | `tryStickySessionHit` 与 `selectByLoadBalance` 在 `account.IsSchedulable() == false` 时 **不**清理 sticky 映射；rate-limited 账号会让同 sessionHash 的请求每 TTL 期内反复读 cache 命中再被过滤一次 | `internal/service/openai_gateway_service.go::tryStickySessionHit` + `openai_account_scheduler.go::selectBySessionHash` | 🟨 P2 | 零 |
| B-8 | `openai_gateway_service.tryStickySessionHit` 在 `recheckSelectedOpenAIAccountFromDB` 返回 nil（DB 已限流/不可调度）时清 sticky，但 `selectByLoadBalance` 路径成功命中槽位时**没有** refresh sticky TTL — 命中即不刷新，TTL 无端缩短 | `internal/service/openai_account_scheduler.go::selectByLoadBalance` (loop) | 🟨 P2 | 零 |
| B-9 | `payment_webhook_handler.handleNotify` 对 `provider not found` 直接回 200 success，导致渠道误投递的 webhook 永远不会被支付方重试，且日志只 `Warn` 一次后吞掉 | `internal/handler/payment_webhook_handler.go::handleNotify` | 🟨 P2 | 零（TK 拥有该 handler） |
| B-10 | `openai_chat_completions.go` 选号失败 fall-through 到 `selection==nil` 分支；当前**写一次** 503，但日志归因错误（写"No available accounts"，丢失上游 err）；与 `openai_gateway_handler.go::Messages` 同形 | `internal/handler/openai_chat_completions.go` 与 `openai_gateway_handler.go::Messages` | 🟨 P3 | 零 |
| B-11 | `tk_newapi_relay_error.TkTryWriteNewAPIRelayErrorJSON` 用 `c.JSON` 写 `nre.Err.StatusCode`，没有先用 `c.Writer.Header()` 校验是否 4xx/5xx；apiErr 可能携带 0 / 200 状态码（new-api 内部 ErrOptionWithStatusCode 在某些上游协议下未设置）→ 回写 200 + error body | `internal/handler/tk_newapi_relay_error.go` | 🟨 P2 | 零 |

下文给出每条的：1) 触发条件 / 现网影响 2) 根因（带行号引用） 3) 推荐修复（哲学符合度 + 上游冲突面）。

---

## B-1 🟥 P0 — NewAPI bridge 错误**从不**触达 RateLimitService

### 触发

- group.platform=`newapi`（或者四平台账号开启了 channel_type>0 走 bridge）。
- bridge dispatch 收到上游 4xx/5xx，`bridge.DispatchChatCompletions / DispatchResponses / DispatchEmbeddings / DispatchImageGenerations` 返回 `*types.NewAPIError`。
- 或者 `bridge.Dispatch...` 内部 panic（已在 anthropic 路径捕获，会变成普通 error）。

### 现网影响（按严重度）

1. **永久禁用类错误（401 token revoked / 402 余额耗尽 / 400 organization disabled / 400 KYC required）**：账号本应立刻 `SetError` 永久下线，实际 **不会**。每次调度仍把它当可用账号选出，每个用户请求都会走到这个账号 → 上游 401 → `NewAPIRelayError` → 直接透传给客户端，下一秒同一账号再被选中。在故障期间 100% 的请求都失败，且账号**永远不会**自愈或下线。
2. **rate-limit 类（429）**：本应 `SetRateLimited`（按 OpenAI `usage_limit_reached` body 读 `resets_at`），实际 **完全不写**。没有 reset_at 字段，调度池下一秒再选这个账号。同一上游 quota 在重置前会被持续打到，配额掉得更快。
3. **过载（529）**：本应 `SetOverloaded` 进 cooldown，实际 **不进**。账号继续被调度。
4. **403 (Validation/Violation/Generic) / 5xx 通用**：本应 `handle403`（Antigravity 走 forbidden classifier）/ `customErrorCode` / `tempUnsched rule`，全部 **失效**。
5. **stream timeout / 自定义错误码 / passthrough rule**：完全走不到 `RateLimitService`。

可观测性：能看到的只有 `openai_gateway.newapi_bridge_dispatch bridge_path=newapi_adaptor_error account_id=...`，没有任何"账号禁用/限流"日志，运维侧很难定位为什么某个 newapi 账号不停打满。

### 根因

- 非 bridge 路径：`openai_gateway_service.go:1845 / 2877 / 2923 / 3424 / 3559` 等处都会显式调 `s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, body)`。
- bridge 路径：`openai_gateway_bridge_dispatch.go::ForwardAsChatCompletionsDispatched / ForwardAsResponsesDispatched / ForwardAsEmbeddingsDispatched / ForwardAsImageGenerationsDispatched` 与 `openai_gateway_bridge_dispatch_tk_anthropic.go::ForwardAsAnthropicDispatched` 收到 `apiErr` 后**直接** `return nil, &NewAPIRelayError{Err: apiErr}`，没有任何 `HandleUpstreamError` 调用。
- handler 侧 `TkTryWriteNewAPIRelayErrorJSON` 也只渲染响应体，不通知 `RateLimitService`。

```go
// openai_gateway_bridge_dispatch.go:45
out, apiErr := bridge.DispatchChatCompletions(ctx, c, in, body)
if apiErr != nil {
    recordBridgeDispatchError()
    logger.L().Info(...)
    return nil, &NewAPIRelayError{Err: apiErr}   // ← 直接返回，未触达 RateLimitService
}
```

### 推荐修复（OPC：单一漏斗，不污染 upstream）

新增 TK 文件 `internal/service/newapi_bridge_rate_limit_tk.go`：

```go
package service

import (
    "context"
    "net/http"
)

// reportNewAPIBridgeUpstreamError feeds bridge-layer apiErr back into
// RateLimitService, mirroring the non-bridge OpenAI / Anthropic / Gemini paths.
// Single funnel for all four bridge endpoints (chat / responses / embeddings /
// images) so adding a sixth never duplicates HandleUpstreamError wiring.
func (s *OpenAIGatewayService) reportNewAPIBridgeUpstreamError(
    ctx context.Context, account *Account, apiErr *NewAPIRelayError,
) {
    if s == nil || s.rateLimitService == nil || account == nil ||
        apiErr == nil || apiErr.Err == nil {
        return
    }
    status := apiErr.Err.StatusCode
    if status < 100 || status > 599 {
        status = http.StatusBadGateway
    }
    body := []byte(apiErr.Err.Error())
    s.rateLimitService.HandleUpstreamError(ctx, account, status, nil, body)
}
```

然后在 4 个 bridge 调用点（`openai_gateway_bridge_dispatch.go` 三个 + `openai_gateway_bridge_dispatch_tk_anthropic.go` 一个）的 `if apiErr != nil` 分支里加一行 `s.reportNewAPIBridgeUpstreamError(ctx, account, &NewAPIRelayError{Err: apiErr})`。`GatewayService.ForwardAs*Dispatched` 中的 4 处同样处理。

**Sentinel**：把 `reportNewAPIBridgeUpstreamError` 加到 `scripts/newapi-sentinels.json`，`must_contain` 包含函数名，rationale 引用本文档。

**为什么不动 RateLimitService 本身**：HandleUpstreamError 已经是稳定接口；拓展逻辑（apiErr → status/body）放在 TK companion 而不是改公共方法，未来 upstream merge 不会冲突。

---

## B-2 🟨 P3 — Service 层错误响应职责不一致（文档/契约 bug）

### 触发

newapi bridge 收到上游错误。

### 现状

- **Anthropic 路径** (`openai_gateway_bridge_dispatch_tk_anthropic.go::ForwardAsAnthropicDispatched`)：service 层直接 `writeAnthropicError(c, ...)` 写出 ClaudeError 形态响应体（line 101/126/140）。
- **OpenAI ChatCompletions / Responses / Embeddings / Images 路径** (`openai_gateway_bridge_dispatch.go`)：service 层不写响应，由 handler 层的 `TkTryWriteNewAPIRelayErrorJSON` 写 OpenAI-shape `{"error": {...}}`。

### 风险

虽然现状各自可工作（Anthropic handler 不调 `TkTryWriteNewAPIRelayErrorJSON`，OpenAI handler 不接 `writeAnthropicError`），但这是一个**隐性契约**——不是代码合约。任何人："为统一性，给 Messages handler 也加上 `TkTryWriteNewAPIRelayErrorJSON`"，立刻 double-write（写过的 ClaudeError + 又写一次 OpenAI-error，gin 报 warn 或写入两次正文）。

属于"约定优于代码"的脆弱点，符合 OPC "把约定转为机械约束" 的反模式。

### 修复

**OPC 路线**：让 service 层**永远不写响应**，统一由 handler 写：
1. 把 `writeAnthropicError(c, ...)` 三处调用从 `ForwardAsAnthropicDispatched` 删除。
2. service 层 returns `*NewAPIRelayError` (已经在做)。
3. handler `openai_gateway_handler.go::Messages` 在 `errors.As(err, *NewAPIRelayError)` 分支调用新 helper `TkTryWriteNewAPIRelayErrorJSONAsAnthropic`，按 Anthropic 形态写出。
4. 这样 service 层只负责"产生错误"，handler 层负责"序列化错误形态"，与 lifetime 边界一致。

或者更小改动：在两个文件头部加注释 + 在 `tk_newapi_relay_error.go` 加 panic-on-double-write guard：
```go
if c.Writer.Written() {
    return false  // service 层已自行写响应（Anthropic 路径），不要重复写
}
```

第二种零代价。第一种是真正的 OPC 净化方案。两种都 0 上游冲突。

---

## B-3 🟧 P1 — `ratelimit_service.handle401` 漏判 PlatformNewAPI 的 OpenAI-shape 401

### 触发

newapi 账号上游（OpenRouter / Moonshot / DeepSeek 等 OpenAI-compat 渠道）返回 401，body 形如 `{"error":{"code":"token_invalidated"}}` 或 `{"detail":"Unauthorized"}`。

### 现网影响

按 US-023 已修过的"Newapi 走 OpenAI-shape 但 case 漏写"模式：
- `handle401` line 165: `if account.Platform == PlatformOpenAI && (openai401Code == "token_invalidated" || ...)` —— newapi 账号永远走不进这条 break，落到下方的 OAuth 分支或非 OAuth 分支，被 `SetTempUnschedulable(10min)` 而不是 `SetError(永久)`。
- 结果：被上游永久作废的 newapi key 每 10 分钟自动恢复一次，造成"幽灵账号"持续轮询。

### 根因

```go
// ratelimit_service.go:165
if account.Platform == PlatformOpenAI && (openai401Code == ...) {  // ❌
if account.Platform == PlatformOpenAI && gjson.GetBytes(...).String() == "Unauthorized" {  // ❌
```

应改为 `IsOpenAICompatPlatform(account.Platform)`（已存在 helper，定义在 `account_tk_compat_pool.go`）。

### 修复（最小冲突面）

```go
// ratelimit_service.go (within handle401 case 401)
openai401Code := extractUpstreamErrorCode(responseBody)
if IsOpenAICompatPlatform(account.Platform) && (openai401Code == "token_invalidated" || openai401Code == "token_revoked") {
    ...
}
if IsOpenAICompatPlatform(account.Platform) && gjson.GetBytes(responseBody, "detail").String() == "Unauthorized" {
    ...
}
```

`ratelimit_service.go` 是上游沿用文件，但这两行只是 if 条件改写，没有结构调整，未来 upstream merge 冲突风险极低。

也建议把 `case 402` 的 `deactivated_workspace`、`case 400` 的 `organization has been disabled` / `identity verification is required` 同步用 `IsOpenAICompatPlatform`（newapi 后端可能也是 OpenAI-shape）。

---

## B-4 🟧 P1 — `BulkUpdateAccounts` 路径绕过 Moonshot 区域探测

### 触发

管理员对 N 个 newapi/Moonshot 账号一起改 api_key 或 base_url（"批量编辑"）。

### 根因

`admin_service.go::CreateAccount`/`UpdateAccount` 都调了 `resolveNewAPIMoonshotBaseURLOnSave`（lines 1599 / 1764），但 `BulkUpdateAccounts`（line 1789+）**完全没调**。批量编辑直接走 `s.accountRepo.BulkUpdate(ctx, ids, repoUpdates)`，把 credentials 整段写库，区域探测被跳过。

后果：批量把 `.cn` key 改成 `.ai` key 的 N 个账号，base_url 全部仍指向 `api.moonshot.cn`，所有 relay 全 401，且 hot-path 没有 per-request fallback（按设计）。

### 修复

在 `BulkUpdateAccounts` 中（在 `BulkUpdate` 调用之前），如果 `input.Credentials != nil`，先 `accounts := s.accountRepo.GetByIDs(ctx, input.AccountIDs)`，对每个 newapi/Moonshot 账号调用 `resolveNewAPIMoonshotBaseURLOnSave`，把解析后的 `base_url` 注入到该账号的 `repoUpdates.Credentials`（注意：`BulkUpdate` 当前用同一个 credentials 给所有账号，需要切换到 per-account 写）。

更简洁的做法：**禁止** `BulkUpdate` 修改 newapi/Moonshot 账号的 credentials.api_key / base_url —— 让运维必须走单账号 update。这个限制需要在 handler 层报 400 + UI 给提示。`BulkUpdate` 本来就不擅长做高风险修改（混合渠道检查也是同源理由）。

**OPC 推荐**：handler 层在 `BulkUpdate` 时，若 `Credentials` 含 `api_key` 或 `base_url` 且涉及任何 newapi 账号，则先 `unsafe = true` 报 400 + 错误码 `bulk_credentials_unsupported_for_newapi`，由前端给"请逐个编辑"。这避免在 `BulkUpdate` 里复杂化区域探测逻辑。

---

## B-5 🟧 P1 — `BatchUpdateCredentials` 触发不必要的 Moonshot 重新探测

### 触发

管理员用 admin UI "批量改 account_uuid"（`POST /api/v1/admin/accounts/batch-update-credentials`），field=`account_uuid`，accounts 中有 N 个 newapi/Moonshot 账号。

### 根因

`account_handler.go::BatchUpdateCredentials` 把每个账号的 `Credentials[field] = value` 后整体作为 `UpdateAccountInput{Credentials: u.Credentials}` 调用 `UpdateAccount`。`UpdateAccount` 检测到 `len(input.Credentials) > 0`，会把 account.Credentials 全替换，然后**无条件**调 `resolveNewAPIMoonshotBaseURLOnSave(ctx, account)`。

后果：批量改一个 UUID 字段，触发 N 次 Moonshot 国内 + 国际的并行 GET /v1/models 探测（可能 N×2 上游请求 + 25s timeout），且会因 transient network 错误把整个批量操作的某些账号变成失败。

### 修复

`resolveNewAPIMoonshotBaseURLOnSave` 内部已经短路：只在 `base_url` 是 `api.moonshot.cn` / `.ai` 且 api_key 非空时才探测。但只要这两条满足，**任何**字段变更都会触发探测。

简单修复：在 `resolveNewAPIMoonshotBaseURLOnSave` 加一个 dirty 检查 —— 比较 `account.Credentials["base_url"]` / `account.Credentials["api_key"]` 与"上一次持久化的值"。但 service 层拿不到 old 值，需要 handler 传入 hint。

**最简单的 OPC 做法**：
1. `BatchUpdateCredentials` 的 field 限制是 `account_uuid|org_uuid|intercept_warmup_requests`，**没有任何一个**会影响 Moonshot 区域。
2. 让 `UpdateAccountInput` 新增 `SkipMoonshotResolve bool`；handler 在已知 field 不影响区域时设为 true。
3. 或者更彻底：handler 直接走 `accountRepo.UpdateCredentials(ctx, id, creds)`（已经有这个 interface，见 `account_credentials_persistence.go`），不经 `UpdateAccount`，绕过整个 Update 逻辑（包含 group binding 校验等无关操作）。

第 3 种最贴合 OPC："批量改一个字段" 应该有专门的写入路径，而不是复用全量 UpdateAccount。

---

## B-6 🟨 P2 — Sticky 注入器对 Responses bridge 路径用错协议（巧合掩盖中）

### 触发

newapi bridge dispatch 用于 `/v1/responses`（`ForwardAsResponsesDispatched`）。

### 根因

`openai_gateway_bridge_dispatch.go:88`：

```go
body = applyStickyToNewAPIBridge(ctx, c, s.settingService, account, body, "")
```

`applyStickyToNewAPIBridge` 内部硬编码使用 `InjectOpenAIChatCompletionsBody`（注释说明这只是 ChatCompletions 的；当前 `InjectOpenAIChatCompletionsBody` 实现里直接 `return InjectOpenAIResponsesBody(...)` —— 巧合两者都注入 `prompt_cache_key` 到 root，行为一致）。

但是 `sticky_session_injector.go:336` 的注释写得很清楚：
> "InjectOpenAIChatCompletionsBody is currently identical to the Responses shape ... Kept as a separate function so future protocol drift is local."

任何 protocol drift（Responses 把 prompt_cache_key 改名/搬位置）都会立刻让 `/v1/responses` 路径走错注入。

### 修复

把 `applyStickyToNewAPIBridge` 拆成两个：`applyStickyToNewAPIChatBridge`（用 ChatCompletions）+ `applyStickyToNewAPIResponsesBridge`（用 Responses）。`ForwardAsChatCompletionsDispatched` 与 `ForwardAsResponsesDispatched` 各自调对应函数。

零上游冲突（全 TK 文件），改动 ≤ 30 行。

---

## B-7 🟨 P2 — sticky 命中后 `IsSchedulable() == false` 不清 Redis 映射

### 触发

某账号被 `SetRateLimited` 后，`IsSchedulable()` 返回 false。同 sessionHash 的请求带着 sticky 命中。

### 根因

`openai_gateway_service.go:1316`：
```go
if !account.IsSchedulable() || !account.IsOpenAICompatPoolMember(groupPlatform) {
    return nil  // ← 只 return，不删 sticky
}
```

`openai_account_scheduler.go::selectBySessionHash:299` 同样：
```go
if shouldClearStickySession(account, req.RequestedModel) || !account.IsOpenAICompatPoolMember(req.GroupPlatform) || !account.IsSchedulable() {
    _ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
    return nil, nil
}
```

—— 注意 scheduler 路径**有**清理，但 `openai_gateway_service.tryStickySessionHit` 路径**没有**。两路不对称，前者的"账号被限流后下次重试就轮换"是对的。

### 影响

每个用 SelectAccountForModel 系列入口（旧路径）的请求，sticky 直到 TTL 自然过期（默认 1h）才会让位。期间所有同 sessionHash 的请求都先被 sticky cache 命中、再被 IsSchedulable 过滤、再走 load-balance，多一次 cache+DB 读、多一次过滤判定，QPS 高时总成本可观。

### 修复

`openai_gateway_service.tryStickySessionHit` 在 `!IsSchedulable() || !IsOpenAICompatPoolMember()` 分支内补一行 `_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)`，与 scheduler 路径对齐。

---

## B-8 🟨 P2 — scheduler load-balance 命中槽位时不刷 sticky TTL

### 触发

非 sticky 命中（来自 load-balance），但 `req.SessionHash != ""`。

### 根因

`openai_account_scheduler.go::selectByLoadBalance` 在槽位 acquire 成功后只调 `s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, fresh.ID)`（重写映射 + 默认 TTL）。OK，本质上是新 bind，TTL 会重置。

但若是 load-balance 选到了 sticky 已经绑定的同一个账号（即"无新事件，仍命中同账号"），sticky 路径返回 nil（被某个过滤拒了，比如 transport mismatch），fall 到 load-balance，最终又选回这个账号 → BindStickySession 把 TTL 刷新到默认值。

实际行为：TTL 不会缩短，但会重置到默认值，相当于"sticky命中链成功一次"语义被悄悄改成"仅 load-balance 选到时才刷 TTL"。这与 `tryStickySessionHit` 的 `refreshStickySessionTTL(... openAIWSSessionStickyTTL())` 不一致（前者是显式的 refresh，可能用更短/更长的特定 TTL）。

### 影响

不同入口 sticky TTL 行为不一致；高级用户调 `cfg.Gateway.OpenAIWS.StickySessionTTLSeconds` 可能困惑。

### 修复

在 `BindStickySession` 内部统一使用 `openAIWSSessionStickyTTL()`，和 `refreshStickySessionTTL` 同源。

---

## B-9 🟨 P2 — 支付 webhook `provider not found` 静默吞掉

### 触发

第三方支付商（EasyPay、Stripe、Wxpay）把 webhook POST 到我们，但 `out_trade_no` 在数据库中不存在、或 provider key 不匹配。

### 根因

`payment_webhook_handler.go:80`：

```go
provider, err := h.paymentService.GetWebhookProvider(c.Request.Context(), providerKey, outTradeNo)
if err != nil {
    slog.Warn(...)
    writeSuccessResponse(c, providerKey)  // ← 200 success
    return
}
```

向支付方回 success → 支付方认为通知已处理 → **不再重试**。如果是真实订单（比如 DB 短暂不可读），这个订单的状态机就再也走不下去。

### 修复

- 对 `provider not found` 只在确认是"未知订单"（明确 `errors.Is(err, ErrOrderNotFound)`）时回 success；其他错误（DB unavailable / provider not registered）回 500，让支付方自行重试。
- 或者更稳妥：先把 webhook 原文持久化到 `payment_webhook_inbox` 表（带 idempotency key），再异步 worker 处理。这样无论 provider lookup 成功与否，事件都不会丢。

这个 handler 是 TK 添加的（sub2api upstream 不含 payment 模块），改起来零冲突。

---

## B-10 🟨 P3 — selection 失败分支 fall-through 日志归因错误

### 触发

`openai_chat_completions.go::ChatCompletions` 中：

```go
if err != nil {
    if len(failedAccountIDs) == 0 {
        defaultModel := ...
        if defaultModel != "" && defaultModel != reqModel {
            ... try fallback selection ...
        }
        if err != nil {
            h.handleStreamingAwareError(c, ...)
            return
        }
    } else {
        ...
        return
    }
}
if selection == nil || selection.Account == nil {
    h.handleStreamingAwareError(c, ...)
    return
}
```

仔细看：第一个 `if err != nil` 块在 `len(failedAccountIDs) == 0` 路径下，如果第二次 `SelectAccountWithScheduler` 成功（`err == nil && selection != nil`），就会通过外层而不 return —— 此时既不写错误也不进入 selection==nil 分支，**正常 forward**。✅

但如果 `defaultModel == reqModel || defaultModel == ""`，则跳过整个 fallback 块，外层 `err != nil` 仍为 true，但**不进 if err** 内部任何一个分支（因为 `if err != nil` 的内层条件都不命中）。代码继续往下：到 `if selection == nil || selection.Account == nil`，selection 是初次失败的 nil → 写 503 → return。✅

实际仔细检查后发现：`if len(failedAccountIDs) == 0 { ... } else { ... return }` 与外层 `if err != nil` 嵌套不严，**外层 err != nil 但内层 if 走不到 return**—— 例如 `defaultModel == ""` 时外层 if 块没有 return，fall-through 到下面的 selection check。selection 仍是 nil，**写一次 503**。OK，但是**这条路径里 err 不是 nil**，选号失败但代码当成"selection==nil"处理，错误日志结构变形。

### 影响

行为正确（写 503 + return），但日志归因错误（看到"No available accounts"，看不到上游原始 err）。

### 修复

补 `return` 或重构成显式分支链：在外层 `if err != nil` 内任何分支结束后强制 return；`selection==nil` 分支单独处理。改动 < 5 行。

---

## B-11 🟨 P2 — `TkTryWriteNewAPIRelayErrorJSON` 不验证 status code

### 触发

bridge 层 `apiErr.StatusCode` 在某些 new-api 路径被 `ErrOptionWithSkipRetry()` 等快捷构造为 0（例如 `errBridgeMissingCredential` 用 `NewErrorWithStatusCode(..., http.StatusBadRequest, ...)` 显式给 400，但许多 `ErrorCodeXXX` 路径不显式给）。

### 根因

```go
func TkTryWriteNewAPIRelayErrorJSON(c *gin.Context, err error, ...) bool {
    var nre *service.NewAPIRelayError
    if !errors.As(err, &nre) || nre == nil || nre.Err == nil {
        return false
    }
    if c.Writer.Size() == writerSizeBeforeForward && !streamStarted {
        c.JSON(nre.Err.StatusCode, gin.H{"error": nre.Err.ToOpenAIError()})  // ❌ 不验 StatusCode
    }
    return true
}
```

如果 `StatusCode == 0`，`c.JSON(0, ...)` 实际写出 200 + error body，让客户端误以为成功。

### 修复

```go
status := nre.Err.StatusCode
if status < 400 || status > 599 {
    status = http.StatusBadGateway
}
c.JSON(status, gin.H{"error": nre.Err.ToOpenAIError()})
```

---

## 附录 A — 上游冲突面分析

修复方案均符合 CLAUDE.md §5.x "deletion discipline" 与 "minimize upstream divergence"：

| Bug | 涉及文件 | 上游归属 | 修复方式 | 冲突面 |
|-----|----------|----------|----------|--------|
| B-1 | 新建 `newapi_bridge_rate_limit_tk.go` + 4 个 bridge 文件单行注入 | bridge 文件本属 TK companion (sentinel registry 注册) | 新文件 + 一行调用 | 几乎为 0 |
| B-3 | `ratelimit_service.go` if 条件改 helper 调用 | sub2api upstream | 仅条件式微调，不改结构 | 极低 |
| B-4 | `admin_service.go::BulkUpdateAccounts` | sub2api upstream | 在 handler 层加 400 拒绝（推荐方案）= 不改 service | 极低 |
| B-5 | 新增 `accountRepo.UpdateCredentialsField` 或绕道走 `UpdateCredentials` | sub2api upstream | 仅 handler 改路径，service 不改 | 低 |
| B-6 | 拆 `applyStickyToNewAPIBridge` | TK 新文件 | 全 TK | 0 |
| B-7 | `openai_gateway_service.tryStickySessionHit` 加一行 `deleteStickySessionAccountID` | sub2api upstream | 一行 | 低 |
| B-8 | `BindStickySession` 内部 TTL 统一 | sub2api upstream | 一行 | 低 |
| B-9 | `payment_webhook_handler.go` | TK 拥有 | 改逻辑 | 0 |
| B-10 | `openai_chat_completions.go` 加 `return` | sub2api upstream | 一行 | 低 |
| B-11 | `tk_newapi_relay_error.go` | TK 文件 | 一段 if | 0 |

---

## 附录 B — 优先级建议

**第一波 PR（修 B-1 + B-3 + B-11）**：消除 newapi 第五平台运行时的"错误黑洞"——
- B-1：bridge 上游错误从未喂给 RateLimitService（影响所有 newapi 账号的禁用/限流/过载状态）。
- B-3：handle401 漏判 newapi 平台的 OpenAI-shape token_revoked。
- B-11：bridge 错误响应可能写出 200 (因为 status code 默认值)。

三处都是"同一个根因家族：oneapi-shape error 没有走通用处理"，可以合成单 PR with 三个 commit，便于 reviewer 串看。修 B-1 后 sentinel 必须同步更新。

**第二波 PR（修 B-9）**：支付 webhook 静默吞错。独立模块，独立 PR，便于回滚。

**第三波 PR（修 B-4 + B-5）**：管理员批量编辑路径的 newapi 区域探测一致性。两个改动同源，建议一起。

**第四波 PR（修 B-6 + B-7 + B-8）**：sticky/TTL 一致性。改动小但分散，作为单独 PR 减少 review 负担。

**第五波（B-2 + B-10 文档化项）**：可与第四波合并，纯属契约清理 + 一行 return。

每个 PR 都需要：
- 加 sentinel（B-1 修复函数名加到 `newapi-sentinels.json`）
- US-XXX 编号 + `.testing/user-stories/` 故事
- 至少 1 正向 + 1 负向 + 1 回归测试（test-philosophy.mdc §1）
- 在 PR body 里说明本文档对应的 B-编号（"fixes B-1 from docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md"）

---

## 修订记录

- 2026-04-22 v1：初版，11 项 bug。
- 2026-04-22 v1.1：B-2 / B-10 在二次复核时降级为 P3（B-2 从 P0 → P3 文档化项；B-10 从 P2 → P3 日志归因）。B-1（bridge 错误未喂 RateLimitService）+ B-3（handle401 漏判 newapi）+ B-9（支付 webhook 吞错）维持 P0/P1/P2，是本审查的主要发现。
