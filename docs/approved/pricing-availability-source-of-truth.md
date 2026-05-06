---
title: Pricing Catalog as Single Source of Truth for Model Availability + Pricing
status: approved
approved_by: yyy
approved_at: 2026-05-06
created: 2026-05-06
owners: [tk-platform]
scope: "pricing catalog / model availability observability / upstream model discovery / client model-list endpoints"
---

# Pricing Catalog as Single Source of Truth for Model Availability + Pricing

## 0. TL;DR

`https://api.tokenkey.dev/pricing` was a hand-curated billing catalog (price,
context window, vision flag). Listing a model there asserted nothing about
whether any account in the fleet could actually serve it. Operators confirming
whether `gemini-2.5-flash` was reachable had to go to admin and Test each
account manually.

This doc defines the architecture for making `/pricing` the **single source of
truth for verified availability AND priced models** across the entire TokenKey
account fleet, AND for making every model-list surface (admin upstream
discovery, client `/v1/models`, `/v1beta/models`, `/antigravity/models`)
read from that same source.

The **two end-user goals** the architecture must deliver:

1. **API account model discovery** (admin "fetch upstream models" with
   `base_url + api_key`):
   - (a) Upstream metadata that explicitly marks a model unavailable
     (Gemini `supportedGenerationMethods` not containing `generateContent`,
     OpenAI permission `deprecated`, VolcEngine status fields, etc.) → drop
     before returning.
   - (b) Remaining models are tagged with `pricing_status` ∈ {`priced`, `missing`}
     based on whether TokenKey's pricing catalog has an entry. **Weak filter**:
     `missing` is returned to admin so operators can see catalog gaps and add
     them; client-facing model-list endpoints emit only `priced`.

2. **All-platform parity** (OAuth: claude / openai / gemini / antigravity;
   API: newapi `channel_type > 0` accounts): the same availability + pricing
   predicate gates every catalog read. Empty pools surface as errors, not as
   silently-served unreachable models.

## 1. Problem Statement

**触发事故（2026-05-06）**：用户配 `gemini-pa` 分组的 `dispatch mapping` 时无法判断
`gemini-2.5-flash` 是否真的可用；被迫 fallback 到唯一实证可用的
`gemini-3.1-pro-preview`。这个临时决策不可扩展——随着 Gemini 系列持续迭代，手工维护的
catalog 与真实上游可用性之间的 gap 只会扩大。

**根本 gap**：

- `Account.IsModelSupported()` 是纵容型（无 model_mapping 时 allow-all），
  调度阶段完全不验证目标模型是否在该平台账号的 upstream catalog 里；第一次失败发生在
  Google 那侧返回 404 `Requested entity was not found.`。
- API 账号的 `FetchUpstreamModelList` 只读 `data[].id`，任何非空 id 都返回；
  上游 metadata 里的"明确不可用"信号被丢弃，TokenKey pricing catalog 也不参与
  过滤。运维新加的 base_url+apikey 渠道里夹杂着上游不再服务的旧模型 id。
- 客户端 `/v1/models`、`/v1beta/models`、`/antigravity/models` 三个端点各自从
  group mapping、上游代理、静态默认列表返回，互不共享 availability/pricing
  事实源。

## 2. 架构决策

### 2.1 复用 `channel_monitor` 子系统，不新建 scheduler

`channel_monitor_*.go`（10+ 文件）+ `scheduled_test_runner_service.go` + ent schemas
（migrations 125–128）已实现：独立 ticker fleet、SSRF-safe pond 工作池、
`enabled+last_checked_at` 索引、history 保留策略、daily rollup 聚合器。

新增 `kind` 字段（`user` | `system_availability`）；系统 seeder 维护
`kind=system_availability` 行；既有 `ChannelMonitorRunner` 保持不变。复用省 ~1500 LOC。

### 2.2 探测策略：被动优先 + cold-tail 主动兜底

