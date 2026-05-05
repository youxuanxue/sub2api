---
title: New-User Cold Start — Public Pricing + Signup Bonus + Auto First API Key (+ Phase-2 Tour & Playground prototype)
status: approved
approved_by: xuejiao
approved_at: 2026-04-22
authors: [agent]
created: 2026-04-22
related_stories: [US-028, US-029, US-030, US-031, US-032]
---

# New-User Cold Start

## 0. TL;DR

新用户在 TokenKey 注册成功之后，今天落到 `/dashboard` 面对**全 0 的统计卡 + 没有 API Key + 没有可见模型清单 + 没有任何赠额**。L 站的真实声音（`linux.do/t/topic/1413702`「点进去只见登录页」、`t/topic/1545819`「找不到赠额就走人」、`t/topic/607092`「站长在问怎么配赠额」、3.1k★ All API Hub 把"自动签到 + 余额查看 + 模型可用性"做成卖点）共同指向一个失败模式：**站点不会自我介绍**。

本文档定义两阶段冷启动改造。

**当前状态（2026-05-05）**：PR #40 已落地 P0 三件套（公开 pricing、注册赠额、自动 trial key）；PR #50 已落地 P1-A 与 Playground prototype A+B；后续 PR #85 / #88 / #92 继续推进 Playground UI 与 pricing 体验。本文仍作为冷启动体验基线，但下列原始范围说明需按当前状态阅读：已 shipped 的部分以代码与故事证据为准，deferred 项继续留作后续边界，不代表必须在本轮 docs 清理中实现。

- **PR 1（P0 三件套，本工作区交付）**：
  - **P0-A** 新增公开模型 + 价格目录页（无需登录 / 无需 API Key），路由 `GET /api/v1/public/pricing` + 前端 `/pricing`；
  - **P0-B** 新增 setting `signup_bonus_balance`（默认 **$1.00**，admin 可配；与 `users.balance` 同口径——美元 USD，前端用现有 `formatCurrency('USD')` 渲染）；邮箱注册 + OAuth 注册路径都在创建用户的同事务内一次性写入余额并落 `system_log`；
  - **P0-C** 新增 setting `auto_generate_default_token`（默认 **true**，admin 可关）+ `auto_generate_default_token_name`（默认 **`"trial"`**，对外暗示"试用"）；注册成功后自动签发一把名为 `trial` 的 API Key，前端 dashboard 新增「快速开始」卡片显示该 Key + 一段 `curl` 例子。
- **PR 2（P1 两件套，Playground 走 prototype-first 子门禁）**：
  - **P1-A** Onboarding Tour 对普通用户开放（删除 `useOnboardingTour.ts:540` 的 admin-only 判断，加 `users.onboarding_tour_seen_at` 字段记忆已看过）。设计简单、低风险，可与 Playground prototype 同 PR 但分 commit。
  - **P1-B** 内置 Playground（`/playground`，复用用户自己的 trial key 直连 `/v1/chat/completions`）—— **本设计文档只给方向，落地前必须先产出 Web UI 视觉级 prototype（Storybook story / 静态 HTML / Figma 截图三选一），由你审过 UI 形态后再写后端 wiring**。详见 §11 PR 2 内部审批门禁。理由：Playground 是"可见即可用"的体验型功能，文字描述与最终形态偏差最大，必须先给眼睛过目。

P2（每日签到）按你的拍板**暂缓**，不在本设计范围。

> 为什么是这五件，不是更多：每条都对应「冷启动失败模式」的一个具体出口——没有目录就看不到值；没有赠额就不会试；没有 Key 就跑不通；没有 Tour 就找不到入口；没有 Playground 就要装客户端。少做一件，新用户走到一半就掉队。多做一件（签到、邀请奖励、欢迎邮件），都是留存或增长功能，**不是**冷启动。

## 1. 原始基线（设计创建时要改的对象）

