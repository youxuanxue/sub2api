---
title: Discovery require live account (精简 B)
status: approved
approved_by: "feng (对话审批 2026-09-18：同意先 C 止血再实现精简 B)"
approved_at: "2026-09-18"
authors: [agent]
created: 2026-09-18
related_design: docs/approved/universal-key-capability-discovery.md, docs/approved/universal-key-routing.md, docs/approved/pricing-serving-single-source-of-truth.md
---

# Discovery：无活账号则不展示

## 背景

2026-09-18 实测：universal key 的 `GET /v1/models` 列出 `claude-fable-5-1`，但
`POST /v1/messages` 返回 `429 No available accounts`。根因是 candidate discovery
从 `ListCandidateAccounts`（含 `schedulable=false` 成员）收集 mapping 并做
dry-plan，不要求存在 **active + Schedulable** 的持有账号；当时仅账号 91/150
持有该映射，且均为 `schedulable=false`。

同日决策（对话确认）：

1. **先 C 止血**：从不可调度持有账号上摘掉空池模型映射（已对 91/150 去掉
   `claude-fable-5-1`；`claude-fable-5` 保留）。
2. **再实现精简 B**（本文）：发现侧排除「没有任何活账号持有路径」的模型。
3. **明确不做 A**：不把瞬时限流 / `temp_unschedulable` / 短窗 overload 写进菜单。

## Goal

让 `/v1/models`（及同一发现投影 owner 的站内 capabilities）在「长期没货」时不再
展示该模型；同时保留「瞬时容量不进菜单」——避免限流时菜单变成心电图。

## Approved decision

修订
[`universal-key-capability-discovery.md`](universal-key-capability-discovery.md)
Decision 5 与 Capability Contract：

| 维度 | 现行（修订前） | 精简 B |
| --- | --- | --- |
| 列入菜单 | 存在可 dry-plan 路径（映射或原生空映射 catalog） | 同上，**且**至少一条路径落在 **活账号** |
| 活账号 | 未定义 | `status=active` **且** `schedulable=true`；**不**把 `temp_unschedulable` / rate-limit / overload 当作「非活」 |
| 瞬时容量 | 不进发现 | **继续不进**（RuntimeReadiness / 请求时 429） |
| Kiro mirror stub | 仍可贡献其 catalog 内模型 | 不变；native-only SKU（如 Fable）本就不由 stub 贡献 |

「活」刻意不含瞬时冷却字段：那些是 RuntimeReadiness 的事。只有运维把账号标成
不可调度（或非 active）时，发现投影才应收起该模型。

## Implementation owners

| Fact | Owner |
| --- | --- |
| Live predicate | `Account.IsLiveForDiscovery` in `account_discovery_live_tk.go` |
| Menu candidate + route gate | `candidate_discovery_tk.go` (`discoverCandidates`) |
| Inference membership snapshot | `ListCandidateAccounts` still returns disabled members; discovery applies the live gate before advertising |

## Non-goals

- 不按 RPM、会话窗剩余、供应商 429 动态藏模型。
- 不改变实际请求调度、粘滞、failover、计费。
- 不在本立项中自动启用/禁用账号或改 mapping（那是运维 C 或 modelops）。
- 不把 public `/pricing` 与 authenticated `/v1/models` 强行合成一个列表。

## 验证

- 正：仅 `Schedulable=false` holder 的模型不出现在 discovery。
- 负：临时 `temp_unschedulable` 的唯一 holder（`Schedulable=true`）仍出现在菜单。
- 回归：有活 peer 时模型仍列出；支付/余额不足不单独藏模型。

测试：`TestUS050_CandidateDiscoveryRequiresLiveAccount`、
`TestUS050_CandidateDiscoverySeparatesPaymentTiersAndIgnoresRuntimeCapacity`、
`TestAccountIsLiveForDiscovery`。

## 止血记录（C，已执行）

- UTC 2026-09-18：prod 账号 **91**（bedrock-2）、**150**（Cursor-93321020@qq.com）
  的 `credentials.model_mapping` 删除键 `claude-fable-5-1`；保留 `claude-fable-5`。
- 脚本：`ops/observability/remediate-drop-fable-5-1-mapping.sh`
  （`CONFIRM=drop-fable-5-1-mapping`）。
