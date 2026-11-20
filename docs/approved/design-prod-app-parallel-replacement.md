---
title: prod 应用机并行替换，取消原地 LUKS 搬盘
status: approved
approved_by: "feng（2026-11-11 会话：同意取消原地搬盘、改为新机并行替换；不中断请求、验证不过不下线旧机；同意双应用节点、摘机演练及 QA 集中管理。2026-11-12 会话：明确批准数据机和应用机各自独立兜底，QA 小时结束满 24 小时后接受丢失并强制清理；node1 归档保留期不变。入口选型及预算待确认）"
approved_at: 2026-11-11
risk: high
revised: 2026-11-20（迁机执行叙事删除；仅保留仍有效不变量；入口/LUKS 见 design-prod-entry-multi-app）
---

# prod 应用机并行替换（不变量保留）

> **入口 / HA / 脱离 LUKS** 已由 [`design-prod-entry-multi-app.md`](design-prod-entry-multi-app.md) 接管并完成。
> 本文**不删除**：双应用机、不中断请求、QA 集中管理与 24h 强制清理边界。

## 仍有效的决定

1. **双应用机保留**：共享数据机；摘机/故障演练验证后再决定是否长期保留两台。
2. **不中断请求**：验证不过不下线旧机；不可逆销毁须另批（LUKS 销毁已在入口设计完成）。
3. **QA 集中管理**：数据机协调归档；应用机只做 capture + 本地短期缓冲；大文件直传 node1。
4. **24h 强制清理**：小时结束后满 24 小时可丢未归档 QA；应用机 payload/DLQ 与数据机分区各自
   `force_retention`，不依赖协调器/对端/归档成功。node1 已归档保留期不变。

## QA 集中管理方案

线上入口见 [`ops/qa/COORDINATOR.md`](../../ops/qa/COORDINATOR.md) 与
[`ops/qa/README.md`](../../ops/qa/README.md)。实现 owner：
`coordinator_host.py`、`source_worker.py`、`data_machine_coordinator.py`、
`source_cleanup.py`、`force_retention.py`。

- 归档绑定站点、稳定 source 身份、UTC 小时与内容摘要；同小时各源隔离发布。
- 共享 DB 分区只在数据机清理；每台应用机只删本机精确小时目录。
- 未证实归档的过期删除记为接受丢失，不伪造 archive 成功。

迁机期候选准备/SSM 回执与入口询价执行表已删除；勿按已删步骤重建 LUKS 或热备入口。