<!-- prettier-ignore -->
| 维度 | 现状 | 文件:行 |
| --- | --- | --- |
| 注册 → balance | `default_balance`，**默认 0**；OAuth 路径**不走** promo code | `auth_service.go:182-208`、`auth_service.go:472-505`、`auth_service.go:587-640` |
| 注册 → API Key | **手动**到 `/keys` 创建；邮箱注册路径与 OAuth 路径都不创建 | `api_key_service.go:329-462` |
| 模型发现 | `GET /v1/models` 在 API Key 鉴权之后；前端 sidebar 无「模型」入口 | `routes/gateway.go:36-50`、`gateway_handler.go:850-908` |
| 价格元数据 | 已有 `model_prices_and_context_window.json`（litellm 格式），目前只用于计费，未对外暴露 | `config/config.go:1318-1321` |
| 计价口径 | `users.balance` 用 USD（前端 `formatCurrency` 默认 `'USD'`、dashboard 直接 `${balance}`、`order_type==='balance'` 显示 `$`）；CNY 仅出现在「充值订单」链路 | `frontend/src/utils/format.ts:61`、`UserDashboardStats.vue:14` |
| Onboarding tour | Driver.js 步骤已写好，但 `useOnboardingTour.ts:540` 提前 return 了非 admin 用户 | `frontend/src/composables/useOnboardingTour.ts` |
| Playground | **不存在** | — |

**已有可复用基础**：

- `SettingService` 完整的 setting 加载 / 默认值 / 更新 pipeline（`setting_service.go:1008+`），新增 setting key 是已知模式。
- `system_logs` 表 + `LogTypeSystem` 用于审计（已用于 promo / invitation 等场景）。
- `userRepo.UpdateBalance` 已存在，promo 路径已用过。
- `apiKeyService.Create` 已存在，admin & user 路径都用它。
- `GetAvailableModels`（`gateway_service.go:8738+`）按 group 聚合模型清单 → 公开页可复用。

## 2. P0-A 公开模型 + 价格目录

### 路由

```text
GET /api/v1/public/pricing                     # PR 1 v1：扁平 catalog
# v1 deferred（follow-up PR 再做）：
# GET /api/v1/public/pricing?group_id=<id>     # 按 group 过滤
# GET /api/v1/public/pricing?platform=<name>   # 按 platform 过滤
```

**鉴权**：无（公开路由组，挂在 `routes/auth.go` 同级，不进 `gateway` 也不进 `admin`）。

**速率**：`rateLimiter.LimitWithOptions("public-pricing", 60, time.Minute, FailOpen)`——比 `auth-register` 宽松，因为是只读元数据；对未登录用户做 IP 限流即可，避免被当成爬虫源头。

### v1 实施范围声明（与原设计的偏差，2026-04-22 落地拍板 = A 路径）

实施时核对代码发现：(a) Ent `Group` schema 没有 `visible_in_catalog` 字段；加这个字段需要 schema 变更 + migration + `go generate ./ent`，与本 PR 的「单一意图、不引入 schema 改动」原则冲突；(b) `groups[]` 反查 + `endpoints[]` 推导属于中等复杂度的多源 JOIN，会让 PR 1 的 backend 改动从 ~5 文件膨胀到 ~12+ 文件。

按 Jobs「对一千件事说不」、OPC「先解决核心痛点再延展」哲学，**PR 1 只 ship 扁平 catalog**：

- ✅ **保留**（v1 必交）：`object`、`data[]`、`data[].model_id`、`data[].vendor`、`data[].pricing.{currency, input_per_1k_tokens, output_per_1k_tokens, cache_read_per_1k, cache_write_per_1k}`、`data[].context_window`、`data[].max_output_tokens`、`data[].capabilities[]`、`updated_at`
- ⏸ **v1 deferred**（follow-up PR 落）：`data[].groups[]`、`data[].endpoints[]`、`data[].platform`、顶层 `vendors[]`/`platforms[]`/`groups[]` 聚合、`?group_id=` / `?platform=` query 过滤、`pricing_catalog_groups_visible` setting、`visible_in_catalog` group 字段

