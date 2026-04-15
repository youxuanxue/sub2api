# US-005-newapi-core-with-upstream-convergence

- ID: US-005
- Title: Preserve newapi core while converging peripheral TK features to upstream
- Version: V1.0
- Priority: P0
- As a / I want / So that: 作为 TokenKey 运维，我希望第 5 平台 `newapi` 的 OpenAI-compatible 路由、bridge 分发与管理端辅助入口在 upstream rebase 后继续可用，同时 passkey、backend mode、payment/webhook 等外围能力收敛回 upstream，以便核心产品能力不中断且后续合并冲突更少。
- Trace: [角色 × 能力] + [防御需求]
- Risk Focus:
- 逻辑错误：`newapi` 组落回 Anthropic 路由，导致 `/messages`、`/responses`、`/chat/completions`、`/embeddings`、`/images/generations` 分发错误。
- 行为回归：移除 passkey / backend mode 后，前端登录页、路由守卫、admin settings 或构建产物仍残留旧开关，造成 UI/路由异常。
- 安全问题：不适用：本次未放宽权限边界，主要是路由接线与配置开关对齐。
- 运行时问题：仓库里残留 passkey schema、backend mode 守卫或未挂载的 TK webhook 实现，导致生成代码、构建或维护误判 live path。

## Acceptance Criteria

1. AC-001 (正向): Given API Key 所属分组平台为 `newapi`，When 请求 `/v1/messages`、`/v1/responses`、`/v1/chat/completions`、`/v1/embeddings`、`/v1/images/generations` 及其无 `/v1` 别名，Then 路由均已注册并走 OpenAI-compatible 入口。
2. AC-002 (正向): Given admin 访问 `channel-types` 与 `aggregated-group-models` 等 TokenKey-only 管理端入口，When `RegisterAdminRoutes` 完成注册，Then 路由可达且不再 404。
3. AC-003 (收敛): Given admin settings 仅保留 `newapi_bridge_enabled` 这一项 TK 核心开关，When `SettingService.UpdateSettings` 与 `parseSettings` 执行，Then 读写结果保持一致，且不再暴露 `passkey_enabled` / `backend_mode_enabled`。
4. AC-004 (收敛): Given payment/webhook、passkey、backend mode 采用 upstream 能力，When 检查 live router、前端登录页、路由守卫与仓库实现，Then 不再保留对应 TK 分支能力制造分叉。

## Assertions

- `RegisterGatewayRoutes` 为 `newapi` 组注册并放通 OpenAI-compatible 5 条核心入口及别名。
- `OpenAIGatewayHandler` 的 `messages` / `responses` / `chat_completions` 通过 `*Dispatched` 方法进入 `channel_type` bridge 分发链路。
- `RegisterAdminRoutes` 正式注册 `channel-types` / `channel-type-models` / `fetch-upstream-models` / `aggregated-group-models`。
- `SettingService` 只持久化并解析 `newapi_bridge_enabled` 这一项 TokenKey bridge 开关。
- passkey / backend mode 相关 handler、route、schema、frontend flow 与依赖已移除。
- payment/webhook 只保留 upstream live path，不再保留未挂载的 TK webhook 支线。

## Linked Tests

- `backend/internal/server/routes/gateway_test.go`::`TestGatewayRoutesNewAPICompatPathsAreRegistered`
- `backend/internal/server/routes/admin_routes_test.go`::`TestAdminRoutesTokenKeyChannelHelpersAreRegistered`
- `backend/internal/service/setting_service_update_test.go`::`TestSettingService_UpdateSettings_TokenKeyBridge`
- `backend/internal/service/setting_service_update_test.go`::`TestSettingService_ParseSettings_TokenKeyBridge`
- `backend/internal/handler/endpoint_test.go`::`TestNormalizeInboundEndpoint`
- `backend/internal/handler/endpoint_test.go`::`TestDeriveUpstreamEndpoint`
- `frontend/src/router/__tests__/guards.spec.ts`
- 运行命令: `cd backend && go test -tags=unit ./internal/server/routes ./internal/handler ./internal/service`
- 运行命令: `cd frontend && pnpm lint:check && pnpm typecheck && pnpm build`

## Evidence

- （无附件归档；以 Linked Tests、contract check 与本次全盘校验输出为准）

## Status

- Done
