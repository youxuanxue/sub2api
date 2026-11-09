# 独立本地生命周期

2026-10-21 已完成生产启用与积压清理，见 [验收记录](../../docs/ops/qa-local-retention-20261021.md)。

`tokenkey-qa-provision.timer` 是本地预建任务：每小时滚动补齐当前起未来 72 小时，
开机后补跑；不依赖 S3 归档和旧 boundary timer。共享实现为
`backend/cmd/qa-local-maintenance` → `lifecycle.RunProvision`，安装/运行入口
`ops/qa/local_maintenance_host.py`，回执 `/var/lib/tokenkey/qa-local/provision.json`。
当前 prod 的预期本地任务由舰队清单 `local_qa_jobs` 登记，现有 HTTPS/job watcher 监控。
部署前先运行 installer plan，核对主机、二进制 SHA-256 与旧任务暂停状态，再 apply；
独立预建不需要发布或重启网关。

验证命令（Go 版本取自 `backend/go.mod`，Linux 二进制架构取自 Docker server；需要 Docker）：
```bash
make -C backend test-qa-local
```

该入口同时运行 Go、宿主与真实 PostgreSQL 测试，由 CI 的 `test-integration` 必跑；
仅修改本地 QA 宿主脚本或其测试也会触发后端检查。

`tokenkey-qa-cleanup.timer` 每 5 分钟清理已完整超过 24 小时的 QA 小时分区及对应
`qa_blobs`/`qa_dlq` 原始文件；用户已授权删除未归档的数据。每条记录至少保留 24 小时，
整小时清理通常保留 24–25 小时加调度延迟。每轮最多 48 小时，失败可重试；
回执为 `/var/lib/tokenkey/qa-local/cleanup.json`，计划回执为同目录 `cleanup-plan.json`。
installer 默认只计划（只读挂载）；`--apply` 才安装并执行。停止新的删除可停用 cleanup timer；
已删除的未归档数据无法恢复。预建与清理均不依赖 S3，清理容器只挂载两个 QA 热目录。

QA S3 已退役，2026-11-08 用户决定；现行路径为本地 24h + node1 每 4h。旧 `tokenkey-qa-maintenance`/`tokenkey-qa-boundary` 的代码、宿主脚本与巡检已删除；
2026-11-09 删除了 `require_legacy_paused()`：它要求主机上必须留着这两个单元的 mask 链接，全新主机因此会被拦住。两台主机换上不含这道检查的 bundle 之后，再删除旧的 mask 链接；先删链接会让旧 bundle 的清理失败。
历史 capture failure 和归档失败回执保留，不宣称全局 capture hard-health 已恢复。

## node1 小时归档

`node1_archive.py` 只读取已经封口的 `qa_blobs`/`qa_dlq` 小时目录，把文件复制到 node1
临时目录，逐文件校验 SHA-256、字节数和文件数后原子发布 `manifest.json` 与
`receipts/<environment>/<UTC-hour>.json`。目标目录默认保留 365 天的容量规划；本实现不做
QA PostgreSQL restore，也不删除源数据。`tokenkey-qa-archive.service/.timer` 只由
reviewed installer 生成，当前 `PHASES` 不包含 archive，因此本次代码不会安装或启用 timer。
安装器只有显式传入 `--include-node1-archive --apply` 才会把 archive 加入 bundle，并同时
把 node1 receipt 根目录接入 cleanup；普通 `--apply` 继续只安装本地 provision/cleanup。

本地 cleanup 在配置 `ArchiveReceiptRoot` 后把 node1 receipt 作为硬门禁：缺失、不可达、
格式错误或校验字段不完整的小时都会保留；不会为了维持 24 小时数字删除唯一副本。门禁按小时
判断：回执校验通过的小时照常删除，只保留缺回执的小时（2026-11-08 起；此前一个小时缺回执会
让整批都不删，两个推送失败的小时曾挡住 62 个已归档小时）。cleanup 回执的
`oldest_receipt_blocked_hour_utc` 记录最早被保留的小时，宿主 watcher 在它超过 24 小时时
按 `job:tokenkey-qa-cleanup` 告警，提示归档已落后。

# QA Operations

