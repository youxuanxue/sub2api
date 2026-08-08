---
title: Prod-only QA 24 小时在线层与 S3 归档生命周期
status: approved
approved_by: "user (conversation approvals, 2026-08-05 and 2026-08-08)"
approved_at: 2026-08-05
last_reviewed: 2026-08-08
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
> `tokenkey-qa-maintenance` 负责归档、校验和受限前向补偿，独立的
> `tokenkey-qa-stale-cleanup` 按年龄清理过期在线数据；S3 原始归档保留 7 天。Edge 完全退出 QA。
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
4. 归档、最终核对和前向补偿由 maintenance 唯一负责；年龄清理由 stale cleanup 唯一负责，
   两者共用 advisory lock，消除竞态和重复 owner。
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
全目录扫描 `qa_blobs/qa_dlq`。当前唯一年龄清理 owner 固定使用数据库时钟计算24小时cutoff，
每小时`:45`触发并最多随机延迟15分钟；它不读取S3归档状态。DB删除按5000行短事务执行，
Blob/DLQ仍按同一cutoff处理，百万小文件扫描成本通过资源限制和独立timer观测。

数值配置的唯一owner是`ops/qa/policy.yaml`；archive maintenance、age cleanup、bootstrap、
CloudFormation、approved文档和测试由`qa-lifecycle-ssot.py`机械交叉校验。Edge QA capture、
cleanup unit和S3权限保持退役。

用户导出虽然可把完成 ZIP 放到 S3，但大量本地 Blob 读取、投影和 ZIP 生成仍发生在 prod，曾有
重型导出影响数据卷和服务的事故类别。

2026-08-08 的生产复核确认：stale cleanup 已按独立 timer 正常运行，maintenance timer 则是不久前才
启用。`2026-08-07 22:54 UTC` 的 root `docker exec` 手工归档成功，只证明应用归档逻辑和该小时数据
可用；随后 `23:01` 的 Persistent 补跑和 `23:15` 的第一个正常计划轮次均失败，不能据此宣称 timer
长期不稳定，也不能把手工旁路当作 timer 健康证据。失败的直接运行时缺口是 host scratch 建在
`/var/lib/tokenkey/data/qa_archive_tmp`，而真实数据 bind mount 是
`/var/lib/tokenkey/app:/app/data`，且实际 scratch 为 `root:root 0700`，固定 UID/GID `1000:1000`
无 read/write/search 权限。

同次复核还发现：`2026-08-07 22:00 UTC` 有 2217 条源记录但没有 shard/control；maintenance DB
heartbeat 停在手工成功，未记录后续失败；默认 `qa_exports_tmp` 与 PostgreSQL 共用 EBS，存在一个
1,041,960,960-byte、无打开句柄的历史孤儿 ZIP；当前 EC2 instance role 同时被 gateway 与
maintenance 容器继承。Phase 2 完成标准必须覆盖这些事实，不能只检查 S3 中是否出现过一个 commit。

## 4. 单一 QA Policy

唯一数值 owner：`ops/qa/policy.yaml`。字段含义与 prod/edge 目标值以该文件为准。
Deploy 注入默认值与 rollout 门禁见 `ops/qa/deploy_rollout.yaml`；目录索引见 `ops/qa/README.md`。
Preflight `scripts/checks/qa-lifecycle-ssot.py` 机械校验 policy 结构、deploy rollout 与
本文语义锚点；禁止在其它文件复制 policy 数值副本。

数值契约只在 policy 中定义；脚本结构化读取 policy，并机械校验 CloudFormation/bootstrap、
archive maintenance、age cleanup 与 Edge 退役状态。当前 production archive owner 为
`tokenkey-qa-maintenance.timer`，年龄删除 owner 为 `tokenkey-qa-stale-cleanup.timer`；
后者与 `online_window_hours` 对齐且独立受控启用。不得再从
`QaStaleRetentionDays`、`TOKENKEY_QA_STALE_RETENTION_DAYS` 或 `qa_capture.retention_days`
派生数值。历史 backfill flag、selector 和脚本必须不存在，不得恢复第二套 archive 或 retention owner。

