# US-028-newapi-bridge-error-pipeline

- ID: US-028
- Title: NewAPI bridge 错误回灌 RateLimitService + handle401/handle402 跨 OpenAI-compat 平台 + relay-error JSON status 兜底
- Version: V1.4.x (hot-fix)
- Priority: P0 (B-1) + P1 (B-3) + P2 (B-11)
- As a / I want / So that:
  作为 **TokenKey 运维**，我希望 **第五平台 newapi 账号被上游永久作废 / 限流 / 过载时，account 状态机能像非 bridge 平台一样自动 SetError / SetRateLimited / SetOverloaded / handle403**，**以便** 调度池不再把已死账号循环选出造成 100% 失败率，且不再在响应里把"上游错误 + status=0"序列化成 HTTP 200 让客户端误以为成功。

- Trace:
  - 防御需求轴线：bridge dispatch (`openai_gateway_bridge_dispatch{,_tk_anthropic}.go` + `gateway_bridge_dispatch.go`) 的 7 个 `if apiErr != nil` 分支历史上**全部**直接 `return *NewAPIRelayError`，从不调 `RateLimitService.HandleUpstreamError`。本故事补齐这条「上游错误 → 账号状态机」的关键通路。
  - 实体生命周期轴线：账号生命周期需要"被上游永久作废 → SetError 永久下线"这条迁移边对所有平台一致；newapi 账号过去因为这条边断开而成为"幽灵账号"。
  - 角色 × 能力轴线：`handle401`/`handle402` 中的 OpenAI-compat 错误码识别（token_invalidated / token_revoked / detail:Unauthorized / deactivated_workspace）必须扩展到 PlatformNewAPI（同样走 OpenAI-compat 协议）。

- Risk Focus:
  - 逻辑错误：B-1 funnel 必须在每个 bridge dispatch entry 的 `if apiErr != nil` 块都被调用一次，而不是只一处；B-3 必须用 `IsOpenAICompatPlatform()`（已有 helper）替换硬编码 `account.Platform == PlatformOpenAI`，否则 newapi 永久作废 key 仍每 10 分钟自动恢复一次。
  - 行为回归：非 bridge 路径 (OpenAI/Anthropic/Gemini direct) 的错误处理 0 变更——funnel 只在 bridge 路径触发；handle401 OpenAI 行为字节级保持。
  - 安全问题：funnel 不引入新的会话级状态存储；不暴露上游 raw response（仅传递 `apiErr.Error()` 摘要）。
  - 运行时问题：funnel 是常数空间纯函数式调用，不增加 goroutine 也不引入锁；out-of-range status (StatusCode == 0) 被 coerce 到 502 防止 c.JSON(0) 写出 HTTP 200。

## Acceptance Criteria

1. **AC-001 (B-1 正向)**：Given OpenAIGatewayService 装配了 RateLimitService stub + newapi 账号，When 调用 `s.reportNewAPIBridgeUpstreamError(ctx, account, apiErr401)`，Then `RateLimitService.HandleUpstreamError` 被调用 1 次，statusCode == 401，且导致 account `SetError` 一次。
2. **AC-002 (B-1 mirror)**：GatewayService receiver 有镜像方法行为相同（用于 `gateway_bridge_dispatch.go` 两处入口）。
3. **AC-003 (B-1 防御)**：funnel 对 `nil apiErr` / `nil account` / out-of-range status (0/-1/999) 都安全 no-op 或 coerce 到 502，不 panic。
4. **AC-004 (B-1 OPC)**：源代码静态扫描断言 7 个 bridge dispatch entry (4 in `openai_gateway_bridge_dispatch.go` + 1 in `_tk_anthropic.go` + 2 in `gateway_bridge_dispatch.go`) 中每个 `if apiErr != nil` 块都包含 `reportNewAPIBridgeUpstreamError` 调用。
5. **AC-005 (B-3 正向)**：`HandleUpstreamError(401, body={code:"token_invalidated"})` 在 PlatformNewAPI 账号上必然 SetError 一次，errorMsg 含 "Token revoked"。
6. **AC-006 (B-3 正向)**：`HandleUpstreamError(401, body={detail:"Unauthorized"})` 在 PlatformNewAPI 上必然 SetError 一次，errorMsg 含 "Unauthorized"。
7. **AC-007 (B-3 回归)**：PlatformOpenAI 的 token_invalidated 行为字节级不变；PlatformAnthropic 不会被误纳入 OpenAI-compat 分支（不会 emit "Token revoked" 前缀）。
8. **AC-008 (B-3 正向)**：`HandleUpstreamError(402, body={detail:{code:"deactivated_workspace"}})` 在 PlatformNewAPI 上必然 SetError 一次。
9. **AC-009 (B-11 正向)**：`TkTryWriteNewAPIRelayErrorJSON(c, &NewAPIRelayError{Err: apiErr{StatusCode:0}}, false, sizeBefore)` 必然写出 HTTP 502（不是 0/200）+ error body。
10. **AC-010 (B-11 回归)**：合法 4xx (401) / 5xx (503) 必须被原样保留；999 / -1 等被 coerce 到 502；non-relay error 不写 body 也不 return true；stream-started 状态下也不写 body 但 return true。
11. **AC-011 (回归 / 全量单元测试)**：`go test -tags=unit -count=1 ./internal/service/... ./internal/handler/...` 全绿。

