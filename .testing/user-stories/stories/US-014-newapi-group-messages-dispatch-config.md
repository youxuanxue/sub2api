# US-014-newapi-group-messages-dispatch-config

- ID: US-014
- Title: newapi group 配置 `messages_dispatch_model_config` 持久化与读取
- Version: V1.1
- Priority: P1
- As a / I want / So that: 作为 TokenKey 管理员，我希望可以为 newapi group 配置 `messages_dispatch_model_config`（模型映射）并经 admin API 写入/读出 PG 后保留全部字段，以便 newapi 平台与 openai 平台对等支持"一站式 K"自定义映射。
- Trace: design `docs/approved/newapi-as-fifth-platform.md` `US-NEWAPI-007`（角色×能力：管理员 × Group 配置 CRUD）
- Risk Focus:
  - 逻辑错误：admin 创建/更新 newapi group 时 `sanitizeGroupMessagesDispatchFields` 不能强清字段
  - 行为回归：anthropic / gemini / antigravity group 的 sanitize 行为不变
  - 安全问题：admin 权限校验与既有路径完全一致（不在本设计扩展）
  - 运行时问题：PG 序列化/反序列化 `MessagesDispatchModelConfig` JSON 字段无破坏

## Acceptance Criteria

1. AC-001 (正向 / 写入)：Given admin 为 newapi group 配置 `AllowMessagesDispatch=true` + `MessagesDispatchModelConfig` 非空，When 写入 PG 并重新读取，Then 三字段全部保留原值 (`TestUS014_NewAPIGroup_MessagesDispatchConfig_RoundTrip`)。
2. AC-002 (回归 / 非 OpenAI-compat 平台仍清空)：Given anthropic group 配置同样字段，When sanitize 后写入 PG，Then 三字段被清空 (`TestUS014_AnthropicGroup_MessagesDispatchConfig_Cleared`)。
3. AC-003 (回归 / openai 不变)：Given openai group 配置，When sanitize，Then 行为完全不变（与 design §2.3 表对齐）(`TestUS014_OpenAIGroup_MessagesDispatchConfig_Preserved`)。

## Assertions

- AC-001 后：`g.AllowMessagesDispatch == true && g.MessagesDispatchModelConfig.equal(input)`
- AC-002 后：`g.AllowMessagesDispatch == false && g.MessagesDispatchModelConfig == zero`
- AC-003 后：openai group 字段值与传入完全一致
- 失败时 testify `require` 立即终止

## Linked Tests

- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS014_NewAPIGroup_MessagesDispatchConfig_RoundTrip`
- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS014_AnthropicGroup_MessagesDispatchConfig_Cleared`
- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS014_OpenAIGroup_MessagesDispatchConfig_Preserved`
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS014_' ./internal/service/`

## Evidence

- `.testing/user-stories/attachments/us014-newapi-messages-dispatch-config-run.txt`

## Status

- [ ] Draft
