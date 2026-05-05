# US-031-onboarding-tour-unlock-for-regular-users

- ID: US-031
- Title: Onboarding Tour 对普通用户开放，且"已看过"用服务端字段记忆（跨设备/清缓存仍生效）
- Version: V1.6 (cold-start P1-A)
- Priority: P1
- As a / I want / So that:
  作为 **新注册的 TokenKey 普通用户**，我希望 **第一次登录 dashboard 时自动启动 Onboarding Tour 引导我完成"找到 trial Key → 找到模型清单 → 试一次"三步**，**以便** 我不再像 L 站 `t/topic/1413702` 用户描述的那样「点进去只见登录页 / 找不到入口」就直接关掉；同时 **已经看过的人不要再被打扰**——而且不要因为换浏览器、清 cookie、换设备就又看一次（PR 1 之前 `useOnboardingTour.ts:540` 提前 return 非 admin 用户，等同于"普通用户永远不会自动看 Tour"，也没有任何服务端状态——纯 localStorage，跨设备完全不一致）。

- Trace:
  - 角色 × 能力轴线：普通用户 × 「能在第一次登入 30 秒内被告知"key 在哪 / 模型在哪 / 怎么试一次"」——今天这个能力对非 admin 用户**完全不存在**。
  - 实体生命周期轴线：用户从「未看过 Tour」→「看过 Tour」→「再不自动启动」，本故事补齐这条隐含但缺失的状态迁移；服务端字段 `users.onboarding_tour_seen_at` 是 `NULL → time.Time` 的迁移边。
  - 系统事件轴线：每次登录 / 每次刷新 dashboard 触发 `useOnboardingTour` 的 `onMounted` 检查；本故事让该检查对普通用户也生效。
  - 防御需求轴线：Tour 完成回调必须落到服务端（不是仅 localStorage），否则换设备/清缓存就回退；服务端字段缺省 NULL → 默认显示。

- Risk Focus:
  - 逻辑错误：删除 `if (!isAdmin) return` 后必须保证 admin 行为完全不变（admin 仍走 admin steps，不是 user steps）；判断条件从 `localStorage.getItem(...)` 切换到 `userStore.user?.onboarding_tour_seen_at == null`，注意 OAuth 路径 / setToken 路径 / 普通登录路径 user 对象都必须包含该字段（DTO 必须 surface）。
  - 行为回归：现有 `getUserSteps` / `getAdminSteps` 不动；现有 driver.js 步骤顺序 / data-tour selector 不动；只改"何时启动" + "何时停止再启动" 两件事；`replayTour` 方法仍可用（人为复看不需要清服务端）。
  - 安全问题：`POST /api/v1/user/onboarding-tour-completed` 必须经过 JWTAuth + BackendModeUserGuard 标准链路；不暴露任何敏感字段，仅返回 `{success: true}`（`gin.H{"success": true}`，与 `totp_handler.go` 系列同 envelope 形态）；幂等（重复调不会改 timestamp 第二次，避免误以为"用户每次刷新 dashboard 都看了一次 Tour"）。
  - 运行时问题：服务端写入失败不阻塞前端 Tour 完成 UX（best-effort + retry on next mount）；migration 必须能在已有 production users 上跑（默认 `NULL`，不影响存量用户的"显示一次 Tour"行为——他们如果已经在 localStorage 标记 seen 就不会再看；如果没有标记就会看一次，符合预期）。

## Acceptance Criteria

