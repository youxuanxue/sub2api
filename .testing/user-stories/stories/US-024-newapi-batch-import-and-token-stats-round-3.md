# US-024-newapi-batch-import-and-token-stats-round-3

- ID: US-024
- Title: NewAPI 第五平台 round-3 audit 修复（BatchCreate 字段透传 + ops openai-token-stats gpt% 过滤）
- Version: V1.0
- Priority: P1
- As a / I want / So that: 作为运维，我希望 newapi 账号在两条管理面/可观测面
  路径上不再静默丢字段或被错误前缀过滤，以便（a）通过 `POST
  /api/v1/admin/accounts/batch` 一次性导入多个 newapi 账号能真正成功（之前
  100% 在 service 层被 "channel_type must be > 0" 拦截，无任何批量导入路径
  可走）；（b）选择 `platform=newapi` 查看 OpenAI Token Stats 卡片时不会因
  写死的 `ul.model LIKE 'gpt%'` 过滤而看到永远空表（newapi 下游 model id 几
  乎从不以 gpt 开头，如 moonshot-v1-32k / claude-shape / 自定义渠道名）。
- Trace: 角色 × 能力（admin × 批量创建 / 运维 × token 速率观测）+ 防御需求
- Risk Focus:
  - 逻辑错误：`BatchCreate` 漏拷 `ChannelType` / `LoadFactor` 到
    `CreateAccountInput`；`ops_repo_openai_token_stats` 写死 gpt% 前缀，对
    `platform=newapi` 不放行
  - 行为回归：单条 `Create` 走 `tkValidateNewAPIAccountCreate` 给清晰错误
    （"channel_type must be > 0 for newapi platform" / "credentials.base_url
    is required for newapi platform"），批量路径却落到 service 层错误码，
    UX 不一致；同名 admin 卡片在 newapi 下"一直空"会让运维误判 newapi 流
    量为零，进而错过限流/容量告警
  - 安全问题：不适用——本批次只补字段透传与卡片过滤
- Round-1 (US-022) 修了 admin 平面 5 项缺口；round-2 (US-023) 修了 runtime
  3 项缺口；round-3 在前两轮基础上覆盖批量导入与第三方 model id 形态——这
  三轮的覆盖是渐进式的，每轮都缩小"newapi 路径与既有 4 平台路径的对称
  性"差距。

## Acceptance Criteria

1. AC-001 (正向 / batch forward): Given 一个 platform=newapi、channel_type=14、
   load_factor=200 的合法批量创建请求，When 调用
   `POST /api/v1/admin/accounts/batch`，Then `adminService.CreateAccount` 必
   须被调用一次，且 `CreateAccountInput.ChannelType == 14`、
   `*CreateAccountInput.LoadFactor == 200`，response status==200，response
   body `data.success == 1`。
2. AC-002 (负向 / batch validate channel_type): Given 一条 platform=newapi 但
   未设 channel_type（默认 0）的批量行，When 调用 BatchCreate，Then 该行
   必须被 handler 层 `tkValidateNewAPIAccountCreate` 拒绝（不应到达
   `adminService.CreateAccount`）；`results[].error` 包含
   `channel_type` 字样；与单条 Create 的 UX 一致。
3. AC-003 (负向 / batch validate base_url): Given 一条 platform=newapi、
   channel_type>0 但 `credentials.base_url` 为空的批量行，When 调用
   BatchCreate，Then 该行被 handler 层拒绝；`results[].error` 包含
   `base_url` 字样。
4. AC-004 (回归保护 / 不波及其它平台): Given 一条 platform=anthropic、
   未传 channel_type 的批量行，When 调用 BatchCreate，Then 该行必须正常透
   传到 `adminService.CreateAccount`，平台仍是 `anthropic`，验证 round-3
   的新校验**只**对 newapi 生效，未误伤 anthropic / openai / gemini /
   antigravity 的既有批量导入流程。
5. AC-005 (正向 / token-stats newapi skip-gpt): Given `platform=newapi` 调
   用 `GetOpenAITokenStats` 且数据库里有 moonshot-v1-32k 与 claude-3-5-
   sonnet 两条记录，When 仓储层执行 SQL，Then 生成的 SQL **不能**包含
   `ul.model LIKE 'gpt%'` 子句（用 sqlmock 正则反向断言），结果集应包含
   两个非 gpt 前缀模型。
6. AC-006 (回归保护 / token-stats openai keep-gpt): Given `platform=openai`
   调用 `GetOpenAITokenStats`，When 仓储层执行 SQL，Then SQL **必须**仍
   含 `ul.model LIKE 'gpt%'` 子句——本次修复绝不能扩大成"OpenAI 卡片也
   显示所有模型"，破坏卡片原有语义。
