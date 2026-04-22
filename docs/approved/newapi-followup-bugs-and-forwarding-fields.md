---
title: "NewAPI 第五平台 — Bug 修复 + UI 暴露 3 个真实影响转发的字段（PR #19 之后）"
status: pending
approved_by: pending
authors: [agent]
created: 2026-04-22
related_prs: []
related_stories: [US-019, US-020, US-021, US-022, US-023, US-024, US-025]
parent_design: docs/approved/admin-ui-newapi-platform-end-to-end.md
---

# NewAPI 第五平台 follow-up — Bug A/B 修复 + 三个转发字段

## 0. TL;DR

PR #19（`docs/approved/admin-ui-newapi-platform-end-to-end.md`）把第五平台 `newapi`
的 admin UI 完整闭合并合入 main。投产后我们做了一轮深度回访（结合
`https://api.tokenkey.dev/admin/accounts` 与上游 new-api 的实际能力），发现两类
**实质性缺口**——它们不是 UX 修饰，而是真实影响"newapi 账号能不能被调度"和
"调度后能不能正确转发"的运行时问题：

1. **Bug A — Scheduler snapshot drift**：`scheduler_snapshot_service.go` 内部还
  残留两处 4 平台硬编码列表（`rebuildByGroupIDs` 与 `defaultBuckets`），
   `PlatformNewAPI` 在快照重建时被静默丢弃，触发 "newapi pool empty" 错误。
2. **Bug B — Moonshot 区域解析没接线**：`ResolveMoonshotRegionalBaseAtSave`
  helper 早就存在，但 admin_service 的 `CreateAccount` / `UpdateAccount`
   没调用它，导致 Moonshot channel-type 的账号永远要管理员手工分辨
   `.cn` vs `.ai`，否则后续 relay 持续 401。
3. **UX gap — 三个真实影响转发的字段**：bridge 已经在读 `model_mapping`，
  但 UI 不收集，导致只能走 admin API 配。同时 `status_code_mapping`
   与 `openai_organization` 是 OpenAI-compat relay 的关键透传字段，
   不暴露就不能在界面上把 newapi 账号配置到与上游 new-api channel 对齐。

按用户指令"先干 B + C，最后再干 A，分两个 PR"，本设计文档就是**第一个 PR**
（B + C，即上面 1 / 2 / 3 三项）的审批基线。第二个 PR（A — 项目级机械约束）
单独立项，不在本文档范围。

## 1. 范围

### 1.1 In-scope（本 PR）

- **Audit-driven fixes（PR review 后追加，US-022）**：基于全代码 audit
  补齐 5 处 newapi 仍被静默忽略的路径——
  (a) `group_handler.go` Create/UpdateGroupRequest 的 Gin `oneof`
  binding 没列 `newapi` → admin HTTP API 无法创建/编辑 newapi 分组；
  (b) `simple_mode_default_groups.go` 没 seed `newapi-default`，导致
  `CreateAccount` 自动绑定时找不到默认分组、newapi 账号被孤立；
  (c) `account_test_service.go::TestAccountConnection` 对 newapi
  fall-through 走 Claude 路径，"测试连接" 用错 upstream + 错 auth；
  (d) `account_handler.go::GetAvailableModels` 同样 fall-through 返回
  Claude 默认模型目录；(e) `openai_chat_completions.go` 与
  `openai_gateway_handler.go` 的非 failover 错误路径漏调
  `TkTryWriteNewAPIRelayErrorJSON`，让 NewAPIRelayError 被通用
  "Upstream request failed" 文案覆盖（embeddings/images 已正确接线，
  但 chat/responses 这两条主热路径不一致）。