1. **AC-001 (正向 / 普通用户首次自动启动)**：Given 新注册普通用户（`user.role == 'user'`、`user.onboarding_tour_seen_at == null`）、`autoStart: true`、未在 simple mode，When 进入 `/dashboard`，Then `driverInstance.isActive() === true`（自动启动 Tour），且 steps 来自 `getUserSteps(t)`。
2. **AC-002 (回归 / admin 首次仍自动启动)**：Given 新注册 admin（`user.role == 'admin'`、`user.onboarding_tour_seen_at == null`），When 进入 `/admin/dashboard`，Then `driverInstance.isActive() === true`，且 steps 来自 `getAdminSteps(t, isSimpleMode)`（admin 行为完全不动）。
3. **AC-003 (负向 / 已看过的不再自动启动)**：Given 用户 `user.onboarding_tour_seen_at != null`，When 进入 dashboard，Then `driverInstance == null` 或 `driverInstance.isActive() === false`（不自动启动），但 `replayTour()` 仍可手动触发。
4. **AC-004 (负向 / simple mode 不自动启动)**：Given `userStore.isSimpleMode == true`（任意角色），When 进入 dashboard，Then 不自动启动（保持现有 simple-mode 行为不动）。
5. **AC-005 (副作用 / Tour 完成调服务端)**：Given 用户首次自动启动 Tour 并点完最后一步「完成」，When `markAsSeen()` 被调用，Then `POST /api/v1/user/onboarding-tour-completed` 被请求一次，服务端写 `users.onboarding_tour_seen_at = NOW()`，下次 `GET /api/v1/user/profile` 返回的 user 对象包含该字段；且后续 `replayTour()` 不会清服务端字段（仅清 localStorage）。
6. **AC-006 (鲁棒 / 服务端写入失败不阻塞 UX)**：Given `POST /onboarding-tour-completed` 返回 500，When 用户点完 Tour，Then Tour 仍正常关闭（UX 无感），但下次进入 dashboard 会再启动一次 Tour（直到服务端写入成功）；server log 经 `response.ErrorFrom`（`response.go:90`）写出一行 `[ERROR] POST /api/v1/user/onboarding-tour-completed Error: mark onboarding tour seen: <wrapped repo err>`（结构化错误链由 service.go 的 `fmt.Errorf("...: %w", err)` 提供 context；handler 不再单独 slog 以避免双写）。
7. **AC-007 (幂等)**：Given 用户已 `seen_at = T1`，When 第二次调 `POST /onboarding-tour-completed`（例如人为 replay 后又点完），Then 服务端**不**更新 timestamp（仍是 T1），返回 200；防御"刷新 dashboard 误以为又看了一次"。
8. **AC-008 (回归 / 现有 admin / user steps 单测)**：Given 本 PR 落地，When 执行现有 `useOnboardingTour` 相关单测，Then 全部通过（不动现有断言）。

## Assertions

- 后端：`PATCH /user/profile` 之后 `users.onboarding_tour_seen_at` 仍为 `NULL`（不被 UpdateProfile 顺带写）；只有 `POST /user/onboarding-tour-completed` 才会写。
- 后端：第一次 POST → DB 行 `onboarding_tour_seen_at` 不为 NULL；第二次 POST → DB 行 `onboarding_tour_seen_at` 等于第一次写入的 timestamp（幂等断言）。
- 后端：`GET /user/profile` 返回 JSON 含 `"onboarding_tour_seen_at": <ISO8601 or null>`；admin 路径同样含该字段。
- 后端：mock UserRepository 必须提供 `MarkOnboardingTourSeen(ctx, userID) error` 方法；`admin_service_delete_test.go` 等 stub 全部实现该方法（接口完备性，CLAUDE.md §6）。
- 前端：composable 单测 `useOnboardingTour({autoStart: true})` 在 `userStore.user.onboarding_tour_seen_at == null && !isSimpleMode` 时启动 driver；在 `seen_at != null` 时不启动；在 `isSimpleMode == true` 时不启动。
- 前端：`markAsSeen()` 触发 `markOnboardingTourSeen()` 调用一次（mock 验证）。函数落在独立 `frontend/src/api/onboarding.ts`，避免 upstream-shaped `frontend/src/api/user.ts` 出现 TK 编辑（CLAUDE.md §5）。
- 迁移：`tk_005_add_users_onboarding_tour_seen_at.sql` 在已有 users 表上执行后，所有现有行 `onboarding_tour_seen_at IS NULL`（不影响存量用户行为）。

## Linked Tests