PR 1 的 catalog 已能解决 L 站 `t/topic/1413702` 反映的核心痛点（"看不到模型清单 + 价格"）；高级筛选 / group 维度归属是进阶用户需求，可独立 follow-up。US-028 的 AC 已同步收敛到 v1 形状，原 AC-002 / AC-005（按 group 过滤、disabled group 隔离）已迁移到 follow-up story 的待办区。

### 响应（DTO 形状）

> **v1 实际响应**：`groups[]`/`endpoints[]`/`platform`/顶层 `vendors`/`platforms`/`groups` 字段在 PR 1 中**不返回**（见上文 v1 范围声明）。下方 JSON 示例保留完整设计形状作为长期目标参考，当前 PR 落地的真实形状以 US-028 AC-001 + AC-002 为准。

```jsonc
{
  "object": "list",
  "data": [
    {
      "model_id": "claude-3-5-sonnet-20241022",
      "platform": "anthropic",
      "vendor": "Anthropic",
      "groups": ["claude-pool-default"],   // 哪些 group 含此模型
      "endpoints": ["/v1/messages", "/v1/chat/completions"],
      "pricing": {
        "currency": "USD",
        "input_per_1k_tokens":  0.003,
        "output_per_1k_tokens": 0.015,
        "cache_read_per_1k":    0.0003,
        "cache_write_per_1k":   0.00375
      },
      "context_window": 200000,
      "max_output_tokens": 8192,
      "capabilities": ["vision", "tool_use", "prompt_caching"]
    }
  ],
  "vendors":  ["Anthropic", "OpenAI", "Google", "Moonshot"],
  "platforms": ["anthropic", "openai", "gemini", "antigravity", "newapi"],
  "groups":   [{ "id": 1, "name": "claude-pool-default", "platform": "anthropic" }],
  "updated_at": "2026-04-22T10:00:00Z"
}
```

**字段命名取舍**：

- 我们 **不**直接复制 new-api `/api/pricing` 的 `quota_type / model_ratio / completion_ratio / group_ratio` 等 token-quota 内部单位——TokenKey 直接用 USD 单价（与 `users.balance` 同口径），照搬 new-api 的"积分 + 倍率"两层结构会让普通用户更迷惑。
- 但**保留** new-api 习惯的几个 wrapper 字段（`object/data/vendors/groups`），让 All API Hub 这类工具的字段映射只需小改。
- `endpoints` 字段直接借鉴 new-api `supported_endpoint`，便于工具识别"这个模型在这个站走 OpenAI 协议还是 Anthropic 协议"。
- `pricing.currency` 显式写 `"USD"`，给未来切换或多币种留口（虽然短期不打算做）。

### 数据来源（v1）

```text
backend/resources/model-pricing/*.json   ← 价格 + context window + capability 元数据（litellm shape，单位 USD per token）
```

v1 deferred:

```text
groupRepo.ListPublic()                    ← v1 deferred（需 visible_in_catalog 字段，要 schema 改动）
gatewayService.GetAvailableModels(g)      ← v1 deferred（与 group 聚合一起进 follow-up）
```

新增 setting（v1）：

<!-- prettier-ignore -->
| Key | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `pricing_catalog_public` | bool | `true` | 总开关；若 admin 关闭，路由返回 404 |

v1 deferred:

<!-- prettier-ignore -->
| Key | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `pricing_catalog_groups_visible` | string(JSON) | `"all"` | follow-up PR 引入；与 `visible_in_catalog` group 字段一同落地 |

**不会暴露**的字段：account-level 配置、channel_type 数字（仅 admin 视角）、内部 cost_per_token 原始小数（前端展示按 1k token 单价四舍五入到 4 位有效数字）。

### 前端（v1）

新增 `frontend/src/views/PricingView.vue`（路由 `/pricing`；文件落在 `views/` 根下而非 `views/public/`，避免无行为的目录迁移）：

