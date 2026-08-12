---
title: Prod-only QA 24 小时在线层与 S3 归档生命周期
status: approved
approved_by: "user (conversation approvals, 2026-08-05 through 2026-08-11; UTC-hour partitions, default-free steady state, no rehome)"
approved_at: 2026-08-05
last_reviewed: 2026-08-11
created: 2026-08-05
authors: [agent]
risk: high
scope:
  - prod QA capture
  - raw evidence archive
  - online retention and cleanup
  - API-key trajectory export
  - ops recovery
  - edge QA removal
---

# Prod-only QA 24 小时在线层与 S3 归档生命周期

## 1. 决策摘要

QA 数据面收敛为一个清晰产品承诺和一条运维流水线：

> 只有 prod 捕获 QA；prod 在线查询严格覆盖过去 24 小时；每小时运行的
> `tokenkey-qa-maintenance` 负责归档、校验和受限前向补偿，整点运行的
> `tokenkey-qa-boundary` 负责未来小时分区覆盖、整分区 DROP 和对应热文件清理；稳态没有 DEFAULT、
> 逐行 DELETE、rehome、copy 或 move。S3 原始归档保留 7 天。Edge 完全退出 QA。
> 用户导出和运维深度分析都不从 prod 下载大文件，
> 也不在 prod 执行重型投影或解压分析。

本设计替代当前并存的 `qa_capture.retention_days=1`、CloudFormation
`QaStaleRetentionDays=1.5`、live host retention=2 天和每日 stale cleanup 等多套语义。
实现后，面向用户和运维只保留以下数据契约：

| 数据层 | 范围 | 生命周期 | 读取路径 |
|---|---|---|---|
| prod 在线 QA | 所有 prod 用户、所有 API key | 逻辑严格 24 小时；物理约 24–25 小时 | 在线查询和轻量关联 |
| S3 raw archive | 所有 prod QA 元数据及脱敏 evidence | 7 天 | 受信 Export Worker；受控 ops 本机工具 |
| 用户 trajectory ZIP | 指定用户、指定 API key | 独立短期生命周期 | 浏览器通过 S3 presigned URL 直下 |
| Edge QA | 无 | 0 | 不适用 |

S3 中保存的是经过现有 QA 脱敏逻辑处理的 evidence，不是未经脱敏的原始网络流量。

## 2. 目标与非目标

### 2.1 目标

1. 用户在线只看到滚动过去 24 小时的 QA 数据。
2. 到期数据在下一次小时维护中及时物理清理，不再形成 48–72 小时锯齿。
3. prod 所有用户、所有 API key 的 QA 原始证据按小时归档到 S3，并保留 7 天；不受用户导出开关影响。
4. 归档、最终核对和前向补偿由 maintenance 唯一负责；小时分区供给与到期 DROP 由 boundary 唯一负责；
   两个确定性阶段共用 advisory lock，消除竞态和重复 owner。
5. 用户 API-key 导出仍由管理员授予的 `traj_export_enabled` 控制，且不在 prod 读取大量 Blob、生成 ZIP
   或承载下载流量。
6. 运维可从 S3 直接下载到本机隔离目录深度分析，不经过 prod API、数据库、磁盘或网络。
7. 只在系统已经执行不可逆保护动作时发送 P0；普通失败和延迟只记 ledger/metrics，不推送噪声告警。
8. Edge 不捕获、不归档、不导出、不清理 QA，也没有 QA S3 权限。
9. QA数值由一个repo-owned policy定义，并由preflight机械校验文档、deploy与runtime owner收敛。

### 2.2 非目标

- 不把 S3 raw archive 建成第二套在线数据库。
- 不允许普通用户直接读取 raw archive。
- 不从 prod 回源补齐用户导出中尚未封口的最后一个小时。
- 不在磁盘压力下自动暂停 QA capture；磁盘保护通过清理最旧 QA 完成。
- 不为 Edge 保留任何 QA 能力或兼容性旁路。
- 不承诺 raw archive 永不丢失；服务可用性和磁盘安全高于冷归档完整性。
- 不提供用户主动删除 QA 的 UI/API；本期删除只来自统一 retention、磁盘 emergency 和受控运维流程。
- 不由本设计文档的 merge 自动创建 AWS 资源、修改线上配置、删除数据或部署。

## 3. 现状与问题

当前 prod QA 默认捕获绝大多数已认证网关请求。evidence 写入本地 `qa_blobs`，元数据写入
`qa_records`；主 BlobStore 写失败时才降级到 `qa_dlq`。用户侧只保留受 `traj_export_enabled`
控制的 API-key trajectory export。

已退休实现曾每天运行一次 `tokenkey-qa-stale-cleanup.timer`，以可变 N-day 配置删除 DB 行并
全目录扫描 `qa_blobs/qa_dlq`；过渡实现也曾使用 `*:45`、随机延迟和分批 DELETE。批准的稳态改为
UTC 小时分区：`tokenkey-qa-boundary.timer` 在 `*:00` 使用数据库时钟供给未来分区并 DROP 所有已到期
小时子表，再删除同小时 Blob/DLQ 目录。legacy stale cleanup 只在 no-move cutover 排空期间保留，
finalize receipt 成功后禁用，不再是稳态 owner。

数值配置的唯一 owner 是 `ops/qa/policy.yaml`；archive maintenance、hourly boundary、bootstrap、
CloudFormation、approved文档和测试由`qa-lifecycle-ssot.py`机械交叉校验。Edge QA capture、
cleanup unit和S3权限保持退役。

用户导出虽然可把完成 ZIP 放到 S3，但大量本地 Blob 读取、投影和 ZIP 生成仍发生在 prod，曾有
重型导出影响数据卷和服务的事故类别。

Phase 2 的生产复核证据、已知异常小时、一次性文件清理对象和精确 rollout/rollback 步骤只由
[`design-qa-phase2-archive-closeout.md`](design-qa-phase2-archive-closeout.md) 维护。本文只保留由该复核
沉淀出的长期契约：timer/operator 共用唯一 runner、前向补偿不恢复历史 backfill、归档与小时边界分阶段运行、
健康判断必须关联 systemd、host receipt、数据库 heartbeat 和 control/catalog 事实，且共享 EC2 role 不得被表述为进程级隔离。

## 4. 单一 QA Policy

唯一数值 owner：`ops/qa/policy.yaml`。字段含义与 prod/edge 目标值以该文件为准。
Deploy 注入默认值与 rollout 门禁见 `ops/qa/deploy_rollout.yaml`；目录索引见 `ops/qa/README.md`。
Preflight `scripts/checks/qa-lifecycle-ssot.py` 机械校验 policy 结构、deploy rollout 与
本文语义锚点；禁止在其它文件复制 policy 数值副本。

数值契约只在 policy 中定义；脚本结构化读取 policy，并机械校验 CloudFormation/bootstrap、
archive maintenance、hourly boundary 与 Edge 退役状态。production archive owner 为
`tokenkey-qa-maintenance.timer`，分区生命周期 owner 为 `tokenkey-qa-boundary.timer`；
legacy `tokenkey-qa-stale-cleanup.timer` 只允许作为 finalize 前的排空工具。不得再从
`QaStaleRetentionDays`、`TOKENKEY_QA_STALE_RETENTION_DAYS` 或 `qa_capture.retention_days`
派生数值。历史 backfill flag、selector 和脚本必须不存在，不得恢复第二套 archive 或 retention owner。

