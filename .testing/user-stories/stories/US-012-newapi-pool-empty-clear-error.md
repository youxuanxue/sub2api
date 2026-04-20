# US-012-newapi-pool-empty-clear-error

- ID: US-012
- Title: newapi group 池空时返回明确错误，且 channel_type=0 账号不入池
- Version: V1.1
- Priority: P1
- As a / I want / So that: 作为运营方排查问题时，我希望 newapi group 池空时返回的是清晰的"无可用 newapi 账号"错误（而非含糊的"openai 账号"错误），并且配置不全的 newapi 账号（`channel_type=0`）一律不进入池，以便快速定位是数据缺失还是路由问题。
- Trace: design `docs/approved/newapi-as-fifth-platform.md` `US-NEWAPI-005`（防御需求 / 输入空间：channel_type=0 的非法账号）
- Risk Focus:
  - 逻辑错误：`IsOpenAICompatPoolMember("newapi")` 必须额外要求 `ChannelType > 0`（design §3.2 语义）
  - 行为回归：openai group 池空时仍返回原 `ErrNoAvailableAccounts`（错误类型不变）
  - 安全问题：channel_type=0 的 newapi 账号被错误使用会导致上游 dispatch 异常
  - 运行时问题：错误信息含 "newapi" 字样以便日志检索

## Acceptance Criteria

1. AC-001 (负向 / 池空)：Given `group.platform=newapi` 且无任何 channel_type>0 的 newapi 账号，When 调度，Then 返回 `ErrNoAvailableAccounts` 且日志/错误链可定位为 newapi 池空 (`TestUS012_NewAPIPool_Empty_ReturnsNoAvailable`)。
2. AC-002 (输入空间 / channel_type=0 排除)：Given `account.Platform="newapi" && account.ChannelType==0`，When `IsOpenAICompatPoolMember("newapi")`，Then 返回 `false` (`TestUS012_PoolMember_NewAPIChannelTypeZero_Excluded`)。
3. AC-003 (输入空间 / channel_type>0 通过)：Given `account.Platform="newapi" && account.ChannelType==1`，When 同上，Then 返回 `true` (`TestUS012_PoolMember_NewAPIChannelTypePositive_Allowed`)。
4. AC-004 (回归 / openai 不受影响)：Given openai group 池空，When 调度，Then 错误类型与历史一致（`ErrNoAvailableAccounts`），不被本设计改变 (`TestUS012_OpenAIPool_Empty_ErrorUnchanged`)。

## Assertions

- AC-001 后：`errors.Is(err, ErrNoAvailableAccounts)`
- AC-002 后：`acct.IsOpenAICompatPoolMember("newapi") == false`
- AC-003 后：`acct.IsOpenAICompatPoolMember("newapi") == true`
- AC-004 后：openai group 错误链与历史快照对比无 diff
- 失败时 testify `require` 立即终止

## Linked Tests

- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS012_NewAPIPool_Empty_ReturnsNoAvailable`
- `backend/internal/service/account_tk_compat_pool_test.go`::`TestUS012_PoolMember_NewAPIChannelTypeZero_Excluded`
- `backend/internal/service/account_tk_compat_pool_test.go`::`TestUS012_PoolMember_NewAPIChannelTypePositive_Allowed`
- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS012_OpenAIPool_Empty_ErrorUnchanged`
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS012_' ./internal/service/`

## Evidence

- `.testing/user-stories/attachments/us012-newapi-pool-empty-run.txt`

## Status

- [ ] Draft
