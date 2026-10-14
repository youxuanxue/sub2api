---
title: TokenKey upstream merge 2026-10-13 approval anchor
status: pending
approved_by: pending
created: 2026-10-13
owners: [tk-platform]
scope: "Isolated import of Wei-Shaw/sub2api upstream/main; no main merge or deployment"
---

# TokenKey upstream merge 2026-10-13

固定 fork 基线为 `90ceba6f9`，上游目标 / `.upstream-ref` 为
`4d9ac3e482106d2da3a39fe3e546d8d6801f1cc1`，共同祖先为
`5fc0e486c3f6a8a191b8bd140f39b60457f611cf`。批准范围是隔离合并与验证，不授权
合入主线、运行时模型激活、生产配置变更或部署。

## Migrations

| File | Change | Risk |
|------|--------|------|
| `242_drop_platform_check_constraints.sql` | upstream 移除 `user_platform_quotas.platform` 与 `composite_model_routes.target_platform` CHECK | 幂等 `DROP CONSTRAINT IF EXISTS`；平台成员资格改由应用层目录（`domain/platforms.go`）校验。 |
| `242_group_i18n.sql` | `groups.i18n JSONB NOT NULL DEFAULT '{}'` | PG11+ 常量默认 ADD COLUMN 仅改 catalog，不重写表；已注记 `bluegreen-safe-destructive-ok`；groups 为控制面小表，回滚即 DROP COLUMN。 |
| `243_gemini_cached_contents.sql` | upstream 新增 Gemini 显式缓存表 | 新建表，无存量行风险。 |
| `244_gemini_cached_contents_channel_usage.sql` | `gemini_cached_contents.channel_usage JSONB NOT NULL DEFAULT '{}'` | 同 242_group_i18n 的 PG11+ catalog-only 形态；已注记 `bluegreen-safe-destructive-ok`。 |
| `245_gemini_cached_contents_mutation_lease.sql` | upstream 新增缓存变更租约列 | 随 243 新表路径进入，存量行为 0。 |
| `tk_103_user_platform_quotas_drop_platform_check.sql` | TK 新增：幂等 DROP `user_platform_quotas_platform_check` | 已发布的 tk_083/087/096 不可变（文件名序在 242 之后，会重建放宽版 CHECK）；tk_103 排在其后 DROP，fresh/prod 两条路径最终态都无 DB 白名单。文件名序测试锁定 tk_103 位于全部重建迁移之后。 |

## TokenKey 不变量裁决

- 平台清单（`domain/platforms.go` / `platformCatalog.builtin.json`，15 平台含
  newapi/kiro）为唯一 SSOT；`group_platform`/`concrete_platform` 绑定校验、
  `AllowedQuotaPlatforms`、前端平台选项全部由目录派生，不再手写 oneof/数组白名单。
- newapi/kiro 不进 composite 回退（composite_precedence 13 平台）与 OpenAI 兼容池（kiro）。
- Vertex 3.x 模型仅 global 端点（#2042，`vertexModelUsesGlobalLocation` 清单保留）；
  gpt-6.1-sol 官方窗 272k/872k + Codex/Work 服务默认 500k（删除 TK 的 1.05M 误判）。
- OpenAI 自定义 apikey 缺 base_url fail-closed（#1766）；Grok relay 要求显式 base_url。
- TK 价格 SSOT 为注册表叠加层；Gemini 缓存计费按创建时计费模型（请求模型）计存储费。
- upstream `scheduleOpenAIResponsesProbe` 删除：TK `scheduleProtocolCapabilityProbes`
  覆盖其全部 3 个调用点语义；upstream 复活的
  `openai_apikey_responses_probe_verdict_test.go` 维持 #1848 有意删除
  （TK 管线接管 verdict 持久化，不写 `responses_supported` extra 标记）。
- Frontend：AccountUsageCell 保留 usage-cells 组件化架构；UseKeyModal 保留薄壳 +
  UseKeyGuide（upstream Codex auth-mode 完整移植）；AdminShell 布局不经 AppLayout。
- VERSION 地板跟随 TokenKey 发布线（合并时 1.8.290，origin/main 发布 1.8.291 后跟进）。

## 验证边界

验收以本次会话/PR 实际跑过的 Go unit（`-tags=unit ./...` 全绿）、migration 文本测试、
frontend vitest/typecheck/lint、`./scripts/preflight.sh` PASS 为准。
本审批文档不宣称已合入或已部署。