Policy 同时固定 maintenance runner UID/GID、host/container scratch、host receipt 和单轮最大补偿窗口，
以及 boundary schedule、未来覆盖范围、boundary receipt 与 host export tmp 路径。这些是运行时契约，
不是可由 unit、operator 或 bootstrap 各自覆盖的建议值。生产 cutover 小时属于数据库不可变控制状态，
不写入静态 policy。

## 5. Prod 在线捕获

### 5.1 捕获范围

prod 显式启用 QA middleware，覆盖现有已认证 gateway surface。它继续捕获并脱敏：

- 客户端 request body；
- 实际 upstream request body（发生网关改写时）；
- response body 与 headers；
- SSE chunks；
- model/platform/account/channel、状态、耗时和 usage；
- trajectory/synth 元数据；
- request/response SHA-256。

multipart 等现有省略规则和 body 上限继续生效。普通请求不因 archive、cleanup 或用户导出同步等待。

### 5.2 evidence 写入

主路径仍是：

```text
capture -> redact/build zstd evidence -> qa_blobs -> qa_records
```

主 BlobStore 失败时：

```text
qa_blobs Put 失败 -> qa_dlq -> capture_status=captured_dlq -> blob_uri=dlq://...
```

`qa_dlq` 只是 evidence 降级落盘，不是业务重试队列。新归档器必须原生读取 `file://` 与
`dlq://`，避免当前 trajectory export 将 `dlq://` 视为 unsupported 的缺口。

cutover 前记录沿用兼容值 `created_at+24h`；cutover 后记录的 `retention_until` 固定为所属 UTC 小时
上界再加 24 小时，与整分区 DROP 时刻一致。DROP 资格只由数据库时钟和 PostgreSQL catalog 中的真实
分区上界决定；任何调用方不得改读 `retention_until` 或解析表名形成第二套 retention owner。

### 5.3 时间分桶

新 evidence key 在现有日期/request-prefix 结构中加入 UTC 小时：

```text
qa_blobs/YYYY/MM/DD/HH/<request-prefix>/<request-id>.json.zst
```

`qa_dlq` 同样按 UTC 小时分桶：

```text
qa_dlq/YYYY/MM/DD/HH/<safe-request-id>.json.zst
```

小时目录允许维护任务只处理一个封口窗口，不再扫描整个树。request ID 的 path-safe 规则继续由
shared trajectory writer 持有。

旧布局由archiver/cleanup兼容读取；不批量移动百万文件。历史backfill入口已删除，旧文件只按
固定年龄规则分批清理。

## 6. 严格 24 小时在线语义

### 6.1 查询层硬边界

所有用户和管理端常规 QA 查询必须加入：

```sql
created_at >= now() - interval '24 hours'
```

包括 per-key trajectory export、管理端 QA 查询和线上 request-id 关联入口。per-key 导出不再有
“全部 retained history”例外。普通 `/users/me/qa/export` 与 synth-session self-export 不属于产品 surface。

逻辑窗口严格为 24 小时，不依赖物理 cleanup 的执行时刻。即使过期行因 crash 暂留 DB，也不能被
普通接口返回。

### 6.2 `retention_until`

cutover 后的新记录固定写入：

```text
retention_until = UTC hour upper bound + 24h
```

boundary 只以数据库小时锚点和 direct child 的真实 catalog 上界选择到期分区；`retention_until` 只描述
该记录随整分区 DROP 的时间，不参与删除资格判断。archive 状态不延长热数据生命周期。

### 6.3 物理窗口

`tokenkey-qa-boundary` 在每小时整点运行且没有 randomized delay。正常情况下，上一批 24 小时边界内
的记录在其小时子表到期时整表 DROP，因此物理保留为 24 至 25 小时；调度延迟、到期分区 backlog 或
任何 overdue attached partition 都由 boundary 健康信号直接暴露。用户可见窗口始终严格为 24 小时。

## 7. 统一每小时 Maintenance

### 7.1 唯一 owner

建立一个 `qa-maintenance` 程序和一个 regular timer：

```text
tokenkey-qa-maintenance.timer
OnCalendar=*-*-* *:15:00
Persistent=true
```

它是唯一 archive owner，不执行物理删除。`HH:15` 给上一小时异步 capture 15 分钟 seal delay；
分区供给和物理 DROP 由 `HH:00` boundary owner 执行。archive 最长运行 40 分钟，避免跨越下一轮 boundary。

systemd timer 和人工 operator 都必须调用 host 上同一个 repo-owned runner；operator 禁止直接
`docker exec` 应用容器。runner 解析 active container 和其 immutable image，使用与 timer 完全相同的
network、mount、环境过滤和命令启动一次性 sibling container，并固定以 UID/GID `1000:1000` 运行。
真实 scratch 映射固定为：

```text
host      /var/lib/tokenkey/app/qa_archive_tmp
container /app/data/qa_archive_tmp
```

`--selftest` 必须解析 live bind mount，并用同 image、同 UID/GID、同 mount 在 container 路径完成
create/read/delete probe，同时从 host 路径核对结果；只检查 `logger` 命令存在不算 self-test。

systemd service 的资源保护基线：

```ini
Nice=15
IOSchedulingClass=idle
CPUQuota=20%
MemoryMax=1G
```

维护程序单 worker、流式读取/写入、数据库 keyset pagination、受控 S3 multipart 并发，禁止把
整小时 evidence 或 ZIP 放进内存。如果一次运行跨过下一小时，systemd 不并发启动第二实例；
PostgreSQL advisory lock 作为跨入口的第二层互斥。

### 7.2 Regular 状态机

每次计划运行固定执行：

```text
preflight/lock
  -> 检查 policy hash、runner UID/mount/scratch 和唯一 forward_cutover
  -> 归档本轮上一完整 UTC 小时（normal window）
  -> normal 失败：记录失败、写 host receipt、非零退出，不执行补偿
  -> normal committed + restore-verified：
       按 UTC 时间轴选择 forward_cutover 之后、normal window 之前最老的可重试未完成小时
       最多补偿一个窗口；无缺口则跳过
  -> 补偿失败：保留机器可读失败状态、写 host receipt、非零退出
  -> 收敛archive partial/tmp
  -> 写 DB heartbeat、host receipt、maintenance ledger 和 metrics
  -> release lock
```

`qa_archive_shards` 中唯一 `forward_cutover=true` 的 committed 且 restore-verified shard 是前向边界。
精确生产锚点和首次补偿窗口由 Phase 2 closeout 文档唯一维护。补偿集合不包含 cutover 及其之前的
历史缺口，不恢复任意窗口 selector、历史 backfill flag 或第二个脚本。时间轴中不可恢复的 terminal
failed 小时持续进入 archive health，但不阻塞后续可重试小时。archive 与 boundary 通过共享锁串行。普通失败
不会阻塞 gateway，也不会触发密集通知。

### 7.3 Crash 可恢复

每个阶段写持久化 checkpoint；所有动作幂等：

- archive 对象先写 immutable generation，再最后提交 commit manifest；
- `forward_cutover` 通过唯一部分索引和行级有效性约束保证全库只有一个 committed +
  restore-verified marker；只允许幂等设置已批准的精确锚点，不存在 move/unset 入口；
- 进程在任一步 crash，下一轮根据 control row、segment membership 和 S3 commit 收敛；
- host runner 在同目录临时文件完成 `fsync` 后 rename，原子写
  `/var/lib/tokenkey/qa-maintenance-last-run.json`，即使 child 在写 DB heartbeat 前失败也留下机器证据；