- 主表：model_id / vendor / 输入价 / 输出价 / 缓存价 / 上下文窗口 / 能力 chips
- 顶部 CTA：未登录且开启注册赠额时显示「立即注册即送 …」按钮；数字来自既有 **`GET /api/v1/settings/public`** 的 `signup_bonus_enabled` + `signup_bonus_balance_usd`（与 `ComputeSignupBonus` 一致；关闭赠额时为 0）。前端用 `formatCurrency(..., 'USD')` 渲染。
- AppHeader 增加一个公开导航 `t('public.pricing')`：「模型与价格」

v1 deferred（follow-up PR 落）：vendor / group 多选筛选条、协议（`/v1/messages` / `/v1/chat/completions`）下拉、表格 `groups` 列。

## 3. P0-B 注册赠额（默认 $1.00 USD，admin 可配）

### 新增 settings

<!-- prettier-ignore -->
| Key | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `signup_bonus_enabled` | bool | `true` | 总开关 |
| `signup_bonus_balance` | float(decimal 20,8) | `1.00` | 注册赠送的 balance（USD，与 `users.balance` 同口径） |

> 命名取舍：用 `signup_bonus_*` 而**不是** new-api 的 `QuotaForNewUser`。原因：(1) TokenKey `users.balance` 用 USD，new-api `quota` 用积分（USD × 内部倍率），单位不同；(2) `signup_bonus` 是行业通用词，新管理员看一眼就懂；(3) 我们**不**承诺 setting key 与 new-api 兼容（CLAUDE.md 规则 5：TK-specific 走自己的 settings），所以命名以本地清晰为先。

### 注入位置（**两条活路径** — 注入策略对齐到「INSERT 时 bake-in」）

实施前重新核对了代码，发现：

| 路径 | 状态 | 处理方式 |
| --- | --- | --- |
| `AuthService.RegisterWithVerification`（邮箱注册，`auth_service.go:115-237`） | ✅ 活路径 | 注入 |
| `AuthService.LoginOrRegisterOAuthWithTokenPair`（OAuth 首登，`auth_service.go:534-678`） | ✅ 活路径（含 with/without invitation 两个内部分支） | 注入 |
| `AuthService.LoginOrRegisterOAuth`（旧 OAuth 路径，`auth_service.go:440-528`） | ⚠️ Dead code（无 handler 引用） | **不**注入；旁加 `// TK-DEADCODE-NOTE: if you wire this, also call ApplySignupBonusUSD per US-029` |
| `adminServiceImpl.CreateUser`（admin 后台创建用户，`admin_service.go:556-574`） | ❌ Out of scope | **不**自动赠额 — admin 已通过 `req.Balance` 显式选定，自动叠加会越权 |

### 落地方式：INSERT-time bake-in（一条 SQL 自带原子性）+ 结构化 audit log

```go
// pseudocode in auth_service.go RegisterWithVerification, around line 195
defaultBalance := s.settingService.GetDefaultBalance(ctx)
bonusUSD := s.settingService.ComputeSignupBonus(ctx)   // 已在 PR 1 Step 1 落地
user := &User{
    ...,
    Balance: defaultBalance + bonusUSD,                // bonus 直接 bake 进 INSERT
}
if err := s.userRepo.Create(ctx, user); err != nil { ... }
if bonusUSD > 0 {
    // best-effort，与 promo failure log 同模式 —— 失败仅 warn，不阻塞注册
    logger.LegacyPrintf("service.auth", "[Auth] signup_bonus_credited userID=%d amount_usd=%.2f source=email",
        user.ID, bonusUSD)
}
```

### Atomicity 取舍（**v3 实现细节调整**，产品行为不变）

初稿要求"user + audit_log 强 atomic（一损俱损）"，落地时改为：

- **Bonus 落地 = 强 atomic**：bonus 直接进 `User.Balance` 字段，与 default_balance 一起作为 INSERT 的字段值落库——一条 SQL 完成，无需额外事务管理。
- **Audit = best-effort**：通过现有结构化日志通道写出（与 promo failure log 同模式），写失败仅 warn，不阻塞注册。

