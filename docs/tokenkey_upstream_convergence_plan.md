# TokenKey Upstream Convergence Plan

## Goal

在持续跟进 `upstream/main` 的前提下，明确 TokenKey 需要长期保留的差异能力，并把非核心外围能力尽量收敛回 upstream，降低后续 merge / rebase 冲突成本。

本计划的核心原则：

1. `newapi` 第 5 平台及其直接相关能力是 TokenKey 最重要、必须长期保留的核心能力。
2. `payment`、`webhook`、`Passkey/WebAuthn`、`Backend Mode` 等外围能力优先使用 upstream 方案，减少 fork 面积。

---

## Scope

本计划覆盖三类内容：

1. 能力点盘点
2. 保留 / 收敛决策
3. 后续执行顺序与验收标准

---

## Capability Map

### A. 必须长期保留

#### A1. `newapi` 第 5 平台核心能力

- 结论：`保留`
- 重要度：`S+`
- 原因：
  - 这是 TokenKey 最核心的产品差异点。
  - 一旦删除，TokenKey 相比 upstream 的主价值会显著下降。
- 覆盖内容：
  - `newapi` 平台建模
  - `channel_type`
  - bridge relay
  - OpenAI-compatible 分发
  - admin channel-type 管理
  - upstream model 抓取
  - affinity / selection
  - `newapi` 账号配置与管理