**被动观察（Phase 1，本 PR）**：真实 OPC 流量已免费覆盖 80% 的热模型。在
`gateway_service.go recordUsageCore` 成功分支 + 4 个 handler 失败分支（gateway_handler.go × 2、
chat_completions、responses、gemini_v1beta）加 hook，把 `(platform, upstream_model,
status_code, account_id)` 写入 `model_availability` 表。

**主动探测（Phase 2，follow-up）**：`pricing_availability_seeder_tk.go` 每 15 分钟
扫一遍 catalog，对 `last_seen_ok_at < now - 24h` 的 cold-tail cell enable 一条
`kind=system_availability` 的 `channel_monitors` 行（`MaxActiveProbesPerHour=20`
硬上限）。

**StaleAfter = 24h**：给运维"今天验证过/没验证过"的语义；OAuth Code Assist 免费
quota（~1k req/day）下负担可控（实测主动探测 ~3 次/天）。

### 2.3 失败分类矩阵（避免账号级信号污染模型级指标）

| 上游响应 | `last_failure_kind` | availability 影响 | account 影响 |
|---|---|---|---|
| HTTP 200 合法 | （清空） | ✅ → ok | 无 |
| 4xx + body 含 `requested entity was not found` / `model ... not found` / `is not supported` | `model_not_found` | 🔴 **单样本立即** → unreachable | 无 |
| HTTP 404 无 model 字样 | `not_found` | 🟡 soft，3 条/24h → unreachable | 无 |
| HTTP 429 / body 含 `rate_limit` / `quota` | `rate_limited` | ⚪ **inconclusive**，不动 sample，只刷 last_checked_at | 走既有 RateLimitService |
| HTTP 401 / 403 | `auth_failure` | ⚪ **inconclusive** | 标账号 unhealthy |
| HTTP 5xx | `upstream_5xx` | 🟡 soft，累 5 条 → stale | 无 |
| Network timeout / DNS / TLS | `network_error` | 🟡 同 5xx | 无 |

**关键不变量**：`rate_limited` / `auth_failure` 不进 `sample_total_24h`，仅刷
`last_checked_at`，防止账号被限速误判为模型不可达（2026-05-06 smoke 段 6 503 事故的
同类问题）。

### 2.4 Upstream Discovery Filter（Goal 1，本 PR 新增）

**问题**：`internal/integration/newapi/fetch_upstream_models.go` 是 admin "Fetch
Upstream Models" 按钮的实现。它接 `(base_url, channel_type, api_key)` 直连上游
`/v1/models`（OpenAI-compat）/ `/v1beta/models`（Gemini）/ Ollama API，把响应里的
模型 id 列表回吐给 admin UI。原先只读 `data[].id`，所有非空 id 都返回。这导致：

- 上游已下线但仍在 `/v1/models` 列表里（处于 deprecated 状态）的模型一并显示
- TokenKey 没定价的模型也显示，运维以为可以用，结果计费时 fallback 到默认价
- `gemini-pa` 这种"该平台理论支持但 metadata 标了不可生成 generateContent" 的
  embedding-only 模型也显示在生成式分组的可选列表里

**决策**：在 fetch 层增加三段过滤管线，结果返回 `[]DiscoveredModel`（带
`pricing_status` 字段）：

```
upstream /v1/models response (raw)
  ↓
[1] per-provider metadata parse → drop explicitly-unavailable
      OpenAI-compat:  permission[].status == "deprecated" / data[].deprecated
      Gemini:         supportedGenerationMethods 不含 generateContent
      VolcEngine:     status_code == "Disabled" / lifecycle.state == "RETIRED"
      Ollama:         （目前无字段，no-op）
  ↓
[2] PricingAvailabilityService.IsUnreachable(platform, model_id) → drop
      uses model_availability 表里 status='unreachable' 的样本
      flag-off (service nil) → no-op，全部保留
  ↓
[3] PricingCatalogService.IsModelPriced(model_id) → tag pricing_status
      priced       — catalog has entry
      missing      — catalog lacks entry (admin sees badge to add)
  ↓
[]DiscoveredModel { ID, PricingStatus }
```