理由：

1. `system_logs` 表当前**不存在** —— 新建会把 PR 1 拖入 schema migration（违反 PR 1 单一意图原则）。
2. 现有 promo flow 已经是 best-effort（`auth_service.go:218-228`，promo 失败也只 log）—— 强行不对称会让 admin 看到不一致的失败模式。
3. **产品行为承诺不变**：用户注册成功 ↔ user.balance 包含 bonus，二者必同时成立或同时不成立（因为 INSERT 失败则用户也不存在）。

未来如要把 audit 升级为 DB 表（例如做 admin 视角的"赠额历史"页），单开 PR 引入 `signup_bonus_audit` Ent schema + migration，与本 PR 解耦。

### 与现有 promo code 的关系

不冲突。最终余额（USD）= `default_balance(INSERT) + signup_bonus(INSERT) + promo_code_bonus(post-INSERT UpdateBalance)`。promo 仍走原路径（`auth_service.go:217-228`），互不抵消（US-029 AC-007 回归覆盖）。

## 4. P0-C 注册成功自动建首个 API Key（名为 `trial`）

### 新增 settings

<!-- prettier-ignore -->
| Key | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `auto_generate_default_token` | bool | `true` | 总开关（"default"是行为标识，非 key 名字） |
| `auto_generate_default_token_name` | string | `"trial"` | 自动签发的 Key 名字；用 `trial` 而非 `default`，对用户暗示"这是试用、可删可换" |

### 注入位置

同 P0-B 的 3 个注册路径，**在 `assignDefaultSubscriptions` 之后**调用 `apiKeyService.Create`：

```go
if s.settingService.IsAutoGenerateDefaultTokenEnabled(ctx) {
    if _, err := s.apiKeyService.Create(ctx, user.ID, CreateAPIKeyRequest{
        Name: s.settingService.GetAutoGenerateDefaultTokenName(ctx),  // 默认 "trial"
        // GroupID: nil → 走系统默认 group 的解析（已有逻辑）
    }); err != nil {
        // fail-open：Key 没建成不阻塞注册成功
        logger.LegacyPrintf("service.auth", "[Auth] Failed to auto-create trial key for user %d: %v", user.ID, err)
    }
}
```

### Dashboard「快速开始」卡片

`frontend/src/views/user/DashboardView.vue` 顶部新增 `UserDashboardQuickStart.vue`：

- 仅当 `stats.total_api_keys === 1 && stats.first_request_at == null` 时显示（首次有调用就消失）
- 显示这把 trial key 的脱敏值 + 一键复制按钮
- 一段动态生成的 `curl` 例子，base URL 从 `useApiBaseUrl()` 拿，model 从 `/api/v1/public/pricing` 第一行拿
- 文案明确：「试用额度 ${bonus}，用完不会自动扣费」（避免用户误以为已绑卡）
- 按钮「在 Playground 中尝试」（P1-B 上线后激活，P0 阶段灰显 + tooltip"即将推出"）

需要后端补一个轻量字段：`UserDashboardStats.first_request_at`（可在 `dashboardService.GetStats` 内联计算，不需要 schema 改动——`api_key_logs` 已有时间戳）。

## 5. PR 2（P1，独立分支 `feature/user-cold-start-tour-playground`）

### P1-A Tour 解锁

- 改 `frontend/src/composables/useOnboardingTour.ts:534-543`：删 `if (!isAdmin) return`
- 改判断条件为 `userStore.user?.onboarding_tour_seen_at == null`
- 新增 `users.onboarding_tour_seen_at` Ent 字段（**首次 schema 改动**，本身极小：`time.Optional().Nillable()`）
- 新路由 `POST /api/v1/users/me/onboarding-tour-completed` 写入时间戳
- `getUserSteps` 已存在，仅微调步骤顺序匹配新的 dashboard 卡片

