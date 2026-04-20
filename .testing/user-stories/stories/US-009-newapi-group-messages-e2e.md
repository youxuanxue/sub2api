# US-009-newapi-group-messages-e2e

- ID: US-009
- Title: newapi group + `/v1/messages`（一站式 K）端到端走通
- Version: V1.1
- Priority: P0
- As a / I want / So that: 作为 TokenKey 网关运营方，我希望 `group.platform=newapi` 的分组通过 messages_dispatch 能力把 Anthropic `/v1/messages` 请求落到 OpenAI 兼容上游，以便 newapi 平台与 openai 平台对等支持"一站式 K"模式。
- Trace: design `docs/approved/newapi-as-fifth-platform.md` `US-NEWAPI-002`（角色×能力：客户端 × Messages 调度 + 模型映射）
- Risk Focus:
  - 逻辑错误：`sanitizeGroupMessagesDispatchFields` 必须对 newapi group 放行（不强清 `MessagesDispatchModelConfig`）
  - 行为回归：anthropic / gemini / antigravity group 的 sanitize 行为不变（仍强清）
  - 安全问题：messages_dispatch 配置不会被跨 group 污染
  - 运行时问题：mapped model 解析与 OpenAI compat dispatch 路径与 openai group 完全等价

## Acceptance Criteria

1. AC-001 (正向)：Given `group.platform=newapi` + `AllowMessagesDispatch=true` + 配置了 `messages_dispatch_model_config`，When `sanitizeGroupMessagesDispatchFields(g)` 被调用，Then `g.AllowMessagesDispatch` / `g.DefaultMappedModel` / `g.MessagesDispatchModelConfig` **保留不变** (`TestUS009_Sanitize_NewAPIGroup_Preserves`)。
2. AC-002 (负向 / 非 OpenAI-compat)：Given `group.platform=anthropic` 配置了 messages_dispatch 字段，When sanitize，Then 三字段被强清 (`TestUS009_Sanitize_AnthropicGroup_Cleared`)；`gemini` / `antigravity` 同理。
3. AC-003 (回归)：Given `group.platform=openai`，When sanitize，Then 行为完全不变 (`TestUS009_Sanitize_OpenAIGroup_Preserves`)。
4. AC-004 (端到端)：Given `group.platform=newapi` + 配 messages_dispatch，When POST `/v1/messages` 含 Anthropic body，Then 网关推导出 mapped model 并经 OpenAI bridge dispatch 转发，返回 200 (`TestUS009_NewAPIGroup_Messages_E2E`)。

## Assertions

- AC-001 后：`g.AllowMessagesDispatch == true && g.MessagesDispatchModelConfig != zero`
- AC-002 后：`g.AllowMessagesDispatch == false && g.DefaultMappedModel == "" && g.MessagesDispatchModelConfig == zero`
- AC-003 后：sanitize 后字段值与传入一致（不变）
- AC-004 后：上游请求体走 OpenAI Chat Completions 协议且 model 已被 mapped model 替换
- 失败时 testify `require` 立即终止

## Linked Tests

Sanitize predicate (this PR, unit, behavioral):

- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS009_Sanitize_NewAPIGroup_Preserves`
- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS009_Sanitize_AnthropicGroup_Cleared`
- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS009_Sanitize_GeminiGroup_Cleared`
- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS009_Sanitize_AntigravityGroup_Cleared`
- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS009_Sanitize_OpenAIGroup_Preserves`
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS009_' ./internal/service/`

HTTP+PG end-to-end (follow-up PR, see `docs/preflight-debt.md` §4):

- `backend/internal/handler/openai_messages_tk_newapi_integration_test.go`::`TestUS009_HTTP_NewAPIGroup_Messages_E2E` *(planned)*

## Evidence

- `.testing/user-stories/attachments/us-newapi-unit-run-2026-04-19.txt`

## Status

- [x] Draft

> **Honest status note (2026-04-20 audit)**: 此故事的核心 AC 是「`POST /v1/messages` Anthropic 协议端到端走通」。当前 PR 仅交付 `sanitizeGroupMessagesDispatchFields` 的 mock 单测（覆盖 newapi group 配置保留、非-compat group 配置清空），未跑过任何真 Anthropic→OpenAI 协议转换 + 真 newapi upstream 响应的 HTTP e2e。AC 标注的 `TestUS009_HTTP_NewAPIGroup_Messages_E2E` 仍是 *(planned)*。按 `test-philosophy.mdc §6` 纪律，本故事 status 保持 `Draft`，待 follow-up PR `feature/newapi-fifth-platform-e2e` 跑通真 e2e 后升级。