**弱过滤理由**：`missing` 在 admin 视图保留，让运维看到 catalog gap 主动补全；
硬过滤会让运维误以为上游断了。客户端 `/v1/models` 等公共端点 (§2.5) 仅暴露
`priced`。

**实现位置**：

- `internal/integration/newapi/fetch_upstream_models.go` — `FetchUpstreamModelList`
  签名升级为 `([]DiscoveredModel, error)`；per-provider 解析新 metadata 字段。
- `internal/integration/newapi/discover_filter_tk.go`（TK 新文件）— `DiscoveryFilter`
  类型 + `Apply(ctx, platform, raw) []DiscoveredModel`，持有
  `*PricingCatalogService` + `*PricingAvailabilityService`（具体类型，无新 interface
  抽象）。
- `handler/admin/channel_handler_tk_newapi_admin.go:FetchUpstreamModels` — 响应
  JSON 从 `{models: ["id"]}` 升级为 `{models: [{"id":"...", "pricing_status":"priced"}]}`。
- frontend `useTkUpstreamModels.ts` composable + 拉模型 modal 渲染 `pricing_status`
  badge。

### 2.5 Unified Client Model-List（Goal 2，本 PR 新增）

**问题**：客户端三个 model-list 端点各自从不同源构造响应：

| 端点 | 当前候选源 | 当前过滤 |
|---|---|---|
| `/v1/models` | `GatewayService.GetAvailableModels` 聚合 group account model_mapping；空时 fallback 到 `openai.DefaultModels` 或 `claude.DefaultModels` | 无 |
| `/v1beta/models` | 上游 Gemini 代理；fallback `gemini.FallbackModelsList()` | 无 |
| `/antigravity/models` | `antigravity.DefaultModels()` 静态 | 无 |

三个端点都不查 model_availability 表、不查 pricing catalog，所以会把 `unreachable`
+ `missing pricing` 的模型暴露给 SDK。这是 Goal 2 要修的核心 gap。

**决策**：新增 `internal/service/model_list_filter_tk.go` 共享 predicate：

```go
type ModelListFilter struct {
    pricing      *PricingCatalogService
    availability *PricingAvailabilityService // 可为 nil（feature-flag-off）
}

// FilterClientFacing returns models present in candidates AND priced AND
// (availability service nil OR not 'unreachable').
//
// On nil pricing OR empty candidates → returns candidates unchanged (fail-open
// during cold-start / degraded mode rather than blank an SDK).
func (f *ModelListFilter) FilterClientFacing(ctx context.Context, platform string, candidates []string) []string
```

三个 handler 端点各加 1 行 thin 注入，保持上游 response shape：

- `gateway_handler.go:Models` (line 905+) — `availableModels` / fallback default
  之后 `ids = filter.FilterClientFacing(...)`。
- `gateway_handler.go:AntigravityModels` — 先用 catalog priced 集合；空时 fallback
  到 `antigravity.DefaultModels()`（**§5.x override default**，不静默删除上游静态
  列表）。
- `gemini_v1beta_handler.go:GeminiV1BetaListModels` — 同 1 行 thin 注入；
  `FallbackModelsList` 路径不动（无可用账号时仍返回静态，避免 0 个模型）。

`/api/v1/public/pricing` 自身 `BuildPublicCatalog` 不做 availability 过滤——这是
**事实源**，公开 catalog 仍要包含所有 priced 模型；只在 `availability` 字段里
反映状态（§5），让客户端自行决定如何展示。

## 3. 数据模型

### 3.1 新表 `model_availability`（migration `tk_009`，PR-1 已落）

