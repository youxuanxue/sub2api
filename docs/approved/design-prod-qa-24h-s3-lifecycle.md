---
title: Prod-only QA 24 小时在线层与 S3 归档生命周期
status: approved
approved_by: "user (conversation approval, 2026-08-05)"
approved_at: 2026-08-05
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

> 只有 prod 捕获 QA；prod 在线查询严格覆盖过去 24 小时；一个每小时运行的
> `tokenkey-qa-maintenance` 先归档上一完整小时到 S3、校验提交，再清理已经完整过期的在线数据；
> S3 原始归档保留 7 天。Edge 完全退出 QA。用户导出和运维深度分析都不从 prod 下载大文件，
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
4. 归档、最终核对和 cleanup 由一个 owner 串行执行，消除两个 timer 的竞态和重复扫描。
5. 用户 API-key 导出仍由管理员授予的 `traj_export_enabled` 控制，且不在 prod 读取大量 Blob、生成 ZIP
   或承载下载流量。
6. 运维可从 S3 直接下载到本机隔离目录深度分析，不经过 prod API、数据库、磁盘或网络。
7. 只在系统已经执行不可逆保护动作时发送 P0；普通失败和延迟只记 ledger/metrics，不推送噪声告警。
8. Edge 不捕获、不归档、不导出、不清理 QA，也没有 QA S3 权限。
9. QA 配置由一个 repo-owned policy 派生，并由 deploy 与 daily audit 机械校验 runtime 收敛。

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

当前主机每天只运行一次 `tokenkey-qa-stale-cleanup.timer`，以 `created_at < now()-N days`
删除 DB 行，并用全目录 `find` 按 mtime 删除 `qa_blobs/qa_dlq`。它不使用 `retention_until`，
也不检查 S3 归档状态。每日粒度使 N 天配置实际最多保留 N+1 天；百万小文件目录还会放大
扫描、递归 chown 和删除成本。

配置 owner 已经漂移：Go 默认值、CloudFormation 参数、live retention 文件和文档不是一个值；
`assert-live-host-state` 虽采集 retention，却没有把预期值纳入 verdict。Edge 也在做无业务价值的
QA capture 和本地清理。

用户导出虽然可把完成 ZIP 放到 S3，但大量本地 Blob 读取、投影和 ZIP 生成仍发生在 prod，曾有
重型导出影响数据卷和服务的事故类别。

## 4. 单一 QA Policy

唯一数值 owner：`ops/qa/policy.yaml`。Deploy 注入默认值与 rollout 门禁见
`ops/qa/deploy_rollout.yaml`；目录索引见 `ops/qa/README.md`。
Preflight `scripts/checks/qa-lifecycle-ssot.py` 机械校验 policy 结构、deploy rollout 与
本文语义锚点，禁止在其它文件复制一份 policy 副本。

数值语义（字段定义以 policy 文件为准）：

- 在线窗口为 24 小时；
- regular maintenance 每小时运行；
- 上一小时结束 15 分钟后封口；
- raw archive 保留 7 天；
- 数据卷达到 80% 或剩余低于 10 GiB 时进入 emergency；
- emergency 清理到低于 70%；
- 不自动暂停 capture。

代码、CloudFormation/bootstrap、deploy 注入、systemd unit、daily audit、文档和测试均由该 policy
派生或校验。实现完成后废弃独立语义的：

- `QaStaleRetentionDays`；
- `TOKENKEY_QA_STALE_RETENTION_DAYS`；
- `qa_capture.retention_days` 的独立配置解释；
- `tokenkey-qa-stale-cleanup.timer`；
- Edge QA retention 和 QA host-unit 同步。

应用可保留兼容期读取，但 deploy 必须以 policy 生成的值覆盖，完成迁移后删除旧配置入口。
Runtime 记录 `QA_POLICY_VERSION` 和 policy SHA-256；prod deploy 与 daily audit 比较期望 hash，Edge
则机械断言 `QA_CAPTURE_ENABLED=false` 且无 QA maintenance unit。

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

