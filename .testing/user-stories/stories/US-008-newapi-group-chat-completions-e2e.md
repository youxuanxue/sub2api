# US-008-newapi-group-chat-completions-e2e

- ID: US-008
- Title: newapi group + `/v1/chat/completions` 端到端走通
- Version: V1.1
- Priority: P0
- As a / I want / So that: 作为 TokenKey 网关运营方，我希望 `group.platform=newapi` 的分组也能正常处理 OpenAI Chat Completions 请求，以便测试同事报告的 P0 集成残缺得到根治、`newapi` 真正成为 first-class 第五平台。
- Trace: design `docs/approved/newapi-as-fifth-platform.md` `US-NEWAPI-001`（角色×能力：API 调用方 × ChatCompletions 调度）
- Risk Focus:
  - 逻辑错误：候选池拉取必须按 `group.Platform` 而非硬编码 `PlatformOpenAI`；scheduler bucket key 自然按 platform 分桶
  - 行为回归：openai group 走旧路径完全不变（与 US-015 联合保护）
  - 安全问题：openai group 的请求绝不能拿到 newapi 账号（与 US-011 联合保护）
  - 运行时问题：scheduler bucket cache 升级后旧 openai key 仍命中、新 newapi key 冷启动正常

## Acceptance Criteria

1. AC-001 (正向)：Given `group.platform=newapi` + 至少一个 `channel_type>0` 的 newapi 账号，When 客户端 POST `/v1/chat/completions`，Then `listOpenAICompatSchedulableAccounts` 返回该 newapi 账号、`selectByLoadBalance` 不被 `IsOpenAI()` 过滤掉，请求经 bridge dispatch 到 newapi 上游并返回 200 (`TestUS008_NewAPIGroup_ChatCompletions_E2E`)。
2. AC-002 (负向 / 池空)：Given `group.platform=newapi` 但池中无可用 newapi 账号，When 请求到达，Then 返回明确错误（`ErrNoAvailableAccounts` 或等价语义），不得回退到 openai 账号 (`TestUS008_NewAPIGroup_PoolEmpty_NoFallback`)。
3. AC-003 (回归)：Given 任一 openai group 配置，When 重跑 `TestUS00X_*` openai group 既有 ChatCompletions 用例，Then 全部通过且不依赖 newapi 数据 (`TestUS008_OpenAIGroup_Unchanged`)。

## Assertions

- AC-001 后：`response.StatusCode == 200`，且选中账号的 `Platform == "newapi"` 与 `ChannelType > 0`
- AC-001 后：`schedulerSnapshot.bucketKey` 含 `"newapi"`（按 platform 分桶）
- AC-002 后：错误类型 `errors.Is(err, ErrNoAvailableAccounts)`，且选中账号 nil
- AC-003 后：openai group 选中账号 `Platform == "openai"`
- 失败时 testify `require` 立即终止，非 0 退出码

## Linked Tests

Scheduler-tier (this PR, unit, mocked snapshot/cache/group repo):

- `backend/internal/service/openai_account_scheduler_tk_newapi_test.go`::`TestUS008_NewAPIGroup_Scheduler_PicksNewAPIAccount`
- `backend/internal/service/openai_account_scheduler_tk_newapi_test.go`::`TestUS008_NewAPIGroup_PoolEmpty_NoFallback`
- `backend/internal/service/openai_account_scheduler_tk_newapi_test.go`::`TestUS008_OpenAIGroup_SchedulerSelect_Unchanged`
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS008_' ./internal/service/`

HTTP+PG end-to-end (follow-up PR `feature/newapi-fifth-platform-e2e`; tracked by this story remaining Draft until covered):

- `backend/internal/handler/openai_chat_completions_tk_newapi_integration_test.go`::`TestUS008_HTTP_NewAPIGroup_ChatCompletions_E2E` *(planned)*

## Evidence

- CI/preflight 中对应 unit test 输出

## Status

- [x] Draft

> **Honest status note (2026-04-20 audit)**: 此故事的核心 AC 是「`POST /v1/chat/completions` 端到端走通」。当前 PR 仅交付 scheduler-tier 的 mock 单测（覆盖"调度池能选到 newapi 账号"+"池空不回退"+"openai 不变"三条调度不变量），未跑过任何真 HTTP→Auth→bridge→newapi upstream 的端到端请求。AC-001 / AC-002 / AC-003 中标注的 e2e 测试函数 `TestUS008_HTTP_NewAPIGroup_ChatCompletions_E2E` 仍是 *(planned)*，不存在于代码中。按 `test-philosophy.mdc §6`「阅读代码 ≠ 验证、单测全绿不是结论」纪律，本故事 status 保持 `Draft`，只有 follow-up PR `feature/newapi-fifth-platform-e2e` 跑通真 e2e 后才能升 `InTest → Done`。
