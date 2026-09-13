---
title: RPM Override Layer — Deferred Removal Decision
status: approved
approved_by: xuejiao
approved_at: 2026-05-19
authors: [agent]
created: 2026-05-19
related_prs: []
related_commits: []
related_stories: []
---

# RPM Override Layer — Deferred Removal Decision

保留 `user_group_rate_multipliers.rpm_override` 及对应 API、UI、计费缓存路径。
这项能力由 upstream 维护，删除收益不足以抵消后续合并冲突、兼容性和迁移成本。

2026-05-19 的 prod-stage0 / edge-us1 调查没有发现非空 override；这是历史快照，
不能作为当前使用率或当前调度策略的证据。原始调查与当时的容量分析可从 Git 历史恢复。

## 当前语义与 owner

- `rpm_override != NULL`：覆盖该 `(user, group)` 的默认值；`0` 表示豁免。
- `rpm_override == NULL`：回退至 group 默认值。
- 执行语义以 [`billing_cache_service.go`](../../backend/internal/service/billing_cache_service.go)
  的 `checkRPM` 为准；不要把每用户、每组计数与账号候选资格或 UI 容量统计混为一谈。
- 配置读取归 [`user_group_rate_repo.go`](../../backend/internal/repository/user_group_rate_repo.go)，
  管理入口归 [`GroupRPMOverridesModal.vue`](../../frontend/src/components/admin/group/GroupRPMOverridesModal.vue)。

## 重新评估条件

- upstream 退役这项能力。
- 当前性能 profile 或实际事故证明 override 路径有显著成本。
- 产品明确决定不再支持此覆盖语义，并单独审阅兼容性与迁移方案。

重新评估时读取当时的真实配置和调用证据；旧调查中的零使用不授权删除数据库字段。
