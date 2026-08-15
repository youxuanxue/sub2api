---
title: Prod-only QA S3 用户面与小时归档生命周期
status: approved
approved_by: "user (conversation approvals, 2026-08-05 through 2026-08-15; S3-only user surface, capture-sealed archive-gated hourly DROP, fail-closed single maintenance owner, no automatic emergency deletion)"
approved_at: 2026-08-05
last_reviewed: 2026-08-15
created: 2026-08-05
authors: [agent]
risk: high
scope:
  - prod QA capture and hard health
  - raw evidence archive
  - archive-gated hourly partition cleanup
  - S3-only user QA list, detail, and export
  - ops recovery
  - edge QA removal
---

# Prod-only QA S3 用户面与小时归档生命周期

## 1. 决策摘要

QA 数据面只有一个目标形态：

> 只有 prod 捕获 QA。`tokenkey-qa-maintenance.timer` 在每小时 `HH:15`
> 供给未来小时分区、归档并 restore-verify 上一小时，然后 DROP 已可靠进入 raw S3 的精确小时分区，
> 最后清理对应 Blob/DLQ。正常完成后 PostgreSQL 只保留当前正在写入的小时；失败小时保留到归档成功，
> 不做 DEFAULT、逐行 DELETE、rehome、copy 或 move。用户列表、详情和导出均读取 user/API-key scoped
> S3 QA Bundle；ZIP 只在用户请求导出时从已提交 Bundle 生成，不回源 prod。Edge 完全退出 QA。

`docs/approved/design-qa-phase2-archive-closeout.md` 保留已经上线的 Phase 2 历史、恢复和证据契约；
本文件是后续运行目标的唯一批准基线。`ops/qa/policy.yaml` 是实现后的目标策略 SSOT，
`ops/qa/deploy_rollout.yaml` 必须分别记录 repository readiness 与 observed live state。
文档批准不表示实现、部署或生产 DROP 已发生。

目标数据契约：

| 数据层 | 范围 | 生命周期 | 读取路径 |
|---|---|---|---|
| prod hot QA | 当前小时；失败时附加未归档 backlog | capture seal、raw commit 与 restore verification 全部成立后删除 | capture 与小时归档输入，不提供用户历史查询 |
| durable capture ledger | runtime、小时 seal 与未决 persist failure | 覆盖 hot QA 删除门禁所需窗口 | capture seal 的唯一持久化事实源；heartbeat 只镜像 |
| S3 raw archive | 所有 prod QA 元数据及脱敏 evidence | 7 天 | Export Worker 与受控 ops recovery |
| S3 QA Bundle | 指定 user、API key、archive watermark 的 24 小时投影 | 独立短生命周期 | 用户网页列表、详情与 ZIP 下载 |
| archive control | shard、segment、membership 与最终 gap receipt | 运维控制生命周期 | archive/DROP 事实与审计，不派生 capture 状态 |

## 2. 目标与非目标

### 2.1 目标

- prod capture 的持久化失败或静默停滞在 5 分钟内进入 durable capture ledger 与 hard-health；
- raw archive 是 QA 历史的唯一恢复源，commit 前必须完成 artifact 与本地 restore 验证；
- 每小时归档、分区供给、DROP 和热文件清理由一个 timer、一个 runner、一个 receipt/heartbeat owner 完成；
- 正常情况下只保留当前小时 hot partition，capture 未封口或 archive 失败绝不 DROP；
- 用户列表、详情和导出复用一个 S3 QA Bundle，不让浏览器访问 raw bucket；
- disk monitor 只报警，不自动提前删除未归档 QA，也不自动暂停 capture；
- Edge 不捕获、不归档、不清理、不导出，也没有 QA S3 权限；
- 代码、Story、policy、rollout、sentinel 和运维说明最终与本文件一致。

### 2.2 非目标

- 不修补或伪造已批准保持原状的历史缺失证据；
- 不恢复任意历史 backfill、第二 catchup worker 或通用 repair API；
- 不提供浏览器 raw S3 访问；
- 不在 prod 构建 ZIP、扫描 raw archive、代理大文件或补当前小时数据；
- 不提供用户主动删除 QA 的 UI/API；
- 不让 generic usage/ops archive 或 retention 工具接管 QA；
- 不建设 Prometheus、新 heartbeat 表、独立 watcher 或第二通知 owner；
- 不保留自动 destructive emergency 模式。

