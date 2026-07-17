# Stage0 prod 数据归档与 RDS 迁移 SOP

> 当前状态：**设计与非生产演练正式启动，生产切换未批准**。审批基线是
> `docs/approved/design-prod-data-archive-rds.md`。该文件 `status/approved_by` 未变为
> `approved`/具体审批人之前，生产 cutover 脚本会机械拒绝执行。

## 0. 先说人话

当前 prod 仍是 EC2 上的本机 PostgreSQL + Redis。数据库约 23.7 GiB，50 GiB 数据卷
约用了 72%，按近期增速进入 `approaching`，旧版“等很久以后再看 RDS”的结论已经过期。

本方案同步推进两件事：

1. **归档**：把到期的 usage/ops/QA 历史可靠放到 S3，验证后再删热库，解决长期增长。
2. **RDS**：把在线 PostgreSQL 搬出 app 主机，解决数据库与单机同生共死、维护和恢复
   都绑在一起的问题。

现有每小时 S3 pgdump 继续保留，但它只保护核心 OLTP/计费数据，明确不含 usage/ops/QA
表行，所以它是灾难备份，不是历史归档。

## 1. 不能破的边界

1. 生产数据写入、删除、容器重建、CFN/RDS 创建与切换都属于独立高风险窗口；本 PR
   只提交设计、惰性基础设施和演练能力。
2. 运行时数据层配置的唯一真相是 SSM SecureString
   `/tokenkey/prod/stage0/data-layer-env`，不能同时在 CFN 参数、主机文件和人工笔记维护
   三套 endpoint。
3. edge 保持本机 PostgreSQL/Redis。本轮只迁 prod，不把 prod 决策扩散到 edge。
4. 归档必须先上传并校验对象、manifest、行数/checksum，再推进水位并删除热数据。
   校验失败时保留热数据，禁止 `|| true` 继续删。
5. RDS 对外开放写入后，禁止直接切回切换前的本地旧库。默认前向修复；只有反向增量
   回放和逐表对账完成后，才允许把写流量迁回。
6. 本地 PostgreSQL 数据卷在切换后至少保留 14 天，但它只是取证/重放来源，不代表
   可以忽略 RDS 新写入直接回滚。

## 2. 目标拓扑

```text
迁移前                              迁移后
EC2: Caddy + app                    EC2: Caddy + app
     + PostgreSQL + Redis                + Redis (AOF)
              |                                |
              |                         VPC private 5432/TLS
              |                                |
              `----------------------> RDS PostgreSQL
                                        |-- 14d PITR
                                        |-- precious pgdump -> S3
                                        `-- expired history -> S3 archive
```

Redis 目前只有 MiB 级，继续留本机。上第二个 app 副本前，或 Redis 真实造成事故时，
再单独评估 ElastiCache。

## 3. 阶段 0：把四个业务决定写进审批文档

生产演练前必须明确：

- usage、ops、QA 各自在热库留多久，S3 冷存多久；
- 最长可接受停写时间；
- Single-AZ 的维护/AZ 故障停机是否可接受，否则直接 Multi-AZ；
- RDS 新写入后发生故障，是前向修复还是投入反向增量回放能力。

这些是业务/架构选择，不能由 Agent 从磁盘大小猜出来。决定写回
`docs/approved/design-prod-data-archive-rds.md`，审批 merge 后才成为执行基线。

## 4. 阶段 1：短期维护加固 Redis 与容器日志

### 4.1 为什么需要 recreate

仓库 compose 已经声明：

- Redis：`appendonly yes` + `appendfsync everysec`；
- 全容器：Docker `json-file`，`max-size=100m`、`max-file=5`。

live Redis 仍是 `appendonly=no`，PostgreSQL/Redis 仍是旧的无界日志配置。Docker restart
不会重读 logging driver 和创建参数，必须 recreate 容器才会真正应用。

### 4.2 何时重建哪些容器

| RDS 预计时间 | 维护动作 | 原因 |
| --- | --- | --- |
| 1-2 周内切 RDS | 只 recreate Redis | PostgreSQL 很快退出本机，避免重复一次 DB 停机 |
| 更晚 | 同一窗口受控 recreate Redis + PostgreSQL | 尽快补 AOF 与日志上限，仓库/live 收敛 |