Policy 同时固定 maintenance runner UID/GID、host/container scratch、host receipt 和单轮最大补偿窗口，
以及 stale cleanup 管理的 host export tmp 路径。这些是运行时契约，不是可由 unit、operator 或
bootstrap 各自覆盖的建议值。生产 cutover 小时属于数据库控制状态，不写入静态 policy。

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

capture写入的`retention_until`固定为`created_at+24h`，仅作为兼容展示元数据。年龄删除资格只由
cleanup使用数据库时钟计算的`created_at < database_now()-24h`决定；任何调用方不得改读
`retention_until`形成第二套retention owner。

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

新记录固定写入：

```text
retention_until = created_at + 24h
```

regular cleanup只以`created_at < database_now()-24h`作为候选；`retention_until`必须保持同值，
但不参与删除资格判断。24小时是capture元数据与cleanup共享的policy值；archive状态不参与年龄资格。

### 6.3 物理窗口

归档维护在每小时 15 分执行；独立stale cleanup按受控timer执行并删除所有已超过24小时的数据。
用户可见窗口始终严格为24小时，物理清理延迟由timer和运行时健康信号独立观测。

`physical_cleanup_max_lag_minutes=75` 是机械健康契约：非 emergency 状态下，最老物理 QA 不应
超过 25 小时 15 分。超过该值进入 ledger/daily diagnostics，但不单独推送告警。

## 7. 统一每小时 Maintenance

### 7.1 唯一 owner

建立一个 `qa-maintenance` 程序和一个 regular timer：

```text
tokenkey-qa-maintenance.timer
OnCalendar=*-*-* *:15:00
Persistent=true
```

它是唯一archive owner，不执行物理删除。`HH:15` 避开整点 PostgreSQL pgdump，并给上一小时
异步 capture 15 分钟 seal delay；年龄删除由独立stale-cleanup owner执行。

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
       选择 forward_cutover 之后、normal window 之前最老的未完成小时
       最多补偿一个窗口；无缺口则跳过
  -> 补偿失败：保留机器可读失败状态、写 host receipt、非零退出
  -> 收敛archive partial/tmp
  -> 写 DB heartbeat、host receipt、maintenance ledger 和 metrics
  -> release lock
