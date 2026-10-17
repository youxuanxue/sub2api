# US-066 Cursor retirement

- ID: US-066
- Title: Cursor retirement
- Priority: P1
- As a / I want / So that: 作为运营者，我要完整退役 Cursor 账号和连接能力，使产品和后台不再保留运行入口。
- Trace: `docs/approved/cursor-retirement.md`；管理员 × 账号生命周期；用户 × 接入指南。
- Risk Focus:
  - 逻辑错误：旧账号 active/schedulable 不得绕过退役边界。
  - 行为回归：其他供应商和客户端接入继续工作。
  - 安全问题：修改平台、删除 provider 标记后，旧专用凭据或 endpoint 仍不能恢复供应。
  - 运行时：移除授权会话、continuation 与后台 refresh；保留历史账单，不做 schema 迁移。

## Acceptance Criteria

1. AC-001（正向）：管理页与接入指南移除 Cursor；用户可以继续选择 Codex。
2. AC-002（负向）：旧授权/导入 API 返回 404；旧账号无法调度或重新创建。
3. AC-003（回归）：普通 API-key 账号可创建和调度；其他供应商原生协议行为保持。

## Assertions

- 浏览器点击支持的客户端，配置区随之切换；无 Cursor 按钮或授权请求。
- 遗留 provider、endpoint 和凭据分别触发 `SUPPLIER_RETIRED`，无外部请求。
- `cursor.sh.example.test` 不会被误判为 Cursor 域名。

## Linked Tests

- `backend/internal/service/retired_supplier_test.go`::`TestUS066_RetiredSupplierCannotScheduleOrRestore`
- `backend/internal/service/retired_supplier_test.go`::`TestUS066_RetiredSupplierPreservesOrdinaryAPIKeys`
- `backend/internal/service/retired_supplier_test.go`::`TestUS066_RetiredSupplierDoesNotBlockProtocolReadiness`
- `backend/internal/repository/retired_supplier_test.go`::`TestUS066_RetiredSupplierCannotEnterRepositoryOrScheduler`
- `backend/internal/server/routes/retired_cursor_routes_test.go`::`TestUS066_RetiredCursorRoutesReturn404`
- `frontend/e2e/cursor-retirement.e2e.ts`::`retired Cursor entries stay absent and supported clients remain usable`
- Run command: `cd backend && go test ./internal/service ./internal/repository ./internal/server/routes -run TestUS066 -count=1`
- Run command: `cd frontend && pnpm exec playwright test --config playwright.cursor-retirement.config.ts`

## Evidence

本地 Playwright 已通过退役管理页、旧 Cursor 书签回退和 Codex 配置切换；原有 Quickstart 桌面/移动端客户端配置旅程也通过。退役浏览器用例已接入 backend-ci 的前端任务。

生产账号 #150 已通过管理员 API 停用并回读确认 `status=inactive`、`schedulable=false`。历史账单和用量保留。代码发布尚未执行，线上入口移除须发布后独立验收。

## Status

- InTest
