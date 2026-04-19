# US-007-readopt-backend-mode-default-true

- ID: US-007
- Title: Re-adopt upstream Backend Mode and default it on for TokenKey
- Version: V1.3.0
- Priority: P1
- As a / I want / So that:
  作为 **TokenKey 运维者**，我希望 **fresh install 默认就处于 Backend Mode**（用户注册 / OAuth 自助登录 / 自助密码重置 / 自助充值入口全部被中间件拦截），**以便** 我们承诺给客户的"管理员发号"形态开箱即生效，而不需要每次新部署后再去后台手动改配置；同时不引入任何 TK 自有的 Go 代码分支，复用上游已有的 `backend_mode_guard` 中间件 + `IsBackendModeEnabled()` 缓存。
- Trace:
  - 防御需求轴线：Backend Mode 是 TK 的**默认安全姿态**，没有它的话 fresh install 默认会暴露注册/自助充值入口。
  - 实体生命周期轴线：上游 commit `6826149a` 引入了 `settings.backend_mode_enabled` 这条 setting；本故事补齐 TK 侧的 fresh-install 默认值这条迁移边。
  - 系统事件轴线：每次 `auth.Use()` / `payment.Use()` / `user.Use()` 这条中间件链上的请求，必须通过 `BackendMode{Auth,User}Guard` 检查。
- Risk Focus:
  - 逻辑错误：`IsBackendModeEnabled` 缓存命中 / 缓存过期 / DB 错误 / setting 不存在 四条分支语义；`UpdateSettings` 写回时缓存必须 invalidation。
  - 行为回归：上游已有的 6 个 `TestIsBackendModeEnabled_*` + `TestUpdateSettings_InvalidatesBackendModeCache` 必须按上游期望（默认 false on missing/error）通过——TK 不能在代码层偏移上游契约，TK 的"默认 true"语义只允许在 migration 里实现。
  - 安全问题：guard 必须 fail-closed（拦截）而非 fail-open（放行），否则 Backend Mode 失效等于注册接口被打开；同时管理员操作（不在 guard 链上）必须可达。
  - 不适用：运行时问题（cache TTL 60s + singleflight 已被上游测试覆盖）。

## Acceptance Criteria

1. **AC-001 (正向 / 默认 true)**：Given fresh install + 已运行 `tk_003_default_backend_mode_enabled.sql`，When `IsBackendModeEnabled(ctx)` 第一次被调用，Then 返回 `true`，且对 `/api/v1/auth/register` 的请求被 `BackendModeAuthGuard` 拦截。
2. **AC-002 (负向 / 缺省 setting)**：Given migration 未跑（极端假设：如裸建库手测），When `IsBackendModeEnabled(ctx)` 命中 `ErrSettingNotFound`，Then 返回 `false` 且写入 60s 缓存——**字节兼容上游**（TK 不在代码里改默认）。
3. **AC-003 (负向 / DB 错误)**：Given `settingRepo.GetValue` 返回非 `ErrSettingNotFound` 的错误，When `IsBackendModeEnabled(ctx)` 被调用，Then 返回 `false` 且写入 5s 错误缓存（`backendModeErrorTTL`）。
4. **AC-004 (副作用 / 缓存失效)**：Given Backend Mode 已被缓存为 `true`，When 管理员通过 `UpdateSettings` 把 `BackendModeEnabled` 改为 `false`，Then 下一次 `IsBackendModeEnabled(ctx)` 必须立刻返回 `false`（singleflight 被 Forget，缓存被覆盖）。
5. **AC-005 (Guard 拦截 / 公开接口)**：Given Backend Mode 开启，When 任意客户端请求 `/api/v1/auth/register`，Then `BackendModeAuthGuard` 必须返回非 200 拒绝（具体码以上游 `backend_mode_guard.go` 实现为准）。
6. **AC-006 (Guard 拦截 / 自助接口)**：Given Backend Mode 开启 + 已登录普通用户，When 该用户请求 `/api/v1/payment/...` 或 `/api/v1/user/...`，Then `BackendModeUserGuard` 必须拦截。
7. **AC-007 (Guard 放行 / 管理员)**：Given Backend Mode 开启，When 管理员请求 `/api/v1/admin/...`（不在 guard 链上），Then 请求正常到达 handler。
8. **AC-008 (回归保护)**：Given 此 PR 落地，When 执行
   `go test -tags=unit -run 'BackendMode|TestUpdateSettings_InvalidatesBackendModeCache' ./internal/service/... ./internal/server/middleware/...`
   Then 全部通过（既包含上游继承的 TestIsBackendModeEnabled_* / TestBackendMode{Auth,User}Guard*，也包含 TestUpdateSettings_InvalidatesBackendModeCache）。