## 3. 当前基线与目标态边界

截至 2026-08-15，已上线 Phase 2 使用 `tokenkey-qa-maintenance.timer` 做 archive，
使用 `tokenkey-qa-boundary.timer` 做 24 至 25 小时 physical retention。该状态是迁移起点，不是最终目标。

目标实现前：

- 现有两个 timer 和 24 小时本地 retention 仍是线上事实；
- `production_recloseout_verified` 只证明 Phase 2 已验证，不能证明本设计已实现；
- `phase3_worker_observed_state: transitional_in_prod` 表示用户路径仍在 prod；
- 不得仅通过修改 rollout YAML 宣称 S3 Bundle、single owner 或 archive-gated DROP 已上线。

目标激活后：

- `tokenkey-qa-maintenance.timer` 是唯一 lifecycle owner；
- append-only `single_owner_activate` receipt 是不可逆 owner 边界；receipt 存在后 boundary success/rollback 均保持 disabled/inactive；
- `tokenkey-qa-boundary.timer` 和它的独立 host receipt/DB heartbeat 退役；
- 用户读取不再依赖 `qa_records`、`qa_blobs`、prod export pool 或 gateway download proxy；
- hot retention 由 raw archive 成功决定，不再由固定 24 小时年龄决定。

## 4. 单一 QA Policy

实现后的 `ops/qa/policy.yaml` 必须表达以下稳定事实：

```yaml
prod:
  capture_enabled: true
  maintenance_schedule_utc: "*:15"
  lifecycle:
    owner: tokenkey-qa-maintenance
    future_horizon_hours: 72
    drop_requires_raw_commit: true
    drop_requires_restore_verified: true
    drop_requires_capture_seal: true
  archive:
    shard_minutes: 60
    seal_delay_minutes: 15
    max_catchup_windows_per_run: 1
    s3_retention_days: 7
  user_qa:
    source: s3_qa_bundle
    prod_fallback: forbidden
  disk_monitor:
    automatic_cleanup: false
    pause_capture: false
edge:
  capture_enabled: false
  archive_enabled: false
  cleanup_enabled: false
  export_enabled: false
  s3_access: false
```

数值、路径、schedule 和 rollout state 只在 policy/rollout 中各有一个机器可读 owner。
文档解释语义，不复制第二套可变运行配置。

## 5. Prod 捕获与 hard-health

### 5.1 捕获范围

prod 对所有用户和 API key 的受支持 gateway 请求捕获脱敏 QA。`traj_export_enabled` 只控制用户读取能力，
不得过滤 capture 或 raw archive。capture 失败不能改变用户请求响应。

生产 `Submit` 必须忽略 caller 提供的 `CreatedAt`，在入队前用服务端可信时钟写入当前 UTC `captured_at`，并只从该值
派生不可变 `source_hour`。caller 不能把实时请求写回过去小时；测试通过注入 clock 控制时间，不在生产入口保留
历史时间 override。进入队列前先增加该小时的 process pending，执行时转为 inflight，最终转为 persisted 或 failed。

这条约束使小时封口很简单：UTC 小时切换后不会再接受属于上一小时的新 capture；上一小时已有的 pending/inflight
仍由同一进程计数，归零前不能生成 seal。稳态没有 DEFAULT；缺失当前小时 child 时持久化失败、用户请求继续，
同时进入 QA hard-health。

### 5.2 单一持久状态

capture 状态只有一个 durable owner：现有共享 QA data volume 下的 **durable capture ledger**，有效路径由 QA policy
管理。QA service 是 ledger 的唯一 writer，内容只保留两类原子 receipt：

- runtime/hour receipt：runtime identity、快照时间、每小时 pending/inflight、`sealed_at` 与最终 `drained`；
- failure receipt：按 `failure_id` 存放 `request_id/source_hour/stage/occurred_at/runtime_identity` 及
  `unresolved` 或 `recovered` 状态。

