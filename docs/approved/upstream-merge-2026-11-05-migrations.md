---
title: Upstream Merge 2026-11-05 Migration Approval Anchor
status: pending
approved_by: pending
authors: [tk-upstream-agent]
created: 2026-11-05
---

# Upstream Merge 2026-11-05: Migration Approval Anchor

本锚点等待人工审批。审批对象是 `upstream/main@25608d216` 并入
`merge/upstream-2026-11-05` 时带入的迁移。人工批准合并对应 PR 即为审批；
合入后状态改为 `shipped` 并补 `related_prs` / `related_commits`。

## Migrations

| File | Change | Risk |
|------|--------|------|
| `247_scoped_admin_api_keys.sql` | 新建 `scoped_admin_api_keys` 表（仅存摘要，保留撤销记录，`deleted_at` 软删） | 纯新增表；无存量数据改写 |
| `248_group_image_count_billing.sql` | `groups` 增加 `image_count_billing BOOLEAN NOT NULL DEFAULT false` | 带默认值的 ADD COLUMN；默认 false，不改变现有分组计费 |
| `249_mcp_oauth.sql` | 新建 `mcp_oauth_grants` / `mcp_oauth_codes` / `mcp_oauth_credentials` 及索引 | 纯新增表；凭据只存摘要 |

编号说明：`247_feedback_tickets.sql`（TK #3003）与上游 `247_scoped_admin_api_keys.sql`
同号。迁移执行器按完整文件名排序并逐文件记账（`migrations_runner.go`），
已有 `242_drop_platform_check_constraints.sql` / `242_group_i18n.sql` 同号先例；两者
互不依赖，执行顺序为 `247_feedback_tickets` → `247_scoped_admin_api_keys`。

## TokenKey invariants preserved alongside these migrations

- `image_count_billing` 默认 false；按张计费只对专用图片端点生效（`imageCountBillingApplies`），TK 定价 SSOT 优先级不变。
- 管理员合规闸门仍为 TK 默认关闭（`TkAdminComplianceGuardIfEnabled`），注册点挂在面板限流之后，与上游顺序一致。
- MCP 用户页挂在 `UserShellView` 持久壳内（`router/user.tk.ts`），OAuth 授权页保持独立认证页。