```

`qa_archive_shards` 中唯一 `forward_cutover=true` 的 committed 且 restore-verified shard 是前向边界。
本次生产收口以 `2026-08-07 21:00 UTC` shard 为锚点；`22:00` 缺口走上述正式补偿状态机，不恢复
任意窗口 selector、历史 backfill flag 或第二个脚本。补偿集合不包含 cutover 及其之前的已知历史
缺口。归档和 cleanup 不并发。普通失败不会阻塞 gateway，也不会触发密集通知。

### 7.3 Crash 可恢复

每个阶段写持久化 checkpoint；所有动作幂等：

- archive 对象先写 immutable generation，再最后提交 commit manifest；
- `forward_cutover` 通过唯一部分索引保证全库只有一个，重复设置同一锚点幂等，切换锚点需独立显式操作；
- 进程在任一步 crash，下一轮根据 control row、segment membership 和 S3 commit 收敛；
- host runner 在同目录临时文件完成 `fsync` 后 rename，原子写
  `/var/lib/tokenkey/qa-maintenance-last-run.json`，即使 child 在写 DB heartbeat 前失败也留下机器证据；
- stale cleanup 自己持有删除 receipt/quarantine 恢复逻辑，maintenance 不写 cleanup receipt。

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
  `evidence-index.jsonl.zst` 对象后缀；
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

`HH:15` 首先处理上一完整小时。只有该 normal window committed 且 restore-verified，本轮才可在
`forward_cutover` 之后选择最老的一个不完整小时做补偿；不能一次扫描或追赶全部历史。对已有 commit
的小时，reconcile 将当前 DB 行和 commit 中已归档 record/blob identity 做集合差，只为新增迟到记录
创建 immutable delta segment，再原子更新 commit。对完全缺失的小时创建 base segment。normal 或
补偿任一失败都保留机器可读状态并使本轮非零退出。

archive完整性判断使用final commit segment集合，不把首次base segment当作最终完整性事实。
归档器处理所有用户和API key，禁止用`traj_export_enabled`过滤archive输入。该判断不参与
独立年龄cleanup。

### 8.4 归档缺口

如果某小时archive仍无法完整提交，shard保持`failed`并记录机器可读原因，不得标记complete。
独立年龄cleanup仍按24小时规则删除本地数据；archive health继续报告历史不可完整恢复，不自动
创建confirmed-gap或把失败转成成功。

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
历史备份。Phase 2 raw restore 验证前，`ops/prod/fetch-qa-dump.sh` 仅作为人工只读 break-glass；它不
定义 lifecycle、不得定时运行、不得作为删除证据，验证通过后必须删除。

## 9. Cleanup 设计

### 9.1 DB 删除

cleanup只以数据库时钟计算的24小时`created_at` cutoff选择候选。首次恢复执行前必须输出数据库时钟、cutoff、
候选行数和时间范围、Blob/DLQ候选数及active image并取得独立确认；计划10分钟内有效。执行时重新核对
这些字段、maintenance/stale timer状态，并在每个5000行短事务取得QA maintenance advisory lock；任一
漂移都在删除前拒绝。首次执行持久化plan marker并持有host file lock；崩溃后只能显式使用
`resume-first`和同plan/确认串续跑，fresh apply不得复用marker，resume也不得创建marker。marker在首次删除
receipt验证通过并启用timer的同一SSM命令中清除，marker存在时scheduled cleanup拒绝运行。archive是否完整
不改变候选集合。

### 9.2 Blob 删除

新小时布局按目录处理：

1. 根据文件mtime计算24小时候选；
2. 删除对应DB过期行；
3. 删除`qa_blobs`/`qa_dlq`中的过期文件；
4. 清理空目录；
5. 下一轮重试失败项。

旧布局按 DB 记录的 Blob URI 分批删除，不对整个树做每小时 `find`。每日某一轮做受限 orphan sweep，
仅作为兜底，不承担主保留策略。

同一个 stale-cleanup runner 是 `qa_exports_tmp` 唯一清理 owner。baseline 必须先从 active container 的
有效配置计算目录：显式 `QA_EXPORT_TMP_DIR` 非空时使用其真实 bind mount；未配置时使用当前默认
container `/app/data/qa_exports_tmp`，即 host `/var/lib/tokenkey/app/qa_exports_tmp`。不得因 env 未配置
而跳过默认目录，也不得把 `/var/lib/tokenkey/qa_exports_tmp` 误当线上路径。

正常 export 在进程内通过 `defer` 删除临时 ZIP；scheduled cleanup 只处理超过统一 24 小时 cutoff、
regular file、位于该单一目录且没有打开句柄的 crash orphan。plan/receipt 必须增加逐文件 basename、
size、mtime、总数和总字节，不记录文件内容。首次纳管现有
`traj-export-4288971549.zip`（1,041,960,960 bytes）必须先输出 no-write plan，并以精确 plan hash
确认后才删除；不能混入 timer 首次无提示清理。`qa_export_jobs` 本次只输出状态/过期分布和
`storage_key` 一致性诊断，不删除历史行、不新增 job retention 语义，也不以 job row 作为 temp ZIP
仍在使用的替代证据。

### 9.3 用户主动删除不在本期范围

产品没有用户主动删除 QA 的 UI、HTTP API 或 service capability。本设计不引入 deletion tombstone、
跨系统撤销或 raw shard rewrite。

本期 QA 删除来源只有：

- regular maintenance 按统一 24 小时在线生命周期清理；
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
prod host 代为列举或下载。用该入口完成至少一个 committed shard 的独立 verify/restore 验收后，删除
`ops/prod/fetch-qa-dump.sh`；在此之前该脚本仅是人工只读 break-glass，不能被 timer 或 cleanup 调用。

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
- 不安装或运行 QA maintenance/stale cleanup timer；
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
一个 true。设为 true 的 shard 还必须是 committed 且 `restore_verified_at IS NOT NULL`；应用选择器和
设置命令双重校验。它只界定自动补偿下界，不代表 cutover 之前所有历史小时完整，也不授权删除。

### 14.2 Cleanup receipt

记录：

```text
window / reason(normal|emergency|ops_approved)
archive_generation 或 gap_id
deleted_rows / quarantined_files / released_bytes
started_at / completed_at / error
```

### 14.3 Archive gap

不可变 gap 记录包含：

```text
window / reason / retry_summary
rows / blob_refs / missing_or_unarchived
local_delete_at / emergency_state / policy_hash
```

P0 卡片引用 gap ID，避免在通知中泄漏 body 或用户数据。

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
- cleanup cutoff、最老物理记录、删除行/文件/字节、duration；
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
- cleanup crash：下一轮按 receipt/quarantine 重入；
- Export Worker 失败：job 标记 failed，用户重试；不得 fallback 到 prod；
- ops fetch 损坏：checksum fail closed，不输出不可信证据。

### 17.2 部署回滚

新链路分阶段激活：

- archive-only 可独立关闭，不改变现有本地 retention；
- off-prod export 上线前仅暂留现有 API-key trajectory in-prod 实现；切换后若 Worker 故障只能返回暂不可用，不能回退重型 prod；
- 24h cleanup只按年龄执行；首次删除绑定实时plan、active image、候选数和独立确认，不以archive soak或恢复完整性为前置；
- Edge capture=false 可通过配置回滚，但不自动恢复已删除 Edge QA；
- emergency mode 可关闭自动动作并回到人工处置，但 P0 disk monitor 必须保留。

Phase 2 运行时回滚固定为先停 maintenance timer，再回滚应用/runner。不得回滚或删除 additive schema、
唯一 `forward_cutover`、已验证 S3 segment/commit 或 host receipt；不得把 22:00 缺口伪造为成功。旧 image
可以忽略新字段，但下一次重新启用前必须重新通过 selftest、cutover/receipt/DB/control 联合健康检查。

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

Phase 2 的独立 deploy 入口为 `ops/qa/deploy_qa_raw_archive_cfn.sh`：它要求显式传入
`APP_INSTANCE_ROLE_ARN`、`OPS_RECOVERY_PRINCIPAL_ARN` 和 prod VPC/route table，并以
`QA_RAW_ARCHIVE_CONFIRM=yes` 作为 change set 执行门闩，部署
`deploy/aws/cloudformation/stage0-qa-raw-archive.yaml`。栈创建专用 recovery role 和专用
CloudTrail audit bucket：指定 principal 只能 assume 该只读角色，角色仅可读取 `raw/` 并经 S3
解密；audit bucket 仅接受本 trail 写入。模板的 SSE-KMS key 与 bucket policy 必须同步授权，避免出现
bucket 允许而 KMS 拒绝的半配置状态。现有 app statement 仍含 `ListBucket` 和 broad
`raw/v1/` / `raw/partial/` read；本次 closeout 按第 8.1 节收窄。

初始 raw bucket/KMS/endpoint/audit 基础设施已存在。本次 production integrity closeout 按以下顺序执行；
`tokenkey-qa-stale-cleanup.timer` 继续运行，不能因归档修复重新暂停已经健康的年龄清理：

1. 停止并确认 `tokenkey-qa-maintenance.timer` inactive；保留 stale cleanup active；
2. 部署 additive `forward_cutover` schema、单一 host runner、真实 selftest、host receipt 和联合健康检查，
   但不启动 scheduled maintenance；
3. selftest 证明 UID/GID `1000:1000` 可经
   `/var/lib/tokenkey/app/qa_archive_tmp:/app/data/qa_archive_tmp` create/read/delete；
4. 独立 verify/restore `2026-08-07 21:00 UTC` committed shard 后，原子设置唯一 cutover；
5. 通过同一 runner 执行一轮 normal archive；normal 成功后由正式补偿选择器处理最老的 22:00 缺口，
   再 verify/restore。若 22:00 源数据已被年龄清理，不创建空 commit、不写假成功，停止 rollout 并单独
   请求不可恢复 gap 的处置批准；
6. 从 ops workstation assume recovery role，直接对 S3 完成独立 restore，随后删除
   `ops/prod/fetch-qa-dump.sh`；
7. 启用 maintenance timer，观察至少两个连续正常计划轮次；每轮联合核对 systemd、host receipt、DB
   heartbeat 和 control rows；
8. 通过 CloudFormation change set 最后移除 app role 的 `ListBucket`/partial read，并把 GetObject 收窄到
   实际对象后缀；执行 archive canary，失败即停 timer 并回滚 IAM change set；
9. closeout 全部通过后，deploy rollout 才可把 prod archive 默认值从显式保守关闭迁移到 policy target；
   历史 backfill 始终保持退休。

### Phase 3：Off-prod 用户导出

1. 创建 DynamoDB job、queue/Pipes、Fargate Worker 和用户 export prefix；
2. 迁移并保留 `traj_export_enabled` UI/服务端授权门禁；
3. 运行 user/key 隔离、安全负向和大体量导出测试；
4. 验证导出期间 prod CPU、内存、磁盘 I/O 和下载带宽没有明显变化；
5. 切换 UI/API，并禁止 prod fallback。

### Phase 4：24 小时在线层

1. 查询接口先硬过滤过去 24 小时；
2. 新记录固定写入兼容元数据`retention_until=created_at+24h`，删除owner仍只读取`created_at`；
3. 先启用每小时archive→verify maintenance并完成新sealed hour的restore证明；
4. 经实时activation plan和独立确认分批收敛当前超过24小时数据，再启用独立每小时age cleanup；
5. 验证最老物理数据、DB latency、WAL、磁盘与用户导出。

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
| `ops/prod/fetch-qa-dump.sh` | 从 prod 打包的只读 break-glass | `qa-archive inspect/fetch/verify` | Phase 2 raw S3 本机 restore 验证通过后删除；之前不得定时或触发 purge |
| root `docker exec ... --qa-maintenance-once` operator 旁路 | 手工执行可绕过 timer 的 UID/mount/receipt 契约 | `/usr/local/bin/tokenkey-qa-maintenance.sh` 单一 host runner | production integrity closeout 时删除 operator caller；timer/operator 都走同一 runner |
| 当前应用内 trajectory worker、localfs/S3 ZIP 生成和 prod 下载代理 | 用户导出的临时实现 | DynamoDB/SQS/Fargate + direct S3 | Phase 3 原子切换；切换后删除 prod 重型 build/read/proxy 与相关 env 注入 |
| `tk_030` / Ent schema 中 daily auto-export、`auto` kind 的历史说明 | 已删除能力的注释残留；兼容列仍被旧数据读取 | manual job 兼容 schema + Phase 3 job model | 本次修正文案但不删列/历史 row、不新增 job retention；Phase 3 再迁移 schema 语义 |
| `QaExportsBucket` | 当前用户 trajectory ZIP artifact bucket | Phase 3 user artifact bucket/prefix | 可迁移或复用，但永远不是 raw archive；TTL/IAM 与 raw bucket 分离 |
| `/var/lib/tokenkey/app/qa_exports_tmp` crash orphan | in-prod export 的共享 EBS 临时文件 | `tokenkey-qa-stale-cleanup` | 本次纳入 plan/receipt；现有 1GB 文件须精确 plan 确认，Phase 3 切换后删除该 prod staging surface |
| `QaStaleRetentionDays`、retention env、daily stale timer/script、`qa_capture.retention_days` | 已退休的可变prod物理清理 | policy固定24小时、每小时`:45`的`tokenkey-qa-stale-cleanup.timer` | 删除可变retention入口；bootstrap仅可单向删除旧env文件；首次执行经实时plan确认后受控启用固定timer |
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
- `retention_until`精确为`created_at+24h`，且cleanup源码不读取该字段；
- archive 小时边界、排序、manifest/checksum；
- `file://`/`dlq://` 读取；
- late record 产生 append-only delta segment，commit CAS 后可读；
- incomplete segment 不可读；
- `forward_cutover` 唯一、只能指向 committed + restore-verified shard，补偿选择严格大于 cutover；
- normal 失败不补偿；normal 成功最多补一个最老缺口；补偿失败写 receipt 并非零退出；
- timer/operator 都调用单一 host runner，不存在 root `docker exec` 旁路；真实 mount 下 UID/GID
  `1000:1000` selftest 行为通过；