process counters 是生成 receipt 的运行时输入，不是第二份持久状态。小时切换后，QA service 只有在 ledger 中
所有与该小时相交的 runtime receipt 都新鲜、pending/inflight 为 0、没有 unresolved failure 且 transition clean 时，
才原子发布 hour seal；这同样覆盖蓝绿切换期间并存的 runtime。
普通成功不能清除 failure；只有按 source identity 证明 row 已存在才写 `recovered`。小时已排空但 identity 仍不存在时，
maintenance 把最终 `confirmed_gap` 写入 archive control，并阻止 DROP 等待人工决策。

任何 ledger 写失败都把当前 runtime sticky 标为不可 seal；后续成功快照或 heartbeat 不能清除它。只有写失败的同一
receipt 已持久化并完成所需精确对账后才能恢复 seal；若进程在此之前退出，缺失 clean drain 产生同样的 fail-closed 结果。

正常 shutdown 由 QA service 在停止接收、排空 worker 后写 `drained=true`；现有 shutdown 顺序先停
`OpsMetricsCollector`，因此 final receipt 不依赖 collector。新进程若看不到上一 runtime 的 clean drain，就在 ledger
记录 `runtime_discontinuity`，与该不确定时间窗相交的小时不得生成 seal。缺失、损坏、过期 ledger 或未知 transition
都只会阻止 DROP 并进入 hard-health；运行时不确定性本身不自动升级 P0。

`OpsMetricsCollector` 每分钟只把 ledger 当前结论镜像到 `qa_capture` heartbeat，供 Admin/Ops 查看。
heartbeat 只是 Admin/Ops 镜像，不参与 DROP 授权。`ops_error_logs` 只写 best-effort 诊断，也不用于重建状态。
即使 PostgreSQL QA 写入
同时导致 heartbeat/ops log 失败，shared-volume ledger 仍保留删除门禁事实。不新增 service、DB table 或 watcher。

状态只有三种：

- `healthy`：没有未决/confirmed-gap failure，也没有超过 5 分钟未收敛的 capture backlog；
- `degraded`：记录已持久化但 evidence 落入 DLQ，数据仍可恢复；
- `failed`：出现 persist failure，或 candidate/submitted 已前进而 persisted 连续 5 分钟无进展。

失败已解析为 `recovered` 后，连续 5 分钟健康才清除当前 hard-health；ledger receipt 保留到其 source hour 完成生命周期。
`confirmed_gap` 不自动清除，继续保留在 archive control，等待独立人工决策。
普通 capture failure 不直接发飞书。只有确认不可恢复 gap、未经归档的数据删除，或磁盘已经需要人工动作时，
才交给现有单一 P0 owner。

QA middleware 的路由接线由真实 router test 与 sentinel 守卫。运行时 heartbeat 不承担检测“代码已把整个
middleware 删除”的第二套职责。

## 6. 统一每小时 Maintenance

### 6.1 唯一 owner

目标态只运行 `tokenkey-qa-maintenance.timer`：

```text
HH:15 acquire QAMA lock
  -> ensure [current_hour, current_hour+72h) partitions
  -> archive + verify + restore previous sealed hour
  -> reconcile at most one oldest retryable post-cutover backlog hour
  -> read target-hour seal from capture ledger, lock child, and recheck seal + membership
  -> DROP each capture-sealed, committed, restore-verified source partition
  -> clean exact-hour Blob/DLQ
  -> write one atomic host receipt and one DB heartbeat
```

scheduled 与 operator execution 使用同一个 host runner、image、UID/GID、mount、scratch、lock、资源限制和
receipt path `/var/lib/tokenkey/qa-maintenance-last-run.json`。`forward_cutover` 继续限制自动 catchup 的下界；
历史 backfill 保持退休。

### 6.2 供给先于归档和 DROP

每轮先用数据库时钟确认当前及未来 72 小时精确 UTC-hour child 覆盖。供给、catalog coverage、lock timeout
或可识别锁竞争重试失败时，本轮在 archive 和 DROP 前失败。重试只覆盖 PostgreSQL lock contention；
非锁错误与 context cancellation 立即停止。

### 6.3 归档成功才 DROP

一个小时只有同时满足以下条件才可 DROP：