- **Bug A 修复**：在 `account_tk_compat_pool.go` 引入 `AllSchedulingPlatforms()`
作为单一事实来源；`scheduler_snapshot_service.go` 的 `rebuildByGroupIDs` 与
`defaultBuckets` 改为消费这个 slice，消除硬编码 4 平台列表。
- **Bug B 修复**：新建 `MaybeResolveMoonshotBaseURLForNewAPI(ctx, account)` 辅助
函数，接线到 `admin_service.go` 的 `CreateAccount` / `UpdateAccount` 的
newapi 分支。helper 内部封装"channel_type 是 Moonshot 且 base_url 是
Moonshot 默认域且 API key 非空且 platform=newapi"四道短路条件，命中后调用
既有的 `ResolveMoonshotRegionalBaseAtSave` 探测器写回 base_url。探测失败
必须把错误向上传播，**禁止静默回退**。
- **UX 三字段**：在 `AccountNewApiPlatformFields.vue` 加 `model_mapping`、
`status_code_mapping`、`openai_organization` 三个 `defineModel` 输入框；
`CreateAccountModal.vue` / `EditAccountModal.vue` 把它们 wire 到 credentials
并做客户端 JSON-object 校验；后端 `bridge.ChannelContextInput` 新增
`StatusCodeMappingJSON` 字段，`PopulateContextKeys` 同步写入 Gin key
`status_code_mapping`；`newapi_bridge_usage.go::newAPIBridgeChannelInput`
把三个字段从 credentials 装载到 `ChannelContextInput`。
- **ChannelTypeBadge**：新建 `frontend/src/components/common/ChannelTypeBadge.vue`，
在 `AccountsView` 列表里把 newapi 账号的 `channel_type` 数字翻译成可读
友好名（"Moonshot" / "DeepSeek" 等），让运维一眼看出账号配的是哪个上游。
- **Round-2 audit fixes（再过一遍 runtime 路径，US-023）**：US-022 收
  口 admin 平面之后又跑了一轮 audit，把目标转向 runtime 热路径。发现
  并修复 3 处——
  (a) `ratelimit_service.go::handle429` body-parse switch 漏 newapi
  分支：当 upstream 返回 429 但响应头没有 `anthropic-ratelimit-unified-reset`
  时，OpenAI 的 `usage_limit_reached` 体（new-api adaptor 透传）被忽略，
  newapi 账号被默认 5 分钟锁兜底——而真实 reset 往往是数小时到数天，
  会导致重复打满。修复：把 `PlatformNewAPI` 与 `PlatformOpenAI` 合并到
  同一 case 走 `parseOpenAIRateLimitResetTime`。
  (b) `ops_retry.go::detectOpsRetryType` 把 `/v1/chat/completions` 错
  分类为 `opsRetryTypeMessages`，导致 admin "重试此请求" 在 chat
  completions 错误日志上对 OpenAI/NewAPI 账号必然失败（用 Anthropic 解
  析器 + Anthropic 转发器）。修复：新增 `/chat/completions` → `opsRetryTypeOpenAI`
  分支。同时 `executeWithAccount` messages-default 加守卫——任何
  PlatformOpenAI/PlatformNewAPI 账号走到这里都返回 explicit failure
  并指向 `opsRetryTypeOpenAI`，防止未来分类器回归被静默吃掉。
  (c) `GroupsView.vue` 主表 platform badge 三元链漏 `value === 'newapi'`
  分支，newapi 分组徽章会回退到 catch-all 蓝色（与 gemini 同色）；同
  文件下方的"账号选择面板"已有正确的 cyan 色——前后不一致是 PR #19
  的遗漏。修复：补 cyan 分支，与下方面板对齐。
- **Round-3 audit fixes（再过一遍 admin 批量 + ops 可观测性，US-024）**：
  US-022/023 之后第三轮 audit 把目标转向 admin 批量导入与 ops 卡片粒度
  上的 newapi 静默路径。修了 2 处——
  (a) `account_handler.go::BatchCreate` 漏拷 `ChannelType` 与
  `LoadFactor` 到 `CreateAccountInput`，且漏调
  `tkValidateNewAPIAccountCreate`。后果：单条 Create 走得通的 newapi
  导入流程在批量路径上 100% 失败（service 层会以
  "channel_type must be > 0 for newapi platform" 拒绝），且
  load_factor 在批量路径上对所有平台都被静默丢弃。修复：透传
  `ChannelType` / `LoadFactor`，并在循环开头先调
  `tkValidateNewAPIAccountCreate` 给清晰错误信息。
  (b) `ops_repo_openai_token_stats.go` 写死 `WHERE ul.model LIKE 'gpt%'`
  过滤。后果：选择 `platform=newapi` 看 OpenAI Token Stats 卡片几乎永
  远空表（newapi 下游 model id 几乎不以 gpt 前缀，例如
  `moonshot-v1-32k`、`claude-3-5-sonnet`、自定义渠道名），等价于
  newapi 在该卡片上没有可观测性。修复：仅当
  `Platform != PlatformNewAPI` 时保留 gpt 前缀过滤；newapi 选项放行所
  有 model id。OpenAI 卡片本身语义不变（含 gpt% 子句的回归测试保护）。

### 1.2 Out-of-scope（明确推迟）

- **PR2 — 项目级机械约束（A）**：preflight §9 扩展、sentinel 文件清单、
upstream 合并 PR-shape 加门。本 PR 合并后独立开。
- **US-008/009/010 完整 e2e（HTTP + PG testcontainer）**：仍然是 0.5-1 day
的工作量，由 `docs/preflight-debt.md` 的 D-003 跟踪，截止 2026-05-03。
- **批量改 `channel_type` 守卫**：`BulkEditAccountModal` 对 newapi
channel_type 的批量切换是破坏性操作，需独立 UX review。
- **品牌名 i18n 化**：5 个平台标签都还是英文硬编码，等项目 i18n 统一
pass 时再做。

