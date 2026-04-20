# US-011-openai-pool-not-polluted-by-newapi

- ID: US-011
- Title: openai group 调度池**不被** newapi 账号污染（混池漏洞防御）
- Version: V1.1
- Priority: P0
- As a / I want / So that: 作为 TokenKey 网关运营方，我希望严格保证"一个 group 一个意图"——`group.platform=openai` 的请求绝不会被分配到 `account.platform=newapi` 的账号上，以便满足设计 §2.1 "不混池"原则、避免计费/凭证错位。
- Trace: design `docs/approved/newapi-as-fifth-platform.md` `US-NEWAPI-004`（防御需求 / 安全风险：跨平台调度泄漏）
- Risk Focus:
  - 逻辑错误：`IsOpenAICompatPoolMember(groupPlatform)` 判定必须严格按 `account.Platform == groupPlatform`
  - 行为回归：openai group 仍然只挑 openai 账号
  - 安全问题：**核心安全断言**——openai group 在任何路径（loadBalance / sticky / freshRecheck）都拿不到 newapi 账号
  - 运行时问题：scheduler bucket cache 按 platform 分桶不互窜

## Acceptance Criteria

1. AC-001 (正向)：Given `account.Platform="openai"` + `groupPlatform="openai"`，When `account.IsOpenAICompatPoolMember("openai")`，Then 返回 `true` (`TestUS011_PoolMember_OpenAIAccountInOpenAIGroup`)。
2. AC-002 (安全 / 跨池排除)：Given `account.Platform="newapi"` + `groupPlatform="openai"`，When `IsOpenAICompatPoolMember("openai")`，Then 返回 `false` (`TestUS011_PoolMember_NewAPIAccountInOpenAIGroup_Rejected`)。
3. AC-003 (安全 / 反向也不混)：Given `account.Platform="openai"` + `groupPlatform="newapi"`，When `IsOpenAICompatPoolMember("newapi")`，Then 返回 `false` (`TestUS011_PoolMember_OpenAIAccountInNewAPIGroup_Rejected`)。
4. AC-004 (集成 / loadBalance 过滤)：Given openai group 池中混入一个 newapi 账号（被错配），When `selectByLoadBalance`，Then newapi 账号被过滤掉，最终选中的账号 `Platform == "openai"` (`TestUS011_LoadBalance_FiltersOutNewAPIFromOpenAIGroup`)。
5. AC-005 (集成 / sticky 强一致)：Given openai group 的 sticky session 因 cache 错位指向 newapi 账号 ID，When sticky 命中检查，Then `IsOpenAICompatPoolMember` 失败导致 sticky 失效降级 (`TestUS011_Sticky_FailsOver_WhenAccountChangedPlatform`)。

## Assertions

- AC-002/003 后：`a.IsOpenAICompatPoolMember(other) == false`
- AC-004 后：`selectedAccount.Platform == "openai"` 永远成立
- AC-005 后：sticky session 被删除，selectByLoadBalance 重新选 openai 账号
- 失败时 testify `require` 立即终止

## Linked Tests

Pure predicate (this PR):

- `backend/internal/service/account_tk_compat_pool_test.go`::`TestUS011_PoolMember_OpenAIAccountInOpenAIGroup`
- `backend/internal/service/account_tk_compat_pool_test.go`::`TestUS011_PoolMember_NewAPIAccountInOpenAIGroup_Rejected`
- `backend/internal/service/account_tk_compat_pool_test.go`::`TestUS011_PoolMember_OpenAIAccountInNewAPIGroup_Rejected`
- `backend/internal/service/account_tk_compat_pool_test.go`::`TestUS011_PoolMember_NilAccount_False`
- `backend/internal/service/account_tk_compat_pool_test.go`::`TestUS011_PoolMember_EmptyGroupPlatform_False`
- `backend/internal/service/account_tk_compat_pool_test.go`::`TestUS011_PoolMember_UnknownPlatform_False`

Scheduler-tier (this PR, mocked snapshot, exercises the security filter at
the loadBalance and sticky boundaries — covers AC-004 / AC-005):

- `backend/internal/service/openai_account_scheduler_tk_newapi_test.go`::`TestUS011_LoadBalance_FiltersOutNewAPIFromOpenAIGroup`
- `backend/internal/service/openai_account_scheduler_tk_newapi_test.go`::`TestUS011_LoadBalance_FiltersOutOpenAIFromNewAPIGroup`
- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS011_Sticky_FailsOver_WhenAccountChangedPlatform`
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS011_' ./internal/service/`

## Evidence

- `.testing/user-stories/attachments/us-newapi-unit-run-2026-04-19.txt`

## Status

- [x] InTest (5 ACs all covered at the right tier; merge gate)
