# spec-delta: 组级不可服务模型负缓存（本地 400 短路）

**Date:** 2026-07-02  
**Depends on:** #1159（`ErrUnsupportedModel` 调度期归类已合并）  
**Companion:** `gateway_service_tk_model_notfound_cache.go`（Anthropic 上游 404 负缓存，60s）  
**Type:** 常规风险 — 网关内部优化；对外 HTTP 契约不变

## Background

prod 2026-07：`api_key_id=172`（Qwen 组 `group_id=18`）持续请求 `gpt-5.4-mini`，在 #1159 之前落库为 routing/platform 429；合并后改为本地 400 + `error_owner=client`，**仍不走上游**。

但每一次请求仍会完整执行账号选择热路径：

- 鉴权 → 列举组内可调度账号 → 渠道定价/映射检查 →（可能）逐账号 upstream 渠道限制扫描 → 池空统计 → 写 400。

客户端 hammer 同一错模型时，浪费的是**网关调度算力**，不是上游配额。Anthropic 已有「上游 404 确认后 60s 负缓存」(`tkModelNotFoundNegativeCache`)，但：

1. 仅 `platform=anthropic`，且在 **Forward 前** 生效（需至少一次真实上游 404 才 populate）。
2. **不覆盖** OpenAI-compat / newapi 调度期本地 `ErrUnsupportedModel`（#1159 主路径）。
3. **不覆盖** Anthropic 调度期本地 unsupported（`tkWrapSelectionFailure` 在选号阶段即返回，未进 Forward）。

目标：对「组内确定不可服务该模型名」的判决做**短时负缓存**，重复请求在选号入口直接 400，跳过账号池扫描。

## Delta

### ADDED

**`tkGroupUnsupportedModelNegativeCache`**（命名可微调，语义固定）

| 项 | 约定 |
|---|---|
| 存储 | 进程内 `go-cache`（与 `tkModelNotFoundNegativeCache` 同_primitive_） |
| TTL | **60s**（与 Anthropic 404 负缓存对齐；配置变更靠 TTL + 主动失效兜底） |
| Key | `group_id` + `\x00` + `lower(trim(requested_model))` |
| Value | 空 struct（存在即判决）；不区分 channel_pricing vs pool_unsupported（客户端契约相同） |
| 读（short-circuit） | 选号**之前**，`group_id != nil` 且 `requested_model` 非空 |
| 写（populate） | 调度器/gateway **首次**因下列原因返回 `ErrUnsupportedModel` 时 |
| 主动失效 | `channelService.invalidateCache()` 时 **flush 整个**负缓存（渠道定价/映射变更后不允许 stale reject） |

**Populate 触发点（写缓存）** — 仅 `errors.Is(err, ErrUnsupportedModel)` 且为**确定性** client fault：

1. `tkOpenAICompatChannelPricingRestrictionError`（组级 `checkChannelPricingRestriction`）
2. `openAICompatNoCandidateError` 且 `tkSelectionFailedDueToUnsupportedModel(stats)`（含 upstream 渠道限制导致的 pure unsupported）
3. `tkWrapSelectionFailure` 且 `tkSelectionFailedDueToUnsupportedModel(stats)`（Anthropic/Gemini 等同路径）

**Short-circuit 挂点（读缓存）** — 单一入口，避免 handler 分叉：

- `OpenAIGatewayService.selectAccountWithScheduler` 入口（在 `checkChannelPricingRestriction` 之前或紧后合并为一次 lookup）
- `GatewayService.SelectAccountWithLoadAwareness` 入口（Anthropic/Gemini 对齐）
- 命中时直接返回 `ErrUnsupportedModel`（复用现有 error 链），**不**列举账号、**不**走 load-balance

**Handler / Ops 契约**

- Short-circuit 命中仍走 `tkSelectFailureStatusMessage` / `tkWriteUnsupportedModelIfApplicable` 既有 400 路径。
- 必须调用 `markOpsClientRequestRejected(c)`（与首次判决一致）。
- 日志：`tk_group_unsupported_negative_cache_hit` / `_populate`（info，含 `group_id`、`model`、`ttl`）。

**Sentinel**（`scripts/sentinels/gateway-tk.json`）

