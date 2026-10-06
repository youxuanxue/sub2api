---
title: TokenKey upstream merge 2026-10-06 approval anchor
status: approved
approved_by: "用户（本会话批准审查修复与迁移锚点；不授权合入主线或部署）"
approved_at: 2026-10-06
created: 2026-10-06
owners: [tk-platform]
scope: "Isolated import of Wei-Shaw/sub2api upstream/main; no main merge or deployment"
---

# TokenKey upstream merge 2026-10-06

固定 fork 基线为 `cf0df4631`，上游目标 / `.upstream-ref` 为
`b8dece9000c68815a5b867ca5a1e6f236e173905`，共同祖先为
`42bc7f6cffe24bcb471608e48e66b4a0afa1f882`。批准范围是隔离合并与验证，不授权
合入主线、运行时模型激活、生产配置变更或部署。

## Migrations

| File | Change | Risk |
|------|--------|------|
| `241_add_payment_order_bonus_amount.sql` | `payment_orders.bonus_amount DECIMAL(20,2) NOT NULL DEFAULT 0` | Additive column with default; runner keys by filename so the duplicate `241_` prefix still applies. Marked blue/green-safe. |
| `241_add_typesafe_platform.sql` | Rebuild `user_platform_quotas` and `composite_model_routes` CHECK lists to add `typesafe` | `DROP`/`ADD CONSTRAINT` takes `ACCESS EXCLUSIVE`. `SET LOCAL lock_timeout = '5s'` so a hot quotas table fails the new color instead of hanging startup. Quota CHECK **must** keep TokenKey `newapi`/`kiro`. |

Both files apply: `schema_migrations.filename` is the primary key, ASCII order is
`241_add_payment_order_bonus_amount.sql` then `241_add_typesafe_platform.sql`.

## TokenKey 不变量裁决

- 本轮 git 文件删除为 0。真实冲刷风险是 CHECK allowlist 收缩、以及取上游函数体时丢掉 TK companion。
- `user_platform_quotas` 保留 `newapi`/`kiro`/`opencode_go` 并追加 `typesafe`。
- `*_tk_*.go` companion（含 `tkServeModels`、thin-pool `HandleSelectionExhausted`、candidate eligibility）保留。
- TypeSafe System One 走 `/v1/systemone`；`jev-latest` 不进入 Codex listing，也不进入 composite `/v1/models`（避免 chat 客户端误打）。
- 自定义 404 账号级 disable 仅 TypeSafe（System One remaining_attempts），不扩大到其它 API-key 平台。
- 密码重置：新发 token 只存 SHA-256；校验/消费在滚动窗口内仍接受 Redis 里未哈希的旧 token。
- 匿名查单限流 40/IP/分钟，覆盖结果页 15 次刷新与 CGNAT。
- Grok CLI pin 跟随上游 `1.0.46`；billing 与 chat 共用 `CLIUserAgent(CLIClientVersion)`。
- VERSION 地板仍为 TokenKey `1.8.272`，不降到上游 `0.2.13`。

## 验证边界

验收以本次会话/PR 实际跑过的 Go unit、migration 文本测试、preflight 为准。本审批文档不宣称已合入或已部署。
