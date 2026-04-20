# US-010-newapi-group-responses-e2e

- ID: US-010
- Title: newapi group + `/v1/responses` 端到端走通
- Version: V1.1
- Priority: P0
- As a / I want / So that: 作为 TokenKey 网关运营方，我希望 `group.platform=newapi` 的分组也支持 OpenAI Responses 协议入口，以便 Codex CLI 等使用 Responses 协议的客户端能用 newapi 上游。
- Trace: design `docs/approved/newapi-as-fifth-platform.md` `US-NEWAPI-003`（角色×能力：Codex CLI × Responses 调度）
- Risk Focus:
  - 逻辑错误：Responses 入口与 ChatCompletions 共享 `listOpenAICompatSchedulableAccounts`，必须正确传 `group.Platform`
  - 行为回归：openai group + Responses 走旧路径完全不变
  - 安全问题：previous_response_id sticky 命中后 recheck 必须按 `IsOpenAICompatPoolMember(groupPlatform)` 验证
  - 运行时问题：Responses 入口的 fresh recheck (`resolveFreshSchedulableOpenAIAccount`) 同步切到 pool member 判定

## Acceptance Criteria

1. AC-001 (正向)：Given `group.platform=newapi` + newapi 账号 `channel_type>0`，When POST `/v1/responses`，Then 选中 newapi 账号经 bridge dispatch 转发并返回 200 (`TestUS010_NewAPIGroup_Responses_E2E`)。
2. AC-002 (负向 / sticky recheck 排除非池账号)：Given sticky session 命中的账号 `Platform != group.Platform`（理论上不该发生，模拟 pool 漂移场景），When `resolveFreshSchedulableOpenAIAccount` 检查，Then 返回 nil 触发降级 (`TestUS010_FreshRecheck_RejectsNonPoolMember`)。
3. AC-003 (回归)：Given openai group + Responses，When 请求处理，Then 与历史行为完全一致 (`TestUS010_OpenAIGroup_Responses_Unchanged`)。

## Assertions

- AC-001 后：`response.StatusCode == 200`，选中账号 `Platform == "newapi"`
- AC-002 后：`resolveFreshSchedulableOpenAIAccount(...) == nil`
- AC-003 后：openai group 选中账号 `Platform == "openai"`
- 失败时 testify `require` 立即终止

## Linked Tests

Scheduler-tier coverage (this PR — same code path serves chat / messages /
responses entrypoints, so US-008 scheduler tests transitively lock the
selection invariant for /v1/responses):

- `backend/internal/service/openai_account_scheduler_tk_newapi_test.go`::`TestUS008_NewAPIGroup_Scheduler_PicksNewAPIAccount` *(transitive)*
- `backend/internal/service/openai_account_scheduler_tk_newapi_test.go`::`TestUS008_OpenAIGroup_SchedulerSelect_Unchanged` *(transitive)*
- `backend/internal/service/openai_gateway_service_tk_newapi_pool_test.go`::`TestUS013_Sticky_NewAPIGroup_HitsBoundAccount` *(transitive — sticky path is shared)*
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS008_|TestUS013_Sticky_NewAPIGroup_HitsBoundAccount' ./internal/service/`

HTTP+PG end-to-end (follow-up PR, see `docs/preflight-debt.md` §4):

- `backend/internal/handler/openai_responses_tk_newapi_integration_test.go`::`TestUS010_HTTP_NewAPIGroup_Responses_E2E` *(planned)*

## Evidence

- `.testing/user-stories/attachments/us-newapi-unit-run-2026-04-19.txt`

## Status

- [x] Draft

> **Honest status note (2026-04-20 audit)**: 此故事的核心 AC 是「`POST /v1/responses` 端到端走通」。当前 PR 完全没有 `/v1/responses` 入口的专属测试 — scheduler-tier 的 newapi 选池逻辑被传递性覆盖，但 responses 入口的 HTTP→Auth→handler→bridge dispatch 链路从未真跑过。AC 标注的 `TestUS010_HTTP_NewAPIGroup_Responses_E2E` 仍是 *(planned)*，且 responses 入口的 unit-tier 也无任何 `TestUS010_*` 函数。按 `test-philosophy.mdc §6` 纪律，本故事 status 保持 `Draft`，是 8 个 newapi 故事中覆盖最薄的一条，follow-up PR 必须优先打掉这条。