- 新 companion：`gateway_service_tk_group_unsupported_cache.go`（或扩展现有 `*_tk_errors.go` 若更简洁）
- 锚点：`tkGroupUnsupportedModelNegativeCache`、`tkGroupUnsupportedModelShortCircuit`、`tkGroupUnsupportedModelRecord`
- 回归测试文件 + 函数名锚点

### MODIFIED

- `channel_service.go`：`invalidateCache()` 回调 Gateway 负缓存 flush（通过注入的 invalidator 或 Gateway 注册的 hook，避免 service→handler 反转；优先 **callback 接口** 于 `GatewayService` / `OpenAIGatewayService` 构造时注册）。
- 不修改对外 HTTP status/body/error type。

### REMOVED

- 无。

### NOT in scope（明确不做）

| 排除项 | 原因 |
|---|---|
| 真容量空池 429 | 瞬态，负缓存会误伤退避语义 |
| `group_id == nil` | 无稳定组维度，保持现状 |
| Redis 跨副本共享 | 优化缓存，非正确性；与 Anthropic 404 缓存一致做 per-replica |
| 按 `api_key_id` 分 key | 同组错模型对所有 key 成立；多 key 不增加维度 |
| 替代 Anthropic 上游 404 缓存 | 互补；上游 404 缓存仍在 Forward 前处理「已转发后确认不存在」 |
| 缓存 403 / 上游 400 | 仅调度期 `ErrUnsupportedModel` |

## Scenarios

### 核心正向

1. **Given** group 18 渠道定价不允许 `gpt-5.4-mini`，**When** 同一 replica 60s 内第 2+ 次相同 `(group_id, model)` 请求，**Then** 选号入口 short-circuit → HTTP 400 `invalid_request_error`，**不调用** `ListSchedulable*` / load-balance。
2. **Given** Qwen 组池内账号全因 `model_unsupported`（含 upstream 渠道限制）被拒，**When** 重复请求，**Then** 同上。

### 核心负向

1. **Given** 组内有人能服务该模型、只是当下全忙/被限，**When** 空池 429，**Then** **不** populate 负缓存；后续请求仍走完整选号。
2. **Given** 负缓存已命中，**When** 管理员修改渠道可服务模型列表并触发 `invalidateCache()`，**Then** 下一次请求重新走完整选号（可成功或新判决）。

### 回归

1. Anthropic `tkModelNotFoundShortCircuit`（上游 404 路径）行为不变。
2. #1159 已有测试（`TestSelectAccountWithScheduler_QwenGroupWrongModelReturnsUnsupported`、`channel_pricing_restriction_returns_400`）在**无缓存冷启动**下仍通过。
3. TTL 过期后（测试用小 TTL）自动恢复完整选号。

## Validation

```bash
go test -tags=unit ./internal/service/... -run 'TestTkGroupUnsupported|TestSelectAccountWithScheduler_CacheHit|TestChannelInvalidateCache_Flushes|TestProvideTKGroupUnsupported|TestP01_Scheduler_Channel|TestSelectAccountWithScheduler_Qwen|TestOpenAICompatNoCandidate'
go test -tags=unit ./internal/handler/... -run 'Unsupported|ChannelPricing|TkSelectFailure'
go build -o /dev/null ./cmd/server/...
```

2026-07-02: 上述 service/handler 回归测试与 `go build ./cmd/server/...` 已通过（本地）。

## 风险与合并顺序

- **公共契约**：HTTP 400 语义不变；仅减少重复调度工作。`no-web-impact`。
- **陈旧判决窗口**：最长 60s；渠道变更靠 `invalidateCache` flush。可接受——与 Anthropic 404 负缓存同哲学。
- **#1156**：SLA `error_owner` SSOT 独立；本特性不改变分子口径。

## 实现草图（单文件优先）

```
selectAccountWithScheduler / SelectAccountWithLoadAwareness
  └─ tkGroupUnsupportedModelShortCircuit(ctx, groupID, model) → ErrUnsupportedModel?
  └─ ... existing selection ...
  └─ on ErrUnsupportedModel (deterministic) → tkGroupUnsupportedModelRecord(groupID, model)

channelService.invalidateCache()
  └─ registered flush callback → tkGroupUnsupportedModelNegativeCache.Flush()
```

Companion 文件建议：`backend/internal/service/gateway_service_tk_group_unsupported_cache.go`（与 `*_model_notfound_cache.go` 并列，避免 `openai_account_scheduler_tk_errors.go` 继续膨胀）。