DB insert 必须原子检查 `retention_until > database_now()`；已经超过 24 小时的迟到 capture 不得再插入
在线表。若 Blob 已先写入，则按 orphan 记账并由维护任务收敛。这个数据库侧 age guard 保证 final
reconcile/cleanup 后不会由长期阻塞的异步 writer 重新产生已过期行，不能只在 Go 进程中做时钟判断。

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

旧布局在迁移窗口内由 archiver/cleanup 兼容读取；不批量移动百万文件。旧文件被 backfill 后按
DB 引用和年龄分批清理。

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

regular cleanup 以 `retention_until` 和完整小时窗口作为候选，不再另算 N days。24 小时是应用与
维护任务共享的事实。

### 6.3 物理窗口

维护任务在每小时 15 分执行。它只删除已经完整过期的小时窗口，因此最旧物理数据通常为
24 小时 15 分至 25 小时 15 分；用户可见窗口仍严格为 24 小时。

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

它替代独立 archive timer 和 cleanup timer。`HH:15` 避开整点 PostgreSQL pgdump，并给上一小时
异步 capture 15 分钟 seal delay。

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

每次运行固定执行：

```text
preflight/lock
  -> 检查 emergency request 与 policy hash
  -> 归档上一完整 UTC 小时
  -> 重试仍在 24h 在线窗口内的不完整 shard
  -> 对即将物理过期的小时做 final reconcile
  -> 提交 complete archive generation 或记录 confirmed gap
  -> cleanup 完整过期小时
  -> 收敛 quarantine/partial/tmp
  -> 写 maintenance ledger 和 metrics
  -> release lock
```

归档和 cleanup 不并发。普通失败不会阻塞 gateway，也不会触发密集通知。

### 7.3 Crash 可恢复

每个阶段写持久化 checkpoint；所有动作幂等：

- archive 对象先写 immutable generation，再最后提交 commit manifest；
- cleanup 先写 batch receipt，再小事务删除 DB 行，再隔离目录；
- quarantine 删除失败可在下一轮继续；
- 进程在任一步 crash，下一轮根据 control row、S3 manifest 和 cleanup receipt 收敛。

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
- prod archiver role 只允许目标 prefix 的 Put/Head、必要 multipart，以及读取自身 commit/manifest
  所需的精确 Get；禁止列举或通用下载 evidence pack；
- Export Worker role 只读 raw archive、只写用户 export prefix；
- ops recovery role 允许受审计 Get/List；
- 普通用户和普通 API key 无 raw archive 权限；
- CloudTrail Data Events 记录 raw archive 读取；
- complete 对象 7 天过期，partial prefix 1 天过期；
- prod API 不为 raw archive 生成用户 presigned URL。

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

`HH:15` 首次封口上一小时。之后每轮维护会重试不完整 shard。该小时即将超过在线 24 小时时，
执行 final reconcile：将当前 DB 行和小时目录 evidence 与 commit 中已归档 record/blob identity
做集合差，只为新增的迟到记录或 orphan evidence 创建 immutable delta segment，再原子更新 commit。

cleanup 判断的是 final commit segment 集合，不把首次 base segment 当作最终完整性事实。归档器处理
所有用户和 API key，禁止用 `traj_export_enabled` 过滤 archive 输入。

### 8.4 归档缺口

如果某小时到物理 cleanup 时 final archive 仍无法完整提交：

1. 执行一次有硬超时的 final retry；
2. 仍失败则写不可变 `qa_archive_gaps` 审计，包含窗口、行数、Blob 数、缺失数和原因；
3. 按 24 小时在线生命周期继续删除本地数据；
4. 发送一条 P0，因为系统刚执行了不可逆的数据缺口动作；
5. shard 不得标记 complete。

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

禁止单个无界 `DELETE`。cleanup 以完整小时窗口和 `retention_until` 选择候选，使用索引、keyset 和
小批事务；每批建议 5,000–10,000 行。实现需避免长锁、长事务和大 WAL 峰值，并记录删除行数与耗时。

### 9.2 Blob 删除

新小时布局按目录处理：

