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

## 0. TL;DR

`user_group_rate_multipliers.rpm_override` 是从 upstream (Wei-Shaw/sub2api / QuantumNous/new-api 血脉) 继承下来的"按 `(user, group)` 配对覆盖 group 默认 RPM 闸门"的细粒度功能。TokenKey 调查结果：**全公司 0 行使用**。功能本身是 Jobs/OPC 视角的过度设计，但**保留代码不动**——删除收益小于 upstream 合并冲突风险。本文档把分析落档，将来如果触发条件成立再清理。

## 1. 三层限流地图

`billing_cache_service.checkRPM` ([backend/internal/service/billing_cache_service.go:711](../../backend/internal/service/billing_cache_service.go)) 把 gateway 每个请求按下面三层并行做限流，**任一超限即 429**：

| 层 | 维度 | cap 来源 | 计数器 | 错误类型 |
|---|---|---|---|---|
| **L1** | `(user_id, group_id)` 配对 | `user_group_rate_multipliers.rpm_override` → `group.rpm_limit` 兜底 | per-(user, group) Redis key | `ErrGroupRPMExceeded` |
| **L2** | `user_id` 全局 | `users.rpm_limit` | per-user Redis key | `ErrUserRPMExceeded` |
| **L3** (account-level)\* | `account_id` | `accounts.extra.base_rpm` + `rpm_sticky_buffer` | per-account Redis key | 不直接拒；影响调度选择（绿/黄/红区） |

\* L3 严格说不是"限流层"，是 [account.go:2092 `CheckRPMSchedulability`](../../backend/internal/service/account.go) 的"账号是否可调度"判定，但它读同一份 RPM 计数，常被混为一谈（见 §3 dashboard 错位的根源）。

L1 的 `rpm_override` 是这一组里**最细粒度**的层级：

```
  rpm_override != NULL: 该 user 在该 group 内 cap = rpm_override (0 表示豁免)
  rpm_override = NULL:  该 user 在该 group 内 cap = group.rpm_limit (默认)
```

## 2. 现状调查（2026-05-19）

在 prod-stage0 + edge-us1 上做 read-only 巡检：

| 字段 | prod | us1 |
|---|---|---|
| `user_group_rate_multipliers WHERE rpm_override IS NOT NULL` 行数 | **0** | **0** |
| `users.rpm_limit > 0` (L2 启用数) | **0 / 12** | **0 / 1** |
| `groups WHERE rpm_limit > 0 AND platform='anthropic'` | 1 (`cc-edges`=16) | 1 (`default`=16) |
| `groups WHERE rpm_limit = 0 AND platform='anthropic'` | 1 (`default`=0) | 0 |

结论：

- **L1 override 零使用。** 表存在、admin UI 弹窗存在 ([frontend/src/components/admin/group/GroupRPMOverridesModal.vue](../../frontend/src/components/admin/group/GroupRPMOverridesModal.vue))、API 端点存在 ([PUT /api/v1/admin/groups/:id/rpm-overrides](../../backend/internal/handler/admin/group_handler.go))、`billing_cache_service.checkRPM` hot path 每请求都 query 一次——但实际未启用。
- **L2 user.rpm_limit 也零使用。** 用户级硬顶字段从未配置。
- 实际生效的限流**只有** L1 兜底分支（`group.rpm_limit`），即"该 group 整体每分钟最多 N 个请求，所有用户共享"。这就是 TokenKey 当前真实业务模型。

## 3. Jobs/OPC 视角的评估

L1 `rpm_override` 是典型的过度设计：