- `traj_export_enabled=false` 时 UI 隐藏入口且 enqueue/list/get/download 均服务端拒绝；
- 导出开关关闭后 prod 不再返回 job/签发 URL，Worker 无 prod DB 依赖，同时 raw archive 仍覆盖该用户；
- cleanup按24小时年龄处理所有过期数据，不读取archive完整性；
- crash 后 receipt/quarantine 重入；
- host receipt 原子替换，systemd/receipt/DB heartbeat/control 任一矛盾都不能报健康；
- `qa_exports_tmp` 默认路径解析、24h/no-open-handle 选择、plan hash 和 1GB 首次精确确认；
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
- 大小时 shard 的 CPU/内存/IO 资源上限；
- cleanup 小事务与 gateway 并发，无长锁和明显 latency 抬升；
- S3 lifecycle、IAM/KMS deny 和 CloudTrail 读取审计。
- app role 无 `ListBucket`/partial Get，GetObject 只覆盖验证所需后缀；共享 EC2 role 边界在测试报告中
  不得误报为 gateway/maintenance 进程隔离。

### 19.3 生产验收

- Edge 连续至少 24 小时无新增 QA 行/文件；
- prod 在线接口严格不返回 24h 以前数据；
- 非 emergency 时最老物理 QA 不超过 25h15m；
- 每个应归档小时有 valid commit，或有明确 gap receipt；
- 21:00 是唯一 cutover 且独立 restore 通过；22:00 由正式补偿收敛，或因源数据已过期明确停止并进入
  单独 gap 审批，不能伪造成功；
