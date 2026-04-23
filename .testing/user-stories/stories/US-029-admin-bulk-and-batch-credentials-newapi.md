# US-029-admin-bulk-and-batch-credentials-newapi

- ID: US-029
- Title: Admin BulkUpdate 拒绝 newapi credentials + BatchUpdateCredentials 走专用 writer 跳过 Moonshot 探测
- Version: V1.4.x (hot-fix)
- Priority: P1 (B-4 + B-5)
- As a / I want / So that:
  作为 **TokenKey 运维**，我希望 **批量编辑 newapi 账号 credentials 时不会因为绕过 Moonshot 区域探测而把整批账号 base_url 写成错的区域**，**以便** 修改 api_key 不再造成所有 relay 请求 401，且批量改 `account_uuid` 这种与区域无关的字段时不会触发 N×25s 的无谓 Moonshot 冷探。

- Trace:
  - 防御需求轴线：`BulkUpdateAccounts` 走 `accountRepo.BulkUpdate`，**不**经 `UpdateAccount`，因此 `resolveNewAPIMoonshotBaseURLOnSave` 完全被跳过。批量改 .ai key 但 base_url 仍指 .cn 时，relay 热路径 401 而不会自愈。
  - 角色 × 能力轴线：admin "批量改 account_uuid" 操作不应该影响 Moonshot 区域；用错路径会让一次 UUID rename 变成多分钟 Moonshot 探测 fan-out。

- Risk Focus:
  - 逻辑错误：B-4 守卫必须在 `len(req.Credentials) > 0` 时才检查（priority/concurrency 等非 credentials 编辑不能误拒）；B-5 必须用 credentials-only writer，不能误调 `UpdateAccount`（否则触发 group binding validation / quota-reset / OAuth privacy goroutine 等无关副作用）。
  - 行为回归：openai / anthropic / gemini / antigravity 平台的 BulkUpdate 行为完全不变；BatchUpdateCredentials 对所有平台的字段写入语义不变。
  - 安全问题：守卫只读 platform 字段不读 credentials 内容，无新增 PII / 凭证暴露面。
  - 运行时问题：BatchUpdateCredentials 改用专用 writer 后单账号写入路径更短（无 GetByID + Update 两步往返）；Moonshot 探测调用次数从 N 降到 0。

## Acceptance Criteria

1. **AC-001 (B-4 正向)**：Given 批量编辑请求 `{AccountIDs:[101 newapi, 102 openai], Credentials:{api_key:"sk"}}`，When `BulkUpdate` handler 处理，Then 返回 HTTP 400，错误体含 `BULK_CREDENTIALS_UNSUPPORTED_FOR_NEWAPI`，且 service 层 `BulkUpdateAccounts` 一次都没被调用。
2. **AC-002 (B-4 回归)**：Given 同样的 newapi+openai 账号组，但只改 `priority`（非 credentials），When `BulkUpdate` 处理，Then 200 且 `BulkUpdateAccounts` 被调用 1 次。
3. **AC-003 (B-4 回归)**：Given 全 openai/anthropic 账号组 + credentials 编辑，When `BulkUpdate` 处理，Then 200 且 `BulkUpdateAccounts` 被调用 1 次（其它平台不受影响）。
4. **AC-004 (B-4 防御)**：Given 空 credentials map (`{}`)，When `BulkUpdate` 处理，Then 不触发守卫（len==0 跳过）。
5. **AC-005 (B-5 正向)**：Given `BatchUpdateCredentials({AccountIDs:[1,2,3], Field:"account_uuid", Value:"x"})`，When handler 处理，Then `AdminService.UpdateAccount` 被调用 0 次，`AdminService.UpdateAccountCredentials` 被调用 3 次。
6. **AC-006 (B-5 正向)**：Given AC-005 同输入，When 检查 stub 收到的 credentials map，Then 包含 `account_uuid: "x"` （字段成功写入）。
7. **AC-007 (B-5 正向)**：`intercept_warmup_requests` 字段（bool）路径同样走 UpdateAccountCredentials 而不是 UpdateAccount。
8. **AC-008 (B-5 回归)**：原 `failingAdminService` 路径（部分账号写入失败）行为不变——只是从 UpdateAccount 改为 UpdateAccountCredentials 计数。
9. **AC-009 (B-5 接口完整性)**：`AdminService` interface 新增 `UpdateAccountCredentials`；唯一的 mock `stubAdminService` 同步加方法（CLAUDE.md §6 接口方法完整性）。
10. **AC-010 (回归 / 全量单元测试)**：`go test -tags=unit -count=1 ./internal/handler/admin/...` 全绿。

## Assertions

- `TestUS029_BulkUpdate_NewAPICredentials_Rejected`: HTTP 400 + body 含 `BULK_CREDENTIALS_UNSUPPORTED_FOR_NEWAPI` + `bulkUpdateInvocations == 0`
- `TestUS029_BulkUpdate_NewAPI_NonCredentialFields_Allowed`: HTTP 200 + `bulkUpdateInvocations == 1`
- `TestUS029_BulkUpdate_OpenAICredentials_Allowed`: HTTP 200 + `bulkUpdateInvocations == 1`
- `TestUS029_BulkUpdate_EmptyCredentials_NotGuardChecked`: HTTP 200 + `bulkUpdateInvocations == 1`
- `TestUS029_BatchUpdateCredentials_AccountUUID_DoesNotTriggerUpdateAccount`: `updateAccountCalls == 0` && `updateAccountCredentialsCalls == 3`
- `TestUS029_BatchUpdateCredentials_PreservesExistingCredentialFields`: `lastCredentials[42]["org_uuid"] == "org-1"`
- `TestUS029_BatchUpdateCredentials_InterceptWarmupRequests_RoutesThroughCredentialsWriter`: `updateAccountCredentialsCalls == 1`，且 value 是 `true` (bool)
- 既有 `TestBatchUpdateCredentials_AllSuccess` / `_PartialFailure` 改写后：`updateCallCount` 计的是 `UpdateAccountCredentials` 而非 `UpdateAccount`，断言数字不变（3 success / 2 success+1 fail）

## Linked Tests

- `backend/internal/handler/admin/account_handler_tk_bulk_credentials_guard_test.go`::`TestUS029_BulkUpdate_*` (AC-001 ~ AC-004)
- `backend/internal/handler/admin/us029_batch_update_credentials_skip_resolve_test.go`::`TestUS029_BatchUpdateCredentials_*` (AC-005 ~ AC-007)
- `backend/internal/handler/admin/batch_update_credentials_test.go`::`TestBatchUpdateCredentials_AllSuccess` / `_PartialFailure` (AC-008 回归)

运行命令：

```bash
cd backend
go test -tags=unit -count=1 ./internal/handler/admin/... -run 'TestUS029|TestBatchUpdateCredentials'
```

## Evidence

- 修复落地：
  - PR #35 commit `8c0126c0` — `fix(admin): reject bulk credentials edits on newapi accounts (Bug B-4)`
  - PR #35 commit `09c32103` — `fix(admin): batch-update-credentials skips full UpdateAccount path (Bug B-5)`
- 接口完整性：`stubAdminService.UpdateAccountCredentials` no-op stub；`failingAdminService` 改为 override 新方法
- Bug audit 文档：`docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md` § B-4 / § B-5

## Status

- [x] InTest（PR #35 待 merge；测试已全绿）
