# 账号用量窗口/冷却/恢复全链路（OpenAI 5h 重点）

> 背景：`PR #25` 提到 OpenAI 存在 5 小时用量窗口。本文件基于当前仓库代码扫描，梳理“限量判定 → cooldown → 恢复”全流程。

## 1. 总览流程图（请求入口到恢复）

```mermaid
flowchart TD
  A["请求进入 Gateway"] --> B["APIKeyAuth 中间件"]
  B --> B1{"订阅模式"}
  B1 -->|是| C["ValidateAndCheckLimits: 日周月窗口"]
  B1 -->|否| D["余额与 Key 状态检查"]

  C --> C1{"是否超限"}
  C1 -->|是| X1["429 USAGE_LIMIT_EXCEEDED"]
  C1 -->|否| C2{"是否需要窗口维护"}
  C2 -->|是| C3["异步 DoWindowMaintenance: 激活重置窗口并失效缓存"]
  C2 -->|否| E["进入 Handler"]
  C3 --> E
  D --> E

  E --> F["CheckBillingEligibility"]
  F --> F1{"订阅缓存是否超限"}
  F1 -->|是| X2["429 billing error"]
  F1 -->|否| F2{"API Key 窗口是否超限"}
  F2 -->|是| X3["429 rate_limit_exceeded"]
  F2 -->|否| G["调度账号: ListSchedulable 与预检查"]

  G --> H["上游请求"]
  H --> I{"上游状态码"}
  I -->|429| J["handle429: SetRateLimited(reset_at)"]
  I -->|529| K["handle529: SetOverloaded(until)"]
  I -->|401 OAuth| L["SetTempUnschedulable(until)"]
  I -->|2xx| M["正常返回并记录用量"]

  J --> R1["到 reset_at 或成功测试后恢复"]
  K --> R2["到 overload_until 或恢复接口清理"]
  L --> R3["到 temp_unschedulable_until 或恢复接口清理"]

  R1 --> S["重新进入可调度池"]
  R2 --> S
  R3 --> S
```



## 2. OpenAI 5h/7d 429 决策流程（PR #25 关联）

```mermaid
flowchart TD
  A["收到 OpenAI 429"] --> B["解析 x-codex 相关响应头"]
  B --> C{"normalize 结果是否可用"}
  C -->|否| D["回退到 body 解析 usage_limit_reached 或默认五分钟"]
  C -->|是| E{"7d 窗口是否达到百分百"}
  E -->|是| F["reset = now + reset7dSeconds"]
  E -->|否| G{"5h 窗口是否达到百分百"}
  G -->|是| H["reset = now + reset5hSeconds"]
  G -->|否| I["都未满但收到 429: 取更大的 reset 秒数"]

  D --> J["SetRateLimited(account, reset_at)"]
  F --> J
  H --> J
  I --> J

  J --> K["写入 accounts.rate_limited_at 与 accounts.rate_limit_reset_at"]
  K --> L["ListSchedulable 过滤 reset_at 晚于当前时间的账号"]
```



说明：

- OpenAI 分支优先用 `x-codex-*` 头判断哪个窗口被打满（5h 或 7d）。
- 如果头不可用，才降级用响应体 `usage_limit_reached/rate_limit_exceeded` 的 `resets_at/resets_in_seconds`。
- 若仍拿不到 reset，使用默认 5 分钟冷却。

## 3. 订阅窗口（日/周/月）与 API Key 窗口（5h/1d/7d）

```mermaid
flowchart LR
  subgraph S1["订阅窗口（日周月）"]
    A1["ValidateAndCheckLimits: 内存修正过期窗口用量"] --> A2{"检查 Daily Weekly Monthly limit"}
    A2 -->|超限| A3["返回 ErrLimitExceeded 并映射到 429"]
    A2 -->|通过| A4["若 needsMaintenance 为 true 则异步维护"]
    A4 --> A5["CheckAndActivateWindow 与 CheckAndResetWindows 并失效缓存"]
  end

  subgraph S2["API Key 窗口（5h 1d 7d）"]
    B1["CheckBillingEligibility"] --> B2["checkAPIKeyRateLimits"]
    B2 --> B3["evaluateRateLimits"]
    B3 --> B4{"窗口是否过期"}
    B4 -->|是| B5["内存置零并异步 reset DB 再失效缓存"]
    B4 -->|否| B6["直接比较 usage 与 limit"]
    B5 --> B6
    B6 -->|超限| B7["ErrAPIKeyRateLimit5h 1d 7d Exceeded -> 429"]
    B6 -->|通过| B8["允许继续请求"]
  end
```