- maintenance timer 至少两个连续正常计划轮次成功，且 systemd、host receipt、DB heartbeat、control
  rows 四方一致；
- stale cleanup 在 maintenance rollout 全程保持健康，`qa_exports_tmp` 现有 1GB orphan 经精确确认清除；
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

Controlled tokenkey-qa-stale-cleanup (retention owner)
  -> plan database/file cutoff
  -> delete all data older than 24h
  -> cleanup metrics/journal

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
- [x] 在线查询严格过去 24 小时；物理清理最多滞后到 25h15m。
- [x] 每小时`HH:15` maintenance只负责archive→verify及受限前向补偿；独立固定24小时stale cleanup负责年龄删除。
- [x] raw archive 覆盖所有 prod 用户/API key，S3 保留 7 天，不受 `traj_export_enabled` 影响。
- [x] 只有 `traj_export_enabled=true` 用户可见/可用 API-key 导出；Fargate 从 S3 生成，禁止 prod fallback。
- [x] ops 从 S3 直达本机隔离分析，不经过 prod。
- [x] 只有执行 emergency、confirmed gap 删除，或 emergency 必须动作无法启动时发送 P0。
- [x] emergency 为 80% 或剩余 10 GiB，清理到 70%，不暂停 capture。
- [x] 本期不提供用户主动删除 QA 的 UI/API，也不引入 deletion tombstone 控制面。
- [x] 各 phase 按独立验证/审批推进，文档 merge 不自动授权线上写入或删除。
- [x] maintenance 采用唯一 host runner；timer/operator 同路径、固定 UID/GID、真实 scratch 和原子 receipt。
- [x] 21:00 作为唯一 forward cutover；normal 成功后每轮最多补一个最老缺口，22:00 不走历史 backfill。
- [x] stale cleanup 唯一清理 `qa_exports_tmp`；现有 1GB orphan 删除前必须精确 plan 确认。
- [x] 当前共享 EC2 role 不是进程级 IAM 隔离；本次只收窄整机权限并如实记录、验收。