- boundary 使用独立 DB heartbeat 和原子 host receipt；partition DROP 与 `source_dropped_at` 同事务，
  hot-file cleanup 失败由 `hot_files_cleaned_at`/`hot_cleanup_error` 幂等续跑；maintenance receipt 永不授权删除。

## 8. S3 Raw Archive

### 8.1 独立安全域

raw archive 使用独立 bucket，例如：

```text
tokenkey-prod-qa-raw-archive-<account>
```

不得与用户 trajectory export bucket 共用权限边界。基线：

- S3 Block Public Access 全开；
- SSE-KMS；
- 不启用 versioning，避免生命周期删除后旧版本继续存在；
- S3 Gateway VPC Endpoint；
- prod instance role 对目标 shard artifact/manifest/commit 允许精确 Put 和必要 multipart；不授予
  `s3:ListBucket`，不授予 `raw/partial/` 读取；`s3:GetObject` 只覆盖维护实际回读校验需要的
  `commit.json`、`manifest.json`、`records.parquet`、`evidence.pack` 和
  `evidence-index.jsonl.zst`、`orphan-evidence-index.jsonl.zst` 对象后缀；
- Export Worker role 只读 raw archive、只写用户 export prefix；
- ops recovery role 允许受审计 Get/List；
- 普通用户和普通 API key 无 raw archive 权限；
- CloudTrail Data Events 记录 raw archive 读取；
- complete 对象 7 天过期，partial prefix 1 天过期；
- prod API 不为 raw archive 生成用户 presigned URL。

Phase 2 的 Stage0 gateway 和 maintenance 仍运行在同一 EC2 instance role 下，因此上述 bucket/KMS
policy 只能收窄整机 principal 的动作，不能构成进程级 IAM 隔离。文档、验收和安全结论必须明确这个
过渡边界：gateway 进程理论上可取得同一 instance credentials。真正的进程级隔离需要把 maintenance
迁移到独立 task/instance role 或增加受控 credential broker，不属于本次生产完整性收口。

### 8.2 小时 shard 与 append-only segment

每个 UTC 小时一个逻辑 shard，由一个 base segment 和零个或多个 late delta segment 组成：

```text
raw/v1/date=YYYY-MM-DD/hour=HH/segments/<segment-id>/
  records.parquet
  evidence.pack
  evidence-index.jsonl.zst
  orphan-evidence-index.jsonl.zst
  manifest.json
raw/v1/date=YYYY-MM-DD/hour=HH/commit.json
```

- `records.parquet` 保存 QA 元数据和 user/API-key 可过滤列；
- `evidence.pack` 顺序拼接现有 `.json.zst` payload，不重复压缩；
- `evidence-index` 保存 request/record、pack offset、length、SHA-256 和原 URI scheme；
- `orphan-evidence-index` 保存小时目录内无 DB 引用的 evidence；它们可供 ops 取证，但因缺少可靠
  ownership metadata，不进入用户导出；
- `manifest.json` 保存窗口、schema、segment kind、行数、引用数、存在/缺失/损坏/orphan 数、
  字节和工件 checksum；
- `commit.json` 最后写入，列出该小时全部已验证 immutable segments 及集合 checksum，是该小时
  唯一读者 commit marker。

首次归档生成 base segment。之后发现迟到记录只新增 delta segment，不复制或重写旧 segment；
commit 使用 S3 conditional write（ETag/前置条件）做 compare-and-swap，冲突时重新读取 commit 并合并，
不得靠 read-then-unconditional-overwrite。S3 的强一致读写使读者在 commit 更新后读取确定集合。
失败 segment 不会进入 commit，并由 partial lifecycle 或后续维护收敛。

### 8.3 迟到记录与最终核对

`HH:15` 首先处理上一完整小时并在读取源数据前确保 control row。处于 retention 内的零记录 normal
window 也必须生成可 restore-verify 的零行 base commit，不能把零流量表示成没有事实。只有该 normal
window committed 且 restore-verified，本轮才按 UTC 小时序列枚举 `forward_cutover` 与 normal window
之间的窗口；候选不能只从仍有源行或已有 control row 的集合反推。

每轮最多选择最老的一个可重试未完成小时：缺少 control 且仍在 retention 内、control 为非终态/可重试
failed，或 committed + restore-verified 但存在尚未进入 verified/committed segment membership 的源
identity。对已有 commit 的小时，reconcile 做 source-minus-committed-membership 差集，只为新增迟到
记录创建 immutable delta segment，再原子更新 commit；差集收敛后不重复创建 delta。处于 retention
内且确认为零记录的小时创建零行 base。若时间轴上的小时首次被发现时既无 control/源行又已经过
retention cutoff，必须创建 `failed/source_unavailable_after_retention` control；已有但尚未 committed 的
可重试小时到达同一条件时也必须转为 terminal failed，不得伪造空 commit 或成功。该 terminal failure
保持可见但不自动重试，以免饿死后续小时。normal 或补偿任一失败都保留机器可读状态并使本轮非零退出。

archive完整性判断使用final commit segment集合，不把首次base segment当作最终完整性事实。
归档器处理所有用户和API key，禁止用`traj_export_enabled`过滤archive输入。该判断不参与
独立年龄cleanup。

### 8.4 归档缺口

如果某小时archive仍无法完整提交，shard保持`failed`并记录机器可读原因，不得标记complete。
独立年龄cleanup仍按24小时规则删除本地数据；archive health继续报告历史不可完整恢复，不自动
创建confirmed-gap或把失败转成成功。即使失败发生在创建 control 前，cutover 后的小时也由 UTC
时间轴重新发现；源数据已过期时落为`source_unavailable_after_retention`，不能静默消失。

服务与磁盘安全高于冷归档完整性；S3 故障不能使 prod 本地 QA 无限增长。

### 8.5 四类存储与备份边界

| 存储面 | 内容 | 生命周期 owner | QA 历史恢复用途 |
|---|---|---|---|
| 独立 QA raw archive bucket | 全部 prod 用户/API key 的脱敏 records/evidence 小时 shard | 本文 policy；7 天 | **唯一** QA 历史恢复源 |
| `QaExportsBucket` / `traj-exports/` | 用户请求生成的 user/key trajectory ZIP artifact | 用户导出 artifact policy；短 TTL | 否；不是 raw archive，不能反推全量 QA |
| generic data-layer archive bucket | usage/ops archive batch、manifest、promote/cleanup ledger | data-layer approved baselines | 否；工具与 manifest 必须拒绝 QA dataset |
| PostgreSQL `pgdump` bucket | 核心 schema/数据备份；`qa_records*` table data 明确排除 | pgdump backup policy | 否；结构可恢复不等于 QA 数据可恢复 |

四者 bucket/prefix、KMS/IAM、Lifecycle 和 operator role 不得混用。不得把用户 ZIP bucket或通用
usage/ops archive bucket 改名后当作 raw QA archive，也不得把 pgdump 中保留的 QA schema 误报为 QA
历史备份。Phase 2 raw restore 验证前，过渡期 prod QA break-glass dump 工具仅作为人工只读 break-glass；它不
定义 lifecycle、不得定时运行、不得作为删除证据，验证通过后必须退役（**已 shipped 2026-08-10**）。

## 9. Hourly Boundary 设计

### 9.1 分区供给与整分区 DROP

