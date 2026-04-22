# US-019-newapi-forwarding-affecting-fields

- ID: US-019
- Title: newapi 账号暴露 model_mapping / status_code_mapping / openai_organization 三个真实影响转发的字段
- Version: V1.4
- Priority: P1
- As a / I want / So that: 作为 sub2api 管理员，我希望在 Admin UI 创建/编辑 newapi 账号时直接配置 `model_mapping` / `status_code_mapping` / `openai_organization` 三个 OpenAI-compat 转发关键字段，以便不必绕到原始 admin API 就能让 newapi 账号与上游 new-api channel 行为对齐（model 别名、状态码改写、OpenAI 组织头）。
- Trace: 角色×能力 (admin × 创建/编辑 newapi 账号) + 防御需求 (转发兼容性) — 来源 PR #19 review 后用户指令"加 model_mapping + status_code_mapping + openai_organization 三个真实影响转发/兼容性的字段"
- Risk Focus:
  - 逻辑错误：`bridge.PopulateContextKeys` 必须把三个字段精确写入 Gin 上下文键（`model_mapping` / `status_code_mapping` / `channel_organization`），否则下游 new-api relay handler 读不到；空值/`{}` 必须被跳过避免污染默认行为。
  - 行为回归：未填这三个字段的 newapi 账号必须与之前完全一致（默认透传），不能因为新加可选字段而改变现有调度/转发结果。
  - 安全问题：不适用（admin 中间件已保护；字段本身不放宽鉴权）。

## Acceptance Criteria

1. AC-001 (正向 / Bridge wiring)：Given 一个 newapi `Account`，其 `Credentials["model_mapping"]={"gpt-4":"gpt-4-turbo"}`、`Credentials["status_code_mapping"]='{"404":"500"}'`、`Credentials["openai_organization"]="org-abc"`，When 调用 `newAPIBridgeChannelInput(account, ...)`，Then 返回的 `bridge.ChannelContextInput` 三个字段全部被填充且 JSON-encoded 与持久化形态一致。
2. AC-002 (负向 / 空值跳过)：Given 一个 newapi `Account` 三个字段均为空（不存在 / 空字符串 / `"{}"`），When 调用 `newAPIBridgeChannelInput`，Then 返回的 `ChannelContextInput` 三个字段均为空字符串；调用 `PopulateContextKeys` 时不写入对应 Gin key（避免误将 `"{}"` 当作"必须 remap"）。
3. AC-003 (回归 / 校验)：Given Admin UI 提交一个 newapi 账号且 `model_mapping` 文本不是 JSON 对象（如数组、scalar、malformed），When 触发提交，Then 客户端校验阻止提交并提示 "Must be a JSON object"，不会 POST 到后端。

## Assertions

- AC-001：单测 `TestNewAPIBridgeChannelInput_WiresForwardingCredentials` 通过 — `input.ModelMappingJSON == \`{"gpt-4":"gpt-4-turbo"}\``、`input.StatusCodeMappingJSON == \`{"404":"500"}\``、`input.Organization == "org-abc"`。
- AC-002：单测 `TestNewAPIBridgeChannelInput_OmitsEmptyForwardingCredentials` 通过 — 三个字段为 `""`。`PopulateContextKeys` 测试观察到对应 Gin key 未被设置（`c.GetString("status_code_mapping") == ""`）。
- AC-003：vitest spec（待补，当前由 Stage-4 manual smoke-test 覆盖）— `validateOptionalJsonObject` 对 `[1,2]` / `"abc"` / `{` 三种非法输入返回非空错误字符串。
- 失败时 testify `require` / vitest `expect` 立即报错并 exit ≠ 0。

## Linked Tests

- `backend/internal/service/newapi_bridge_usage_test.go`::`TestNewAPIBridgeChannelInput_WiresForwardingCredentials` *(AC-001)*
- `backend/internal/service/newapi_bridge_usage_test.go`::`TestNewAPIBridgeChannelInput_OmitsEmptyForwardingCredentials` *(AC-002)*
- 运行命令：`cd backend && go test -tags=unit -v -run 'TestNewAPIBridgeChannelInput_' ./internal/service/`

AC-003（前端 JSON 校验）当前由 Stage-4 manual smoke-test 在 PR 描述中声明；UI mount 的 vitest spec 待 follow-up（与 US-018 AC-005 / AC-009 同样的 heavy-modal 复杂度）。

## Evidence

- `.testing/user-stories/attachments/us-019-newapi-bridge-fields-go-test-2026-04-22.txt`（待补）

## Status

- [x] InTest
