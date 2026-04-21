# US-003-gateway-responses-chat-affinity-entrypoints

- ID: US-003
- Title: Gateway responses/chat affinity integration
- Version: MVP
- Priority: P0
- As a / I want / So that: 作为平台管理员，我希望 Anthropic 网关兼容入口 `/v1/responses` 与 `/v1/chat/completions` 也接入 Affinity 选路，以便不同入口保持一致的优选账号命中行为。
- Trace: 角色 x 能力（同分组多入口调度一致性）；系统事件（每次账号选择都应复用 affinity 线索）
- Risk Focus:
- 逻辑错误：入口未注入 affinity 预取上下文，导致调度退化为普通负载选择
- 行为回归：成功转发后未记录 affinity success，影响后续命中率
- 安全问题：不适用：本变更不涉及鉴权与权限模型变更

## Acceptance Criteria

1. AC-001 (正向): Given `/v1/responses` 或 `/v1/chat/completions` 请求命中 affinity，When 执行账号选择，Then 通过 `WithPrefetchedStickySession` 注入预取账号上下文。
2. AC-002 (负向): Given affinity 未命中，When 执行账号选择，Then 保持原有选择路径，不注入预取上下文。
3. AC-003 (回归): Given 请求转发成功，When handler 结束，Then 记录 `MarkAffinitySelected` 与 `RecordAffinitySuccess`。

## Assertions

- `gateway_handler_responses.go` 与 `gateway_handler_chat_completions.go` 在选路前调用 `GetPreferredAccountByAffinity`。
- 两个入口在选中账号时调用 `MarkAffinitySelected`。
- 两个入口在成功返回后调用 `RecordAffinitySuccess`。

## Linked Tests

- `backend/internal/handler/openai_gateway_affinity_test.go`::`TestWithAffinityPrefetchedSession_Hit`
- `backend/internal/handler/openai_gateway_affinity_test.go`::`TestWithAffinityPrefetchedSession_Miss`
- **说明**：`/v1/responses` 与 `/v1/chat/completions` 在选路前通过 `OpenAIGatewayHandler.withAffinityPrefetchedSession` 注入上下文；与 US-002 共用同一组单测（无独立 HTTP 级用例），Assertions 中的 handler 行为以代码审阅与整包回归为准。
- 运行命令: `cd backend && go test -count=1 ./internal/handler -run 'TestWithAffinityPrefetchedSession_(Hit|Miss)' && go test -count=1 ./internal/handler` (行为断言 + 整包回归)

## Evidence

- （无附件归档；以 Linked Tests 命令输出为准）

## Status

- Done