`tokenkey-qa-boundary` 是唯一热数据生命周期 owner。每轮在共享 QA advisory lock 下读取数据库 UTC 小时，
确保 `[current,current+72h)` 的每一个规范小时范围由恰好一个 direct child 覆盖，并枚举 catalog 上界不晚于
`date_trunc('hour', database_now())-24h` 的规范小时子表。表名只作佐证；DEFAULT、未知布局、错位/重叠边界、
覆盖缺口或 overdue partition 都 fail closed。未来分区供给是本轮 DROP 的硬前置条件：供给或覆盖核验失败时
本轮立即退出，不检查 archive control、不 DROP，也不继续文件清理。

每个到期小时在一个事务内完成：先对目标 direct child 取得 `ACCESS EXCLUSIVE` 锁，再重读 catalog 并确认
parent、规范表名和上下界仍与枚举事实一致；随后检查 archive control 与剩余 source membership；需要时把该小时持久化为
terminal `source_unavailable_after_retention`；DROP 整个 child；在同一 shard control 记录
`source_partition_name/source_dropped_at`。archive 已失败或不完整不会延长 24 至 25 小时热保留，但 terminal
事实写入失败时 DROP 同事务回滚，禁止小时从控制时间轴静默消失。稳态没有逐行 DELETE。

### 9.2 Blob/DLQ 与 export scratch

数据库事务提交后，boundary 只删除同一 `window_start` 对应的
`qa_blobs/YYYY/MM/DD/HH` 与 `qa_dlq/YYYY/MM/DD/HH` 目录。路径必须经过 exact-hour containment、祖先
symlink 拒绝和规范化校验；目录不存在视为幂等成功。成功记录 `hot_files_cleaned_at`，失败记录经截断脱敏的
`hot_cleanup_error`，后续 boundary 从 control state 有界续跑，不扫描或删除其它小时。

`qa_exports_tmp` 不能按分区处理，仍由同一个 boundary host runner 管理。它从 active container 的真实配置
解析 effective path：未配置时 container 为 `/app/data/qa_exports_tmp`、host 为
`/var/lib/tokenkey/app/qa_exports_tmp`。scheduled cleanup 只选择超过 retention boundary、regular file 且
没有打开句柄的 crash orphan；每轮先生成 plan，再以同一 cutoff、精确 plan hash 和 expected count apply，
并校验删除 receipt。首次纳管现存 orphan 仍需 activation marker。`qa_export_jobs` 只诊断，不删除历史行，
不新增 job retention 语义。

### 9.3 Cutover 期间的 legacy cleanup

旧 DEFAULT/monthly rows 与旧日期目录不复制、不移动、不 rehome。`tokenkey-qa-stale-cleanup` 只在未来 T0
激活后的排空窗口继续按已批准旧逻辑处理这些 legacy 数据；新 boundary timer 此时保持 disabled。finalize
必须在至少 25 小时后重新确认 DEFAULT 仍存在且为空、不存在非空或未知布局 legacy child、legacy file 与
export orphan 均已排空、未来小时覆盖完整、archive host receipt 与 DB heartbeat 新鲜成功，并消费同 T0 的
activate receipt。仍附着的空 legacy monthly child 以精确 schema/name/bounds 纳入 finalize plan hash；apply 在父表锁和
同一事务内复核清单完整、边界不变且仍为零行，再与空 DEFAULT 一并 DROP。任何缺失、额外、改界或重新非空均在
首个 DROP 前失败。数据库 insert trigger 也拒绝
任何没有同 T0 activate 的 finalize，避免应用、operator 或手工 SQL 绕过。finalize receipt 落库后，
owner-switch 自动禁用 legacy timer 并启用无随机延迟的 boundary timer。durable finalize 后
legacy 已永久关闭；切换失败时必须继续保持 legacy disabled、best-effort 保持 boundary enabled，
并以非零退出硬告警，禁止制造一个实际必然失败的 legacy fallback。

### 9.4 用户主动删除不在本期范围

产品没有用户主动删除 QA 的 UI、HTTP API 或 service capability。本设计不引入 deletion tombstone、
跨系统撤销或 raw shard rewrite。

本期 QA 删除来源只有：

- boundary 按统一 24 至 25 小时物理生命周期执行整小时分区 DROP；
- disk emergency 按第 10 节执行 QA-only 提前清理；
- 经独立人工批准的受控运维流程。

未来若正式提出用户主动删除需求，必须另行设计 UI/API、授权、导出 artifact 撤销以及 S3 归档隐私
语义，不得把本设计解读为已批准该产品能力。

## 10. 磁盘 P0 与 Emergency

### 10.1 触发

保留现有独立于 Docker 的短周期磁盘采样，但它不再发送 warning。只有 prod 数据卷满足任一条件时
请求 emergency：

```text
used_percent >= 80
free_space < 10 GiB
```

磁盘采样器写入 durable emergency request，并启动同一 `qa-maintenance` 程序的 emergency mode。
Regular 和 emergency 共用 advisory lock/host lock；正在归档的 regular 任务在分页/分块边界检查
request，停止非必要 archive 并转入 emergency，避免并发争抢磁盘。

### 10.2 必须动作

Emergency 顺序固定：

1. 删除所有已超过 24 小时的 QA；
2. 删除 archive partial、export tmp、orphan 和旧 quarantine；
3. 若仍未恢复，删除 24 小时窗口内最旧且已成功归档的 QA；
4. 若仍未恢复，删除最旧未归档 QA，并写 archive/online gap；
5. 持续到数据卷低于 70%；
6. 如果可删 QA 已耗尽仍无法恢复，停止破坏性动作并要求人工扩容/排查其他数据；绝不碰 billing、usage、账号或其他核心表。

按已批准决策，系统不自动暂停 QA capture。

### 10.3 只保留 P0 通知，并设单一通知 owner

只在以下不可逆动作发生或必须动作无法启动时发送通知：

- emergency 已开始执行提前删除或其他磁盘保护动作；
- 到期 shard 未归档但已按生命周期删除，形成 confirmed archive gap；
- 数据卷已进入 emergency 条件，但 maintenance 在限定时间内无法获得执行权或启动，因而必须人工
  介入。

`qa-maintenance` 的 event/receipt ID 是通知去重 owner。on-box sampler 和 CloudWatch 只负责写入/
触发同一个 durable emergency request，不分别发送飞书消息；maintenance 完成、失败或确认无法启动后，
由唯一 delivery path 针对 event ID 最多发送一张结果卡：

```text
reason / before / actions / after / online_gap / archive_gap / next_manual_action
```

归档临时失败、archive lag、cleanup lag、单个 missing Blob、`qa_dlq` 写入和普通磁盘水位仅写
metrics、maintenance ledger 与 daily diagnostics，不主动推送。这样告警必然意味着系统已经做了
必须审计的动作，或本应执行的动作无法执行，而不会由多个监控面重复刷屏。

## 11. 用户 API-key 导出

### 11.1 禁止 prod 重型路径

只有 `users.traj_export_enabled=true` 的用户才能看到和使用 API key trajectory 导出。用户点击导出时，
prod 只做：

- JWT、user、`traj_export_enabled` 和 API-key ownership/platform 权限校验；
- 固化导出窗口与 latest complete archive watermark；
- 创建轻量 export job；
- 返回 job ID；
- 轮询独立 job store 并在完成后返回 presigned URL。

prod 明确禁止：