### 4.3 窗口门禁

窗口前只读确认：最新 S3 pgdump 新鲜且可解压、卷剩余空间够 AOF rewrite、Redis
`used_memory`/peak、容器 mount、当前镜像和 compose config。先 drain app，按一个容器一个
容器执行 recreate，不做 `down -v`，不删除 volume。

窗口后必须验证：

```bash
redis-cli INFO persistence        # aof_enabled:1，最近 AOF 状态正常
redis-cli CONFIG GET appendonly   # yes
docker inspect tokenkey-redis --format '{{json .HostConfig.LogConfig}}'
docker inspect tokenkey-postgres --format '{{json .HostConfig.LogConfig}}'
docker compose ps
```

然后跑 `ops/stage0/post_deploy_smoke.sh`，观察 Redis AOF rewrite、主机 IO、容器启动耗时
和错误率。AOF 增加持续写盘与 rewrite 峰值；日志轮转会覆盖旧本地日志。两者都是短期
保险丝，不替代 RDS、S3 归档或集中日志。

## 5. 阶段 2：先证明归档闭环

### 5.1 usage/ops

归档 worker 按 UTC 时间批次执行：

```text
选定已封口批次 -> 导出不可变文件 -> 生成 manifest/checksum/行数
-> 上传 S3 临时键 -> 远端 HEAD/下载抽检 -> 原子提交归档水位
-> 删除批次或 DROP 已过保留期分区
```

重复执行同一批次必须幂等；对象键和水位必须能判断“已完成”，不能靠值班人员记忆。
usage 保留 90 天是候选值，ops 热存与所有 S3 生命周期由审批文档最终决定。

### 5.2 QA

当前只证明过人工导出，未证明每日 auto-archive 在 prod 持续运行。按
`docs/qa-export-s3-and-auto-archive.md` 启用/演练后，至少连续观察多个调度周期：ZIP 非空、
manifest/checksum 正确、失败不会推进 cleanup。只有这条链路成立，才可以按候选 2 天
热存清 QA records/blobs。

### 5.3 归档验收

- 从 S3 随机恢复一个封口批次，行数和 checksum 与 manifest 一致；
- 重跑同一批次不产生重复或覆盖错误；
- 模拟上传/校验失败，热库数据和水位不动；
- daily diagnostics 能报告最近成功水位、延迟和失败原因；
- S3 lifecycle 与审批的冷存年限一致。

归档不通过，RDS 仍可做非生产演练，但不批准生产切换。否则只是把增长账单从 EBS
搬到 RDS。

## 6. 阶段 3：惰性平台就位

PR 合并本身不得创建 RDS 或改生产 overlay。平台包含：

- 双模式 compose：本机 `localpg,localredis`；RDS 模式 `localredis` + external override；
- `tokenkey-psql` / `tokenkey-pg_dump` / `tokenkey-redis-cli` 统一访问 seam；
- app 栈私有子网/Exports 与独立 `stage0-data.yaml`；
- SSM `data-layer-env` 单一运行时配置；
- compose/CFN 生成物和两模式行为门禁。

存量 wrapper 安装是幂等平台动作，但对 prod 执行仍必须放到已批准变更窗口。任何 ops
脚本若仍硬编码 `docker exec tokenkey-postgres`，必须先改走 wrapper；外部模式没有该容器。

## 7. 阶段 4：RDS 非生产演练

### 7.1 创建候选环境

候选默认：PostgreSQL 18.1、`db.t4g.medium`、gp3 100 GiB、autoscaling ceiling 500 GiB、
PITR 14 天、Single-AZ、private SG-only。最终参数以演练结果和审批文档为准。

演练至少覆盖：

1. 从与 prod 同量级的脱敏/快照数据做完整 restore，记录 dump、传输、restore、索引恢复、
   analyze 和逐表对账耗时；
2. app 连接 TLS、连接池乘数、pgdump client/server 版本；
3. 正常请求、计费去重、后台 job、QA/cleanup 和每小时珍贵类 pgdump；
4. RDS reboot/failover、连接恢复、PITR 到新 endpoint；
5. 开放写入前撤回成功；开放写入后脚本拒绝回旧库，按前向修复流程处理；
6. app 实例 replace 后从 SSM overlay 自动恢复到相同数据层，不发生 split-brain。

