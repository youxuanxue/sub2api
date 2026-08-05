---
title: Prod-only QA 24 小时在线层与 S3 归档生命周期
status: pending
approved_by: pending
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
3. prod 所有用户、所有 API key 的 QA 原始证据按小时归档到 S3，并保留 7 天。
4. 归档、最终核对和 cleanup 由一个 owner 串行执行，消除两个 timer 的竞态和重复扫描。
5. 用户 API-key 导出不在 prod 读取大量 Blob、生成 ZIP 或承载下载流量。
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
- 不由本设计文档的 merge 自动创建 AWS 资源、修改线上配置、删除数据或部署。

## 3. 现状与问题

当前 prod QA 默认捕获绝大多数已认证网关请求。evidence 写入本地 `qa_blobs`，元数据写入
`qa_records`；主 BlobStore 写失败时才降级到 `qa_dlq`。用户无筛选导出默认是过去 24 小时，
但 per-key trajectory 导出可读取全部本地 retained history。

当前主机每天只运行一次 `tokenkey-qa-stale-cleanup.timer`，以 `created_at < now()-N days`
删除 DB 行，并用全目录 `find` 按 mtime 删除 `qa_blobs/qa_dlq`。它不使用 `retention_until`，
也不检查 S3 归档状态。每日粒度使 N 天配置实际最多保留 N+1 天；百万小文件目录还会放大
扫描、递归 chown 和删除成本。

配置 owner 已经漂移：Go 默认值、CloudFormation 参数、live retention 文件和文档不是一个值；
`assert-live-host-state` 虽采集 retention，却没有把预期值纳入 verdict。Edge 也在做无业务价值的
QA capture 和本地清理。

现有应用内 auto-export 是 trajectory 投影，且只覆盖 `traj_export_enabled` 用户；它不是所有
prod QA 的无损原始归档。现有用户导出虽然可把完成 ZIP 放到 S3，但大量本地 Blob 读取、投影和
ZIP 生成仍发生在 prod，曾有重型导出影响数据卷和服务的事故类别。

## 4. 单一 QA Policy

实现阶段在 repo 的 ops QA namespace 新建唯一 `policy.yaml` owner，内容契约如下：

```yaml
schema_version: 1

prod:
  capture_enabled: true
  online_window_hours: 24
  maintenance_schedule_utc: "*:15"
  physical_cleanup_max_lag_minutes: 75

  archive:
    enabled: true
    scope: all_users_all_api_keys
    shard_minutes: 60
    seal_delay_minutes: 15
    s3_retention_days: 7
    raw_user_access: false

  user_export:
    source: s3_raw_archive
    compute: ecs_fargate
    download: direct_s3
    prod_fallback: forbidden

  disk_emergency:
    used_percent: 80
    free_gib: 10
    cleanup_target_percent: 70
    pause_capture: false

edge:
  capture_enabled: false
  archive_enabled: false
  cleanup_enabled: false
  export_enabled: false
  s3_access: false
```

数值是本设计的行为契约：

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
- 应用内旧 daily trajectory auto-export loop；
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

包括普通 QA export、per-key trajectory export、synth/session export、管理端 QA 查询和线上
request-id 关联入口。per-key 导出不再有“全部 retained history”例外。

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

### 8.3 迟到记录、删除 tombstone 与最终核对

`HH:15` 首次封口上一小时。之后每轮维护会重试不完整 shard。该小时即将超过在线 24 小时时，
执行 final reconcile：将当前 DB 行和小时目录 evidence 与 commit 中已归档 record/blob identity
做集合差，只为新增的迟到记录或 orphan evidence 创建 immutable delta segment，再原子更新 commit。

用户删除后已经归档的记录不会从 S3 segment 移除，也不会被“DB 行减少”误判成 archive drift；
它们由第 9.3 节 deletion tombstone 禁止再进入用户导出，并等待 S3 lifecycle 到期。cleanup 判断
的是 final commit segment 集合，不把首次 base segment 当作最终完整性事实。

### 8.4 归档缺口

如果某小时到物理 cleanup 时 final archive 仍无法完整提交：

1. 执行一次有硬超时的 final retry；
2. 仍失败则写不可变 `qa_archive_gaps` 审计，包含窗口、行数、Blob 数、缺失数和原因；
3. 按 24 小时在线生命周期继续删除本地数据；
4. 发送一条 P0，因为系统刚执行了不可逆的数据缺口动作；
5. shard 不得标记 complete。

服务与磁盘安全高于冷归档完整性；S3 故障不能使 prod 本地 QA 无限增长。

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

### 9.3 用户主动删除与 export tombstone

用户删除 QA 数据时，本地行和 Blob 按现有产品语义尽快删除；已进入 raw archive 的数据不重写
小时 segment，S3 lifecycle policy 为 7 天，实际物理删除遵循 S3 lifecycle 的异步执行语义。
产品隐私说明必须明确该受控安全归档窗口。

删除流程必须写一个耐久、可审计、供 off-prod Worker 强一致读取的
`qa_export_deletion_tombstone`，至少包含 user、可选 API key、`deleted_before`、创建时间和覆盖 raw
archive 最长生命周期的 expiry。它不是 S3 raw 删除指令，而是导出授权否定事实。

删除顺序 fail closed：

1. 先在外部 job/auth store（目标态为 DynamoDB）以 strongly-consistent 可读方式提交 tombstone；
2. 将该 user/key 的 pending/running export jobs 标记 revoked；
3. 删除已经生成的 user export S3 artifacts，使已有 presigned URL 立即失效；
4. 上述撤销成功后，再删除本地 `qa_records` 和 Blob；
5. 任一步失败都不得向用户声称删除完成；已写 tombstone 保持导出禁用，后台幂等重试剩余动作。