9. **AC-009 (Migration 幂等)**：Given `tk_003_default_backend_mode_enabled.sql` 已经执行过一次（运维者后续在后台把 setting 改为 `false`），When 二次部署再次跑 migrations，Then `ON CONFLICT (key) DO NOTHING` 必须保留运维者改过的 `false`，不被覆盖回 `true`。

## Assertions

- `IsBackendModeEnabled` 默认（无 setting 行）→ `false`（字节兼容上游）。
- `UpdateSettings` 成功后，`backendModeSF.Forget("backend_mode")` 被调用 + `backendModeCache` 被覆盖为新值（断言下次调用立刻返回新值）。
- `tk_003` migration 中存在 `ON CONFLICT (key) DO NOTHING`（断言：运维者后续修改不被回写覆盖）。
- 上游测试 `TestIsBackendModeEnabled_ReturnsFalseOnNotFound` / `_ReturnsFalseOnDBError` 在 TK 实现上仍 pass —— 用来断言 TK 没有在代码层偏移上游默认语义。
- `RegisterAuthRoutes` / `RegisterUserRoutes` / `RegisterPaymentRoutes` 函数签名包含 `settingService *service.SettingService`（断言：guard 的依赖被显式注入，不是隐式全局）。

## Linked Tests

- `backend/internal/service/setting_service_backend_mode_test.go`::`TestIsBackendModeEnabled_ReturnsTrue`
- `backend/internal/service/setting_service_backend_mode_test.go`::`TestIsBackendModeEnabled_ReturnsFalse`
- `backend/internal/service/setting_service_backend_mode_test.go`::`TestIsBackendModeEnabled_ReturnsFalseOnNotFound`
- `backend/internal/service/setting_service_backend_mode_test.go`::`TestIsBackendModeEnabled_ReturnsFalseOnDBError`
- `backend/internal/service/setting_service_backend_mode_test.go`::`TestIsBackendModeEnabled_CachesResult`
- `backend/internal/service/setting_service_backend_mode_test.go`::`TestUpdateSettings_InvalidatesBackendModeCache`
- `backend/internal/server/middleware/backend_mode_guard_test.go`::`TestBackendModeAuthGuard*`
- `backend/internal/server/middleware/backend_mode_guard_test.go`::`TestBackendModeUserGuard*`

运行命令：

```bash
# 单元 (本地，秒级)
go test -tags=unit -count=1 -run 'BackendMode|TestUpdateSettings_InvalidatesBackendModeCache' \
  ./backend/internal/service/... \
  ./backend/internal/server/middleware/...

# 全包回归 (~90s)
go test -tags=unit -count=1 ./backend/internal/service/... ./backend/internal/server/middleware/... ./backend/internal/server/routes/...

# Migration 幂等 (集成测试需 Docker)
go test -tags=integration -count=1 ./backend/internal/repository/...
```

## Evidence

- PR: https://github.com/youxuanxue/sub2api/pull/2
- CI（PR #2 head sha）：8/8 checks pass —— `backend-security` / `frontend-security` / `golangci-lint` / `test`（×2 runs）。
- 本地自检（commit `224f80ad`）：
  - `go build ./...` clean
  - `go vet ./...` clean
  - `go test -tags=unit ./...` → **39 packages OK，0 FAIL**
  - `golangci-lint run ./internal/service/... ./internal/server/middleware/... ./internal/server/routes/...` → **0 issues**

## Status

- [x] InTest（PR #2 等 review；merge 后转 Done，并随下一次 prod 部署 `v1.3.0` 收尾验证）