- 从本地 `qa_blobs` 大批读取；
- 构建大型 ZIP；
- 大规模 trajectory 投影；
- 将下载文件通过 gateway 代理给用户；
- 在 S3 数据尚未封口时 fallback 回源 prod 补最后一段。

### 11.2 Off-prod Export Worker

推荐控制面：

```text
prod API -> DynamoDB export job + SQS/EventBridge Pipes
         -> ECS Fargate one-job worker
         -> raw S3 Range GET
         -> trajectory projection + ZIP
         -> user export S3
         -> DynamoDB done/failed
         -> prod 返回短期 presigned URL
```

队列不能由用户直接写。job 只携带或引用经过 prod 授权固化的 `user_id`、`api_key_id`、窗口和格式；
Worker 不接受任意跨用户 S3 路径。Worker 先读每小时 `records.parquet` 过滤该 user/key，再按
`evidence-index` 对 `evidence.pack` 发 Range GET，只下载必要 payload。

`traj_export_enabled` 是用户 trajectory 导出的 capability gate，而不是 capture/archive selector。该字段
保持管理员在用户编辑 UI 授权、默认 false 的现有产品语义：

- `false`：API key 卡片不显示导出按钮/面板；手工调用 enqueue/list/get/download API 必须服务端拒绝；
- `true`：仅对该用户拥有且平台可投影的 API key 展示入口，并允许创建和读取该用户自己的 jobs；
- 从 `true` 改为 `false` 后，UI 立即隐藏入口，prod API 不再创建 job、返回 job 状态或签发新的下载 URL；
- 已合法入队的 Worker 可以离线完成并等待 artifact lifecycle 清理，Worker 不访问 prod 用户库；
- 已经签发的 presigned URL 在自身短 TTL 内仍可能有效；该开关是功能授权门禁，不是即时撤销机制；
- 无论开关值为何，该用户的 QA 都继续被 prod 全量捕获、归档和按统一生命周期清理。

用户浏览器从 S3 直接下载。用户 export bucket/prefix 与 raw archive 分权，生命周期独立且较短。

### 11.3 新鲜度语义

用户导出以 latest complete archive watermark 为截止时间。每小时 `HH:15` 归档上一小时，因此
正常水位落后约 15–75 分钟。UI/API 必须返回并展示：

```text
data_from / data_until / archive_watermark
```

导出窗口是 watermark 之前的 24 小时。用户可等待下一小时重新导出，但不能切回 prod 重型路径。
在线 QA 查询仍可覆盖严格的当前过去 24 小时；只有大文件 trajectory 导出采用归档水位。

## 12. 运维从 S3 本机排障

提供 repo-owned CLI：

```text
qa-archive inspect
qa-archive verify
qa-archive restore --window ... --destination ... --confirm-private-data
qa-archive fetch --request-id ...
qa-archive fetch --since ... --until ... --user-id ... --api-key-id ...
```

执行路径：

```text
ops workstation -> assume ops recovery role -> S3 -> local isolated workspace
```

不调用 prod API、prod PostgreSQL、prod 数据盘、prod CPU/内存或 prod 网络。工具必须：

- 先读取 commit/manifest 并校验 checksum；
- 默认只输出元数据与脱敏摘要；
- 查看/落盘正文需要显式隐私确认参数；
- 本地目录权限收紧并带 TTL cleanup；
- 记录 CloudTrail S3 Data Events；
- 不自动写回 prod DB；
- 支持完全离线的 evidence decode 和 trajectory projection。

工具必须能在 ops workstation assume recovery role 后直接定位指定 window 的 `commit.json`，不依赖
prod host 代为列举或下载。用该入口完成至少一个 committed shard 的独立 verify/restore 验收后，退役
过渡期 prod QA break-glass dump 工具；在此之前它仅是人工只读 break-glass，不能被 timer 或 cleanup 调用。

## 13. Edge 退出 QA

所有 deployable Edge 显式设置：

```text
QA_CAPTURE_ENABLED=false
QA_ARCHIVE_ENABLED=false
```

并保证：

- QA middleware 不读取/tee request 与 response；
- 不新增 `qa_records`；
- 不写 `qa_blobs/qa_dlq/qa_exports_tmp`；
- 不安装或运行 QA maintenance/boundary/legacy-cleanup timer；
- 不配置 raw archive/export S3；
- Edge instance role 无 QA S3 权限；
- Edge deploy smoke 断言 capture disabled；
- Edge host-unit sync 只保留 disk/memory、Docker/GHCR 等非 QA 能力。

QA schema 可暂留，避免不必要的 prod/edge migration 分叉，但数据必须保持空。

切换流程：先部署 `capture=false`，连续观察至少一小时确认行数和文件数不再增长，再一次性清空 Edge
存量 `qa_records/qa_blobs/qa_dlq/qa_exports_tmp`，最后 disable/remove 旧 QA timer。任何一步失败都不
影响 Edge gateway 主路径，且不得误删 ops、usage 或账号数据。

## 14. 数据模型与控制状态

### 14.1 Archive shard control

新增 control 表或等价持久化实体，至少记录：

```text
window_start / window_end / generation / state
record_count / blob_ref_count / blob_present / blob_missing
logical_bytes / artifact_bytes / checksums
s3_prefix / manifest_key / commit_key
first_attempt_at / completed_at / last_error
forward_cutover / verified_at / restore_verified_at
```

状态只允许：

```text
pending -> writing -> verified -> committed
                    \-> failed -> later bounded forward compensation
expired_unarchived -> gap_recorded
```

只有 `committed` generation 可被 Export Worker 和 ops 工具读取。

`forward_cutover` 为非空默认 false 的 boolean，并由 `WHERE forward_cutover` 唯一部分索引保证全库最多
一个 true；行级约束要求 true 只能出现在 committed 且 `restore_verified_at IS NOT NULL` 的 shard。
应用选择器和设置命令再次校验。设置命令只接受 Phase 2 closeout 批准的精确锚点，重复设置同一锚点
幂等，不提供 move/unset。它只界定自动补偿下界，不代表 cutover 之前所有历史小时完整，也不授权删除。

cutover 后每个已封口小时最终都必须有 durable control：成功状态是独立 restore 通过的 commit（包括
在 retention 内确认的零行 base），失败状态至少包含机器错误码。`source_unavailable_after_retention`
表示未 committed 小时被时间轴处理时已经没有可证明完整的源数据；它覆盖无既存 control 和已有
retryable control 但源数据随后过期两种情况，是 terminal failed，不是空归档、confirmed gap 或删除授权。
缺少 `ListBucket` 时，缺失 key 的 `GetObject` 403 只能证明 commit 存在性未知；在 conditional create 或
后续成功读取完成消歧前，不得把过期零行源误判为 `source_unavailable_after_retention`。该状态以
`commit_existence_unknown` 保持可重试，通用失败写入不得把已有 committed shard 降级。
该可重试状态不延长热保留：boundary 仍按 catalog 上界 DROP 到期小时并记录
`source_partition_name/source_dropped_at`，但不得把 control 改写成 terminal gap。

### 14.2 Boundary lifecycle control 与 receipt

不新建第二套 cleanup/gap 表。每个小时继续由 `qa_archive_shards` 唯一承载 archive outcome 与热源移除状态：

```text
source_partition_name / source_dropped_at
hot_files_cleaned_at / hot_cleanup_error
verification_error_code=source_unavailable_after_retention
```

