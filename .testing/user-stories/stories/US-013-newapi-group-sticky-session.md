# US-013-newapi-group-sticky-session

- ID: US-013
- Title: newapi group + sticky session 命中后 recheck 通过/账号漂移时降级
- Version: V1.1
- Priority: P1
- As a / I want / So that: 作为客户端使用方，我希望 newapi group 也享有与 openai group 同等的 sticky session 行为——同会话内连续命中同一账号、账号被换平台时自动降级，以便长程会话有 prompt cache 收益且不出现"幽灵账号"被锁定。
- Trace: design `docs/approved/newapi-as-fifth-platform.md` `US-NEWAPI-006`（实体生命周期：sticky session 命中 → recheck → 降级）
- Risk Focus:
  - 逻辑错误：sticky 命中后 recheck 必须用 `IsOpenAICompatPoolMember(groupPlatform)` 而非 `IsOpenAI()`
  - 行为回归：openai group 的 sticky 行为完全不变（与 US-006/sticky-routing 共生）
  - 安全问题：账号被改 platform 后 sticky 不能继续指向旧账号（自动失效）
  - 运行时问题：sticky key 复用 `openai:{groupID}:{sessionHash}` 命名空间（design §2.5 决策，不按 platform 拆 key）

## Acceptance Criteria

1. AC-001 (正向)：Given newapi group + 同一 sessionHash 的连续 3 个请求，When 第 1 个请求选定 newapi 账号 A 并写入 sticky，Then 第 2/3 个请求 sticky 命中并 recheck 通过、复用 A (`TestUS013_NewAPIGroup_StickyHit_ReusesAccount`)。
2. AC-002 (降级 / 账号被改平台)：Given sticky 已写入账号 A（platform=newapi），When 账号 A 被运营改为 `platform=openai`，再次同 session 请求 newapi group，Then sticky recheck 用 `IsOpenAICompatPoolMember("newapi")` 失败 → 删除 sticky → 重新走 loadBalance (`TestUS013_NewAPIGroup_Sticky_FailsOver_WhenPlatformChanged`)。
3. AC-003 (回归)：Given openai group 的 sticky 用例完全照旧（不传 groupPlatform 或传 "openai"），When 重跑历史 sticky 测试，Then 行为不变 (`TestUS013_OpenAIGroup_Sticky_Unchanged`)。

## Assertions

- AC-001 后：第 2/3 个请求 `selectedAccount.ID == A.ID`
- AC-002 后：`getStickySessionAccountID(...) == 0` 或 sticky key 已被删除；新选账号 `Platform == "newapi"`
- AC-003 后：openai group sticky 路径与既有快照对比无 diff
- 失败时 testify `require` 立即终止

## Linked Tests

- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS013_NewAPIGroup_StickyHit_ReusesAccount`
- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS013_NewAPIGroup_Sticky_FailsOver_WhenPlatformChanged`
- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS013_OpenAIGroup_Sticky_Unchanged`
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS013_' ./internal/service/`

## Evidence

- `.testing/user-stories/attachments/us013-newapi-sticky-session-run.txt`

## Status

- [ ] Draft
