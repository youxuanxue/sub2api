---
title: Capacity-first 数据层安全设计（原型已晋升）
status: approved
approved_by: "xuejiao (design approval, 2026-07-20)"
approved_at: 2026-07-20
related_prs: [1385]
---

# Capacity-first 数据层安全设计（原型已晋升）

## 决策

先用低成本扩盘保险丝和可验证冷热分离解除容量倒计时，再根据可靠性、恢复责任和长期
净增长决定是否迁 RDS。

PR #1385 交付了三个安全构件的原型实现；正式 prod 只读容量探针与 verdict 已在
`docs/approved/design-phase1-prod-activation-gates.md`（PR #1536 及后续）晋升完成。
当前唯一 owner：

1. `ops/observability/probe-data-layer-capacity.sh` + `data_layer_capacity_verdict.py`（daily diagnostics 只读 prod 采样）；
2. `ops/observability/data_layer_capacity_projection.py`（离线投影，不连网）；
3. `ops/stage0/cfn_datavolume_parameter_plan.py` + `reconcile-cfn-datavolume-no-replace.sh`（no-execute change set 预览；执行仍须单独审批）。

归档 worker、S3 bucket、生产保留期、在线文件系统扩容和数据删除不在本设计范围。
本文件保留 PR #1385 的设计契约与阈值；**不再**描述「禁止 prod 查询」——那只适用于
原型阶段。任何 prod 卷扩容或 change set 执行仍须独立确认串与人工审批。

## 零影响边界

“不影响线上”定义为不得产生用户可感知影响，而不是宣称生产归档完全不消耗 CPU/IO。
原型阶段不执行任何 prod 命令；晋升后的只读 prod 核验必须满足：

- PostgreSQL session 强制 `default_transaction_read_only=on`；
- `lock_timeout=100ms`，不等待 DDL/维护锁；
- 近 30 天增长扫描 `statement_timeout=2s`；
- 总行数来自 `pg_stat_user_tables` 估算，不做全表 `COUNT(*)`；
- usage/ops 分区表大小按叶分区汇总，禁止把无存储的分区父表误当真实占用；
- 基础目录查询缺失、增长扫描超时或统计缺失一律输出 `unknown`，禁止猜成 green；
- 不运行 `VACUUM FULL`、大表 rewrite、锁表 DDL、容器重建、重启或清理。

这些阈值是线上保护契约；若真实演练证明仍有可感知影响，只能进一步收紧或停用，不能
为了拿到数字放宽。

## 容量投影契约

离线投影器消费已脱敏的 `PGSTATS` / `PGGROWTH` / `DFSTATS`，不连接网络。目标卷默认
100 GiB、usage 热层默认 90 天，但下列不确定量必须由调用方逐次显式提供：

- ops 物理可回收空间下界/上界；
- 非归档数据每月残余净增长；
- 运营告警水位。

输出必须同时给出低/高回收两种 scenario，并保留警告：普通 PostgreSQL `DELETE` 只证明
页可复用，不证明宿主机 `df` 已回收。任何 growth probe 超时、卷缩小或参数不完整都
fail closed；ops 回收上界不得超过 snapshot 观测到的 ops 关系总大小。

## DataVolume plan 状态机

```text
参数缺失/缩盘/prod 未确认 -> 拒绝，AWS 调用前结束
  |
  `-> 参数合法 -> 读取 stack -> 离线生成 grow-only 参数
       -> 创建唯一临时 AMI SSM 参数 -> 创建 no-execute change set
       -> guard 只接受 DataVolume Modify + Replacement=False + Properties/Size
          | 失败：删除预览工件并退出
          ` 通过：默认删除；显式 keep 只留待人工复核
```

脚本源码不得出现 `aws cloudformation execute-change-set`。prod plan 虽不执行资源变更，
仍会写临时 SSM 参数和 change set，因此必须提供与 prod stack 完全一致的确认串；本阶段
禁止实际调用该 prod 路径。

每个保留的 change set 使用唯一 SSM 参数，避免并发/重复预览覆盖共享参数。若目标大小
小于 live `DataVolumeSizeGiB`、出现 `Instance`/`EIPAssoc`、卷 replacement 或 Size 之外
的属性，计划必须拒绝。

## 归档 steady state

Generic usage/ops archive 已收口：`archive_health` 三 flag 绿 +
`OpsCleanupService` 日常 retention。Exception path 与 CLI 契约见
`ops/archive/README.md`、`design-prod-archive-bucket.md`、
`design-data-layer-prod-export-canary.md`。

候选保留策略为 usage 热 90 天、raw ops 热 30 天。prod 上 `usage_logs`
已完成日分区 cutover（见 `design-data-layer-phase1-closeout.md`），不在 prod
做 `VACUUM FULL` 或直接 rewrite 来追求 `df` 好看。QA 不由本通用设计管理。
扩盘与归档分别审批，任何一个完成都不自动授权另一个。

## 验收门

- [x] 探针正向返回字段化 snapshot，超时/缺统计负向返回 `unknown`（正式 probe + 单测）。
- [x] 容量探针已按 `design-phase1-prod-activation-gates.md` 晋升；DataVolume plan 执行仍须单独审批。
- [ ] 离线投影对 50→100 GiB 和低/高回收 scenario 的计算由测试覆盖。
- [ ] DataVolume 参数计划拒绝缩盘、缺 size 和错误 prod 确认串。
- [ ] change-set guard 只接受恰好一条 `DataVolume/Modify/Replacement=False/Properties/Size`。
- [ ] plan shell 不含 execute path，不调用部署、SSM run-command 或容器命令。
- [ ] 本地 preflight 全绿后提交人工审查；merge 不代表批准任何 prod 操作。

## 明确不做（本设计范围内）

- 不通过本设计自动创建 prod change set/SSM 参数或执行 DataVolume 变更（prod plan 预览仍须
  单独确认串与人工审批）。
- 不自动扩文件系统、不重启任何服务。
- 不新增生产归档 schema/worker/S3 bucket，不通过本设计删除 usage/ops 数据。
- 不改变 RDS PR #587，也不把容量缓解冒充数据库高可用。

注：只读 prod 容量探针已在 Phase1 activation gates 晋升（见上文「零影响边界」）；「原型阶段
禁止 prod 查询」不再适用于 daily diagnostics 路径。