1. source child 是 catalog 直接 child，bounds 精确为该 UTC 小时；
2. durable capture ledger 存在该 source hour 的有效 seal，且当前 runtime receipt 新鲜、连续；
3. seal 证明该小时 pending、inflight 和 unresolved 均为 0，且没有与该小时相交的 `runtime_discontinuity`；
4. shard state 为 `committed`；
5. `restore_verified_at` 非空，S3 commit 与 control aggregate 一致；
6. DROP 事务先取得该 child 的 `ACCESS EXCLUSIVE` lock，再重新读取同一 ledger seal，并在同一事务内复核 committed
   membership；锁后 membership 覆盖 child 中仍存在的全部 source identities；
7. DROP 与 `source_partition_name/source_dropped_at` 在同一数据库事务提交。

capture seal 是删除授权，不是普通监控提示。ledger 缺失、过期、runtime identity 不连续、source-hour 状态未知，
或 lock 后 seal/membership 任一变化，都必须回滚并保留 partition。child lock 阻止目标小时残留写入，lock 后复核
证明归档覆盖一个稳定 source set；两者缺一不可。

任一条件失败都保留分区并写 hard-health。archive failure、S3 failure、missing/corrupt evidence 或 restore failure
绝不自动转成删除授权。及时的零记录小时也必须产生可 restore 的零行 commit 后才能 DROP。

正常 run 完成后只剩当前小时 child。整点到 `HH:15` 之间会短暂存在当前与上一小时两个 child；失败时还会
保留明确可见的 backlog。下一轮先处理正常上一小时，再最多补一个最老可重试 backlog，避免旧故障阻塞新小时。

### 6.4 DROP 后文件清理

数据库事务提交后删除匹配小时的 Blob/DLQ 目录。失败不会回滚已完成的 partition DROP；
`source_dropped_at` 驱动下一轮幂等续做并写 `hot_files_cleaned_at`。路径必须 canonicalize、限定在精确小时目录，
拒绝 symlink 与目录逃逸。

旧 prod export worker 与 `qa_exports_tmp` staging surface 已从目标仓库退役；maintenance 不保留 export-orphan cleaner。

## 7. Raw S3 Archive

沿用 Phase 2 已验证模型：immutable base/delta segments、manifest/checksum、ETag compare-and-swap commit、
read-after-write verification、local restore verification、bounded pagination 和 file-backed artifact construction。

`commit.json` 是 reader marker。看到 commit 仍不充分：reader 必须拒绝 missing/corrupt evidence、aggregate mismatch
和未 restore-verified shard。segment 在 commit 引用前不可见，失败/orphan membership 不能隐藏 source row。

archive normal window 是上一 sealed UTC hour。normal 失败时不运行 catchup。normal 成功后最多处理一个
`forward_cutover` 之后的最老 retryable backlog。raw archive 覆盖所有 prod 用户/API key，
不读取 `traj_export_enabled`。

已批准的历史 terminal gaps 保持不可变，不因新 retention 设计重新 backfill。新 gap 必须持久化、进入 hard-health，
并由单一 P0 owner 请求人工决策；不能靠到龄自动删除来隐藏。

## 8. 存储与备份边界

### 8.1 Raw archive bucket

raw bucket 私有、Block Public Access、KMS、VPC endpoint 和 CloudTrail data events 保持启用。
普通用户、浏览器和普通 API key 没有 raw 权限，prod API 不为 raw object 签发用户 URL。

### 8.2 User QA Bundle bucket

Bundle 使用独立 user/job prefix 与短生命周期。Export Worker 只读 raw archive 并只写授权 job prefix；
prod 只为校验过的 user/API-key prefix 签发短期 URL。

### 8.3 Ops recovery

ops workstation 通过独立 recovery role 直接 inspect、verify、restore raw S3，不经过 prod API、主机或数据库。
恢复正文必须显式确认隐私风险并写审计证据。

### 8.4 PostgreSQL backup

pgdump 继续排除 `qa_records*` data，保留 schema/control；该排除不是 lifecycle owner，raw S3 才是 QA 历史恢复源。

### 8.5 四类存储与备份边界

| 存储 | Owner | 是否可替代 raw QA |
|---|---|---|
| raw QA archive | `tokenkey-qa-maintenance` + archive control | 是，唯一历史恢复源 |
| user QA Bundle | Fargate Bundle Worker | 否，只是 user/key 投影 |
| generic usage/ops archive | data-layer owner | 否，必须拒绝 QA dataset |
| pgdump | database backup owner | 否，只保留 QA schema/control |