1. archive final commit 或 gap receipt 已落盘；
2. 创建 cleanup batch receipt；
3. 小事务删除对应 DB 行；
4. 原子改名小时目录到 quarantine；
5. 在线路径立即收敛；
6. 低 CPU/idle I/O 后台分批删除 quarantine；
7. 下一轮重试未完成 quarantine。

旧布局按 DB 记录的 Blob URI 分批删除，不对整个树做每小时 `find`。每日某一轮做受限 orphan sweep，
仅作为兜底，不承担主保留策略。

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
```

状态只允许：

```text
pending -> writing -> verified -> committed
                    \-> failed -> retry
expired_unarchived -> gap_recorded
```

只有 `committed` generation 可被 Export Worker 和 ops 工具读取。

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

## 16. 安全与隐私边界

- raw archive 不公开，不提供普通用户 presigned URL；
- 用户只能下载 Worker 生成的 user/key scoped trajectory ZIP；
- Worker job spec 由 prod 授权固化，不能由客户端构造 S3 范围；
- `traj_export_enabled` 在 UI 和服务端双层门禁，关闭后禁止 trajectory job/list/poll/download；
- raw bucket、export bucket、prod archiver、Export Worker 和 ops recovery 使用分离 IAM role/policy；
- S3/KMS key policy 拒绝跨边界读取；
- secrets 不进入命令行或 systemd journal；
- manifest、ledger 和 P0 不包含 request/response body、token、cookie 或 API key；
- S3 raw archive 配置 7 天 expiration policy；S3 异步物理删除可能略晚，由 lifecycle 收敛；
- ops 本机恢复默认脱敏、小输出、隔离目录和显式正文确认。

## 17. 失败处理与回滚

### 17.1 常规失败

- archive 临时失败：记 ledger，下一小时重试，不推送；
- cleanup crash：下一轮按 receipt/quarantine 重入；
- Export Worker 失败：job 标记 failed，用户重试；不得 fallback 到 prod；
- ops fetch 损坏：checksum fail closed，不输出不可信证据。

### 17.2 部署回滚

新链路分阶段激活：

- archive-only 可独立关闭，不改变现有本地 retention；
- off-prod export 上线前仅暂留现有 API-key trajectory in-prod 实现；切换后若 Worker 故障只能返回暂不可用，不能回退重型 prod；
- 24h cleanup 只在 archive soak/恢复演练通过后启用；
- Edge capture=false 可通过配置回滚，但不自动恢复已删除 Edge QA；
- emergency mode 可关闭自动动作并回到人工处置，但 P0 disk monitor 必须保留。

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
解密；audit bucket 仅接受本 trail 写入。模板的 SSE-KMS key 与 bucket policy 同步授权：prod app
role 仅可经 S3 访问 `raw/v1/` / `raw/partial/` 前缀，避免出现 bucket 允许而 KMS 拒绝的半配置状态。

1. 创建独立 raw bucket、KMS、VPC Endpoint、IAM、Lifecycle 和 CloudTrail Data Events；
2. 部署 shard/control/manifest，但不删除本地数据；
3. backfill 当前仍在线数据；
4. 连续运行并验证每小时 generation；
5. 从 S3 直接恢复到本机隔离目录并校验。

### Phase 3：Off-prod 用户导出

1. 创建 DynamoDB job、queue/Pipes、Fargate Worker 和用户 export prefix；
2. 迁移并保留 `traj_export_enabled` UI/服务端授权门禁；
3. 运行 user/key 隔离、安全负向和大体量导出测试；
4. 验证导出期间 prod CPU、内存、磁盘 I/O 和下载带宽没有明显变化；
5. 切换 UI/API，并禁止 prod fallback。

### Phase 4：24 小时在线层

1. 查询接口先硬过滤过去 24 小时；
2. 新记录 `retention_until=created_at+24h`；
3. 启用统一每小时 archive→verify→cleanup；
4. 分批收敛当前超过 24 小时数据；
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
| 当前应用内 trajectory worker、localfs/S3 ZIP 生成和 prod 下载代理 | 用户导出的临时实现 | DynamoDB/SQS/Fargate + direct S3 | Phase 3 原子切换；切换后删除 prod 重型 build/read/proxy 与相关 env 注入 |
| `QaExportsBucket` | 当前用户 trajectory ZIP artifact bucket | Phase 3 user artifact bucket/prefix | 可迁移或复用，但永远不是 raw archive；TTL/IAM 与 raw bucket 分离 |
| `QaStaleRetentionDays`、retention env、daily stale timer/script、`qa_capture.retention_days` | 当前 prod 物理清理保护 | repo policy + hourly `qa-maintenance` | Phase 4 在新 timer 已安装、dry-run/restore/lock 验证通过时原子删除 |
| `tokenkey-pgdump.sh` 的 `qa_records*` data exclusion | 避免 pgdump 被 QA 撑爆的备份边界 | raw QA archive 负责 QA 历史 | 长期保留 exclusion，但注释必须指向本文；它不是 lifecycle owner |

Phase PR 必须同时删除该行所有 deploy、test、doc 和 generated fixture 引用；不得只关开关后留下可再次启用
的旧代码。迁移完成后 repository sentinel 应只允许本文、目标 policy/maintenance/worker 及明确的负向
契约测试出现 QA lifecycle 定义。

## 19. 测试与验收

### 19.1 确定性单元/契约测试

- policy schema、派生值和 runtime hash；
- generic data-layer rehearsal/inventory 拒绝 QA dataset，不存在第二 QA retention owner；
- 普通 `/users/me/qa/export`、daily auto-export 和 destructive full purge 不存在；
- Edge capture disabled 与无 QA timer/S3 权限；
- 24h 查询硬过滤和 per-key 不再越界；
- `retention_until` 精确为 created_at+24h，DB insert age guard 阻止 cleanup 后迟到回插；
- archive 小时边界、排序、manifest/checksum；
- `file://`/`dlq://` 读取；
- late record 产生 append-only delta segment，commit CAS 后可读；
- incomplete segment 不可读；
- `traj_export_enabled=false` 时 UI 隐藏入口且 enqueue/list/get/download 均服务端拒绝；
- 导出开关关闭后 prod 不再返回 job/签发 URL，Worker 无 prod DB 依赖，同时 raw archive 仍覆盖该用户；
- cleanup 只处理完整过期小时；
- crash 后 receipt/quarantine 重入；
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