## Assertions

- `TestUS028_ReportNewAPIBridgeUpstreamError_OpenAIGateway_Forwards401`: `repo.setErrorCalls == 1` && `repo.lastErrorMsg` 含 "401"
- `TestUS028_ReportNewAPIBridgeUpstreamError_OpenAIGateway_Forwards402`: 同上 "402"
- `TestUS028_ReportNewAPIBridgeUpstreamError_GatewayService_Forwards401`: GatewayService 镜像
- `TestUS028_ReportNewAPIBridgeUpstreamError_NilApiErr_NoCall` / `_NilAccount_NoPanic` / `_OutOfRangeStatus_FallsBackTo502`
- `TestUS028_AllBridgeDispatchSitesCallReportHelper`: 三个文件 grep `reportNewAPIBridgeUpstreamError(` 出现次数 ≥ {4, 1, 2}
- `TestUS028_Handle401_NewAPI_TokenInvalidated_SetsError` / `_TokenRevoked_SetsError` / `_DetailUnauthorized_SetsError`
- `TestUS028_Handle401_OpenAI_TokenInvalidated_SetsError_Regression` / `TestUS028_Handle401_Anthropic_TokenInvalidated_NotMatched_Regression`
- `TestUS028_Handle402_NewAPI_DeactivatedWorkspace_SetsError`
- `TestUS028_TkTryWriteNewAPIRelayErrorJSON_ZeroStatusCoercedTo502` / `_PreservesValid4xx` / `_PreservesValid5xx` / `_TooLargeStatusCoercedTo502` / `_StreamStartedReturnsTrueWithoutWrite` / `_NonRelayErrorReturnsFalse`

## Linked Tests

- `backend/internal/service/us028_newapi_bridge_error_pipeline_test.go`::`TestUS028_*` (覆盖 AC-001 ~ AC-008)
- `backend/internal/service/us028_test_helpers_test.go` (test 工具)
- `backend/internal/handler/tk_newapi_relay_error_status_test.go`::`TestUS028_TkTryWriteNewAPIRelayErrorJSON_*` (覆盖 AC-009 / AC-010)

运行命令：

```bash
cd backend
go test -tags=unit -count=1 ./internal/service/... -run 'TestUS028'
go test -tags=unit -count=1 ./internal/handler/... -run 'TestUS028'
```

## Evidence

- 修复落地的两处 commit：
  - PR #35 commit `9141401d` — `fix(newapi): wire bridge upstream errors back into RateLimitService (Bug B-1)`（含 B-1 + B-3 + B-11）
- Sentinel 注册：`scripts/newapi-sentinels.json` 新增 `newapi_bridge_rate_limit_tk.go` 条目，preflight § 10 守护
- Bug audit 文档：`docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md` § B-1 / § B-3 / § B-11

## Status

- [x] InTest（PR #35 待 merge；测试已全绿）
