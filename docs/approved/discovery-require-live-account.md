---
title: Discovery require live account (精简 B)
status: pending
approved_by: pending
authors: [agent]
created: 2026-09-18
related_design: docs/approved/universal-key-capability-discovery.md, docs/approved/universal-key-routing.md, docs/approved/pricing-serving-single-source-of-truth.md
---

# Discovery：无活账号则不展示

## 背景

2026-09-18 实测：universal key 的 `GET /v1/models` 列出 `claude-fable-5-1`，但
`POST /v1/messages` 返回 `429 No available accounts`。根因是发现投影只要求
「授权组账号有 model_mapping / 可 dry-plan」，不要求存在 **active + schedulable**
的持有账号；当时仅账号 91/150 持有该映射，且均为 `schedulable=false`。

同日决策（对话确认）：

1. **先 C 止血**：从不可调度持有账号上摘掉空池模型映射（已对 91/150 去掉
   `claude-fable-5-1`；`claude-fable-5` 保留）。
2. **再立项精简 B**（本文）：发现侧排除「没有任何活账号持有路径」的模型。
3. **明确不做 A**：不把瞬时限流 / `temp_unschedulable` / 短窗 overload 写进菜单。

## Goal

让 `/v1/models`（及同一发现投影 owner 的站内 capabilities）在「长期没货」时不再
展示该模型；同时保留「瞬时容量不进菜单」——避免限流时菜单变成心电图。

## Proposed decision（待审批）

修订
[`universal-key-capability-discovery.md`](universal-key-capability-discovery.md)
Decision 5 与 Capability Contract：

| 维度 | 现行 | 精简 B（提案） |
| --- | --- | --- |
| 列入菜单 | 存在可 dry-plan 路径（映射或原生空映射 catalog） | 同上，**且**至少一条路径落在 **活账号** |
| 活账号 | 未定义 | `status=active` **且** `schedulable=true`；**不**把 `temp_unschedulable` / rate-limit / overload 当作「非活」 |
| 瞬时容量 | 不进发现 | **继续不进**（RuntimeReadiness / 请求时 429） |
| Kiro mirror stub | 仍可贡献其 catalog 内模型 | 不变；native-only SKU（如 Fable）本就不由 stub 贡献 |

「活」刻意不含瞬时冷却字段：那些是 RuntimeReadiness 的事。只有运维把账号标成
不可调度（或非 active）时，发现投影才应收起该模型。

## Non-goals

- 不按 RPM、会话窗剩余、供应商 429 动态藏模型。
- 不改变实际请求调度、粘滞、failover、计费。
- 不在本立项中自动启用/禁用账号或改 mapping（那是运维 C 或 modelops）。
- 不把 public `/pricing` 与 authenticated `/v1/models` 强行合成一个列表。

## 影响面（实现时）

- Owner：universal capability discovery 投影（`GetAvailableModels` /
  capabilities 同源路径），以
  [`universal-key-capability-discovery.md`](universal-key-capability-discovery.md)
  为审批锚。
- 回归：曾仅由不可调度账号映射「挂名」的模型从菜单消失；有活账号映射的模型行为不变。
- 验证：正 — 仅不可调度 holder 的模型不出现在 `/v1/models`；负 — 临时
  `temp_unschedulable` 的唯一 holder 仍出现在菜单；回归 — `claude-fable-5` 在
  115/124/136 可调度时仍列出且可请求。

## 止血记录（C，已执行）

- UTC 2026-09-18：prod 账号 **91**（bedrock-2）、**150**（Cursor-93321020@qq.com）
  的 `credentials.model_mapping` 删除键 `claude-fable-5-1`；保留 `claude-fable-5`。
- 脚本：`ops/observability/remediate-drop-fable-5-1-mapping.sh`
  （`CONFIRM=drop-fable-5-1-mapping`）。
- 验证：同日 universal key `GET /v1/models` 仅见 `claude-fable-5`，不见
  `claude-fable-5-1`。
- 残留风险：若后续 `apply-accounts` / supplier sync / 人工编辑把
  `claude-fable-5-1` 写回不可调度账号，菜单可能再次出现空池模型——精简 B 落地前
  靠运维纪律；落地后由发现规则兜底。

## 审批门禁

`approved_by: pending`。人工审批并改 status 前 **不实现** 发现投影代码变更。
审批通过后的实现 PR 须绑定本文，并带上表验证。