应用 boundary heartbeat 记录 run ID、trigger、数据库 UTC anchor、未来覆盖、DROP/cleanup 结果和失败码；
host runner 原子写 `/var/lib/tokenkey/qa-boundary-last-run.json`，再与 systemd、DB heartbeat、catalog/control
交叉核对。archive receipt 始终 `deletion_authorized=false`，不能替代 boundary 删除事实。

### 14.3 Cutover phase receipt

`qa_lifecycle_receipts` 只允许 append-only `activate` 与 `finalize` 两行，绑定 `phase/plan_hash/t0_utc/applied_at`。
同 phase、同 hash、同 T0 的重放幂等成功；任何不同事实都 fail closed。finalize 必须消费同 T0 的 activate
receipt；owner switch 只在 durable finalize receipt 存在后关闭 legacy cleanup。

### 14.4 Export authorization snapshot

export job 记录必须包含授权时的服务端判定：

```text
user_id / api_key_id / traj_export_authorized=true
platform / window / requested_at
```

prod job creator 必须从服务端用户事实读取 `traj_export_enabled` 并校验 key ownership/platform，不能信任
客户端字段。Worker 只消费已授权、不可由用户直接写入的 job，不读取 prod 用户库；当前开关由 prod
在后续 list/get/download 请求再次校验。

## 15. 可观测性与静默健康信号

下列信号保留但不主动通知：

- capture submitted/persisted/failed、queue depth、sync fallback、captured_dlq；
- latest committed hour、archive lag、shard rows/bytes、duration/retry；
- missing/corrupt Blob、partial generation；
- boundary database anchor、未来小时覆盖、到期/overdue partition、DROP 小时、hot cleanup backlog 和 duration；
- policy hash、timer state、Edge capture disabled；
- S3 lifecycle 与超过 8 天对象检查。

它们进入 maintenance ledger、Admin/Ops 和 daily diagnostics。主动通知只遵守第 10.3 节 P0 契约。

host runner 额外原子写 `/var/lib/tokenkey/qa-maintenance-last-run.json`，至少包含 schema version、run ID、
trigger、started/finished time、active container/image、runner UID/GID、normal window/result、可选 catchup
window/result、child/runner exit code 和 redacted error code。systemd unit 启动但 child 未进入应用时，DB
heartbeat 不会更新，因此 host receipt 是该失败区间的 owner；receipt 不能反过来伪造 archive 成功。

maintenance 健康判断必须同时消费并交叉核对四类事实：systemd timer/service enabled/active/last result、
host receipt、新旧 DB heartbeat、对应 `qa_archive_shards`/segments/control rows。只有时间和 run/window
关联一致、normal window committed + restore-verified、且本轮要求的 catchup 没有失败时才报告健康。
缺 receipt、receipt stale、systemd success 但 DB 无对应进展、DB heartbeat 未记录 child 失败、或 control
row 与 receipt 冲突都必须报告明确的 degraded/failure 原因，不能沿用最后一次成功 heartbeat 报绿。
cutover 后任何未处置的 terminal failed 小时都使 archive health 保持 degraded，即使后续 normal 成功。

boundary 健康同样交叉核对 `tokenkey-qa-boundary` systemd、原子 host receipt、`qa-boundary` DB heartbeat 和
`qa_records` catalog/control。稳态出现 DEFAULT、当前/未来小时覆盖缺口、overdue attached partition、DROP 后
hot-file backlog、未知终态码或四方矛盾都必须 failed 且 CLI 非零；只有四方一致的
`source_unavailable_after_retention` 历史事实可以 degraded。

## 16. 安全与隐私边界

- raw archive 不公开，不提供普通用户 presigned URL；
- 用户只能下载 Worker 生成的 user/key scoped trajectory ZIP；
- Worker job spec 由 prod 授权固化，不能由客户端构造 S3 范围；
- `traj_export_enabled` 在 UI 和服务端双层门禁，关闭后禁止 trajectory job/list/poll/download；
- raw bucket 与 export bucket 分离 policy；Export Worker 和 ops recovery 使用独立 role。当前 Stage0
  prod archiver 与 gateway 共享 EC2 instance role，只能按第 8.1 节收窄整机权限，不能宣称进程隔离；
- S3/KMS key policy 拒绝跨边界读取；
- secrets 不进入命令行或 systemd journal；
- manifest、ledger 和 P0 不包含 request/response body、token、cookie 或 API key；
- S3 raw archive 配置 7 天 expiration policy；S3 异步物理删除可能略晚，由 lifecycle 收敛；
- ops 本机恢复默认脱敏、小输出、隔离目录和显式正文确认。

## 17. 失败处理与回滚

### 17.1 常规失败

- archive normal window 临时失败：写 DB/host 失败状态并非零退出，本轮不补偿；后续某轮 normal 成功后，
  由每轮最多一个窗口的正式补偿选择器收敛，不推送；
- archive 在 control 创建前失败：后续按 cutover 后 UTC 时间轴重新发现；若发现时源数据已过期，写
  terminal `source_unavailable_after_retention` 并持续报 degraded，不伪造零行成功；
- partition DROP 前失败：事务回滚并保留 child；DROP 后 hot-file cleanup 失败：下一轮按 shard control 幂等续跑；
- Export Worker 失败：job 标记 failed，用户重试；不得 fallback 到 prod；
- ops fetch 损坏：checksum fail closed，不输出不可信证据。

### 17.2 部署回滚

新链路分阶段激活：

- archive-only 可独立关闭，不改变现有本地 retention；
- off-prod export 上线前仅暂留现有 API-key trajectory in-prod 实现；切换后若 Worker 故障只能返回暂不可用，不能回退重型 prod；
- finalize 前 legacy cleanup 继续排空旧布局；finalize 后只有 boundary 按 catalog 小时上界执行整分区 DROP，
  不以 archive 成功延长热保留；
- Edge capture=false 可通过配置回滚，但不自动恢复已删除 Edge QA；
- emergency mode 可关闭自动动作并回到人工处置，但 P0 disk monitor 必须保留。

Phase 2 运行时回滚固定为先停 maintenance timer，再回滚应用/runner。不得回滚或删除 additive schema、
唯一 `forward_cutover`、已验证 S3 segment/commit 或 host receipt；不得把补偿失败或 terminal failure
伪造为成功。旧 image 可以忽略新字段，但下一次重新启用前必须重新通过
selftest、cutover/receipt/DB/control 联合健康检查。

任何 generic image rollback 都不得用旧 retention 配置覆盖更晚的 live policy；policy 由独立 deploy owner
同步并做 hash audit。

## 18. 上线阶段与审批门禁

### Phase 1：Edge 退出 QA

1. 部署 Edge `QA_CAPTURE_ENABLED=false`；
2. 验证 QA 行/文件不再增长；
3. 清理 Edge 存量；
4. 删除 QA timer/config/S3 权限；
5. 复核 gateway/OAuth、磁盘和内存。

### Phase 2：Prod raw archive-only

Phase 2 的生产 inventory、精确修复窗口、执行顺序和回滚步骤只由
[`design-qa-phase2-archive-closeout.md`](design-qa-phase2-archive-closeout.md) 维护。本节只定义稳定退出条件：

1. 专用 raw bucket、KMS、VPC endpoint、recovery role 和 CloudTrail audit 边界部署完成，S3/KMS
   授权一致，并按第 8.1 节收窄 app role；
2. timer/operator 共用唯一 host runner，真实 bind mount 上的固定 UID/GID selftest、host receipt 和
   DB/control 联合健康检查通过；