### 7.2 选择搬迁方式

- 若完整 dump/restore + 对账小于批准的停写窗口，选停写搬迁，链路最简单。
- 若超过窗口，改用 DMS/logical replication，先同步存量和增量，最后短暂停写收尾。

不得在生产窗口第一次验证 DDL、sequence、大对象、复制槽或 pgdump 恢复速度。

## 8. 阶段 5：生产预检与创建资源

这是独立高风险审批，不因本 PR 合并自动获得授权。

1. app 栈先创建 change set，只允许新增私有子网/路由/Exports；`Instance`、`DataVolume`
   出现在 replacement 列表立即停止。
2. RDS 密码仅写 SSM SecureString，不进入 shell history、PR、日志或 CFN 参数历史。
3. 数据栈 change set 使用审批后的 class/storage/Multi-AZ/连接告警参数。
4. 验证 RDS endpoint 仅私网可达、TLS 生效、Performance Insights/alarms/PITR 正常。
5. 再跑一次归档水位、S3 pgdump 新鲜度、卷空间、源库健康和恢复演练证据检查。

## 9. 阶段 6：生产切换窗口

### 9.1 停写搬迁路径

```text
公告维护 -> drain 并确认 in_flight=0 -> 阻断新写入 -> 最终 dump/restore
-> extensions/sequence/逐表行数与关键金额对账 -> app 指向 RDS
-> 内部 smoke -> 开放写入 -> 观察
```

对账失败且尚未开放写入：恢复本地配置并开回本地库。对账成功后由审批人执行生产
cutover 原语；脚本会再次检查设计审批 frontmatter。

### 9.2 低停机同步路径

```text
全量同步 -> 持续追增量 -> 复制延迟归零 -> 短暂停写
-> sequence/最终水位对账 -> 切 endpoint -> 开放写入 -> 观察
```

DMS/logical replication 的任务配置、复制槽清理和失败重放必须在选定该路径后补成受审
工件；不能拿上面的流程图直接当生产命令。

### 9.3 开放写入后的规则

从 RDS app 第一次可接收请求起，切换前本地库即视为只读取证。任何失败都保持 overlay
指向 RDS，优先修连接、参数、实例或从 PITR 新 endpoint 前向恢复。没有 RDS -> local
增量回放与对账证据，禁止删除 overlay 后回旧库。

## 10. 观察与收尾

至少观察完整业务周期：错误率、请求/计费一致性、连接数、CPU、FreeableMemory、
FreeStorageSpace、Read/WriteLatency、IOPS、归档水位和 pgdump 新鲜度。

本地 PostgreSQL 卷至少保留 14 天。到期后是否清理属于新的破坏性审批；先确认 PITR、
S3 pgdump、归档恢复和审计证据齐全。RDS 资源保留
`DeletionProtection + DeletionPolicy/UpdateReplacePolicy: Retain`。

## 11. 故障与恢复摘要

| 时点 | 允许动作 |
| --- | --- |
| 尚未切 endpoint | 修复/重做搬迁；源库仍是唯一真相 |
| 已切 endpoint、尚未开放写入 | 可撤回本地配置，重新对账 |
| 已开放 RDS 写入 | 前向修复或 PITR 到新 RDS endpoint |
| 必须回本地 | 先回放 RDS 增量并逐表对账，再经单独审批切流 |

完整灾难恢复语义见 `deploy/aws/RUNBOOK-disaster-recovery.md`。该 runbook 不再把
“允许丢 RDS 新写入的一键 rollback”视为合法恢复方式。

## 12. 已知锁定关系

- app 栈的 `VpcId` / `PrivateSubnetIds` / `AppSecurityGroupId` 被数据栈 Import 后不能直接
  改名/删除；先处理数据栈引用。RDS 本体受 Retain/DeletionProtection 保护。
- RDS 存储扩容后不能原地缩小；500 GiB 是保险丝，不是允许归档长期失败的预算。
- Redis AOF、Docker 日志轮转和 RDS 是三个不同层次的措施，不能互相冒充完成。
