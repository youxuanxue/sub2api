# US-034-messages-compaction-policy

- ID: US-034
- Title: OpenAI-compat `/v1/messages` 大输入自动压缩（账号优先、分组兜底）
- Version: V1.6
- Priority: P1
- As a / I want / So that:
  作为 **TokenKey 网关运维与调用方**，我希望 **在 OpenAI-compat 长会话输入过大时，网关按账号优先、分组兜底策略自动触发压缩，并在续接回退时产出可观测分类字段**，**以便** 在不改全局配置的前提下稳定长轮次质量并快速定位回退原因。

- Trace:
  - 设计锚点：`docs/approved/messages-compaction-policy.md`
  - 数据模型：`groups.messages_compaction_enabled` / `groups.messages_compaction_input_tokens_threshold`
  - 策略优先级：`account.extra > group > disabled`
- Risk Focus:
  - 逻辑错误：账号/分组策略优先级判断错误导致误压缩或漏压缩。
  - 行为回归：未配置策略时默认行为被改写，影响历史会话路径。
  - 运行时问题：`previous_response_id` 回退后状态机处理不一致导致重复失败重试。

## Acceptance Criteria

1. **AC-001（策略优先级）**：Given account 与 group 同时配置，When account 明确配置，Then 账号策略优先于分组。
2. **AC-002（显式关闭覆盖）**：Given account 明确 `messages_compaction_enabled=false` 且 group 开启，When 请求命中 compat replay guard，Then 不触发压缩。
3. **AC-003（阈值触发）**：Given 策略开启且阈值 N，When `input_tokens >= N`，Then 触发压缩；When `< N`，Then 不触发。
4. **AC-004（默认不变）**：Given account/group 均无策略字段，When 请求进入该路径，Then 行为与历史一致（不压缩）。
5. **AC-005（回退观测）**：Given `previous_response_id` 回退发生，When 原因为 not_found/unsupported，Then 日志包含 `compat_previous_response_fallback_reason`，并正确标注 `compat_continuation_disabled_after_fallback`。
6. **AC-006（透传链路）**：Given admin 创建/更新 group 策略字段，When 经 repo/cache/dto/contract 路径读取，Then 字段值一致且可见。

## Assertions

- 策略解析优先级保持 `account.extra > group > disabled`，且账号显式关闭时不会被分组开启覆盖。
- `input_tokens` 达到阈值才触发压缩，阈值无效（<1）或策略关闭时不压缩。
- `previous_response_id` 回退日志按 `not_found|unsupported` 分类，且 continuation disable 标记与账号类型语义一致。
- 透传链路 create/update/repo/cache/contract 中新增字段值保持一致。

## Linked Tests

- `backend/internal/service/openai_messages_compaction_tk_test.go`::`TestResolveOpenAICompatMessagesCompactionPolicy_AccountOverridesGroup`
- `backend/internal/service/openai_messages_compaction_tk_test.go`::`TestResolveOpenAICompatMessagesCompactionPolicy_AccountDisableWins`
- `backend/internal/service/openai_messages_compaction_tk_test.go`::`TestShouldApplyOpenAICompatMessagesCompaction`
- `backend/internal/service/openai_compat_model_test.go`::`TestForwardAsAnthropic_ReplaysWithoutContinuationWhenPreviousResponseMissing`
- `backend/internal/service/openai_compat_model_test.go`::`TestForwardAsAnthropic_OAuthDisablesContinuationAfterPreviousResponseNotFound`
- `backend/internal/service/openai_messages_replay_guard_test.go`::`TestApplyAnthropicCompatFullReplayGuard_KeepsToolBoundaryIntact`
- `backend/internal/service/admin_service_group_test.go`::`TestAdminService_CreateGroup_NormalizesMessagesDispatchModelConfig`
- `backend/internal/service/api_key_service_cache_test.go`::`TestAPIKeyService_SnapshotRoundTrip_PreservesMessagesDispatchModelConfig`
- `backend/internal/server/api_contract_test.go`::`TestAPIContracts`

运行命令：

```bash
cd backend
go test -tags=unit ./internal/service/... -run "TestOpenAICompatMessagesCompaction|TestForwardAsAnthropic|TestOpenAICompatContinuation|TestAdminService.*Group|TestAPIKeyService"
go test -tags=unit ./internal/server/... -run "TestAPIContract"
```

## Status

- [x] InTest — compaction policy优先级与continuation回退路径已由 service 单测覆盖；admin/repo/cache/contract 透传链路断言已纳入现有测试。