### 19.3 生产验收

- Edge 连续至少 24 小时无新增 QA 行/文件；
- prod 在线接口严格不返回 24h 以前数据；
- 非 emergency 时最老物理 QA 不超过 25h15m；
- 每个应归档小时有 valid commit，或有明确 gap receipt；
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

Hourly HH:15 tokenkey-qa-maintenance (single owner)
  -> archive previous hour
  -> verify immutable generation
  -> final reconcile expiring hour
  -> commit or confirmed gap
  -> cleanup fully expired hour
  -> maintenance ledger/metrics

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

这张图是实现后 QA 数据面的唯一目标态；不得保留另一套 daily archive、daily stale cleanup 或 Edge QA
旁路。

## 21. 书面审批项

- [x] 只有 prod 启用 QA，Edge 完全退出 QA。
- [x] 在线查询严格过去 24 小时；物理清理最多滞后到 25h15m。
- [x] 一个每小时 `HH:15` maintenance 串行执行 archive→verify→cleanup。
- [x] raw archive 覆盖所有 prod 用户/API key，S3 保留 7 天，不受 `traj_export_enabled` 影响。
- [x] 只有 `traj_export_enabled=true` 用户可见/可用 API-key 导出；Fargate 从 S3 生成，禁止 prod fallback。
- [x] ops 从 S3 直达本机隔离分析，不经过 prod。
- [x] 只有执行 emergency、confirmed gap 删除，或 emergency 必须动作无法启动时发送 P0。
- [x] emergency 为 80% 或剩余 10 GiB，清理到 70%，不暂停 capture。
- [x] 本期不提供用户主动删除 QA 的 UI/API，也不引入 deletion tombstone 控制面。
- [x] 各 phase 按独立验证/审批推进，文档 merge 不自动授权线上写入或删除。