3. 唯一批准 cutover 已独立 restore-verify；其后的每个已封口小时都有可恢复 commit 或明确 terminal
   failure，正式补偿不得伪造成功；
4. ops workstation 可经 recovery role 直接从 S3 restore，随后删除 prod-host break-glass 下载脚本；
5. maintenance timer 达到 closeout 规定的连续健康标准，且 legacy stale cleanup 在 finalize 前保持运行；
6. archive canary 和 IAM rollback 证据完整后，deploy rollout 才可把 prod archive 默认值迁移到 policy
   target；历史 backfill 始终保持退休。

### Phase 3：Off-prod 用户导出

1. 创建 DynamoDB job、queue/Pipes、Fargate Worker 和用户 export prefix；
2. 迁移并保留 `traj_export_enabled` UI/服务端授权门禁；
3. 运行 user/key 隔离、安全负向和大体量导出测试；
4. 验证导出期间 prod CPU、内存、磁盘 I/O 和下载带宽没有明显变化；
5. 切换 UI/API，并禁止 prod fallback。

### Phase 4：24 小时在线层

1. 查询接口先硬过滤过去 24 小时；
2. 通过 hash-bound activate plan 选择未来精确 T0，建立 `[T0,T0+72h)` 小时子表并固化不可变 T0；
3. 新记录写入小时目录/子表，`retention_until` 为所属小时上界加 24 小时；旧 DEFAULT/monthly 数据原地排空；
4. 至少 25 小时后通过 hash-bound finalize plan 移除空 DEFAULT，持久化 finalize receipt，并原子切换到
   无 randomized delay 的 `*:00` boundary owner；
5. 验证当前加未来覆盖、至少一次真实 partition DROP、hot cleanup 恢复、DB latency、WAL、磁盘与用户导出。

### Phase 5：Emergency 与 P0

1. 接入 80%/10 GiB emergency request；
2. 演练 archive failure、到期 gap 和磁盘压力；
3. 验证只发最终 P0 卡片；
4. 验证不会误删非 QA 数据，也不会自动暂停 capture。

每个 phase 必须有单独 dry-run/验证证据。Phase 2 完成不自动授权 Phase 4 删除；Phase 4 与 Phase 5
生产启用均需要独立人工批准。

### 18.1 现状 owner → 唯一目标 owner → 退役门禁

本表是迁移期间唯一兼容清单。表外 QA lifecycle/archive/purge/export 方案一律视为冲突，不得新增。
“暂留”只表示避免在替代能力上线前制造保护真空，不授予旧实现继续定义数值、扩展功能或新增 caller。

| 当前文件/能力 | 当前角色 | 唯一目标 owner | 处理阶段与删除门禁 |
|---|---|---|---|
| `docs/qa-export-s3-and-auto-archive.md`、`docs/operator/qa-export-partner.md`、US-033 | 第二方案/旧产品契约 | 本文 | 本设计审批时删除，不保留历史副本 |
| `ops/prod/qa-export-and-purge.sh` | 全量导出后 `TRUNCATE`/删 Blob | `qa-maintenance` receipts + emergency | 本设计审批时立即删除；任何 Phase 都不得恢复 | <!-- script-ref-allow-missing -->
| 普通 `/users/me/qa/export`、`/users/me/qa/exports/*key`、`DeleteUserData` | 无 UI entitlement 的重型/半成品 surface | 仅 API-key trajectory export | 本设计审批时删除；synth 字段只保留 capture/project metadata |
| 应用内 daily auto-export、`ArchiveAuto`、`auto_export_enabled`、auto UI kind | 只覆盖 entitlement 用户的投影 | raw archive + Fargate export | 本设计审批时删除；不得作为 archive fallback |
| generic data-layer rehearsal/inventory 中的 `qa` dataset、`QA_RETENTION_DAYS`、Blob scan | usage/ops 之外的重复 QA owner | 本文专属 QA pipeline | 本设计审批时删除，并以负向测试拒绝重新引入 |
| Edge workflow/Lightsail bootstrap/host-unit sync/remediation 中的 QA capture/cleanup wiring | 现网兼容保护 | `edge.capture_enabled=false` 且无 QA unit/IAM | Phase 1 原子替换；先验证不再增长，再清存量并删除全部 wiring |
| prod QA break-glass dump（已退役） | 从 prod 打包的只读 break-glass | `qa-archive --workstation` | Phase 2 workstation S3 restore 验证通过后退役；之前不得定时或触发 purge |
| root `docker exec ... --qa-maintenance-once` operator 旁路 | 手工执行可绕过 timer 的 UID/mount/receipt 契约 | `/usr/local/bin/tokenkey-qa-maintenance.sh` 单一 host runner | production integrity closeout 时删除 operator caller；timer/operator 都走同一 runner |
| 当前应用内 trajectory worker、localfs/S3 ZIP 生成和 prod 下载代理 | 用户导出的临时实现 | DynamoDB/SQS/Fargate + direct S3 | Phase 3 原子切换；切换后删除 prod 重型 build/read/proxy 与相关 env 注入 |
| `tk_030` / Ent schema 中 daily auto-export、`auto` kind 的历史说明 | 已删除能力的注释残留；兼容列仍被旧数据读取 | manual job 兼容 schema + Phase 3 job model | 本次修正文案但不删列/历史 row、不新增 job retention；Phase 3 再迁移 schema 语义 |
| `QaExportsBucket` | 当前用户 trajectory ZIP artifact bucket | Phase 3 user artifact bucket/prefix | 可迁移或复用，但永远不是 raw archive；TTL/IAM 与 raw bucket 分离 |
| `/var/lib/tokenkey/app/qa_exports_tmp` crash orphan | in-prod export 的共享 EBS 临时文件 | `tokenkey-qa-boundary` host runner | 每轮 plan→exact-hash apply；首次对象受 activation marker 保护，Phase 3 切换后删除该 prod staging surface |
| `QaStaleRetentionDays`、retention env、daily/`:45` stale timer/script、`qa_capture.retention_days` | 已退休的可变 prod 物理清理与 cutover 排空工具 | policy 固定的 `*:00` `tokenkey-qa-boundary.timer` | legacy timer 只保留到 durable finalize receipt；owner switch 成功后禁用，稳态禁止逐行 DELETE |
| `tokenkey-pgdump.sh` 的 `qa_records*` data exclusion | 避免 pgdump 被 QA 撑爆的备份边界 | raw QA archive 负责 QA 历史 | 长期保留 exclusion，但注释必须指向本文；它不是 lifecycle owner |

Phase PR 必须同时删除该行所有 deploy、test、doc 和 generated fixture 引用；不得只关开关后留下可再次启用
的旧代码。迁移完成后 repository sentinel 应只允许本文、目标 policy/maintenance/worker 及明确的负向
契约测试出现 QA lifecycle 定义。

## 19. 测试与验收

### 19.1 确定性单元/契约测试

- policy schema及其在approved文档、systemd、cleanup脚本、CloudFormation和Edge退役状态中的机械映射；
- generic data-layer rehearsal/inventory 拒绝 QA dataset，不存在第二 QA retention owner；
- 普通 `/users/me/qa/export`、daily auto-export 和 destructive full purge 不存在；
- Edge capture disabled 与无 QA timer/S3 权限；
- 24h 查询硬过滤和 per-key 不再越界；
- cutover 后 `retention_until` 精确为所属小时上界加 24 小时，boundary 不读取该字段或解析表名决定 DROP；
- archive 小时边界、排序、manifest/checksum；
- `file://`/`dlq://` 读取；
- committed + restore-verified 小时存在未覆盖 late identity 时仍进入补偿选择，产生恰好一个
  append-only delta segment，membership 收敛后不再重复选择；
