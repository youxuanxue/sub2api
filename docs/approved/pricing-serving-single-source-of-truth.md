---
title: 模型交付承诺 SSOT——一个承诺、三个判定
status: approved
approved_by: "xuejiao（2026-08-26 本会话确认）"
approved_at: "2026-06-17"
revised_at: "2026-08-29"
revision_note: >
  generation plan cache 只覆盖 request digest 与 accountID；发送前用当前
  owner 重规划，路由事实不等价才 stale。不再把 account/capability revision
  当 Execute 裁判。availability 只作 Evidence。
authors: [agent]
created: 2026-06-17
related_prs: [1821]
related_stories: []
related_design: docs/approved/pricing-registry-hot-reload.md, docs/approved/pricing-availability-source-of-truth.md, docs/approved/priced-or-it-doesnt-ship.md, docs/approved/protocol-routing-ssot.md, docs/approved/universal-key-capability-discovery.md
supersedes: "pricing-availability-source-of-truth.md 中把 /pricing 或 model_availability 视为 serving eligibility owner 的声明"
---

# 模型交付承诺 SSOT——一个承诺、三个判定

新请求只问一件事：**这次执行现在能不能交付？**

```text
CatalogPolicy + RequestPlan + RuntimeReadiness = 本次可交付
```

不落 `deliverable` 总表。定价、探测成功、菜单可见，任何一项都不能单独放行。
观测是证据，不是裁判。

已创建的异步任务不再回答这三个问题：按提交时写入的 canonical task record 走
`TaskContinuation`。新工作要规划；已创建的工作只续接，不重新选号。
`TaskContinuation` 是生命周期分支，不是第四个商业事实。

## 1. 三个判定

| 判定 | 只回答 | 读哪些 owner | 不拥有 |
| --- | --- | --- | --- |
| **CatalogPolicy** | 能不能公开展示、按什么价结算 | 公共目录投影；`tk_pricing_overlay.json`（含 `_aliases`）；scope 内 `channel_model_pricing` | 账号映射、协议、容量 |
| **RequestPlan** | 这个 operation 在这个账号上有没有合法路径 | 统一 outcome：合法 plan 或 reason。generation 现由 `protocolrouter` 实现；其它 family 读各自已有 owner | 实时容量、客户价格 |
| **RuntimeReadiness** | 已有路径的账号现在能不能跑 | schedulable、cooldown、quota、concurrency、capacity | 展示、价格、协议 |

组合层只返回 outcome。`model_mapping`、endpoint protocol capability、channel/adapter
capability 都是 RequestPlan 的输入，不是第二套可交付真相。
`protocol_endpoint_capabilities.supported_protocols` 只描述 canonical endpoint 的原生
generation wire，推不出某个模型、feature、operation 或账号的请求合法性。

## 2. RequestPlan 是同形 outcome，不是万能 package

凡是会创建一次新上游执行的 operation，都必须给出同形 outcome。这不要求所有 family
迁入同一个 package。

| family | 当前 planning owner |
| --- | --- |
| generation（`messages` / `chat_completions` / `responses` / Gemini `generateContent`） | `protocolrouter`；细节只在 `docs/approved/protocol-routing-ssot.md` |
| embedding / 独立 image / video submit / utility | 各自已有 model、endpoint、adapter、task owner |

Gemini `generateContent` 是 generation wire，不是媒体例外；经它出图仍走 generation route。
独立 image/video API 才进对应 family。不得为了形式统一把 media/embedding/utility 提前并进
`protocolrouter`。

image poll 读 `ImageTaskRecord`，video fetch/status 读 `VideoTaskRecord`。二者都是
`TaskContinuation`：可刷新同一账号 token，不得改选账号、endpoint 或 upstream task。

## 3. 目录只投影 CatalogPolicy

```text
catalog candidate
AND 能解析到 direct price owner 或 `_aliases`
AND 没有 structurally-gone 证据
= 可公开展示
```

`display=false` 不等于 API 禁止。`priced` 不能推出 Plan 或容量。
`model_availability`、probe、upstream `/models`、流量只是 Evidence。
只有明确 `model_not_found/retired` 可裁目录；5xx/429/auth/普通 unreachable 不能裁目录，
也不能当 scheduler gate。证据分类见 `docs/approved/pricing-availability-source-of-truth.md`。

## 4. 价格与 alias

```text
匹配 scope 的 channel_model_pricing
  > active tk_pricing_overlay.json
    > embedded last-known-good
```

公开价格 alias 只写 `_aliases: alias -> owner`：owner 必须存在，只单跳，禁止 self/chain/cycle。
路由归一化不拥有价格；legacy substring matcher 是债务。运行期 priced-serving gate 只保证
发往上游前能解析结算价，见 `docs/approved/priced-or-it-doesnt-ship.md`。

## 5. 本文拥有的门禁

- 组合公式与 `TaskContinuation` 分界只在本文；协议文档不得复制公式或拥有 `TaskContinuation`。
- 各 family 的 RequestPlan 测试从 canonical owner 派生样本，不手写模型/协议清单。
- generation plan cache 覆盖 request digest 与 accountID；成败都只规划一次。发送前用当前 owner 重规划，路由事实不等价才 stale。
- image/video 续接必须读提交时 task record，禁止重新 scheduler 选号。
- `_aliases` 校验 owner 存在、单跳、无重叠、无环。
- catalog / model-list / admin discovery 共用 structurally-gone predicate。
- 其它文档、skill、注释、sentinel 不得再声明第二套 delivery 或 capability truth。

实现是否对齐由当前代码的测试和 preflight 判断。本文不嵌入 SHA、镜像、edge 或某次探测结果。

## 6. 不做什么

- 不建复制价格、mapping、协议、capability、容量的 mega-registry。
- 不建全协议 × 全 operation 自动转换图。
- 不把任务续接伪装成新的 RequestPlan。
- 不把 `/pricing`、availability、probe、`/models` 或流量当作请求准入。
- 不让价格 presence 自动上架、自动写 mapping 或自动声明协议能力。

审查只问：新执行还是续接？读哪个 owner？owner 被绕过时哪个检查会失败？