- 主要代码区域：
  - `backend/internal/integration/newapi/`
  - `backend/internal/relay/bridge/`
  - `backend/internal/service/*bridge_dispatch*.go`
  - `backend/internal/server/routes/admin_tk_channel_routes.go`
  - `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
  - `frontend/src/composables/useTkAccountNewApiPlatform.ts`
  - `frontend/src/components/account/AccountNewApiPlatformFields.vue`
- 粗估代码量：`约 6k+`
- 删除后果：
  - `newapi` 账号与 `channel_type` 体系失效
  - 第 5 平台的管理、分发、relay 能力大面积退化
  - TokenKey 核心产品差异基本丧失

#### A2. `newapi` 相关兼容与辅助能力

- 结论：`保留`
- 重要度：`S`
- 覆盖内容：
  - OpenAI-compatible 路径兼容
  - endpoint normalization
  - Moonshot 区域解析
  - `newapi` 相关错误映射 / bridge usage
- 主要代码区域：
  - `backend/internal/handler/endpoint_tk.go`
  - `backend/internal/handler/openai_gateway_embeddings_images.go`
  - `backend/internal/service/admin_service_tk_moonshot.go`
  - `backend/internal/integration/newapi/moonshot_resolve_save.go`
- 删除后果：
  - 客户端兼容性下降
  - 特定 `newapi` 渠道配置更易出错

---

### B. 原则上收敛回 upstream

#### B1. Payment / Webhook

- 结论：`收敛到 upstream`
- 重要度：`C`
- 原因：
  - 非 TokenKey 核心差异能力
  - 最容易与 upstream 支付体系持续冲突
  - 重复保留会造成理解和维护噪音
- 当前原则：
  - live path 只保留 upstream `payment` / `payment webhook`
  - 不保留 TokenKey 私有 payment / webhook 支线
- 删除后果：
  - 几乎没有 TokenKey 核心能力损失
  - 只会失去 fork 自己的支付旁路实现
- 验收目标：
  - 仓库内不存在 TokenKey 私有 payment / webhook route/service 残留
  - 只保留 upstream payment 路由与处理链

#### B2. Passkey / WebAuthn

- 结论：`收敛到 upstream`
- 重要度：`C`
- 原因：
  - 不是 TokenKey 核心竞争力
  - 后端、前端、Ent schema、settings、路由都会增加长期维护面
  - 与 upstream auth 主链路耦合较深
- 主要代码区域：
  - `backend/internal/service/passkey_service.go`
  - `backend/internal/service/passkey_webauthn.go`
  - `backend/internal/handler/auth_handler_tk_passkey.go`
  - `backend/internal/server/routes/auth_tk_passkey_routes.go`
  - `backend/ent/schema/passkey_credential.go`
  - `frontend/src/composables/useTkPasskeyLogin.ts`
  - `frontend/src/components/auth/LoginPasskeySection.vue`
- 粗估代码量：`约 1.8k`
- 删除后果：
  - 已使用 passkey 的用户需要回退到 upstream auth 方案
  - 会失去无密码登录这类外围能力
- 验收目标：
  - 不再保留 TokenKey 私有 passkey 路由 / service / setting / 前端入口
  - auth 主链路尽量与 upstream 保持一致

#### B3. Backend Mode

- 结论：`收敛到 upstream`
- 重要度：`C`
- 原因：
  - 属于平台运营策略能力，不是 TokenKey 核心卖点
  - 它会直接改动 auth / user / payment 等共享路由，是 merge 高冲突点
- 主要代码区域：
  - `backend/internal/server/middleware/backend_mode_guard.go`
  - `backend/internal/server/routes/auth.go`
  - `backend/internal/server/routes/user.go`
  - `backend/internal/server/routes/payment.go`
  - `backend/internal/service/setting_service.go`
- 删除后果：
  - 系统行为会回到 upstream 默认模式
  - 如果当前依赖“后台托管式”运营，需要评估实际业务影响
- 验收目标：
  - backend mode guard 不再侵入共享主路由
  - 路由行为与 upstream 尽量一致

---

### C. 附带清理项

#### C1. TokenKey 专用 settings merge / audit（非 `newapi` 必需部分）

- 结论：`跟随外围能力一起清理`
- 说明：
  - 如果 passkey / backend mode 收敛，这部分对应的 merge/audit 扩展应同步删除
- 主要代码区域：
  - `backend/internal/handler/admin/setting_handler_tk_merge.go`
  - `backend/internal/handler/admin/setting_handler_tk_audit.go`
  - `backend/internal/service/setting_service_tk_bridge_passkey_payments.go`

#### C2. Tier2 SDK / 旧术语 / 运维小增强

- 结论：`非核心，按需清理`
- 说明：
  - 包括遗留的 `tier2_sdk`、无调用的 CLI helper、TokenKey 命名但已无实际价值的指标/注释等
- 处理原则：
  - 如果不再支撑 `newapi` 核心链路，就清理
  - 如果只是命名噪音，就重命名或移除

---

## Recommended Execution Order

建议按下面顺序执行，避免误伤 `newapi` 核心能力。

### Phase 1. 锁定 `newapi` 核心边界

- 目标：
  - 先确认哪些代码属于第 5 平台核心主链路，禁止误删
- 应重点保护：
  - `backend/internal/integration/newapi/`
  - `backend/internal/relay/bridge/`
  - `channel_type`
  - `admin_tk_channel_routes`
  - `gateway_tk_openai_compat_handlers`
  - `openai/gateway bridge dispatch`
- 验收标准：
  - `newapi` 路由、admin 配置、bridge dispatch、上游模型抓取仍全部可用

### Phase 2. 清理 payment / webhook 私有残留

- 目标：
  - 只保留 upstream payment / webhook
- 动作：
  - 清理 TokenKey 私有 payment/webhook route/service/helper/test
  - 清理只服务于这些分支的 settings / constants / docs / metrics
- 验收标准：
  - 仓库内不存在私有 payment/webhook 支线残留
  - live path 仅走 upstream payment

### Phase 3. 清理 Passkey / WebAuthn

- 目标：
  - 让认证能力回到 upstream 主链路
- 动作：
  - 删除 passkey service / handler / route / setting / schema / frontend 入口
  - 清理对应测试、文案、metrics
- 风险：
  - 如有真实用户在用 passkey，需要先做迁移或通知
- 验收标准：
  - auth 相关主流程与 upstream 尽量一致
  - 不再保留 passkey 专属代码路径

### Phase 4. 清理 Backend Mode

- 目标：
  - 去掉对 auth/user/payment 主路由的额外行为改写
- 动作：
  - 删除 backend mode guard、settings 字段、前端开关
- 验收标准：
  - auth / user / payment 路由行为尽量与 upstream 对齐

### Phase 5. 清理附属 merge/audit/helpers

- 目标：
  - 删除随 passkey/backend mode 一起失效的 TokenKey settings merge/audit 扩展
- 验收标准：
  - settings 结构回到与 upstream 尽量接近的状态

---

## Execution Checklist

执行前确认清单：

- [ ] `newapi` 核心边界已单独标记
- [ ] payment / webhook 已确认只保留 upstream
- [ ] passkey 是否已有真实用户在使用，已评估
- [ ] backend mode 是否仍有真实运营依赖，已评估

执行后检查清单：

- [ ] `newapi` 第 5 平台 admin 创建 / 更新 / 导入正常
- [ ] `newapi` bridge dispatch 正常
- [ ] OpenAI-compatible 路由正常
- [ ] frontend 对 `newapi` 的管理与表单链路正常
- [ ] payment / webhook 只走 upstream
- [ ] backend build / targeted tests / frontend typecheck 通过
- [ ] 与 `upstream/main` 的 diff 明显收缩

---

## Acceptance Baseline

确认后执行时，建议把下面这些作为最低验收标准：

1. `newapi` 第 5 平台相关链路全部正常。
2. 非核心外围能力尽量回归 upstream。
3. 不再保留“看似存在、实际不走 live path”的私有支线代码。
4. 清理完成后，rebase / merge `upstream/main` 的冲突面明显减少。

---

## Decision Needed From User

请确认以下事项：

1. 【确认】按本计划执行：
   - `保留 newapi 第5平台`
   - `收敛 payment / webhook / passkey / backend mode`
2. 【确认】Passkey 允许直接移除。
3. 【确认】Backend Mode 允许直接移除。

确认后，我将按本文件顺序执行，并在每个阶段结束后做对照检查。