| 列 | 类型 | 说明 |
|---|---|---|
| `(platform, model_id)` | UNIQUE | 主查询键 |
| `status` | ENUM ok/stale/unreachable/untested | 单一可见状态 |
| `last_seen_ok_at` | TIMESTAMPTZ | 最近成功时间 |
| `last_failure_at` / `last_failure_kind` | TIMESTAMPTZ / VARCHAR(50) | 最近失败 |
| `upstream_status_code_last` | INT | 上游 HTTP 状态 |
| `last_checked_at` | TIMESTAMPTZ | 最近探测时间（含 inconclusive） |
| `sample_ok_24h` / `sample_total_24h` | INT | 24h rolling 计数 |
| `rolling_window_started_at` | TIMESTAMPTZ | 窗口起始 |

### 3.2 channel_monitors 扩展（PR-1 已落，Phase 2 启用）

`kind VARCHAR(24) DEFAULT 'user'`：区分运维配置监控与 seeder 自动生成的探测行。
`seed_source VARCHAR(64) DEFAULT ''`：记录来源（如 `pricing_catalog`）。

## 4. Tap 点（被动观察）

| 位置 | 覆盖路径 | 信号强度 |
|---|---|---|
| `gateway_service.go recordUsageCore` 末尾（1 行 hook） | 所有平台成功 forward | ✅ 强，100% 命中成功 |
| `gateway_handler.go` 2 处 `gateway.forward_failed` 后 | Anthropic /v1/messages + Gemini /v1/messages 失败 | model_not_found 命中 |
| `gateway_handler_chat_completions.go` | OpenAI chat 失败 | model_not_found 命中 |
| `gateway_handler_responses.go` | Responses 失败 | model_not_found 命中 |
| `gemini_v1beta_handler.go` | /v1beta 失败 | model_not_found 命中 |

### 4.1 Forward 上游 status 保留（R-004 fix，本 PR）

PR-1 review 暴露：5 处 handler tap 都给 `TKRecordForwardFailure` 传 `statusCode=0`，
而 `classifyFailureKind` 要求 `UpstreamStatusCode` 是 4xx 才能识别 model_not_found body。
真实 Gemini/OpenAI 404 落到 `default` 分支被分类成 `upstream_5xx`，**§1.3 单样本翻
unreachable 的强信号在生产上从未触发过**。

**修法**：`internal/handler/gateway_handler_tk_forward_error.go`（TK 新文件）封装
`TkRecordFailureFromErr` helper，用 `errors.As` 解开既有的
`*service.UpstreamFailoverError`（gateway 服务在所有上游错误返回点都已构造它，自带
`StatusCode int` + `ResponseBody []byte`）。5 处 handler tap 全部替换成单行调用，
不引入新 error type、不改 `gateway_service.go` Forward 签名。Per CLAUDE.md §5
thin-injection 政策。

`gateway_handler_tk_forward_error_test.go` 用 in-memory `ModelAvailabilityRepository`
钉死端到端：真 Gemini 404 body → 单样本翻 unreachable；429 body → inconclusive，
不动 sample。

## 5. API 契约（向后兼容）

### 5.1 公开 `/api/v1/public/pricing`

每条 model 加 `availability` 子对象（`omitempty`）：

```json
{
  "model_id": "gemini-2.5-flash",
  "pricing": {"currency": "USD", "input_per_1k_tokens": 0.075},
  "availability": {
    "status": "ok",
    "last_verified_at": "2026-05-06T14:22:10Z",
    "last_checked_at": "2026-05-06T14:22:10Z",
    "sample_count_24h": 8421,
    "success_rate_24h": 0.998
  }
}
```

`status` 枚举：`ok | stale | unreachable | untested`。`untested` 显式返回（不
omit）；前端渲染灰点，避免把"从未探测"隐藏成"正常"。

### 5.2 客户端 model-list 端点