**当前运行策略（2026-11-08）：** QA S3 已退役，2026-11-08 用户决定；现行路径为本地 24h + node1 每 4h。capture、本地 provision/cleanup、node1 归档与数据库备份
继续运行；raw 记录不经产品 API 或 UI 暴露。策略为 `ops/qa/policy.yaml`，发布状态为
`ops/qa/deploy_rollout.yaml`，现行设计见
[node1 QA](../../docs/approved/design-tencent-cos-backup-node1-qa.md#node1-qa)。

已删除（仓库内不再有执行入口）：网关 `--qa-maintenance` / `--qa-boundary` / single-owner 子命令、
`backend/cmd/qa-archive` 恢复 CLI、`deploy/aws/stage0/tokenkey-qa-maintenance.sh` 等 raw 宿主脚本、
`stage0-qa-raw-archive.yaml` CloudFormation 模板、Phase 2 live health 与 raw archive IAM 巡检。
数据库中 S3 段与 gap 回执表已无代码读写，由后续迁移删除（须先把主机上的 `qa-local-maintenance`
换成不再 JOIN 段表的构建）；`qa_archive_shards`
保留，本地 cleanup 用 generation 0 行记录 `source_dropped_at` / `hot_files_cleaned_at`。
其中 2026-10 以前的 `s3_prefix` 指向已过期对象，仅为历史控制记录。

## 生产存储与恢复分界

历史 raw 对象（AWS `tokenkey-prod-qa-raw-archive-682751977094` 与 Hetzner fsn1）均按七天生命周期
自然到期，仓库已无恢复 CLI。历史切换与验收记录见 [qa-raw-only](../../docs/approved/qa-raw-only.md)
与 git 历史；新的 QA 副本只在 node1。AWS raw 栈与桶的删除由单独收尾执行。

## 巡检

| 检查 | 执行入口 | 判读 |
| --- | --- | --- |
| 数据盘与根盘容量 | 宿主 `tokenkey-disk-metrics.timer` 每五分钟；宿主 watcher 检查任务时效 | 核对真实挂载、用量与增长；QA 只按本地 24 小时规则清理 |
| 本地 provision/cleanup/archive | 宿主 `tokenkey-https-watch.timer` 每两分钟 | `local_qa_jobs` 中的任务必须按时成功；cleanup 回执的 `oldest_receipt_blocked_hour_utc` 超过 24 小时即告警 |
| 数据库备份 | 现行 pgBackRest 任务与宿主 watcher | 核对最近成功回执，见 [现网入口](../../deploy/hetzner/README.md) |

```bash
# 数据卷空间与账本增长；unknown 表示证据不足，不是容量安全。
set -o pipefail
bash ops/observability/run-probe.sh --target prod \
  --script ops/observability/probe-data-layer-capacity.sh --timeout-seconds 120 \
  | python3 ops/observability/data_layer_capacity_verdict.py
python3 scripts/checks/qa-lifecycle-ssot.py --self-test
python3 scripts/checks/qa-lifecycle-ssot.py
```

## QA 与备份锁冲突

本地 provision/cleanup 与 pg_dump 共享 `/var/lib/tokenkey/qa-lifecycle/host.lock`，排队规则见
[HCloud 运维入口](../../deploy/hetzner/README.md#qa-与备份排队旧-aws-定时任务收尾)。
2026-10-14 曾观测到旧 boundary 与 pg_dump 同时启动，随后分区创建因 lock timeout 失败；
这说明存在时间重叠，不能据此断言 pg_dump 是唯一数据库 blocker。

## 旧 capture 目录修复

cn-prod 的线上修复结果见 [验收记录](../../docs/ops/cn-qa-layout-repair-20261027.md)。

若宿主缺少 `qa_hourly_storage_cutover_utc`，新 capture 会继续写旧日目录，
小时清理任务成功也不代表这些文件已清理。`repair_capture_layout.py` 是一次性修复入口：
先 `plan`，核对主机、文件数和 cutover，再用输出的 SHA 执行 `apply`。两阶段均须传
`--expected-host`、`--app`、`--postgres`、`--data-dir`、`--plan`；apply 另传 `--plan-sha256`。
计划绑定运行中的网关及 PostgreSQL 容器身份、数据目录和半小时时效；
不得覆盖不同的既有 cutover。当前工具使用版本 2 计划，旧版计划须重新生成。

工具持有既有宿主锁和 QAMA 数据库锁，核对 capture 身份、时间及压缩文件 SHA，
按原始捕获小时创建硬链接，事务更新四个 URI 字段，提交成功后才移除旧目录项。
文件内容不变，过期删除仍由现有 cleanup owner 执行。最近一分钟的文件留待下一轮计划；
apply 另外核对已提交的数据库引用：保留期内无引用的文件保持原路径，回执记录
`deferred_files`，避免数据库延迟 INSERT 提交后引用已移除的路径。只有已完整超过
保留边界的孤儿文件可以无引用整理。在线修复后必须等待 capture 队列排空，再次核对旧路径、
新请求小时路径和所有保留引用，并观察 cleanup 回执。不要仅凭一次 apply 宣称完成。
旧小时过期文件也先整理为小时目录，不额外引入按 mtime 删除规则。

运行不需要重启网关，不恢复 S3，不清除历史 failure。工具应与
`local_maintenance_host.py` 一起放入独立修复目录，不覆盖已安装的生命周期 owner。
真实 PostgreSQL/libzstd 验证已接入 `make -C backend test-qa-local`，
修改修复工具或测试会触发后端 CI。可单独运行：

```bash
QA_LAYOUT_INTEGRATION=1 python3 -m unittest discover -s ops/qa -p 'test_repair_capture_layout*.py' -v
```

macOS Homebrew 的 libzstd 不在默认动态库搜索路径时，显式设置
`DYLD_LIBRARY_PATH=/opt/homebrew/lib` 后运行同一命令。