任何工具不得把 user export、generic archive 或 pgdump 误报为 raw QA 恢复源。

## 9. S3 QA Bundle 用户面

### 9.1 一个 Bundle 支持三种能力

用户打开 QA 页面时，prod 校验 JWT、`traj_export_enabled`、API key ownership 和 projectable platform，
然后创建或复用 `(user_id, api_key_id, archive_watermark, format_version)` 唯一 job。Fargate 从 raw S3 构建
可浏览的 committed Bundle：

```text
manifest.json
pages/<page>.json.gz
```

每个 page 按记录数和字节数双重限界，记录同时包含列表字段与完整详情。`manifest.json` 只包含 data window、
archive watermark、record count、page map、checksums 和格式版本。列表与详情读取同一 page。
Bundle 不创建 `records/*` 层。打开页面只等待 committed pages，不等待 ZIP。

用户点击导出后，prod 创建或复用 `(bundle_generation, export_format)` 唯一 export job；同一个 Fargate Worker
只读取已经 committed 的 Bundle pages，生成 `exports/<opaque-export-id>/export.zip`，不重新扫描 raw archive。
因此列表、详情和导出仍共享一个投影 owner，但大导出不会阻塞首次浏览。

### 9.2 原子发布与复用

Worker 先写不可见 generation，校验全部 pages 后最后发布 manifest。没有 committed manifest 的 partial generation
对用户不可见并由 lifecycle 清理。相同 user/key/watermark 复用已完成 Bundle；新 watermark 创建新 generation，
旧 Bundle 按独立短生命周期过期。

export job 只引用一个 immutable committed Bundle generation。ZIP 先写临时 object，校验后以 durable export job receipt
发布；失败或 partial ZIP 不改变 Bundle manifest，也不影响列表和详情。相同 Bundle generation/format 复用已完成 ZIP。

### 9.3 Prod 与浏览器边界

prod 只负责鉴权、创建/查询 job、校验 artifact 属于授权 prefix、签发短期 presigned URL。
prod 禁止读取 `qa_records`/`qa_blobs`、扫描 raw S3、构建 ZIP、代理大文件或补当前小时。

浏览器只访问 scoped Bundle object；JSON object 使用标准 HTTP gzip metadata，由浏览器原生解码。
手工调用 create/list/get/sign/download 时服务端重复校验 entitlement 与 ownership。page key 必须来自
已经 committed 的 immutable Bundle manifest；ZIP key 必须来自已经完成的 export job receipt；两者都不能接受客户端
拼接 raw key 或任意 object key。
关闭 `traj_export_enabled` 后不再创建 job、返回状态或签发新 URL。已经签发的 URL 只在自身短 TTL 内有效。

### 9.4 新鲜度

用户窗口固定为 latest complete Bundle watermark 之前的 24 小时。正常 `HH:15` 归档使 UI 落后当前时间约
15 至 75 分钟。API/UI 明确返回 `data_from`、`data_until` 和 `archive_watermark`；Bundle 失败只显示暂不可用/重试，
禁止回源 prod。

Bundle 是否完成不增加新的 hot partition DROP 门禁：DROP 仍严格要求 capture seal、raw commit 与 restore verification；
一旦三者成立，raw S3 已足以重试任意用户投影。

## 10. Disk Monitor 与 P0

自动 destructive emergency 被删除。磁盘使用率和剩余空间阈值只产生 durable hard-health 与单一 P0，
不会自动：

- 删除未归档 QA；
- 提前删除当前小时或 backlog partition；
- 暂停 capture；
- 切换到 row DELETE、TRUNCATE、rehome 或 generic cleanup；
- 创建第二通知 owner。

收到 P0 后由人工决定扩容、修复 archive，或对精确 confirmed gap 执行另行批准的不可逆操作。
任何删除未可靠归档 source 的操作都必须是独立高风险审批，不能隐藏在 timer 或 disk monitor 中。

## 11. 控制状态与健康

