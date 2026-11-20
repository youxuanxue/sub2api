---
title: NewAPI as First-Class Fifth Platform — Scheduling & Dispatch Convergence
status: shipped
approved_by: xuejiao (PR #9 squash-merge)
approved_at: 2026-04-19
shipped_at: 2026-04-19
authors: [agent]
created: 2026-04-19
related_prs: ["#9", "#10"]
related_commits: [e768deed, af9a73a6, e9f8bc56, 0211c2b7, c5130c29, 6e0c0fce, bf784cab, 4441b642, ab39ddbb, 90d5d90c, d13b2646]
related_stories: [US-008, US-009, US-010, US-011, US-012, US-013, US-014, US-015]
revised: 2026-11-20（删现状盘点/实施清单/rollout 散文；保留不变量与 sentinel）
---

# NewAPI as First-Class Fifth Platform

`status: shipped`。选号 / 协议现行契约以
[`candidate-eligibility-ssot.md`](candidate-eligibility-ssot.md) 与
[`protocol-routing-ssot.md`](protocol-routing-ssot.md) 为准；本文只保留第五平台
调度池收敛的批准边界。Admin UI 见
[admin-ui-newapi-platform-end-to-end](admin-ui-newapi-platform-end-to-end.md)；
字段 follow-up 见 [newapi-followup-bugs-and-forwarding-fields](newapi-followup-bugs-and-forwarding-fields.md)。

## 0. TL;DR

补齐「调度池按 `group.platform` 分桶 + `messages_dispatch` 对 newapi 放行」。
不混池、不扩展 `IsOpenAI()` 上游语义、不新增协议入口、不改 bridge/计费。

## 2.1 核心原则

- **不混池**：`openai` group 不得拿到 `newapi` 账号，反之亦然。
- **单一 helper**：`IsOpenAICompatPoolMember(groupPlatform)` 表达 OpenAI-compat
  调度池成员资格；禁止再硬编码「四平台」列表。
- 路由 / endpoint / bridge 已支持 newapi；本文批准的是调度与 dispatch 放行。

## 2.2–2.6 调度语义（摘要）

- 池成员 = `account.platform` 与 `group.platform` 对齐的 OpenAI-compat 账号。
- Sticky / bucket cache / messages_dispatch 对 `platform=newapi` 与 openai 同形放行；
  空池须返回明确错误（不得静默当 openai 空池）。
- Bridge dispatch 路径不变。

## 3.1 / 3.2 Owners（实现锚点）

| 行为 | Owner |
| --- | --- |
| 调度池成员资格 | `account_tk_compat_pool.go`（`IsOpenAICompatPoolMember` / `AllSchedulingPlatforms`） |
| newapi 候选拉取 | `openai_gateway_service_tk_newapi_pool.go` |
| messages_dispatch 放行 | `openai_messages_dispatch_tk_newapi.go` |
| Sticky / 空池错误 | `openai_gateway_service_tk_newapi_pool.go`、`openai_account_scheduler_tk_errors.go` |

验收：US-008…015 Linked Tests；实现清单与 rollout 估算散文已删。

## 6. 不做的

混池；扩展 `IsOpenAI()`；新协议入口；本文件范围内的 admin UI（见 admin-ui 文）；
改 bridge/计费。

## 12. Sentinel registry

不可缺失载体登记在
[`scripts/sentinels/newapi.json`](../../scripts/sentinels/newapi.json)；
由 `scripts/sentinels/check-newapi.py` + preflight / upstream-merge Check 4 守卫。
新增第五平台关键符号须同 commit 追加 registry；禁止为过检查删条目。
