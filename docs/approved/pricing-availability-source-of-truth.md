---
title: Pricing Catalog as Single Source of Truth for Model Availability
status: pending
approved_by: pending
approved_at: ""
created: 2026-05-06
owners: [tk-platform]
scope: "pricing catalog / model availability observability"
---

# Pricing Catalog as Single Source of Truth for Model Availability

## 0. TL;DR

`https://api.tokenkey.dev/pricing` was a hand-curated billing catalog (price, context
window, vision flag). Listing a model there asserted nothing about whether any account
in the fleet could actually serve it. Operators confirming whether `gemini-2.5-flash` was
reachable had to go to admin and Test each account manually.

This doc defines the architecture for making `/pricing` the **single source of truth for
per-model verified availability** across the entire TokenKey account fleet.

## 1. Problem Statement

**触发事故（2026-05-06）**：用户配 `gemini-pa` 分组的 `dispatch mapping` 时无法判断
`gemini-2.5-flash` 是否真的可用；被迫 fallback 到唯一实证可用的
`gemini-3.1-pro-preview`。这个临时决策不可扩展——随着 Gemini 系列持续迭代，手工维护的
catalog 与真实上游可用性之间的 gap 只会扩大。

**根本 gap**：`Account.IsModelSupported()` 是纵容型（无 model_mapping 时 allow-all），
调度阶段完全不验证目标模型是否在该平台账号的 upstream catalog 里；第一次失败发生在
Google 那侧返回 404 `Requested entity was not found.`。

## 2. 架构决策

### 2.1 复用 `channel_monitor` 子系统，不新建 scheduler

`channel_monitor_*.go`（10+ 文件）+ `scheduled_test_runner_service.go` + ent schemas
（migrations 125–128）已实现：独立 ticker fleet、SSRF-safe pond 工作池、
`enabled+last_checked_at` 索引、history 保留策略、daily rollup 聚合器。

新增 `kind` 字段（`user` | `system_availability`）；系统 seeder 维护
`kind=system_availability` 行；既有 `ChannelMonitorRunner` 保持不变。复用省 ~1500 LOC。

### 2.2 探测策略：被动优先 + cold-tail 主动兜底

**被动观察（Phase 1）**：真实 OPC 流量已免费覆盖 80% 的热模型。在
`gateway_service.go recordUsageCore` 成功分支 + 3 个 handler 失败分支加 hook，把
`(platform, upstream_model, status_code, account_id)` 写入 `model_availability` 表。

**主动探测（Phase 2）**：`pricing_availability_seeder_tk.go` 每 15 分钟扫一遍 catalog，
对 `last_seen_ok_at < now - 24h` 的 cold-tail cell enable 一条 `kind=system_availability`
的 `channel_monitors` 行（`MaxActiveProbesPerHour=20` 硬上限）。

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

## 3. 数据模型

### 3.1 新表 `model_availability`（migration `tk_009`）

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

### 3.2 channel_monitors 扩展

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

## 5. API 契约（向后兼容）

`/api/v1/public/pricing` 每条 model 加 `availability` 子对象（`omitempty`）：

```json
"availability": {
  "status": "ok",
  "last_verified_at": "2026-05-06T14:22:10Z",
  "last_checked_at": "2026-05-06T14:22:10Z",
  "sample_count_24h": 8421,
  "success_rate_24h": 0.998
}
```

Status 枚举：`ok | stale | unreachable | untested`。

`untested` 显式返回（不 omit）；前端渲染灰点，避免把"从未探测"隐藏成"正常"。

## 6. 渐进发布（3 PR）

| PR | 范围 | 功能 flag |
|---|---|---|
| **PR-1（本 PR）** | schema `tk_009` + ent 生成 + `PricingAvailabilityService` + 被动 tap + `DecorateWithAvailability` + 本文档 | `availability` service nil = flag off；/pricing 响应不变；后台收数据 |
| **PR-2** | `pricing_availability_seeder_tk.go` + probe 复用 channel_monitor + `kind` 区分 + admin "force re-probe" | `active_probe.enabled=false` 默认；运维 toggle 才开主动探 |
| **PR-3** | seeder 自动接管全 catalog + 前端 badge（🟢🟡🔴⚪）+ `active_probe.enabled=false` 默认 prod toggle | 公开 badge；`active_probe` 保留 false 直到 7 天观察通过 |

## 7. 失败模式

- **Google 摘掉模型**：下次真流量打过去返 4xx model_not_found → handler tap 单样本翻
  unreachable → 30s Redis TTL → /pricing 反映到位
- **探测限流**：429 → inconclusive，不改 availability，不累 sample；不会把限速误判为不可达
- **账号删除**：`model_availability` 按 `(platform, model_id)` 而非 account_id，无 FK，安全
- **probe 死循环**：复用 channel_monitor_runner.go 既有 ctx timeout + pond pool + in-flight guard

## 8. 遗留事项

- PR-2：seeder + active probe
- PR-3：前端 badge + 公开 flag
- Follow-up：billing 阻止 `unreachable` 模型（归 gateway_billing_block.go）
- Follow-up：Redis INCR write-buffer（替换 PR-1 的同步 PG 写，减少热模型写 contention）
- Follow-up：i18n 文案中性化（当前 admin UI 标题仍是 "OpenAI Messages 调度配置"）
