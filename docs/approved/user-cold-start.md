---
title: New-User Cold Start — Public Pricing + Signup Bonus + Auto First API Key (+ Phase-2 Tour & Playground prototype)
status: approved
approved_by: xuejiao
approved_at: 2026-04-22
authors: [agent]
created: 2026-04-22
related_stories: [US-028, US-029, US-030, US-031]
revised: 2026-11-20（再压 P0-A 实施考古/完整 JSON 目标形；保留 v1 契约）
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

## 1. 基线（已由下文 P0 闭合）

设计时缺口：注册默认 balance=0、无自动 trial Key、公开无定价目录、非 admin Tour
提前 return、无 Playground。P0 闭合前三项；Tour/Playground 见 §5 / §11（Studio）。
复用 `SettingService`、`system_logs`、`UpdateBalance`、`apiKeyService.Create`。

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

### v1 响应契约

扁平 catalog（无 schema 改动）：`object`、`data[]`（`model_id`、`vendor`、`pricing.{currency,input/output/cache_*_per_1k_tokens}`、`context_window`、`max_output_tokens`、`capabilities[]`）、`updated_at`。
单价为 USD（对齐 `users.balance`），不照搬 new-api 积分/倍率。
**v1 deferred**：`groups[]` / `endpoints[]` / `platform` / 顶层聚合 / `?group_id=` / `?platform=` / `visible_in_catalog`。验收以 US-028 为准。

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

实现约束：

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

## 5. P1 Tour / Playground

- Tour：非 admin 可走；以 `onboarding_tour_seen_at` + `POST /api/v1/users/me/onboarding-tour-completed` 为准。
- Playground 体验已并入 `/studio` Chat（见 §11）；不再维护独立 `PlaygroundView` / 静态原型。

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

## 10. 不做（明确说不）

- **不做**邀请人/被邀请人额外奖励（需要先加 `redeem_codes.created_by` 字段，独立 schema 改动 PR）
- **不做**每日签到（P2 已暂缓）
- **不做**欢迎邮件（与现有 SMTP 链路耦合，且不影响"第一次能跑通"）
- **不做**多语言 pricing 页（先中英对齐 i18n key 即可，复杂表头本 PR 不展开）
- **不做**充值入口改造（已有 SubscriptionsView，本 PR 只在 dashboard 卡片加链接，不重做）

## 11. Playground / Tour

P1-A Tour 可走默认实现路径。P1-B Playground 体验已并入 `/studio` Chat
（`ChatStudio.vue` + Vitest）；不再维护独立 Playground prototype / US-032。
独立静态 HTML / Figma 副本已删。