### P1-B Playground —— prototype-first

**原型阶段**以 §11 的 A+B 门禁为准；**实装阶段**在仓库中为 `PlaygroundView.vue`：浏览器 `fetch` + 用户 API Key 调网关 **`/v1/models`** 与 **`/v1/chat/completions`**（model 列表已隐含当前 Key 所属 group，无需单独 group 下拉）。

- 路由 `/playground`，sidebar 增加入口
- 顶部 **model** 下拉（列表来自 `/v1/models`，使用用户 Key；trial key 由注册流程自动签发时可优先选取名为 `trial` 的 Key）
- 中间消息列表 + 输入框（仿 OpenAI Playground 的 chat 形态）
- 右侧 collapse 的高级面板：temperature / max_tokens / system prompt
- 直接用浏览器 `fetch` 调 `/v1/chat/completions`，**不走** SSE 简化版（首版只支持一次性 JSON 响应，stream 留 P2）
- 不引入新后端路由（用户自己的 trial key 已有完整鉴权）
- 安全：禁用 `tools` / `tool_use`（首版避免 function calling 副作用）；max_tokens 默认 1024，单次最长 60s 超时

P1 不需要新 setting，不需要 schema 之外的迁移（除 P1-A 的 `onboarding_tour_seen_at`）。

## 6. 与 new-api 的字段命名对齐表

CLAUDE.md 规则 4 / 5：**不能** import new-api 的 `controller/` + `model/`（GORM 绑定）。下表说明哪些命名与 new-api 对齐（生态兼容），哪些故意不对齐（语义不同）。

<!-- prettier-ignore -->
| 关注点 | new-api | TokenKey 本设计 | 对齐 / 不对齐 | 理由 |
| --- | --- | --- | --- | --- |
| 公开 pricing endpoint | `GET /api/pricing` (TryUserAuth) | `GET /api/v1/public/pricing` | **不对齐路径**，对齐结构 | 我们已有 `/api/v1/public/*` 命名空间习惯 |
| 响应 wrapper | `{data, vendors, group_ratio, usable_group, supported_endpoint}` | `{object, data, vendors, platforms, groups, updated_at}` | 部分对齐 | All API Hub 解析时需要一个 small adapter |
| 注册赠额 setting | `QuotaForNewUser`（积分） | `signup_bonus_balance`（USD） | **不对齐** | 单位不同（积分 vs USD），强行对齐反而误导 |
| 邀请人/被邀请人赠额 | `QuotaForInviter` / `QuotaForInvitee` | **不在本 PR 范围** | — | 现 `redeem_codes` schema 没有 `created_by`，要做需先加字段；推到独立 PR |
| 自动签发 default key | env `GENERATE_DEFAULT_TOKEN`（key 名硬编码 `default`） | setting `auto_generate_default_token` + `auto_generate_default_token_name`（默认 `"trial"`，可改） | 行为对齐，命名差 | 我们用 settings 不用 env；key 名用 `trial` 更明确暗示"试用"语义 |
| 每日签到 | `checkin_setting`（`enabled/min_quota/max_quota`） | **不在本设计范围** | — | P2 暂缓 |

## 7. 测试与回归

<!-- prettier-ignore -->
| 故事 | 主要 AC | 运行命令 |
| --- | --- | --- |
| US-028（公开 pricing） | 未登录可访 200；setting 关闭时返回 404；group_id 过滤生效；非 active group 不暴露 | `go test -tags=unit ./internal/handler/... -run TestUS028_` |
| US-029（注册赠额，USD） | 邮箱注册 + OAuth 双路径都加额；setting 关闭时不加额；setting=0 时不加额；system_log 正确写入；与 promo 不冲突 | `go test -tags=unit ./internal/service/... -run TestUS029_` |
| US-030（自动建 trial key） | 邮箱注册 + OAuth 双路径都建 Key；setting 关闭时不建；建 Key 失败不阻塞注册成功 | `go test -tags=unit ./internal/service/... -run TestUS030_`（Dashboard 快速开始卡片 = US-030 AC-007，**v1.5 deferred** 到 follow-up，与 PR 2 P1-A/P1-B 同 milestone，详见 `.testing/user-stories/stories/US-030-auto-first-api-key.md` AC-007 注记） |
| 回归 | 现有 promo / invitation / OAuth 全部测试不动 | `make test`（backend + frontend lint） |