继续复用 `qa_archive_shards`、`qa_archive_segments`、`qa_archive_segment_records`、
`qa_archive_gap_decision_receipts`、`qa_lifecycle_receipts` 和 `ops_job_heartbeats`；不新增平行状态表。
durable capture ledger 独立承担 capture seal，archive control 与 heartbeat 不复制或反向推导它。

单 owner maintenance heartbeat/receipt 至少关联：

- run/window/image identity；
- partition coverage；
- normal 与 optional catchup archive outcome；
- commit/restore facts；
- source partition DROP 与 hot cleanup；
- attempts/lock retries；
- backlog 与 machine-readable failure reason。

systemd 只证明 timer/unit enabled、service 未 failed 或超时运行。新鲜度和业务结果以 host receipt、DB heartbeat
与 archive/control state 为准，不再依赖 `ExecMainExitTimestamp`。unit 安装必须 content-idempotent；内容未变化时
不重写 unit、不 `daemon-reload`。

`qa_capture` heartbeat 只镜像 ledger 当前健康结论。普通 degraded/failed health 进入 Admin/Ops 与自动诊断但不直接通知；
不可恢复 gap、未经归档删除或磁盘需要人工
动作才由现有单一 P0 owner 通知。

## 12. 失败语义

| 失败 | 结果 |
|---|---|
| capture persist | 用户请求不失败；5 分钟内 hard-health；风险记录保留 |
| raw archive / upload / commit | watermark 不前进；source partition 保留 |
| restore verify | 等同 archive failure；source partition 保留 |
| DROP transaction | 回滚；partition/control 保持原样 |
| post-DROP Blob/DLQ cleanup | partition DROP 保持；下一轮幂等续做 |
| Bundle build/publish | raw archive 不受影响；列表/详情暂不可用，可从 raw 重试 |
| ZIP build/publish | committed Bundle 不受影响；列表/详情继续可用，导出可重试 |
| disk threshold | hard-health/P0；无自动删除或 capture pause |
| confirmed irrecoverable gap | 等待独立人工决策；系统不自行授权 DROP |

总原则：raw S3 尚未可靠就保留本地 partition；raw S3 已可靠时，用户派生失败不阻塞本地清理。

## 13. 安全与隐私

- user job spec 由 prod 根据服务端事实固化，客户端不能提交任意 user、raw prefix 或 object key；
- Worker 不访问 prod 用户库，只消费已授权 job snapshot；
- cross-user、foreign/deleted/no-group/unprojectable key 一律拒绝且不创建 job；
- user Bundle 与 raw bucket 使用不同 prefix/bucket policy 和 lifecycle；
- prod gateway 不代理 raw evidence 或 ZIP；
- recovery role 与 Export Worker role 分离；
- 当前共享 EC2 instance role 不能被描述为进程级 IAM 隔离；
- logs、heartbeat、receipt 不写 request/response body 或 credential。

## 14. Cutover 与无搬运约束

现有 DEFAULT 已按 Phase 2 no-move cutover 移除，目标态不得重新创建。未来 schema 变化仍必须遵守：

- 不复制、不 move、不 rehome 旧 QA；
- 不创建 staging/dedupe migration path；
- parent 写入只能路由到精确 UTC-hour child；
- 缺 child 时 capture hard-health，而不是落到 DEFAULT；
- archive/export 从 parent/control/S3 读取，不依赖 child 名称作为真相。

Phase 2 的 exact `forward_cutover`、历史 terminal decisions 和 immutable S3 commits 保持不变。

## 15. 回滚

S3 Bundle 切换前可以回滚 UI/API 与 Worker，而不改变当前 hot retention。Bundle 切换后故障只返回暂不可用，
不能回退到 prod DB 重型路径。

single-owner 激活不假装跨两个 systemd unit 原子切换。唯一 host activation runner 必须执行 fail-closed 有序交接：

1. 取得同一 host lock，先 `disable --now` 独立 boundary timer，阻止新 run；
2. 等待 boundary service 自然结束；超时立即停线且禁止强杀，随后验证 timer disabled/inactive、service inactive、
   boundary receipt/heartbeat 不再前进；
3. 取得 QAMA database advisory lock，重新验证上述事实；
4. 最后在数据库事务中写 append-only single-owner activation receipt，maintenance 的 DROP phase 只认该 receipt；
5. 释放锁后等待下一个自然 `HH:15` maintenance，不手工启动 DROP。