实现按 CLAUDE.md §5 隔离纪律落到 `*_tk_*` 伴侣文件：

- `backend/internal/handler/user_handler_tk_onboarding_test.go`::`TestUS031_MarkOnboardingTourSeen_FirstCall_WritesTimestamp` — AC-005 落地
- `backend/internal/handler/user_handler_tk_onboarding_test.go`::`TestUS031_MarkOnboardingTourSeen_Idempotent_SecondCallNoChange` — AC-007 幂等
- `backend/internal/handler/user_handler_tk_onboarding_test.go`::`TestUS031_MarkOnboardingTourSeen_Unauthenticated_401` — 401 兜底
- `backend/internal/handler/user_handler_tk_onboarding_test.go`::`TestUS031_MarkOnboardingTourSeen_SuccessEnvelopeShape` — 响应契约（`{success: true}`，与 `totp_handler.go` 系列对齐）锁定
- `backend/internal/service/user_service_tk_onboarding_test.go`::`TestUS031_MarkOnboardingTourSeen_DelegatesToRepo`
- `backend/internal/service/user_service_tk_onboarding_test.go`::`TestUS031_MarkOnboardingTourSeen_AlreadySeen_NoUpdate`
- `backend/internal/service/user_service_tk_onboarding_test.go`::`TestUS031_MarkOnboardingTourSeen_RepoError_PropagatesWrapped`
- `frontend/src/composables/__tests__/useOnboardingTour.tk.spec.ts`::`US-031 普通用户 Tour 解锁`
  - `frontend/src/composables/__tests__/useOnboardingTour.tk.spec.ts`::`AC-001 普通用户首次自动启动`
  - `frontend/src/composables/__tests__/useOnboardingTour.tk.spec.ts`::`AC-002 admin 首次自动启动`
  - `frontend/src/composables/__tests__/useOnboardingTour.tk.spec.ts`::`AC-003 已看过不再自动启动`
  - `frontend/src/composables/__tests__/useOnboardingTour.tk.spec.ts`::`AC-004 simple mode 不启动`
  - `frontend/src/composables/__tests__/useOnboardingTour.tk.spec.ts`::`AC-005 调用 markOnboardingTourSeen`

运行命令：

```bash
cd backend && go test -tags=unit -count=1 -v -run 'TestUS031_' ./internal/handler/... ./internal/service/...
cd frontend && pnpm vitest run src/composables/__tests__/useOnboardingTour.tk.spec.ts
```

## Evidence

- 完成事实：后端 7 个 unit test（handler 4 + service 3）+ 前端 7 个 composable test（auto-launch gate 5 + markAsSeen 2）全绿；以 Linked Tests 命令和 CI/preflight 输出为准。
- DB 迁移 evidence：`backend/migrations/tk_005_add_users_onboarding_tour_seen_at.sql` 在 fresh schema 与 existing schema 上 `psql -c '\d users'` 输出新增列。
- 前端 manual smoke：以 `role=user`、`onboarding_tour_seen_at=null` 用户登录 dashboard，driver.js popover 自动出现并完整走完 6 步 user steps；refresh dashboard 不再触发；`localStorage.removeItem(...)` 不会让它再触发（因为服务端字段是源真相）。

## Status

- [x] InTest — PR 2 已落地；backend 7 个 unit test（service 3 + handler 4，**handler 测试驱动的是真正的 `*UserHandler.MarkOnboardingTourSeen`**，不是测试桩）+ frontend 7 个 composable test 全绿；`tk_005_add_users_onboarding_tour_seen_at.sql` 新增 `users.onboarding_tour_seen_at` 列（默认 NULL，不影响存量用户）；`/api/v1/user/onboarding-tour-completed` 路由已接入 JWTAuth + BackendModeUserGuard 链路；`useOnboardingTour.ts` 删除 admin-only gate，改用 `userStore.user?.onboarding_tour_seen_at` 判断；`markAsSeen()` best-effort POST 到服务端（失败不阻塞 UX）。等待 integration / e2e 测试套件着陆后再翻 Done。
