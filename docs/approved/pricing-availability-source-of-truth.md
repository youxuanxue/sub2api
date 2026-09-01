---
title: 模型可用性观测与目录投影——证据，不是交付资格 SSOT
status: approved
approved_by: "xuejiao（2026-08-25 本会话确认 B+）"
approved_at: 2026-05-06
revised_at: 2026-09-01
created: 2026-05-06
owners: [tk-platform]
scope: "model availability evidence / upstream discovery / catalog decoration and structural-gone pruning"
superseded_by: docs/approved/pricing-serving-single-source-of-truth.md
revision_note: >
  superseded_by 仅表示「交付公式」由 pricing-serving 拥有；本文仍是 availability
  Evidence owner（观测、徽章、structurally-gone），不是整篇作废。
---

# 模型可用性观测与目录投影——证据，不是交付资格 SSOT

交付资格只在 `docs/approved/pricing-serving-single-source-of-truth.md`。
本文只拥有 availability Evidence：带时间的观测、展示徽章，以及 structurally-gone 裁剪谓词。
Evidence 可以影响展示和运维动作，但不能成为第四个 serving owner。

`/pricing` 是目录投影，不是可用性与价格的统一准入。model-list 各入口共用本文的
structurally-gone 谓词，不另写一套 availability + pricing 准入。

## 1. 本文唯一拥有的事实

`model_availability` 只保存带时间范围的观测证据，例如：

- 最近一次成功/失败时间；
- provider/platform/model 维度的样本；
- `model_not_found`、`rate_limited`、`auth_failure`、`upstream_5xx`、
  `network_error` 等失败分类；
- stale/untested/degraded/structurally-gone 等派生展示状态。

每条证据必须带时间语义。没有时间戳的 `available=true` 没有资格存在。

它不拥有：

- `credentials.model_mapping`；
- `protocol_endpoint_capabilities.supported_protocols`；
- `protocolrouter.Plan`；
- account schedulable/cooldown/concurrency/capacity；
- global price、channel price 或 pricing alias；
- API key group/entitlement。

## 2. 允许的消费者

### 2.1 Admin upstream discovery

上游 `/models` 是 discovery feed。admin 可以看到 retired/deprecated 元数据、是否存在
canonical price owner、最近 evidence，以及 `priced` / `missing` 操作提示。
这是运营候选信息，不是自动上架或自动写 mapping 的指令。

### 2.2 公共目录装饰

公共目录可以展示 `degraded`、`stale`、`untested` 徽章。5xx、网络错误、限速或认证错误
不得让模型在橱窗中闪进闪出。

### 2.3 结构性消失的裁剪

只有 provider 明确、可重复地返回 model-not-found/retired，且信号属于 platform/model
而非单账号 auth/quota 时，公共投影可以隐藏 structurally-gone 模型。

即使隐藏：canonical price owner 仍保留到独立 lifecycle review 批准删除；mapping 与协议
owner 不被 availability 改写；admin/审计面仍能看到原始证据。

### 2.4 运维触发器

availability 可以触发 re-probe、告警、人工 review、catalog/mapping/price drift 审计、
provider 下线调查。它只能触发 owner 的正常写路径，不能直接跨写 owner。

## 3. 禁止的用法

- scheduler 用 `model_availability.status` 代替 request-time `Plan`；
- 因过去的 200 判定当前有容量；
- 因单个账号 404/401/429 删除 platform-wide 模型；
- 因 availability 为 ok 自动上架、自动定价或自动写 `model_mapping`；
- 把 price presence 当作 upstream reachability；
- 把 probe 的 `supported_protocols` 结论外推到每个 model×request-feature；
- 让 `/pricing` 成为权限、协议或容量 owner。

## 4. 信号分类

| 信号 | availability 含义 | 对交付决策 |
| --- | --- | --- |
| 成功 200 | 该时间、该账号/平台上成功过 | 证据；仍需本次 Plan 与 runtime |
| 明确 model-not-found/retired | 结构性消失候选 | 可触发 prune/review，不能替代 mapping/price 生命周期审批 |
| 404 无模型语义 | 弱负向样本 | 不足以单样本判死 |
| 429/quota | inconclusive | runtime/account 信号，不是模型不存在 |
| 401/403 auth | inconclusive | account credential 信号，不是模型不存在 |
| 5xx/network | degraded/stale 候选 | provider/runtime 故障证据，不是静态准入 |
| 无近期样本 | untested/stale | 未知，不等于不可用 |

## 5. 与其它 owner 的边界

- 交付公式：`pricing-serving-single-source-of-truth.md`。
- generation capability / route：`protocol-routing-ssot.md`。probe 只经 canonical writer
  更新对应 capability；availability 不写 capability，也不建第二套 route graph。
- 价格与 `_aliases`：`pricing-registry-hot-reload.md`。availability 只能显示价格是否缺失。
- newapi mapping / manifest：`tk_served_models.json` +
  `model-surface-activation-contract.md`。availability/probe 只作激活或复核证据。

## 6. 稳定判别

- 同一模型在不同账号上的结果可以不同，不能压成全局 serving 布尔值。
- 不同观测窗口可以给出不同错误率，日志不能单独判定可交付。
- 过去的成功不能证明当前存在可调度账号或容量。
- 账号级 auth、quota、rate-limit 或 network 失败不能改写共享 endpoint protocol capability。

## 7. 机械约束

- model-list/catalog 共享同一个 structurally-gone predicate；single、batch 与 admin
  discovery 不得各写一套 `unreachable` 判断。
- structurally-gone predicate 必须有正向、暂时降级负向、auth/quota 负向测试。
- availability 写入必须保留 platform/model/account/time/failure-kind。
- scheduler 读取 availability 作为 hard gate 必须触发 review finding。
- availability 写 price、alias、mapping 或 group entitlement 必须失败。
- `superseded_by` 指向交付总规范，禁止两份 approved 文档同时宣称 serving SSOT。

## 8. 验收

- 运营能看到最新证据和证据时间；
- 客户目录不会因瞬时 5xx/429 闪烁；
- 结构性消失可被一致裁剪；
- request-time 仍只由本次 Plan 与账号 runtime state 决定；
- price、alias、mapping、protocol、capacity 各自保持唯一 owner；
- availability 失效或缺失时表达“未知/陈旧”，不伪造不可交付。