任一步在 activation receipt 前失败，都保持 boundary disabled 且 maintenance DROP phase 未激活，即暂时没有删除 owner；
不得自动回开 boundary。receipt 提交后 boundary 永久禁止启用，maintenance 是唯一 DROP owner。该顺序允许短暂停删，
但任何时刻都不允许两个删除 owner。

第一次 archive-gated DROP 后，数据库历史不可回滚。回滚只能停止后续 DROP、修复 maintenance，
并继续从 raw S3 服务用户和 ops；不得重建 DEFAULT、恢复固定-age deletion 或把 S3 历史写回 prod。

## 16. 实施分解

### PR 1：非破坏性健康闭环

- 服务端可信 capture time、durable capture ledger 与 `qa_capture` heartbeat 镜像；
- 5 分钟 hard-health 和 Admin/Ops 可见性；
- systemd unit content-idempotent sync；
- evaluator 移除 `ExecMainExitTimestamp` 新鲜度依赖；
- 文档、Story、policy/rollout 和 sentinel 对齐。

PR 1 不改变 archive、DROP、用户读取或线上 owner。

### PR 2：S3 Bundle 用户面

- S3 QA Bundle job/Worker/artifact/API/UI；
- 用户列表/详情/导出切换到 Bundle，删除 prod 重型 export/read/proxy；
- list/detail committed Bundle 先就绪，ZIP 从 Bundle 懒生成；
- entitlement、ownership、presigned URL、真实 UI 与无 prod fallback 闭环。

PR 2 不改变 partition DROP、systemd lifecycle owner 或 disk emergency 行为。

### PR 3：唯一目标生命周期

- maintenance 内聚 partition provision、archive、restore、capture seal、DROP 与 cleanup；
- fail-closed activation runner 和 append-only single-owner receipt；
- 激活后退役独立 boundary timer/runner/receipt/heartbeat；
- 删除自动 emergency action，只保留 disk hard-health/P0。

PR 3 的代码发布不自动激活 DROP；生产激活仍需独立高风险批准。

## 17. 测试与验收

核心行为必须自动化：

- capture healthy、persist failure、5 分钟无进展、DLQ degraded 与恢复；
- caller `CreatedAt` 不能让生产 capture 进入过去小时；hour rollover 后不再出现新的旧小时 submission；
- unresolved failure 跨进程重启保留，普通成功不能清除；recovered/confirmed-gap 派生确定且可审计；
- clean drain 可继承健康；缺失 drain receipt 的 runtime discontinuity 持续阻止相交小时 DROP；
- ledger 缺失、损坏或过期均禁止 DROP；heartbeat/ops log 失败不丢失 capture gate；
- ledger 写失败后，后续成功快照不能清除 sticky unsealable；精确对账前始终禁止 DROP；
- router/sentinel 证明 QA middleware、durable ledger owner 与 heartbeat 镜像边界未漂移；
- local PostgreSQL + fake/S3-compatible store 验证 archive+restore 成功才 DROP；
- target-hour pending/inflight/unresolved、stale ledger、runtime identity drift 任一存在均禁止 DROP；
- child lock 后重检 seal 与 membership；archive、restore、catalog、membership、transaction 任一失败均保留 partition；
- DROP 后文件清理失败可幂等续做；
- Bundle generation 原子发布、watermark reuse、partial invisible；page 同时服务列表/详情，且不存在 per-record object；
- ZIP 从 committed pages lazy build，失败不影响列表/详情；
- entitlement、ownership、projectable platform、cross-user/object traversal 负向；
- prod API 不查询 QA source、不构建 ZIP、不代理下载；
- Playwright 经真实 UI 验证列表、详情、导出、watermark 和失败态；
- systemd 内容未变化时不 reload，健康判断不读取 `ExecMainExitTimestamp`；
- activation 每一步故障均保持 fail-closed，机械证明从未同时存在两个 DROP owner；
- sentinel 拒绝 DEFAULT、rehome、row DELETE、第二 owner、prod fallback 和自动 emergency deletion。

生产验收只能使用自然调度和显式审批的激活步骤；设计/开发阶段不写线上。

## 18. 上线与审批门禁

