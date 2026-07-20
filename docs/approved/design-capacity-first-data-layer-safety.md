---
title: Capacity-first 数据层安全原型
status: pending
approved_by: pending
---

# Capacity-first 数据层安全原型

## 决策

先用低成本扩盘保险丝和可验证冷热分离解除容量倒计时，再根据可靠性、恢复责任和长期
净增长决定是否迁 RDS。本阶段只实现本地与非生产工件的设计/测试，不批准任何 prod
查询、CloudFormation/SSM 写入、卷扩容、容器重启、数据导出或删除。

当前原型只交付三个安全构件：

1. 有锁等待和执行时间上限的只读容量探针；
2. 输入假设全部显式化的离线容量投影器；
3. 只创建并验证 no-execute change set 的 DataVolume plan 工具。

归档 worker、S3 bucket、生产保留期、在线文件系统扩容和数据删除不在本原型实现范围。

## 零影响边界

“不影响线上”定义为不得产生用户可感知影响，而不是宣称生产归档完全不消耗 CPU/IO。
本原型更严格：不执行任何 prod 命令。未来进入只读核验时也必须满足：

- PostgreSQL session 强制 `default_transaction_read_only=on`；
- `lock_timeout=100ms`，不等待 DDL/维护锁；
- 近 30 天增长扫描 `statement_timeout=2s`；
- 总行数来自 `pg_stat_user_tables` 估算，不做全表 `COUNT(*)`；
- usage/ops/QA 分区表大小按叶分区汇总，禁止把无存储的分区父表误当真实占用；
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
fail closed。

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

## 后续归档闭环

归档实现进入下一审批阶段，顺序固定为：

```text
非生产封口批次 -> 导出 -> manifest/行数/checksum -> 随机恢复
-> dry-run 水位 -> 单批 canary（仍不删）-> 独立批准后才允许小批删除
```

候选保留策略为 usage 热 90 天、raw ops 热 30 天、QA 本机 2 天。ops 优先整分区 drop；
usage 当前不是自动分区表，不在 prod 做 `VACUUM FULL` 或直接 rewrite 来追求 `df` 好看。
扩盘与归档分别审批，任何一个完成都不自动授权另一个。

## 验收门

- [ ] 探针正向返回字段化 snapshot，超时/缺统计负向返回 `unknown`。
- [ ] 离线投影对 50→100 GiB 和低/高回收 scenario 的计算由测试覆盖。
- [ ] DataVolume 参数计划拒绝缩盘、缺 size 和错误 prod 确认串。
- [ ] change-set guard 只接受恰好一条 `DataVolume/Modify/Replacement=False/Properties/Size`。
- [ ] plan shell 不含 execute path，不调用部署、SSM run-command 或容器命令。
- [ ] 本地 preflight 全绿后提交人工审查；merge 不代表批准任何 prod 操作。

## 明确不做

- 不连接或查询 prod，不创建 prod change set/SSM 参数。
- 不修改当前 50 GiB 卷，不扩文件系统，不重启任何服务。
- 不新增生产归档 schema/worker/S3 bucket，不删除 usage/ops/QA 数据。
- 不改变 RDS PR #587，也不把容量缓解冒充数据库高可用。
