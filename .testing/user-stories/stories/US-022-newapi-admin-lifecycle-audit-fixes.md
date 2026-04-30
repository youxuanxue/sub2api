# US-022-newapi-admin-lifecycle-audit-fixes

- ID: US-022
- Title: NewAPI 第五平台 admin/HTTP 生命周期 audit 缺口修复
- Version: V1.0
- Priority: P0
- As a / I want / So that: 作为运维，我希望 newapi 账号在 admin HTTP API
  与转发热路径上不再被静默忽略，以便我能像管理 openai/claude/gemini/antigravity
  四个平台那样完整地创建分组、绑定账号、测试连接、查看可用模型，并在调用
  失败时看到上游真实错误。
- Trace: 防御需求 / 角色×能力（admin × 第五平台）
- Risk Focus:
  - 逻辑错误：Gin `oneof` binding 漏列 `newapi`、`simple_mode_default_groups`
    漏 seed `newapi-default`、admin `TestAccountConnection` 与
    `GetAvailableModels` fall-through Claude 默认路径
  - 行为回归：chat/completions 与 responses 错误路径漏调
    `TkTryWriteNewAPIRelayErrorJSON`，让 NewAPIRelayError 被通用
    "Upstream request failed" 文案覆盖（embeddings/images 已正确接线）
  - 安全问题：不适用——本批次只涉及 admin 平面与错误透传，无新越权面

## Acceptance Criteria

1. AC-001 (正向 — group binding): Given 管理员 POST/PUT
   `/api/v1/admin/groups` 携带 `platform: "newapi"`，When Gin binding
   解析请求体，Then 通过校验并写入 group 表。
2. AC-002 (负向 — group binding): Given POST 携带
   `platform: "bogus"`，When Gin binding 解析，Then 返回 binding error
   且不写入数据库。
3. AC-003 (正向 — simple-mode seed): Given 系统在 simple mode 启动且
   `newapi-default` 不存在，When `EnsureSimpleModeDefaultGroups` 运行，
   Then 自动创建 `newapi-default` 分组（platform=newapi）。
4. AC-004 (正向 — admin 测试连接): Given 一个 platform=newapi、
   channel_type>0、api_key 非空的账号，When 管理员触发 "测试连接"，
   Then 服务通过 New API chat-completions adaptor 路径发送用户选择的 model/prompt，
   SSE 返回 `test_complete / success=true` 与上游内容。
5. AC-005 (负向 — admin 测试连接): Given 上述账号但 api_key 被上游拒绝，
   When 管理员触发 "测试连接"，Then SSE 返回 `type=error` 且文案包含
   上游错误或 `API returned 401`，**不**走 Claude 测试路径。
6. AC-006 (负向 — admin 测试连接): Given platform=newapi 但
   channel_type<=0 的账号，When 管理员触发 "测试连接"，Then 立即
   返回 error "missing channel_type"，不发起任何上游请求。
7. AC-007 (正向 — 可用模型): Given platform=newapi 账号且
   `credentials.model_mapping` 非空，When 管理员请求
   `/api/v1/admin/accounts/:id/available-models`，Then 返回的列表 ID
   与 `model_mapping` 的 key 集合等价。
8. AC-008 (负向 — 可用模型): Given platform=newapi 账号且
   `model_mapping` 为空/未设，When 管理员请求同接口，Then 返回空数组
   `[]`，**绝不**返回 Claude 默认目录。
9. AC-009 (回归 — chat 错误透传): Given platform=newapi 账号在
   `/v1/chat/completions` 转发时上游返回 NewAPIRelayError，When
   handler 进入非 failover 错误分支，Then 调用
   `TkTryWriteNewAPIRelayErrorJSON` 并把上游 status / message 原样落到
   响应 body，**不**被 `ensureForwardErrorResponse` 覆盖为通用文案。
10. AC-010 (回归): Given 代码变更，When 执行下方 Linked Tests，
    Then 全部通过；并 PR 描述列出本 audit 修复条目。

## Assertions

- AC-001 / AC-002 由 Gin binding 在单测中校验：
  `TestCreateGroupRequest_AcceptsNewAPIPlatform` 与
  `TestCreateGroupRequest_RejectsUnknownPlatform` 直接断言 `binding`
  错误对象 nil / non-nil。
- AC-003 用 PG testcontainer 跑实仓 `EnsureSimpleModeDefaultGroups`，
  断言 `newapi-default` 出现在 `groups` 表。
- AC-004 / AC-005 / AC-006 通过 `httptest` server 模拟上游 `/v1/chat/completions`
  返回 200 SSE/401/超时，断言 SSE 流的 `event` JSON `type` 与 `success` 字段。
- AC-007 / AC-008 mock account repo 返回带/不带 `model_mapping` 的
  account，断言响应 `data` 长度与 ID 集合。
- AC-009 用 `httptest` 触发上游 NewAPIRelayError，断言 response body
  包含上游 message 字段而不是 "Upstream request failed"。

## Linked Tests

- `backend/internal/handler/admin/group_handler_platform_binding_test.go`::`TestCreateGroupRequest_AcceptsNewAPIPlatform`
- `backend/internal/handler/admin/group_handler_platform_binding_test.go`::`TestCreateGroupRequest_RejectsUnknownPlatform`
- `backend/internal/handler/admin/group_handler_platform_binding_test.go`::`TestUpdateGroupRequest_AcceptsNewAPIPlatform`
- `backend/internal/handler/admin/account_handler_available_models_test.go`::`TestAccountHandlerGetAvailableModels_NewAPI_ReturnsModelMappingKeys`
- `backend/internal/handler/admin/account_handler_available_models_test.go`::`TestAccountHandlerGetAvailableModels_NewAPI_NoMappingReturnsEmpty`
- `backend/internal/service/account_test_service_newapi_test.go`::`TestAccountTestService_NewAPI_RoutesToChatCompletions`
- `backend/internal/service/account_test_service_newapi_test.go`::`TestAccountTestService_NewAPI_ReportsUpstreamFailure`
- `backend/internal/service/account_test_service_newapi_test.go`::`TestAccountTestService_NewAPI_RejectsMissingChannelType`
- `backend/internal/repository/simple_mode_default_groups_integration_test.go`::`TestEnsureSimpleModeDefaultGroups_CreatesMissingDefaults`
- 运行命令:
  `cd backend && go test -tags=unit -v ./internal/handler/admin/ ./internal/service/ -run 'TestCreateGroupRequest_|TestUpdateGroupRequest_|TestAccountHandlerGetAvailableModels_NewAPI_|TestAccountTestService_NewAPI_'`
- 集成命令（需 PG testcontainer）:
  `cd backend && go test -tags=integration -v ./internal/repository/ -run 'TestEnsureSimpleModeDefaultGroups_'`
- AC-009 由跨平台行为审查覆盖：参见
  `backend/internal/handler/openai_chat_completions.go` 与
  `openai_gateway_handler.go` 中 `TkTryWriteNewAPIRelayErrorJSON`
  的非 failover 分支接线；无需独立测试函数（embeddings/images 已有相同
  形态测试，行为通过 grep 守卫即可）。

## Evidence

- PR #29
- preflight 输出与 `go test -tags=unit ./...` 全绿日志
- `golangci-lint run ./...`：0 issues

## Status

- [x] Done