### 18.1 现状 owner → 唯一目标 owner → 退役门禁

| 能力 | 当前线上 owner | 唯一目标 owner | 退役门禁 |
|---|---|---|---|
| raw archive | `tokenkey-qa-maintenance.timer` | 同一 timer | 保留 Phase 2 恢复与证据契约 |
| partition provision/DROP | `tokenkey-qa-boundary.timer` | `tokenkey-qa-maintenance.timer` | Bundle 用户面验证、single-owner 人工批准 |
| user list/detail/export | prod DB/export worker | S3 QA Bundle | 一个真实 Bundle 经 UI 验证，无 prod fallback |
| disk protection | monitor + legacy emergency design | monitor + single P0 owner | 自动 destructive action 全部不存在 |

生产顺序：

1. 发布 PR 1，只观察 capture hard-health 与幂等 systemd 行为；
2. dark deploy Bundle Worker/API/UI，验证一个 user/key/watermark Bundle；
3. 切换用户读取并观察至少一个自然 archive + Bundle 周期；
4. 经独立高风险批准，执行 fail-closed 有序交接并提交 single-owner activation receipt；
5. 观察自然 archive -> restore -> DROP -> cleanup 周期后，确认 boundary runtime 退役；
6. rollout 才能把 repository/observed state 更新为 target verified。

任何步骤失败都停止推进，不通过手工触发 timer、DDL 或线上写入来伪造验证。

## 19. 迁移完成后的唯一运行图

```text
Prod gateway
  -> current UTC-hour qa_records + hourly Blob/DLQ
  -> durable capture ledger (capture seal owner)
  -> qa_capture heartbeat mirror (Admin/Ops; no DROP authority)

HH:15 tokenkey-qa-maintenance
  -> provision future partitions
  -> archive + verify + restore previous hour
  -> reconcile at most one backlog hour
  -> read target-hour ledger seal and lock/recheck seal + stable membership
  -> DROP only capture-sealed + committed + restore-verified source partitions
  -> clean exact-hour files
  -> one host receipt + one DB heartbeat

Raw S3 (private, 7d)
  -> Fargate builds committed full-record pages for list/detail
  -> export request lazily builds ZIP from those committed pages
  -> browser list/detail/export from scoped Bundle S3
  -> ops recovery role restores to isolated workstation

Disk monitor
  -> hard-health + one P0 owner
  -> no automatic deletion and no capture pause

Edge
  -> no QA capture/archive/cleanup/export/S3 access
```

## 20. 书面审批项

- [x] 只有 prod 捕获 QA，Edge 完全退出。
- [x] 用户列表、详情和导出全部来自 S3 QA Bundle，禁止 prod fallback。
- [x] 用户窗口为 latest complete watermark 之前 24 小时，允许约 15 至 75 分钟延迟。
- [x] raw archive 覆盖所有 prod 用户/API key，保留 7 天且不受 entitlement 影响。
- [x] maintenance 是唯一目标 lifecycle owner；独立 boundary timer 在激活后退役。
- [x] raw commit + restore verification 是 partition DROP 的必要条件。
- [x] DROP 还要求 durable capture ledger 的 target-hour seal；pending、inflight、unresolved 或 stale/unknown ledger
  均 fail closed。
- [x] 正常完成后数据库只保留当前小时；失败小时保留并 hard-health。
- [x] 稳态没有 DEFAULT、row DELETE、rehome、copy 或 move。
- [x] Bundle failure 不阻塞已可靠 raw archive 对应的 hot cleanup。
- [x] 自动 destructive emergency 删除取消；disk monitor 只做 hard-health/P0，不暂停 capture。
- [x] production `Submit` 使用服务端当前 UTC 时间，caller 不能把实时 capture 写入过去小时。
- [x] capture failure 在 5 分钟内进入 durable ledger；heartbeat 只做 Admin/Ops 镜像，未决状态跨重启保留。
- [x] Bundle page 同时包含列表字段和完整详情，不存在 `records/*`；ZIP 只从 committed pages 懒生成。
- [x] single-owner 采用先停旧 owner、排空、最后提交 activation receipt 的 fail-closed 交接，不伪称 systemd 原子切换。
- [x] 设计批准不授权线上写入、手工 timer、DDL 或生产激活。
