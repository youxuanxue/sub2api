# US-023-newapi-runtime-path-audit-round-2

- ID: US-023
- Title: NewAPI 第五平台 runtime 路径 audit round 2 修复
- Version: V1.0
- Priority: P1
- As a / I want / So that: 作为运维，我希望 newapi 账号在 429 限流处理与
  ops 重试这两条 runtime 路径上不再 fall through 到错误的解析器/转发器，
  以便（a）429 重置时间能从 OpenAI-compat 响应体里准确抽取，避免被默认
  5 分钟低估而导致重复打满；（b）admin "重试某账号" 在
  `/v1/chat/completions` 错误日志上能正确路由到 OpenAI 转发器，而不是被
  Anthropic 转发器接住后必然失败。
- Trace: 防御需求 / 系统事件（429、ops retry）
- Risk Focus:
  - 逻辑错误：`ratelimit_service.handle429` body-parse switch 漏 newapi
    分支，落入 5min 默认锁；`ops_retry.detectOpsRetryType` 把
    `/v1/chat/completions` 误判为 messages 类型，对 OpenAI/NewAPI 账号
    走 Anthropic 转发器
  - 行为回归：默认 5 分钟锁会让 newapi 账号在真正长窗口限流（>>5min）期
    被反复尝试；ops 重试静默失败会让"重试此请求"这个 admin 工具在 chat
    completions 上对 newapi 永远不可用
  - 安全问题：不适用——本批次仅修限流冷却时长与 admin 重试转发选择
- Round-1 audit (US-022) 已修 admin 平面 5 项缺口；本次 round-2 audit 转
  向 runtime 热路径，本身就是 round-1 设计文档承诺的"再过一遍 runtime"
  闭环。

## Acceptance Criteria

1. AC-001 (正向 / ratelimit): Given 一个 `Platform=newapi, ChannelType=1`
   的账号收到 upstream 429，响应体为
   `{"error":{"type":"usage_limit_reached","resets_at":<unix_ts>}}` 且响
   应头不含 `anthropic-ratelimit-unified-reset`，When `handle429` 处理
   该响应，Then `accountRepo.SetRateLimited` 收到的 `resetAt` 必须等于
   响应体里的 `resets_at`，**不能**退化为默认 `now + 5min`。
2. AC-002 (负向 / ratelimit): Given 同一 newapi 账号收到 429 但响应体里
   既无 `resets_at` 也无 `resets_in_seconds`（无可解析重置时间），When
   `handle429` 处理该响应，Then 仍然落入文档化的默认 5 分钟兜底分支并
   调用一次 `SetRateLimited`，验证修复**未**变成"总是解析 OpenAI 体"
   的过度干预。
3. AC-003 (正向 / ops retry classifier): Given `detectOpsRetryType` 收
   到任意 `/chat/completions`（含 `/v1/chat/completions` 与
   `/V1/Chat/Completions` 大小写），When 分类执行，Then 必须返回
   `opsRetryTypeOpenAI`；与此同时 `/v1/responses`、`/v1beta/...`、
   `/v1/messages`、空字符串这 4 个既有路径分类**保持原样**（回归保护）。
4. AC-004 (负向 / ops retry guard): Given 一个 OpenAI 或 NewAPI 账号被
   错误地以 `opsRetryTypeMessages` 调入 `executeWithAccount`（模拟未来
   分类器回归），When 走到 messages-default 分支，Then 必须返回
   `opsRetryStatusFailed`、错误信息中包含平台名 `openai` / `newapi` 与
   `OpenAI-compat` 或 `opsRetryTypeOpenAI` 字样，且**不能**真正调用
   `gatewayService.Forward`（用 `gatewayService=nil` 验证不会 panic / nil
   deref，证明守卫先于转发执行）。
5. AC-005 (回归): Given 本次代码变更，When 执行 `go test -tags=unit -run
   'TestUS023_' ./internal/service/...`，Then 全部通过。

## Assertions

- `repo.rateLimitedAt.Unix() == expectedResetUnix`（AC-001）
- `repo.rateLimitedCalls == 1` 且 `rateLimitedAt` 落在 `[before+5min,
  after+5min]` 窗口（AC-002）
- `detectOpsRetryType("/v1/chat/completions") == opsRetryTypeOpenAI`
  且 `/v1/messages` 仍 == `opsRetryTypeMessages`（AC-003）
- `exec.status == opsRetryStatusFailed`，`exec.errorMessage` contains
  platform 与 `OpenAI-compat` 或 `opsRetryTypeOpenAI`（AC-004）
- 完整 `go test -tags=unit ./internal/service/...` exit 0（AC-005）

## Linked Tests

- `backend/internal/service/us023_newapi_handle429_test.go`::`TestUS023_NewAPI_Handle429_ParsesOpenAICompatBody` (AC-001)
- `backend/internal/service/us023_newapi_handle429_test.go`::`TestUS023_NewAPI_Handle429_FallsBackTo5MinWhenBodyHasNoResetTime` (AC-002)
- `backend/internal/service/us023_newapi_handle429_test.go`::`TestUS023_OpsRetry_ClassifiesChatCompletionsAsOpenAI` (AC-003)
- `backend/internal/service/us023_newapi_handle429_test.go`::`TestUS023_OpsRetry_ExecuteWithAccount_GuardsOpenAICompatInMessagesDefault` (AC-004)
- 运行命令: `go test -tags=unit -v -run 'TestUS023_' ./backend/internal/service/...`

## Evidence

- 单测均通过（见 PR #29 CI `test` 与 `golangci-lint` 阶段）
- 修改的代码位置：
  - `backend/internal/service/ratelimit_service.go` L766-789（switch case 加 `PlatformNewAPI`）
  - `backend/internal/service/ops_retry.go` L388-401（classifier 加 chat/completions）
  - `backend/internal/service/ops_retry.go` L595-616（messages-default 守卫）

## Out of Scope (Round-2 audit findings 但本次不修)

- `account_service.go::TestCredentials` 是 dead code（无任何调用方），newapi
  会落入 `default` 返回 `unsupported platform`。等到该 API 被真正使用时再
  删/补，避免无 caller 的死代码扩散。
- `error_passthrough_rule.go::AllPlatforms()` 是 model 包内自维护的死常
  量，与 `service.AllSchedulingPlatforms()` 重复且无实际使用。和上一条
  一并等真正接入再清理。
- `ops_retry.go` 对 `/v1/messages` 走到 newapi 账号的场景（需要 bridge
  把 Anthropic body 转 OpenAI body 再让 openAIGatewayService 转发）。本
  次只放守卫挡住静默错路由，完整支持等 follow-up。
- `setting_service.GetFallbackModel`、`token_cache_invalidator.go`、
  `crs_sync_service.refreshOAuthToken` 没有 newapi 分支——目前 newapi
  无 OAuth、无 fallback 默认模型、无 CRS 同步需求，符合设计；不修。
- 其它 `/v1/embeddings`、`/v1/images/*`、`/v1/audio/*` 等 OpenAI-compat
  路径在 ops retry 分类器里仍归 `opsRetryTypeMessages`——本次仅修
  chat/completions（数量级最大、用户最痛），其余 follow-up 时一并处理。

## Status

- [x] Done