prod 创建 export job 时先应用 tombstone，拒绝或截断已删除范围；job 固化 tombstone version/hash。
Export Worker 在读取 raw shard 前、生成 ZIP 后和发布对象/presigned URL 前均使用 strongly-consistent
read 复查 tombstone；任一命中都 fail closed，删除临时/已上传未发布 ZIP，不发布下载链接。
Deletion tombstone TTL 必须晚于其可能覆盖的 raw S3 对象生命周期和 lifecycle 异步删除余量，不能
先于 archive 过期。

普通用户接口不能再发现该数据。raw archive 只允许受信 Export Worker 在 tombstone 守卫下按授权
job 派生，或 ops recovery role 受审计读取；ops 读取不恢复用户在线可见性。已批准的策略保持不变：
raw 小时 archive 不因用户删除重写，等待 S3 lifecycle 到期。

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

用户点击 API key 导出时，prod 只做：

- JWT、user 和 API-key ownership/feature 权限校验；
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

队列不能由用户直接写。job 只携带或引用经过 prod 授权固化的 `user_id`、`api_key_id`、窗口、格式和
第 9.3 节 tombstone version/hash；Worker 不接受任意跨用户 S3 路径。Worker 先以 strongly-consistent
read 应用 tombstone，再读每小时 `records.parquet` 过滤该 user/key，并按 `evidence-index` 对
`evidence.pack` 发 Range GET，只下载必要 payload。生成后及发布前再次复查 tombstone，消除 job
创建、ZIP 上传与导出完成之间的删除竞态。

用户浏览器从 S3 直接下载。用户 export bucket/prefix 与 raw archive 分权，生命周期独立且较短；
DynamoDB job index 必须能枚举该 user/key 的未过期 artifacts，使用户删除可以撤销 job 并删除对象，
从而让已签发 URL 失效。

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
window / reason(normal|emergency|user_delete)
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

### 14.4 Export deletion tombstone

耐久 tombstone 记录包含：

```text
user_id / optional api_key_id / deleted_before
tombstone_version / created_at / expires_at / audit_actor
```

其 expiry 必须覆盖 raw archive 的实际 lifecycle 窗口和异步删除余量。该事实存于 Export Worker
可 strongly-consistent read 的外部 auth/job store。Prod job creator 与 Worker 都必须 fail closed
消费同一 owner；不能只靠 UI 或本地 `qa_records` 已删除来推断授权。Job/artifact index 必须支持
按 user/key 撤销 pending/running job 并删除所有未过期用户 export 对象。

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
- 用户删除 tombstone 在 job 创建、Worker 读取、ZIP 上传/发布前多重 fail-closed 校验，并撤销已生成 artifact；
- raw bucket、export bucket、prod archiver、Export Worker 和 ops recovery 使用分离 IAM role/policy；
- S3/KMS key policy 拒绝跨边界读取；
- secrets 不进入命令行或 systemd journal；
- manifest、ledger 和 P0 不包含 request/response body、token、cookie 或 API key；
- S3 raw archive 配置 7 天 expiration policy；S3 异步物理删除可能略晚，用户主动删除后已归档数据
  不重写、等待 lifecycle 收敛；
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
- off-prod export 上线前保留旧导出入口，但切换后若 Worker 故障只能返回暂不可用，不能回退重型 prod；
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

1. 创建独立 raw bucket、KMS、VPC Endpoint、IAM、Lifecycle 和 CloudTrail Data Events；
2. 部署 shard/control/manifest，但不删除本地数据；
3. backfill 当前仍在线数据；
4. 连续运行并验证每小时 generation；
5. 从 S3 直接恢复到本机隔离目录并校验。

### Phase 3：Off-prod 用户导出

1. 创建 DynamoDB job、queue/Pipes、Fargate Worker 和用户 export prefix；
2. 运行 user/key 隔离、安全负向和大体量导出测试；
3. 验证导出期间 prod CPU、内存、磁盘 I/O 和下载带宽没有明显变化；
4. 切换 UI/API，并禁止 prod fallback。

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

## 19. 测试与验收

### 19.1 确定性单元/契约测试

- policy schema、派生值和 runtime hash；
- Edge capture disabled 与无 QA timer/S3 权限；
- 24h 查询硬过滤和 per-key 不再越界；
- `retention_until` 精确为 created_at+24h，DB insert age guard 阻止 cleanup 后迟到回插；
- archive 小时边界、排序、manifest/checksum；
- `file://`/`dlq://` 读取；
- late record 产生 append-only delta segment，commit CAS 后可读；
- incomplete segment 不可读，用户删除不会触发 S3 segment rewrite；
- deletion tombstone 在 job 创建/完成竞态中均 fail closed，已签发 artifact 删除后 URL 失效；
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

- [ ] 只有 prod 启用 QA，Edge 完全退出 QA。
- [ ] 在线查询严格过去 24 小时；物理清理最多滞后到 25h15m。
- [ ] 一个每小时 `HH:15` maintenance 串行执行 archive→verify→cleanup。
- [ ] raw archive 覆盖所有 prod 用户/API key，S3 保留 7 天。
- [ ] 用户 API-key 导出由 Fargate 从 S3 生成，浏览器直连 S3，禁止 prod fallback。
- [ ] ops 从 S3 直达本机隔离分析，不经过 prod。
- [ ] 只有执行 emergency、confirmed gap 删除，或 emergency 必须动作无法启动时发送 P0。
- [ ] emergency 为 80% 或剩余 10 GiB，清理到 70%，不暂停 capture。
- [ ] 用户删除先强一致撤销 jobs/artifacts，再删本地数据；raw archive 等待 7 天 lifecycle，不重写小时 segment。
- [ ] 各 phase 按独立验证/审批推进，文档 merge 不自动授权线上写入或删除。