## 8. 风险与回滚

<!-- prettier-ignore -->
| 风险 | 概率 | 缓解 |
| --- | --- | --- |
| 公开 pricing 暴露内部 channel/account 信息 | 低 | 新增端点只读 group + model 元数据，账号字段一律不进 DTO；增加 unit test 断言响应 JSON 不含 `account_id/channel_type/api_key/access_token` 等 |
| 注册赠额被注册机滥用 | 中 | 已有 `auth-register` rate limit 5/min；email 验证开启时门槛已经存在；admin 可一键设 `signup_bonus_balance=0` 立即停掉而不重启 |
| 自动建 trial key 让用户误以为已自动绑卡 / 自动消费 | 低 | dashboard 卡片明确标注"试用额度 ${bonus}，用完不会自动扣费"；超额请求被现有 quota 中间件拦截 |
| Pricing 数据来源 (litellm JSON) 与实际可调模型可能不完全一致 | 中 | **v1** 仅展示定价 JSON 扁平目录（见 §2 v1 范围）；与调度池取交集列为 **follow-up**，避免在本 PR 引入多源 JOIN；Playground 用真实 Key 调 `/v1/models` 反映「本站可调」模型 |

回滚：所有改动都通过 settings 控制，admin 可一键关闭：

- `pricing_catalog_public=false` → 公开页隐藏
- `signup_bonus_enabled=false` → 不再赠额
- `auto_generate_default_token=false` → 不再自动建 key

无需回滚镜像即可降级。

## 9. 实施顺序（PR 1 内部）

1. **Step 1**：新增 5 个 settings（`signup_bonus_enabled` + `signup_bonus_balance` + `auto_generate_default_token` + `auto_generate_default_token_name`（默认 `"trial"`）+ `pricing_catalog_public`），改 `domain_constants.go` + `setting_service.go` 默认值 / 加载 / 更新链路；补 settings UI 表单。
2. **Step 2**：实现 `signupBonusApplier` + `autoFirstKeyApplier` 两个内部小 helper，挂到 3 个注册路径（unit test US-029 + US-030）。
3. **Step 3**：实现 `pricingCatalogService.GetPublic()` + `GET /api/v1/public/pricing` 路由（unit test US-028）。
4. **Step 4**：前端 `/pricing` 页面 + AppHeader 公开导航 + `UserDashboardQuickActions` 第 4 个 tile 引导到 `/pricing`。Dashboard 专用「快速开始」卡片（trial key + curl 例子，US-030 AC-007）按 §11 prototype-first 同精神**deferred 到 follow-up PR**：体验型 UI 需先有 Storybook story / 静态 HTML 视觉 prototype 过审；trial key 已通过 `/keys` 页面立即可见，curl 例子作为体验增强独立 PR 更合适。
5. **Step 5**：preflight + agent contract regen + 集成回归 (`make test`)。
6. **Step 6**：单 PR 推送，PR 标题 `feat(cold-start): public pricing + signup bonus + auto trial key`，PR body 三段（Summary / Risk / Validation），按 §5.y squash-merge。

## 10. 不做（明确说不）

- **不做**邀请人/被邀请人额外奖励（需要先加 `redeem_codes.created_by` 字段，独立 schema 改动 PR）
- **不做**每日签到（P2 已暂缓）
- **不做**欢迎邮件（与现有 SMTP 链路耦合，且不影响"第一次能跑通"）
- **不做**多语言 pricing 页（先中英对齐 i18n key 即可，复杂表头本 PR 不展开）
- **不做**充值入口改造（已有 SubscriptionsView，本 PR 只在 dashboard 卡片加链接，不重做）

