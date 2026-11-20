---
title: Candidate routing runtime snapshot and one-pass request path
status: approved
approved_by: "user (conversation approval: 同意。请乔布斯，拆解方案计划，编排 Agent 军团实现目标。开 pr)"
created: 2026-09-25
authors: [codex]
risk: high
revised: 2026-11-20（删实现散文/类型 dump；契约并入 candidate-eligibility / protocol-routing，本文只留批准边界）
related_docs:
  - candidate-eligibility-ssot.md
  - protocol-routing-ssot.md
  - universal-key-routing.md
  - qa-redaction-identified-formats-only.md
---

# Candidate routing runtime snapshot（批准边界）

实现 owner 与行为不变量以
[candidate-eligibility-ssot](candidate-eligibility-ssot.md#owners) 与
[protocol-routing-ssot](protocol-routing-ssot.md) 为准。本文只保留当时批准的高风险边界，不重复活 SSOT。

## 决策

网关把三类数据面分开，而不是在每个请求上重复计算静态账号/请求事实：

1. **不可变 candidate read snapshot** — 授权与静态路由事实的物化只读模型；
2. **runtime readiness** — 容量、配额、冷却、健康；
3. **一次构建的 `protocolrouter.CanonicalRequest`** — 整次 attempt 复用。

Snapshot 不是新的 eligibility / protocol / billing / security owner。现有 SSOT 继续裁决结果。不引入第二套 protocol graph、universal alias 表、全局 billing cache 或并行 candidate eligibility 服务。

## Snapshot 不变量（摘要）

- 由既有 DB/outbox 事实物化，经 scheduler cache/outbox 投递；**不存凭据**。
- 只含完整 generation；缺口/失败保留上一完整代并标 lag，从不发布半填充 map。
- 可预过滤，但**不能**授权、裁决 Plan 合法性、readiness 或计费来源。
- 缺失/滞后/无效 snapshot 必须安全失败；执行仍重读权威账号与 Plan。
- Provider 可对 miss 做一次有界 batch repository fallback，不得在请求 goroutine 重建/发布 snapshot，也不得 per-group 查询。

Owner 文件：`candidate_read_snapshot_tk.go`（见 candidate-eligibility Owners 表）。

## CanonicalRequest / QA

- Ingress 一次构建 immutable `CanonicalRequest`，candidate / handler / execution 复用。
- QA redaction 优化不得改变 capture/blob/archive ownership（见
  [node1 QA](design-tencent-cos-backup-node1-qa.md) 与
  [qa-redaction-identified-formats-only](qa-redaction-identified-formats-only.md)）。

## 批准边界

本文批准的是 **shadow/fallback 契约与只读物化**。当前实现保持 snapshot **只读且 shadow-only**：不授权 schema migration、公共 API 变更、生产切主路径或删除现有 DB 路径。那些 rollout 需单独验收证据与审批。

行为验收（与 candidate-eligibility / 既有测试对齐）：组序与拓扑不变；重复 membership 不放大选中概率；Direct/Universal 映射分离；计费来源保留；过期 sticky/continuation 重验；slot race / retry rebind 与最终路由等价；Plan 仍是唯一 model/converter owner；未知 QA 格式仍 passthrough。