`/v1/models`、`/v1beta/models`、`/antigravity/models` 响应 shape 完全保留，仅过滤
返回的模型集合至 `priced ∩ ¬unreachable`。无新字段，无现有字段语义变化。

### 5.3 Admin upstream discovery — `/api/v1/admin/channel-types/fetch-upstream-models`

响应从 `{models: ["id1", "id2"]}` 升级为：

```json
{
  "models": [
    {"id": "gpt-4o", "pricing_status": "priced"},
    {"id": "gpt-5-experimental", "pricing_status": "missing"}
  ]
}
```

`pricing_status` ∈ {`priced`, `missing`}。`missing` 让运维看到 catalog gap；前端
按 badge 标注。

## 6. 渐进发布

| 阶段 | 范围 | 功能 flag |
|---|---|---|
| **PR #128（本 PR，"全装"）** | schema `tk_009` + 被动 tap (PR-1 已落) + R-001 wire DI 接线 + R-002 upstream discovery filter (Goal 1) + R-003 client model-list filter (Goal 2) + R-004 forward status preserve + R-005 doc approval | service nil = flag off；空 catalog → 客户端 model-list fail-open 返回 candidates |
| **Follow-up** | seeder + active probe (channel_monitor `kind=system_availability`) | `active_probe.enabled=false` 默认 |
| **Follow-up** | 前端公开 badge（🟢🟡🔴⚪）+ Redis INCR write-buffer + billing 阻 unreachable 模型 | 公开 badge；7 天观察期通过后开 |

折叠到一个 PR 的理由：R-001 / R-004 / R-005 是 review 暴露的硬阻塞，必须随 R-002 /
R-003 一并解封；分 PR 反而拉长 catalog 不一致窗口。

## 7. 失败模式

- **Google 摘掉模型**：下次真流量打过去返 4xx model_not_found → handler tap
  （已通过 R-004 拿到真 status）单样本翻 unreachable → `model_list_filter_tk.go`
  在下一次 `/v1/models` 自动剔除；admin "fetch upstream models" 也立刻反映。
- **探测限流**：429 → inconclusive，不改 availability，不累 sample；不会把限速
  误判为不可达。R-004 测试钉死该不变量。
- **新模型上线**（catalog 还没补 pricing）：admin "fetch upstream models" 显示
  `pricing_status: missing`，运维点 badge 跳到 catalog editor 补价；客户端
  `/v1/models` 期间不会暴露未定价模型（避免免费派发 + 计费失败）。
- **空 catalog / cold-start**：`PricingCatalogService.IsModelPriced` 返回 false
  时 `ModelListFilter.FilterClientFacing` 行为 = fail-open（返回 candidates 不
  过滤），避免 SDK 看到 0 模型；运维有 admin 端可见的 "catalog empty" warning。
- **账号删除**：`model_availability` 按 `(platform, model_id)` 而非 account_id，
  无 FK，安全。
- **probe 死循环**：复用 channel_monitor_runner.go 既有 ctx timeout + pond pool
  + in-flight guard。

## 8. 遗留事项

- Follow-up：seeder + active probe（PR-2 from original plan）。
- Follow-up：前端公开 badge（🟢🟡🔴⚪）。
- Follow-up：Redis INCR write-buffer（替换 PR-1 的同步 PG 写，减少热模型写
  contention）。
- Follow-up：billing 阻止 `unreachable` 模型（归 gateway_billing_block.go）。
- Follow-up：i18n 文案中性化（当前 admin UI 标题仍是 "OpenAI Messages 调度配置"）。
- Follow-up：Per-platform pricing split — 当前 `PricingCatalogService` 的 catalog
  是平台无关的（一个 model_id 一个 pricing 行）。如果未来需要"同模型不同平台不同
  价"，`IsModelPriced(modelID, platform)` 已经预留 `platform` 参数；迁移时只需在
  catalog JSON 加 `platforms[]` 字段。