7. AC-007 (回归保护 / token-stats no-platform keep-gpt): Given `platform=""`
   未指定平台时调用 `GetOpenAITokenStats`，When 仓储层执行 SQL，Then SQL
   仍含 `ul.model LIKE 'gpt%'` 子句——空平台等价于"看全 GPT 模型"，与
   修复前行为一致。
8. AC-008 (回归): Given 本次代码变更，When 执行 `go test -tags=unit -run
   'TestUS024_' ./internal/handler/admin/... ./internal/repository/...`，
   Then 全部通过。

## Assertions

- `adminSvc.createdAccounts[0].ChannelType == 14`、
  `*adminSvc.createdAccounts[0].LoadFactor == 200`（AC-001）
- `len(adminSvc.createdAccounts) == 0` 且
  `results[0].error contains "channel_type"`（AC-002）
- `results[0].error contains "base_url"`（AC-003）
- `len(adminSvc.createdAccounts) == 1` 且 `Platform == "anthropic"`（AC-004）
- sqlmock `ExpectQuery` 正则**不含** `ul\.model LIKE 'gpt%'`，结果集模型为
  `moonshot-v1-32k`、`claude-3-5-sonnet`（AC-005）
- sqlmock `ExpectQuery` 正则**必须含** `ul\.model LIKE 'gpt%'`（AC-006、AC-007）
- 完整 `go test -tags=unit ./internal/handler/admin/... ./internal/repository/...`
  exit 0（AC-008）

## Linked Tests

- `backend/internal/handler/admin/us024_account_handler_batch_create_test.go`::`TestUS024_BatchCreate_NewAPI_ForwardsChannelTypeAndLoadFactor` (AC-001)
- `backend/internal/handler/admin/us024_account_handler_batch_create_test.go`::`TestUS024_BatchCreate_NewAPI_RejectsZeroChannelType` (AC-002)
- `backend/internal/handler/admin/us024_account_handler_batch_create_test.go`::`TestUS024_BatchCreate_NewAPI_RejectsMissingBaseURL` (AC-003)
- `backend/internal/handler/admin/us024_account_handler_batch_create_test.go`::`TestUS024_BatchCreate_AnthropicWithoutChannelType_StillPasses` (AC-004)
- `backend/internal/repository/us024_newapi_token_stats_filter_test.go`::`TestUS024_OpenAITokenStats_NewAPI_SkipsGPTPrefixFilter` (AC-005)
- `backend/internal/repository/us024_newapi_token_stats_filter_test.go`::`TestUS024_OpenAITokenStats_OpenAI_KeepsGPTPrefixFilter` (AC-006)
- `backend/internal/repository/us024_newapi_token_stats_filter_test.go`::`TestUS024_OpenAITokenStats_NoPlatform_KeepsGPTPrefixFilter` (AC-007)
- 运行命令: `go test -tags=unit -v -run 'TestUS024_' ./backend/internal/handler/admin/... ./backend/internal/repository/...`

## Evidence

- 全部 7 个单测通过（已本地验证）
- 修改的代码位置：
  - `backend/internal/handler/admin/account_handler.go` BatchCreate 段：
    新增 `tkValidateNewAPIAccountCreate` 调用 + 透传 `ChannelType` /
    `LoadFactor` 到 `CreateAccountInput`
  - `backend/internal/repository/ops_repo_openai_token_stats.go` L34-43：
    将 `WHERE ul.model LIKE 'gpt%'` 包到 `if Platform != PlatformNewAPI`
    分支里

## Out of Scope (Round-3 audit findings 但本次不修)

- `BulkUpdateAccounts` 不支持更新 `channel_type`：单条 `UpdateAccount` 已
  支持，admin 可以走单条路径修复 channel_type；批量更新平台间一致性弱，
  follow-up 再加。
- 非 simple-mode 部署不会自动 seed `<platform>-default` 分组：这是
  `ensureSimpleModeDefaultGroups` 一直以来的设计（4 个老平台同样行为），
  不是 newapi-specific 的回归，归到运维 runbook。
- `account_data.go` 导入/导出未携带 `LoadFactor`：影响所有平台，不是
  newapi-specific。
- `refreshSingleAccount` 对未识别平台 fall through 到 Anthropic OAuth 刷新：
  函数入口已用 `account.IsOAuth()` 守门，newapi 是 apikey-only，不会进到
  这里；属于纯防御性 cleanup，价值低于风险。
- `OpsErrorLogger.guessPlatformFromPath` 在缺少 group 上下文时把 newapi
  失败归到 openai：发生在路由层 pre-auth 错误，影响面非常窄，等观测体系
  独立做归因增强时一并处理。
- 前端 fallback model 设置只有 4 个平台对应字段：与 `setting_service.
  GetFallbackModel` 实际只服务 Antigravity 一致，无 newapi 行为缺失。

## Status

- [x] Done