## 2. 数据/契约影响

### 2.1 `bridge.ChannelContextInput`（向后兼容地新增字段）

新增字段 `StatusCodeMappingJSON string`。空字符串与 `"{}"` 必须被
`PopulateContextKeys` 跳过——避免误把"空 mapping"当作"必须 remap"，
保持现有 newapi 账号行为字节级不变。

### 2.2 `Account.Credentials` JSON 形态


| key                   | 类型                    | 备注                                                                                      |
| --------------------- | --------------------- | --------------------------------------------------------------------------------------- |
| `model_mapping`       | JSON object           | 与 `Account.GetModelMapping()` 兼容（既有路径 `credentials["model_mapping"].(map[string]any)`）。 |
| `status_code_mapping` | JSON-string of object | bridge 直接透传给上游 new-api relay handler；UI 端校验为合法 JSON object 后以字符串持久化。                    |
| `openai_organization` | plain string          | 设置 OpenAI-Organization 出站头；空字符串视为不设置。                                                   |


三者都是**可选**——空值/空对象等价于"不传"，bridge 跳过对应 Gin key。

### 2.3 数据库 schema

不改 schema。所有字段都在 `accounts.credentials::jsonb` 内。

## 3. 测试

### 3.1 自动化（本 PR 必须通过）

- US-019：`backend/internal/service/newapi_bridge_usage_test.go`
  - `TestNewAPIBridgeChannelInput_WiresForwardingCredentials`
  - `TestNewAPIBridgeChannelInput_OmitsEmptyForwardingCredentials`
- US-020：`backend/internal/service/scheduler_snapshot_platforms_test.go`
  - `TestAllSchedulingPlatforms_IncludesNewAPI`
  - `TestRebuildByGroupIDs_RebuildsNewAPIBucket`
  - `TestDefaultBuckets_IncludesNewAPI`
- US-021：`backend/internal/integration/newapi/moonshot_resolve_save_helper_test.go`
  - 6 个 `TestMaybeResolveMoonshotBaseURLForNewAPI`_* 用例覆盖正向 +
  5 类短路条件 + 探测失败传播。
- US-022（audit follow-ups）：
  - `backend/internal/handler/admin/group_handler_platform_binding_test.go`
    - `TestCreateGroupRequest_AcceptsNewAPIPlatform`
    - `TestCreateGroupRequest_RejectsUnknownPlatform`
    - `TestUpdateGroupRequest_AcceptsNewAPIPlatform`
  - `backend/internal/handler/admin/account_handler_available_models_test.go`
    - `TestAccountHandlerGetAvailableModels_NewAPI_ReturnsModelMappingKeys`
    - `TestAccountHandlerGetAvailableModels_NewAPI_NoMappingReturnsEmpty`
  - `backend/internal/service/account_test_service_newapi_test.go`
    - `TestAccountTestService_NewAPI_RoutesToUpstreamModelsProbe`
    - `TestAccountTestService_NewAPI_ReportsUpstreamFailure`
    - `TestAccountTestService_NewAPI_RejectsMissingChannelType`
  - `backend/internal/repository/simple_mode_default_groups_integration_test.go`
    - `TestEnsureSimpleModeDefaultGroups_CreatesMissingDefaults` 已扩展
      断言 `newapi-default` 存在。
- US-024（admin 批量 + ops 可观测性 audit round 3）：
  - `backend/internal/handler/admin/us024_account_handler_batch_create_test.go`
    - `TestUS024_BatchCreate_NewAPI_ForwardsChannelTypeAndLoadFactor`（正向：
      ChannelType=14 / LoadFactor=200 必须透传到 CreateAccountInput）
    - `TestUS024_BatchCreate_NewAPI_RejectsZeroChannelType`（负向：
      handler 层校验拦截，不到达 CreateAccount）
    - `TestUS024_BatchCreate_NewAPI_RejectsMissingBaseURL`（负向：
      base_url 缺失被拒）
    - `TestUS024_BatchCreate_AnthropicWithoutChannelType_StillPasses`（回归：
      其它平台批量导入未被新校验误伤）
  - `backend/internal/repository/us024_newapi_token_stats_filter_test.go`
    - `TestUS024_OpenAITokenStats_NewAPI_SkipsGPTPrefixFilter`（正向：
      newapi 时 SQL 不含 `ul.model LIKE 'gpt%'`，moonshot/claude-shape
      模型可见）
    - `TestUS024_OpenAITokenStats_OpenAI_KeepsGPTPrefixFilter`（回归：
      OpenAI 卡片仍只显示 gpt 前缀模型）
    - `TestUS024_OpenAITokenStats_NoPlatform_KeepsGPTPrefixFilter`（回归：
      未指定平台时仍保留 gpt 前缀子句，行为与修复前一致）
