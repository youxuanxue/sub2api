# US-056 Edge 管理会话安全交接

- ID: US-056
- Priority: P1
- Title: 一键进入 Edge，读取凭据不能签发管理会话
- As a / I want / So that: 管理员希望从控制台一键进入 Edge，同时凭据只在对应安全边界内流转。
- Trace: `docs/approved/edge-admin-handoff-v2.md`（本会话已批准实现）
- Risk Focus:
  - 逻辑错误：授权过期、重复使用、错误 proof 或 attempt 不应建立会话。
  - 行为回归：两个管理入口继续一键进入；不可用时仍能直接登录。
  - 安全问题：独立签发权限、固定 audience、精确窗口绑定、凭据不进 URL/父页面。
  - 运行时：并发兑换、Redis 故障、迟到响应、权限撤销、会话签发失败。

## Acceptance Criteria

1. AC-001（正向）：管理员点击后打开干净的 Edge 页面，兑换会话并进入账号管理。
2. AC-002（负向）：镜像 Key 不能签发会话；篡改、错误目标、过期与重放均被拒绝。
3. AC-003（安全）：错误 verifier 或窗口来源不能完成交接，也不能消费合法兑换记录。
4. AC-004（并发）：同一交接码只有一次成功兑换；Redis 故障不得绕过消费检查。
5. AC-005（回归）：控制台两个入口共享同一生命周期 owner；失败提供重试和直接登录。
6. AC-006（撤销）：Edge 管理员被停用后无法兑换；签发后的会话族可按交接审计标识撤销。

7. AC-007（故障隔离）：非法配置关闭交接但不阻断服务初始化和普通登录；交接基础设施失败用 503，不触发代理 502/504 的健康摘除策略。

## Assertions

断言返回凭据的接收窗口、URL、错误返回、消费次数、重放和伪造窗口消息的实际拒绝行为。

## Linked Tests

- Run command: `pnpm --dir frontend exec playwright test --config playwright.edge-handoff.config.ts`

- `frontend/e2e/edge-handoff.e2e.ts`：既有两个入口、干净 URL、可续期会话、弹窗受阻、失败重试、旧 URL 拒绝。
- `frontend/src/composables/__tests__/useEdgeAdminHandoff.tk.spec.ts`：窗口/来源绑定、超时和迟到 mint、卸载清理。
- `frontend/src/stores/__tests__/auth.spec.ts`：取消交接后清理本次凭据，迟到用户响应不恢复会话、不影响后续登录。
- `frontend/src/views/admin/__tests__/EdgeHandoffView.spec.ts`：同源兑换、旧 URL 拒绝、迟到响应不安装会话，加载用户期间超时、卸载或 pagehide 取消本次安装。
- `backend/internal/service/edge_admin_handoff_tk_test.go`::`TestEdgeAdminHandoff_DelegationValidation`：签名、issuer/audience/purpose、过期、kid 撤销。
- `backend/internal/service/edge_admin_handoff_forward_tk_test.go`：目标固定、无镜像 Key、拒绝重定向。
- `backend/internal/handler/edge_tk_admin_session_handler_test.go`：一次性 mint、错误 proof 不消费、来源/管理员权限、旧接口 410。
- `backend/internal/handler/admin/edge_accounts_handler_tk_test.go`：当前 JWT 发起者、仅返回 code、原有库存刷新回归。
- `backend/internal/repository/edge_admin_handoff_cache_tk_integration_test.go`::`TestEdgeAdminHandoffRedis`：真实 Redis 并发/故障/过期、刷新轮转与定向族撤销。
- `backend/internal/service/auth_service_tk_edge_session_test.go`：初次/轮转族索引失败关闭。
- `backend/internal/server/middleware/audit_log_test.go`：交接请求体不落审计日志。
- `backend/internal/config/edge_handoff_tk_test.go` + `backend/cmd/edge-handoff-config/main_test.go`：信任配置、独立密钥、私有权限与禁止覆盖。

- `backend/internal/service/edge_admin_handoff_tk_test.go`::`TestEdgeAdminHandoffBadConfigurationIsIsolated`：缺失、非法、不可读、权限及部分配置全拒绝，初始化继续。
- `backend/internal/handler/admin/edge_accounts_handler_tk_test.go`::`TestMintAdminSession_EdgeFailureDoesNotMarkGatewayUnhealthy`：交接错误不返回代理故障状态。
- `frontend/e2e/edge-handoff.e2e.ts`：`EDGE_HANDOFF_E2E_INVALID=1` 配合 `--grep "invalid trust"` 验证坏配置下两个实例的普通登录。

## Evidence

生产浏览器入口：`pnpm --dir frontend exec playwright test --config playwright.edge-handoff.config.ts`。
本机先 build frontend；隔离 fixture 使用真实 Go handler/AuthService/JWT/Redis，固定用户/库存
代替生产 DB/Edge 发现。真实部署网络、配置分发和历史会话不在本地验收范围，
见 `docs/ops/edge-admin-handoff.md`。CI candidate-browser job 执行相同 UI 旅程。

刷新族撤销只撤销刷新能力；已有 access token 沿用原有效期，用户状态/TokenVersion
守卫仍实时生效，不新增立即撤销单个 access token 的承诺。

## Status

- Done
