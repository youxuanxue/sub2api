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
| `242_group_i18n.sql` | `groups.i18n JSONB NOT NULL DEFAULT '{}'` | PG11+ 常量默认 ADD COLUMN 仅改 catalog，不重写表；已注记 `bluegreen-safe-destructive-ok`；groups 为控制面小表；镜像回滚保留新增列，不自动 DROP。 |
| `243_gemini_cached_contents.sql` | upstream 新增 Gemini 显式缓存表 | 新建表，无存量行风险。 |
| `244_gemini_cached_contents_channel_usage.sql` | `gemini_cached_contents.channel_usage JSONB NOT NULL DEFAULT '{}'` | 同 242_group_i18n 的 PG11+ catalog-only 形态；镜像回滚保留该列；已注记 `bluegreen-safe-destructive-ok`。 |
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

## 复审后的发布边界（2026-10-14）

本节记录核实与缓解结果，**不把 pending 改成部署批准**。

- prod 和四个 deployable edge 的只读核查均为 PostgreSQL 18.6、应用 1.8.291；
  六个本 PR SQL 均尚未执行，Gemini 缓存表尚不存在。迁移仅修改这些未发布 SQL，
  不改已发布迁移 checksum。
- 所有六个新迁移设置事务内 `lock_timeout=5s` / `statement_timeout=30s`（每次锁等待/单条语句）。
  真实 PostgreSQL 集成测试复现旧实例读锁、第二张表阻塞时首个 ALTER 回滚、
  失败不登记迁移、解锁后重试、旧列写入及 newapi/kiro 配额保留；超时设置不泄漏到应用连接。
  DDL 超时将使新色启动失败，保留旧色服务；不能把启动失败当作允许切流。
- 五节点待改的 groups 为 8–91 行，quota/composite route 表均为空；
  03:14 UTC 抽样没有长事务或锁等待。小表降低执行成本，但不替代锁等待上限。
  五节点 02:00 UTC 周期备份服务均成功；此次未执行生产恢复或迁移。
- 五节点 Gemini 账号均为自定义 API Key 上游，未声明 `explicit_cache_upstream`；
  当前配置不会启用新增缓存创建/延长扣费。后续启用必须单独验证缓存与计费，
  不能靠回滚镜像退款或清除上游缓存。
- 当前 OpenAI 账号的长上下文计费开关均为 false；02:00–03:00 UTC 最近一小时
  GPT-6.1 Sol 无附加费记录。prod 是 API Key 镜像，edge 是 OAuth，不能将 edge
  台账与 prod 收入相加。过去 24 小时的历史附加费不能当作本次发布的收入下降预测。
- 线上定价文件已经包含 Gemini 3.6 Flash 的新价和 Haiku 5.5 阶梯；按记录模型筛选对应窗口未见这两类
  模型的用量记录。本 PR 的 fallback 对齐不等于再次降低当前生效目录价。
- GPT-6.1 Sol 的新客户端默认上下文仍可能使长会话更早压缩；其变化需发布说明。
  发布使用新版本，并在一个 edge 验证协议、流式、工具调用和计费之后逐步扩展。
