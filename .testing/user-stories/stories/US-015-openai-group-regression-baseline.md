# US-015-openai-group-regression-baseline

- ID: US-015
- Title: 历史 openai group 行为完全不变（回归基线）
- Version: V1.1
- Priority: P0
- As a / I want / So that: 作为 prod 运营方，我希望本设计 §3.1 的 7 处 upstream 注入点完全不影响历史 openai group 的任何调度/sticky/recheck/messages_dispatch 行为，以便升级后既有客户保持平稳、回滚成本最低。
- Trace: design `docs/approved/newapi-as-fifth-platform.md` `US-NEWAPI-008`（防御需求 / 行为回归）
- Risk Focus:
  - 逻辑错误：`groupPlatform=""` 或未传时必须等价于历史"硬编码 PlatformOpenAI"行为
  - 行为回归：scheduler bucket key 中 openai group 部分完全保持原 `(groupID, "openai", mode)`
  - 安全问题：openai group 不能因为本次改动意外吃到 newapi 账号（与 US-011 联合保护）
  - 运行时问题：scheduler cache 升级前已 warm 的 openai bucket 升级后仍命中（cache 不需 invalidate）

## Acceptance Criteria

1. AC-001 (回归 / 默认调度)：Given `group.platform=openai` + 标准账号集合，When 走 `selectByLoadBalance`，Then 选中账号与升级前快照完全一致 (`TestUS015_OpenAIGroup_LoadBalance_BehaviorUnchanged`)。
2. AC-002 (回归 / sticky 路径)：Given openai group + sticky session 已写入，When sticky 命中，Then recheck 通过且 `selectedAccount` 与历史一致 (`TestUS015_OpenAIGroup_Sticky_BehaviorUnchanged`)。
3. AC-003 (回归 / fresh recheck)：Given openai 账号通过 `resolveFreshSchedulableOpenAIAccount`，When 验证，Then 与历史返回一致 (`TestUS015_OpenAIGroup_FreshRecheck_BehaviorUnchanged`)。
4. AC-004 (回归 / messages_dispatch sanitize)：Given openai group + messages_dispatch 配置，When sanitize，Then 字段保留（行为不变）(`TestUS015_OpenAIGroup_MessagesDispatchSanitize_Unchanged`)。
5. AC-005 (运行时 / cache 兼容)：Given scheduler bucket cache 已 warm `(groupID, "openai", mode)`，When 升级后第一个请求，Then bucket key 仍命中（无 cache miss 风暴）(`TestUS015_SchedulerBucketCache_OpenAIKeyStillHits`)。

## Assertions

- AC-001 后：选中账号 ID 与历史 fixture 一致
- AC-002 后：sticky `selectedAccount.ID` 与历史一致
- AC-003 后：`resolveFreshSchedulableOpenAIAccount` 返回与历史一致（同账号或同 nil 决策）
- AC-004 后：openai group `g.AllowMessagesDispatch` 等字段不变
- AC-005 后：bucket cache hit 计数 +1，miss 计数不变
- 失败时 testify `require` 立即终止

## Linked Tests

- `backend/internal/service/openai_account_scheduler_tk_newapi_test.go`::`TestUS015_OpenAIGroup_LoadBalance_BehaviorUnchanged`
- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS015_OpenAIGroup_Sticky_BehaviorUnchanged`
- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS015_OpenAIGroup_FreshRecheck_BehaviorUnchanged`
- `backend/internal/service/openai_messages_dispatch_tk_newapi_test.go`::`TestUS015_OpenAIGroup_MessagesDispatchSanitize_Unchanged`
- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS015_SchedulerBucketCache_OpenAIKeyStillHits`
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS015_' ./internal/service/`

## Evidence

- `.testing/user-stories/attachments/us015-openai-group-regression-run.txt`

## Status

- [ ] Draft