- US-023（runtime audit round 2）：
  - `backend/internal/service/us023_newapi_handle429_test.go`
    - `TestUS023_NewAPI_Handle429_ParsesOpenAICompatBody`（newapi 429
      响应体里的 `resets_at` 必须被采用，**不**走 5min 默认）
    - `TestUS023_NewAPI_Handle429_FallsBackTo5MinWhenBodyHasNoResetTime`
      （负向：缺 reset 字段时 5min 兜底依旧生效，确保修复未"过度解析"）
    - `TestUS023_OpsRetry_ClassifiesChatCompletionsAsOpenAI`
      （`/chat/completions` → `opsRetryTypeOpenAI`，`/v1/messages` /
      `/v1/responses` / `/v1beta/...` 4 条历史分类回归保护）
    - `TestUS023_OpsRetry_ExecuteWithAccount_GuardsOpenAICompatInMessagesDefault`
      （PlatformOpenAI/PlatformNewAPI 在 messages-default 走守卫并 fail
      fast，**不**调用 `gatewayService.Forward`）
- 前端 vitest：`ChannelTypeBadge.spec.ts` 渲染断言。

### 3.2 手动 stage-4 smoke

- 在 `https://test-api.tokenkey.dev/admin/accounts` 创建一个 Moonshot
channel-type 的 newapi 账号，用 `.cn` 区域 API key，base_url 留空，
保存后用 `psql` 验证 `credentials.base_url` 被写为 `https://api.moonshot.cn`。
- 同一账号编辑时填入 `model_mapping = {"gpt-4":"gpt-4-turbo"}`、
`status_code_mapping = {"404":"500"}`、`openai_organization = "org-abc"`。
发起一次 `/v1/chat/completions` 调用，用 mitmproxy 验证出站请求带
`OpenAI-Organization: org-abc`，model 名被替换。
- AccountsView 列表中该账号显示 "New API" + cyan badge + "Moonshot"
channel-type badge。

## 4. 风险

- **Bug A 修复扩散**：`AllSchedulingPlatforms()` 是新的单一源，可能漏算入某些
历史快照路径。缓解：单测覆盖 `defaultBuckets` + `rebuildByGroupIDs` 两处
实际触发点；其他读取者要么走这个函数，要么走 `OpenAICompatPlatforms`。
- **Bug B 探测失败阻塞保存**：探测请求失败必须把错误向上传播——缓解：六个
短路条件确保只在"channel_type=Moonshot + base_url 是默认域 + API key 非空 +
platform=newapi"四道闸全开时才探测，运维已经手工配置好的账号永远不会
触发探测路径。
- **UI 字段误配**：`status_code_mapping` 写错可能让上游 5xx 被强制改成 2xx。
缓解：客户端 JSON-object 校验 + 字段说明文案明确"留空使用上游默认"。
- **回归到 4 平台硬编码**：未来 PR 可能又写硬编码列表。这是 PR2 项目级
机械约束（A）要解决的元问题——本 PR 不通过脚本约束，靠 reviewer。

## 5. 验收

- `./scripts/preflight.sh` 全绿
- `cd backend && go test -tags=unit ./...` 全绿
- `cd frontend && pnpm typecheck && pnpm lint:check && pnpm test:run` 全绿
- `https://test-api.tokenkey.dev` stage-4 smoke 三步全过
- PR 描述含 §3.2 smoke 截图与 mitmproxy 输出

## 6. 与 PR2（A）的边界

PR2 的目标是"上游合并不再覆盖 newapi 能力"——通过 preflight 脚本扫描
sentinel 文件（如本 PR 引入的 `account_tk_compat_pool.go::AllSchedulingPlatforms`、
`admin_service_tk_newapi_save.go::MaybeResolveMoonshotBaseURLForNewAPI`、
`bridge.ChannelContextInput.StatusCodeMappingJSON` 等）来阻止它们在
upstream merge 时被静默删除。PR1（本 PR）是"先把 sentinel 文件实装出来"，
PR2 是"再加锁防止它们消失"。两个 PR 的拆分点是：本 PR 引入的所有新
sentinel 文件路径在 PR2 设计文档里会被引用为白名单种子。