- incomplete segment 不可读；
- `forward_cutover` 唯一、只能指向 committed + restore-verified shard，只能幂等设置批准窗口且不能
  move/unset，补偿选择严格大于 cutover；
- normal 失败不补偿；normal 成功最多补一个最老缺口；补偿失败写 receipt 并非零退出；
- cutover 后候选由 UTC 小时时间轴生成；control 前 crash 后源数据过期必须形成 durable
  `source_unavailable_after_retention`，不能从候选与 health 中消失；
- retention 内零记录小时生成可 restore 的零行 base；过期后才首次发现的未知小时禁止生成空 commit；
- timer/operator 都调用单一 host runner，不存在 root `docker exec` 旁路；真实 mount 下 UID/GID
  `1000:1000` selftest 行为通过；
- `traj_export_enabled=false` 时 UI 隐藏入口且 enqueue/list/get/download 均服务端拒绝；
- 导出开关关闭后 prod 不再返回 job/签发 URL，Worker 无 prod DB 依赖，同时 raw archive 仍覆盖该用户；
- boundary 按 catalog 上界 DROP 所有到期小时，不读取 archive 完整性延长 retention；
- DROP/control 同事务，hot-file cleanup crash 后按 `source_dropped_at` 重入；
- archive/boundary host receipt 原子替换，systemd/receipt/DB heartbeat/control/catalog 任一矛盾都不能报健康；
- `qa_exports_tmp` 默认路径解析、24h/no-open-handle 选择、no-write plan 和精确 plan hash 确认；
- `qa_export_jobs` 只有诊断查询，没有历史删除 SQL；
- confirmed gap 后仍按生命周期删除并生成一次 P0；
- emergency 删除顺序与 70% stop 条件；
- Export Worker user/key 隔离和 S3 Range；
- 用户不能直接读取 raw bucket；
- ops fetch checksum 与正文确认守卫。

### 19.2 集成测试

- 隔离 PostgreSQL + local object-store/S3-compatible 环境完成 archive→verify→restore；
- Fargate-equivalent Worker 从 raw shard 生成 trajectory ZIP；
- 归档中断、S3 失败、DB timeout、missing Blob、重复运行和并发 lock；
- control 前 crash→年龄清理→terminal failure、零记录小时 empty base、committed 小时 late delta；
- 大小时 shard 的 CPU/内存/IO 资源上限；
- 小时 partition DROP 与 gateway 并发，无异常长锁和明显 latency 抬升；
- S3 lifecycle、IAM/KMS deny 和 CloudTrail 读取审计。
- app role 无 `ListBucket`/partial Get，GetObject 只覆盖验证所需后缀（含已声明的
  `orphan-evidence-index.jsonl.zst`）；共享 EC2 role 边界在测试报告中不得误报为
  gateway/maintenance 进程隔离。

### 19.3 生产验收

- Edge 连续至少 24 小时无新增 QA 行/文件；
- prod 在线接口严格不返回 24h 以前数据；
- 正常调度时物理 QA 保留为 24 至 25 小时，且不存在 overdue attached partition；
- cutover 后每个已封口小时有 valid restored commit 或明确 terminal failure；年龄清理不能让缺少 control
  的小时静默消失；
- Phase 2 closeout 指定的唯一 cutover 独立 restore 通过；首次补偿窗口由正式选择器收敛，或以明确
  terminal failure 停止并进入单独 gap 审批，不能伪造成功；
- maintenance timer 达到 closeout 规定的连续健康标准，且 systemd、host receipt、DB heartbeat、
  control rows 四方一致；
- finalize 前 legacy cleanup 保持健康；finalize 后 boundary timer 独占 retention，现存 `qa_exports_tmp` orphan 经精确 plan 确认清除；
- S3 raw archive 7 天生命周期有效，partial 1 天收敛；
- ops 从 S3 到本机恢复成功且不访问 prod；
- 用户导出生成/下载期间 prod 无重型读取、ZIP、代理下载；
- 用户下载为 S3 presigned direct URL；
- emergency 演练只删除 QA，并按顺序清到目标水位；
- 普通失败不推送，执行不可逆动作时恰好一条 P0；
- prod/edge runtime policy hash 与 repo SSOT 一致。

## 20. 迁移完成后的唯一运行图

```text
Prod gateway
  -> local QA capture (strict online query window = 24h)
  -> qa_records + hourly qa_blobs/qa_dlq

Hourly HH:15 tokenkey-qa-maintenance (archive owner)
  -> single host runner (UID/GID 1000:1000, real bind-mounted scratch)
  -> archive + restore-verify previous hour
  -> after normal success, reconcile at most one oldest post-cutover gap
  -> atomic host receipt + DB heartbeat + archive ledger/metrics

Hourly HH:00 tokenkey-qa-boundary (partition lifecycle owner)
  -> provision exact [current,current+72h) UTC-hour coverage
  -> classify terminal archive fact + DROP expired whole partitions
  -> cleanup exact-hour blob/DLQ paths + plan/apply export orphans
  -> atomic host receipt + DB heartbeat + catalog/control health

Raw S3 (7d, private)
  -> trusted Fargate Export Worker -> user-scoped ZIP -> direct S3 download
  -> ops recovery role -> ops workstation isolated analysis

Disk P0
  -> same qa-maintenance emergency mode
  -> QA-only cleanup to target
  -> one action/result P0

Edge
  -> no QA capture / archive / export / cleanup / S3 access
```

这张图是实现后QA数据面的唯一目标态；不得增加第二archive owner、第二retention owner或Edge QA旁路。

## 21. 书面审批项

- [x] 只有 prod 启用 QA，Edge 完全退出 QA。
- [x] 在线查询严格过去 24 小时；正常物理保留为 24 至 25 小时。
- [x] `HH:15` maintenance 只负责 archive→verify 及受限前向补偿；`HH:00` boundary 负责小时分区供给与整分区 DROP。
- [x] raw archive 覆盖所有 prod 用户/API key，S3 保留 7 天，不受 `traj_export_enabled` 影响。
- [x] 只有 `traj_export_enabled=true` 用户可见/可用 API-key 导出；Fargate 从 S3 生成，禁止 prod fallback。
- [x] ops 从 S3 直达本机隔离分析，不经过 prod。
- [x] 只有执行 emergency、confirmed gap 删除，或 emergency 必须动作无法启动时发送 P0。
- [x] emergency 为 80% 或剩余 10 GiB，清理到 70%，不暂停 capture。
- [x] 本期不提供用户主动删除 QA 的 UI/API，也不引入 deletion tombstone 控制面。
- [x] 各 phase 按独立验证/审批推进，文档 merge 不自动授权线上写入或删除。
- [x] maintenance 采用唯一 host runner；timer/operator 同路径、固定 UID/GID、真实 scratch 和原子 receipt。
- [x] forward cutover 只接受 Phase 2 closeout 批准的精确锚点；normal 成功后每轮最多补一个最老缺口，
  不恢复历史 backfill。
- [x] boundary 唯一清理 `qa_exports_tmp`；任何现存 orphan 删除前必须经 plan、精确 hash/count apply 和 receipt 校验。
- [x] 当前共享 EC2 role 不是进程级 IAM 隔离；本次只收窄整机权限并如实记录、验收。