## 11. PR 2 内部审批门禁（Playground prototype-first）

P1-A（Tour 解锁）属于低风险代码改动，可直接走默认实现路径；**P1-B（Playground）必须在写一行后端 wiring 之前先过一道 prototype 审批**，门禁条件如下。

### 11.1 必须先产出（A + B 两件，**都做**）

按你的拍板，PR 2 的 prototype 阶段必须**同时**交付以下两件产物——A 用于工程对接（含 props/state 真实形态、Vitest 可挂），B 用于直接给眼睛过目（无需启动任何服务）：

<!-- prettier-ignore -->
| 必交件 | 产物形态 | 角色 |
| --- | --- | --- |
| A. **工程对接件**（必做） | `PlaygroundPrototype.vue` + Vitest（原表写 Storybook — 仓库未引入 Storybook 依赖时以 **Vitest 组件装载**为等价实现；见 `.testing/user-stories/stories/US-032-playground-prototype-AB.md`） | 状态机 / DOM 契约可在 CI 跑 |
| B. **静态 HTML mockup**（必做） | `docs/approved/attachments/playground-prototype-2026-04-23.html` 单页，CSS 内联，数据 hard-coded | 视觉决策基线 — 双击浏览器打开即可 |

> 不再保留 Figma 选项（C），原因：**Figma 截图与最终前端实现之间没有任何工程链路保证**——人改了 Figma 工程不会知道，工程改了实现 Figma 也不会同步。A + B 都是"代码或 HTML"，可被 git diff、可被 review、可被 stat 块跟踪，符合 OPC 自动化原则。

A 与 B 的内容**必须一致**（同样的 4 个状态、同样的文案、同样的字段顺序、同样的颜色变量）。我会在 prototype PR 里加一段 commit 说明 + 4 张并排截图证明 A/B 视觉一致。

两件产物都必须**至少**呈现以下 4 个画面/状态：

1. **首次进入**：空消息列表 + 顶部 group/model 下拉默认选中（用户当前 trial key 所属 group 的第一个 model） + 输入框 placeholder 文案
2. **正在请求**：消息列表上有 user message + assistant 占位骨架 + 中断按钮
3. **响应完成**：assistant message 渲染（含 markdown / code block 高亮）+ 用量小条（input/output tokens、estimated cost USD）
4. **错误态**：余额不足 / quota 超限 / 模型 404 三种错误的统一展示样式

### 11.2 必须明确表态（prototype 文档底部）

- 是否允许 system prompt（首版）
- 是否显示推理过程（如 Claude reasoning / OpenAI o1 思考过程）—— 倾向不显示（避免上游 schema 演进风险）
- multi-turn 上下文是仅前端持有还是入库（倾向仅前端）
- 单次最大消息数 / 单次最大 token / 单次最长超时（倾向 50 turns / 4096 / 60s）
- 移动端断点：是否支持 < 768px（倾向首版桌面优先，移动端后续）

### 11.3 审批通过后才能进入

- 前端 `PlaygroundView.vue` 实装（交互页可与 prototype 组件并存；**参数契约**：默认 `max_tokens=1024`、硬上限 `4096`、超时 `60s`、浏览器内最多保留 50 轮 user/assistant 对、不发送 `tools`）
- 路由 `/playground` 注册 + sidebar 入口
- （可选）e2e：Playwright 覆盖主路径；或以运维脚本 `scripts/tk_gateway_smoke.sh` 对生产/测试网关做 **Key 级**烟雾验证（不在日志中打印 Key）
- follow-up：独立故事文件 `US-032-playground-experience.md` 可在收紧 e2e 门禁时补档

**不做 prototype（A + B 任缺一件）就实装 Playground = 违反本门禁的原意**；当前仓库已在落地 Playground 前合并 A+B（见 `US-032-playground-prototype-AB.md` Evidence）。
