# US-002-openai-affinity-entrypoints

- ID: US-002
- Title: OpenAI entrypoints affinity prefetch integration
- Version: MVP
- Priority: P0
- As a / I want / So that: 作为平台管理员，我希望 OpenAI 的 `/v1/responses` 与 `/v1/chat/completions` 也能复用 Affinity 选路能力，以便同一模型请求更稳定地命中历史优选账号并降低抖动。
- Trace: 角色 x 能力（管理员配置账号后，用户请求在多入口获得一致调度）；系统事件（每次网关选路都应复用 Affinity 线索）
- Risk Focus:
- 逻辑错误：Affinity 命中但未写入预取上下文，导致调度无法复用优选账号
- 行为回归：OpenAI Responses 在 `previous_response_id` 场景被 Affinity 覆盖，破坏已有粘性语义
- 安全问题：不适用：本变更不新增权限边界或敏感数据暴露面

## Acceptance Criteria

1. AC-001 (正向): Given Affinity 命中账号且请求模型非空，When OpenAI 入口构造调度上下文，Then 在上下文中写入 `prefetched_sticky_account_id/group_id`。
2. AC-002 (负向): Given Affinity 未命中，When 构造调度上下文，Then 不写入任何预取粘性字段。
3. AC-003 (回归): Given OpenAI Responses 带 `previous_response_id`，When 进行选路，Then 保持原有 previous-response 优先语义（仅在无 previous-response 时应用 Affinity 预取）。

## Assertions

- `withAffinityPrefetchedSession` 在命中时写入 `PrefetchedStickyAccountID/PrefetchedStickyGroupID`。
- `withAffinityPrefetchedSession` 在未命中时不污染上下文。
- `Responses` 入口仅在 `previous_response_id` 为空时注入 Affinity 预取上下文。

## Linked Tests

- `backend/internal/handler/openai_gateway_affinity_test.go`::`TestWithAffinityPrefetchedSession_Hit`
- `backend/internal/handler/openai_gateway_affinity_test.go`::`TestWithAffinityPrefetchedSession_Miss`
- 运行命令: `cd backend && go test ./internal/handler -run 'TestWithAffinityPrefetchedSession_(Hit|Miss)'`

## Evidence

- （无附件归档；以 Linked Tests 命令输出为准）

## Status

- Done