## 4. 冷却与恢复流程（429/529/401 + 管理端/定时任务）

```mermaid
flowchart TD
  A["handle429"] --> A1["SetRateLimited(reset_at)"]
  B["handle529"] --> B1{"overload cooldown 是否启用"}
  B1 -->|是| B2["SetOverloaded(now + cooldownMinutes)"]
  B1 -->|否| B3["仅记录日志 不写冷却字段"]
  C["OAuth 401"] --> C1["SetTempUnschedulable(now + OAuth401Cooldown)"]

  A1 --> D["ClearRateLimit 可清理 rate limit overload temp unsched model limits"]
  B2 --> E["查询谓词在 OverloadUntil 到期后自动放行"]
  C1 --> F["查询谓词在 temp_unschedulable_until 到期后自动放行"]

  G["Admin Test 成功"] --> H["RecoverAccountAfterSuccessfulTest"]
  I["POST recover-state"] --> J["RecoverAccountState 可选 invalidate token"]
  K["Scheduled Test auto_recover"] --> H

  H --> D
  J --> D
  D --> L["账号恢复可调度"]
  E --> L
  F --> L
```



## 5. 关键状态字段与查询门槛

`accounts` 表（及扩展字段）中与本流程直接相关的状态：

- `rate_limited_at` / `rate_limit_reset_at`：429 冷却窗口。
- `overload_until`：529 过载冷却窗口。
- `temp_unschedulable_until` / `temp_unschedulable_reason`：OAuth 401 等临时不可调度窗口。
- `session_window_start` / `session_window_end` / `session_window_status`：Anthropic 5h 会话窗口状态。
- `extra.model_rate_limits`：模型级限流（Antigravity/策略分支）。

调度层的“可调度”核心谓词：

- `OverloadUntil IS NULL OR OverloadUntil <= now`
- `RateLimitResetAt IS NULL OR RateLimitResetAt <= now`
- `temp_unschedulable_until IS NULL OR temp_unschedulable_until <= NOW()`
- 以及 `status=active`、`schedulable=true`、未过期等基础条件

## 6. 代码入口索引（按职责）

- **请求前限量判定**
  - `backend/internal/server/middleware/api_key_auth.go`
  - `backend/internal/service/subscription_service.go`
  - `backend/internal/service/billing_cache_service.go`
  - `backend/internal/handler/gateway_handler.go`（429 映射）
- **429/529/401 冷却写入**
  - `backend/internal/service/ratelimit_service.go`
  - `backend/internal/repository/account_repo.go`
- **恢复入口**
  - `backend/internal/handler/admin/account_handler.go`（`/test`、`/recover-state`、`/clear-rate-limit`）
  - `backend/internal/service/scheduled_test_runner_service.go`（`auto_recover`）
- **529 冷却配置**
  - `backend/internal/service/setting_service.go`
  - `backend/internal/handler/admin/setting_handler.go`
  - `frontend/src/api/admin/settings.ts`
  - `frontend/src/views/admin/SettingsView.vue`

## 7. 扫描结论（可直接用于排障）

- OpenAI 429 已存在 5h/7d 窗口分流逻辑，优先依赖 `x-codex-`* 响应头判定具体 reset 时间。
- 订阅（日/周/月）与 API Key（5h/1d/7d）是两套并行窗口：前者偏用户订阅配额，后者偏 key 级限量。
- 冷却“恢复”有两类机制：  
  - **时间驱动自动恢复**（查询谓词到点放行）  
  - **主动恢复**（管理端测试成功、recover-state、定时测试 auto_recover）
- 529 冷却支持开关和分钟数配置，可在 Admin 设置页调整。

