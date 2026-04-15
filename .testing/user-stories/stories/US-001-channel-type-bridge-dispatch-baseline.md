# US-001-channel-type-bridge-dispatch-baseline

- ID: US-001
- Title: Channel type bridge dispatch baseline
- Version: MVP
- Priority: P0
- As a / I want / So that: 作为平台管理员，我希望为账号设置 `channel_type` 并在网关按端点触发桥接分流判定，以便在不影响既有四平台原生链路的前提下逐步接入 New API adaptor。
- Trace: 角色 x 能力（管理员配置账号路由能力）；实体生命周期（账号创建/更新后立即影响分流判定）
- Risk Focus:
- 逻辑错误：`channel_type<=0` 仍被错误分流，导致走错链路
- 行为回归：导入/更新接口未校验 `channel_type`，写入异常值破坏调度行为
- 安全问题：不适用：本变更不涉及权限边界扩展

## Acceptance Criteria

1. AC-001 (正向): Given 账号 `channel_type>0` 且端点属于 `chat_completions/responses/embeddings/images`，When 执行桥接判定，Then 返回 true。
2. AC-002 (负向): Given `channel_type<0` 的导入数据，When 执行导入校验，Then 返回错误并拒绝写入。
3. AC-003 (回归): Given 已完成后端改动，When 执行 `TestShouldDispatchToNewAPIBridge` 和 `TestValidateDataAccount_ChannelTypeMustBeNonNegative`，Then 全部通过。

## Assertions

- `ShouldDispatchToNewAPIBridge` 对空账号、`channel_type=0`、未知端点返回 false。
- `ShouldDispatchToNewAPIBridge` 对已支持端点在 `channel_type>0` 返回 true。
- `validateDataAccount` 在 `channel_type<0` 时返回 `channel_type must be >= 0`。

## Linked Tests

- `sub2api/backend/internal/service/gateway_bridge_dispatch_test.go`::`TestShouldDispatchToNewAPIBridge`
- `sub2api/backend/internal/service/openai_gateway_bridge_dispatch_test.go`::`TestOpenAIShouldDispatchToNewAPIBridge`
- `sub2api/backend/internal/handler/admin/account_data_test.go`::`TestValidateDataAccount_ChannelTypeMustBeNonNegative`
- 运行命令: `cd sub2api/backend && go test ./internal/service -run 'Test(OpenAIShouldDispatchToNewAPIBridge|ShouldDispatchToNewAPIBridge)' && go test ./internal/handler/admin -run TestValidateDataAccount_ChannelTypeMustBeNonNegative`

## Evidence

- （无附件归档；以 Linked Tests 命令输出为准）

## Status

- Done