- **零使用率 + 三处维护成本**：DB 表 + repo 方法 + admin handler + Vue 弹窗。任何 schema 演进、ent 重生成、upstream 合并都要 touch 这条线。
- **hot path 性能负担**：每个 gateway 请求在 `checkRPM` 都要先查 `rpm_override` (auth cache 兜底 DB query)，0 命中也付了 cache lookup 成本。
- **dashboard 错位的部分原因**：`group_capacity_service.RPMMax = Σ base_rpm` 与 L1 真实闸门 `group.rpm_limit` 不一致，部分根源就是 L1 多层概念存在让人不知道展示哪一层。本次 dashboard 修复（A1）把 RPMMax 改成读 `group.rpm_limit`，是因为 L1 override 零使用让我们能放心把 dashboard 与 L1 兜底分支对齐。

TokenKey 的实际业务心智模型是 **L2 (user) + L1 兜底 (group)** 两层就够：

- 普通用户：靠 L1 group 闸门共享 cap
- VIP：未来如果要区分，**用 group 分档**（e.g. `cc-vip` rpm_limit=100 / `cc-default` rpm_limit=16），新用户按 plan 绑对应 group。比 L1 override 的"在 group 内单独开洞"更干净、dashboard 更直观、是常见 SaaS 模式。

`rpm_override` 的设计场景（"同一个 user 在 group A 是 VIP，在 group B 是普通"）冷门且与"plan tier = group"的 SaaS 习惯反直觉。**没必要保留这个能力。**

## 4. 决策

**保留 L1 `rpm_override` 代码与 DB 表，不动。** 原因：

1. **upstream 合并风险**：这条线源自 fork upstream（含 `billing_cache_service.checkRPM` 主路径、`user_group_rate_repo`、admin handler、ent migrations）。删掉会让下一次 `merge upstream/main` 在多个文件上产生冲突，违反 [CLAUDE.md §5 Upstream Isolation](../../CLAUDE.md) "minimize diff against upstream" 原则。
2. **删除收益**：零使用 + hot-path 微小性能 + 多文件维护成本——是真实负担但量级小。
3. **风险 vs 收益**：保留的成本是"dashboard 设计需要意识到这层概念"（本文档解决了），删除的成本是"未来 upstream 合并冲突 + 重写 admin UI + 写 down-migration"。保留更划算。

## 5. 触发后续清理的条件

如果将来出现下列任一条件，**重新评估删除**：

- **upstream 自己删了 `rpm_override`**：TokenKey 跟着删反而是 alignment（无冲突）。
- **`checkRPM` 的 hot-path 性能成为瓶颈**：审计 profile 看 `userGroupRateRepo.GetRPMOverrideByUserAndGroup` 占比；若有意义占比，删 L1 直接省一次 DB/cache 查询。
- **L1 override 被错误启用并造成事故**：即出现"我设了 group.rpm_limit=N 但某些用户跑得比 N 快"或反之的 incident，根因为遗忘的 override 行。这种情况删 L1 减少误操作面。
- **TokenKey 决定整组从 upstream 出走**（不再保持 fork relationship）：失去 §4.1 的合并风险，删除的代价归零。

未触发上述条件前，本文档作为"我们看过、想过、决定不动"的 audit trail。

## 6. 关联代码

- L1 hot path：[backend/internal/service/billing_cache_service.go:711-783](../../backend/internal/service/billing_cache_service.go) `checkRPM`
- L1 repo：[backend/internal/repository/user_group_rate_repo.go](../../backend/internal/repository/user_group_rate_repo.go)
- L1 admin API：[PUT /api/v1/admin/groups/:id/rpm-overrides](../../backend/internal/handler/admin/group_handler.go) `BatchSetGroupRPMOverrides`
- L1 admin UI：[frontend/src/components/admin/group/GroupRPMOverridesModal.vue](../../frontend/src/components/admin/group/GroupRPMOverridesModal.vue)
- dashboard 修复 (A1)：[backend/internal/service/group_capacity_service.go](../../backend/internal/service/group_capacity_service.go) `getGroupCapacity` — 把 `RPMMax` 从 `Σ base_rpm` 改为 `group.rpm_limit`（fallback Σ base_rpm 当 group 不限速）
