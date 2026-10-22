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

S3 上传、旧 boundary/maintenance 继续暂停；旧 DROP pause 保持原值，独立清理执行本次明确授权的
24 小时本地保留规则。未来恢复上传需重新核对 owner 和可用源数据，不能补归档已删除的数据。
历史 capture failure 和归档失败回执保留，不宣称全局 capture hard-health 已恢复。

# QA Operations

**当前运行策略（2026-10-21）：** 用户已暂停向 AWS S3 上传 QA。现网矩阵
`deploy/tencent/candidate-usw-20261022.json` 的 `host_watch.paused_jobs` 是停用期望 owner：
`tokenkey-qa-maintenance`（上传暂停）与 `tokenkey-qa-boundary`（历史归档验收问题保持停用，
DROP pause 保留）均应 disabled/inactive；capture、本地预建/清理与数据库备份继续运行。宿主 HTTPS watcher
按该清单期望停用态，意外重新 enable 会告警。boundary 列入 `paused_jobs` 只声明巡检期望停用，
不等于把未验收清理标成正常。暂停清单是 boundary 是否应运行的唯一真相：`qa_phase2` 判定在 boundary 被暂停时
不论 qa_records lifecycle（含 finalized）都只要求 boundary 停用，不再要求 boundary 回执。配置中的 AWS S3 坐标代表恢复/未来归档能力，不代表允许上传。
普通发布和宿主脚本更新应保持原 timer 状态；恢复上传或重新启用 boundary 须明确授权并
同步暂停 owner 与 watcher 配置。以下带日期的存储验收是历史记录，不证明当前归档任务在运行。

QA is raw-only. When explicitly enabled, production capture is sealed, archived to the private raw object
store, restore-verified, and cleaned by `tokenkey-qa-maintenance.timer`. Uploads are currently paused.

The paused archive owner is `deploy/aws/stage0/tokenkey-qa-maintenance.sh`; the active local
provision/cleanup owner is described above. Policy is
`ops/qa/policy.yaml` and rollout state is `ops/qa/deploy_rollout.yaml`. Raw
records are never exposed through the product API or user interface.

The approved contract is
[`docs/approved/qa-raw-only.md`](../../docs/approved/qa-raw-only.md). Production
switched new archive hours to Hetzner on 2026-10-13. Following the AWS host
replacement, the user authorized returning new daily archives to AWS S3 on
2026-10-16. Historical QA is not migrated; both stores keep their original
seven-day lifecycle and recovery access.
AWS resource destruction requires separate approval.

## 生产存储与恢复分界

以下坐标来自生产切换验收回执，按 **UTC 归档小时**选择存储，不能按恢复当天或网关镜像版本选择。

| 归档小时 | 存储 | 恢复坐标 |
| --- | --- | --- |
| 截至 `2026-10-13T08:00:00Z` | AWS 旧 raw | bucket `tokenkey-prod-qa-raw-archive-682751977094`，region `us-east-1`，prefix `raw/v1` |
| 已在 Hetzner 提交的小时：`2026-10-13T09:00:00Z` 起，最后为 `2026-10-16T03:00:00Z` | Hetzner 历史 raw | bucket `tokenkey-qa-raw-fsn1-dd2ca9019b22`，region `fsn1`，endpoint `https://fsn1.your-objectstorage.com`，prefix `raw/v1` |

新 AWS prod 的未提交及新增小时使用上述 AWS 桶；以实际 committed 回执确认，不能将表中的区间理解为小时完整性承诺。历史 terminal gaps 保留。

AWS 恢复沿用 `backend/cmd/qa-archive/` 的 `--workstation` 入口，显式传旧 bucket、region、
raw 栈输出 `QaRawArchiveRecoveryRoleArn` 和 `--recovery-run-id`；旧 KMS 与恢复角色须保留。
Hetzner 恢复使用同一 CLI 的 `verify` / `restore` 与 Hetzner storage 配置，凭据从受保护文件
读取；`--workstation` 是 AWS 角色入口，不能用于 Hetzner。`restore` 仍需窗口绑定的隐私确认，
输出到新建的独立恢复目录；不能把恢复文件写回在线 capture 目录或启动网关。
此前少量预复制对象不构成完整历史副本，旧小时仍以 AWS 为恢复来源。

切换验收已记录在 prod `/var/lib/tokenkey/qa-release/verified.json`，原始回执位于
`/var/lib/tokenkey/qa-storage-cutover-20261013T09`。运维取证副本保存在本机受保护的
`~/.local/state/tokenkey/qa-raw-migration-20261013/`；不提交凭据、恢复内容或主机 `.env`。

- `09:00`、`10:00` 两个新小时由真实 timer 归档，均为 `committed`、`restore_verified=true`、
  缺失 blob 为 0；回执为 `scheduled-hour-09-receipt.json` 与 `scheduled-hour-10-receipt.json`。
- 新 `09:00` 小时经独立目录恢复，`verified=true`、缺失 blob 为 0；回执为
  `new-target-isolated-host-restore.json`。验收后临时恢复文件已清理，源对象未删除。
- DROP pause 已解除，原 boundary timer 已恢复。QA 操作未重启或切换 gateway、Caddy、
  主 PostgreSQL/Redis；同期独立网关发布的验收不由 QA 存储验收代替。

本次回切前，AWS 最后一次已核实写入为 `2026-10-13T09:18:49Z`，最后小时 commit 的 S3 `Expiration`
为 **`2026-10-21T00:00:00Z`**。这是已观测对象的到期边界，不是删桶时间：到期删除异步进行，
本次日常归档重新使用该桶，旧对象到期不再构成退役依据。任何未来销毁仍需确认无新写入、恢复和其他依赖后另行审批。

## AWS 新 prod 迁移后的维护恢复

2026-10-16 在现役 AWS prod 上恢复了原 boundary 与 maintenance timer，继续使用上述
Hetzner raw 存储和独立 maintenance pin（1.8.288），没有搬运旧主机 QA raw，也未启动退役 worker。
首次 GetObject 遇 503；再次执行在 02:28 UTC 完成新 `2026-10-16T00:00Z` 小时归档，
2,333 条记录、9,332 个 blob 引用，缺失 0，恢复验证通过；补偿旧小时也通过。
之后解除本次临时 DROP pause 并恢复 hourly timer；boundary 已验证未来 72 小时覆盖。
源端未迁入的原始文件不因恢复 timer 而补齐；历史 terminal gap 保留，不能宣称整个 QA 历史无缺口。
主机身份、备份验收及后续状态见 [AWS 迁移收尾](../../docs/approved/aws-migration-closeout-20261016.md)。

### 日常归档回归 AWS S3

用户明确不迁历史 QA。2026-10-16 09:00:31 UTC 已将独立 pin 的日常目标改为 AWS S3，仍用 1.8.288 原镜像；采用 EC2 实例角色，IMDSv2 required、hop limit 2。私有 raw 桶、KMS 和七天生命周期复用已有配置；已从容器网络实际验证 Put/Get、内容 SHA 和 KMS 解密。

用户要求停止 Hetzner 任务后，已停 maintenance service/timer，确认无上传容器残留。停止时 `07:00 UTC` 分片已 verified 但尚未提交（旧桶 commit HEAD=404）；保存完整控制行后仅将这一未提交尝试标为 orphaned，旧对象、源记录和身份关联保留。同一小时从新 prod 本地 raw 重新归档到 AWS，不复制历史对象、不伪造完成或改变 forward cutover。旧 `00:00/02:00/03:00` 已提交小时没有未覆盖源记录。

原 pin/env/runner 备份及切换回执在 prod `/var/lib/tokenkey/qa-aws-storage-cutover-20261016/`；未提交分片备份在 `/var/lib/tokenkey/qa-storage-abandoned-segment-20261016/`。安装脚本回滚不得自行恢复 Hetzner 上传。新增 `2026-10-16T07:00Z` 小时于 09:03:32 UTC 在 AWS 提交并通过恢复校验：4,167 条记录、16,668 个 blob 引用、缺失 0；整轮约 2 分 18 秒。随后独立目录恢复通过，耗时约 47 秒，回执 `isolated-restore.json`。这些结果证明新存储可写可恢复，不代表 QA 整体健康。

整轮维护因历史小时 `2026-10-15T09:00Z` 的 `commit_existence_unknown` 返回失败。
当时只查新 AWS 目标，本地源记录和分片为 0、HEAD 返回 403，不能证明原归档不存在。
2026-10-17 复核已在原 Hetzner 存储验证该小时的完整 commit、3,070 条记录及 12,280 个
blob 引用，缺失 0，见 [历史归档复核回执](../../docs/ops/tokyo-qa-history-review-20261017.json)。
因此撤回“空窗口”判断及单桶 gap 方案，未执行 gap-apply，未修写东京控制行，未授权删除。
东京元数据仍报失败；恢复归档链路的上传/清理前须对齐历史小时的存储归属，不能把原存储的有效归档
分类成 terminal gap。该回执证明复核时对象完整，七天生命周期仍生效，不保证未来永久可读。

后续 `08:00Z` 小时也由 timer 完成 AWS 归档与恢复校验：2,947 条记录、11,788 个 blob 引用、缺失 0，整轮约 2 分 19 秒。09:21:27 UTC 已通过既有 release-state owner 记录**仅存储切换验收**，其中明确保留历史补偿失败；解除本次 DROP pause 并恢复 boundary timer，原有逐小时 seal、归档及 restore 门禁继续生效。验收副本为 `qa-aws-storage-cutover-20261016/storage-accepted.json`。旧 Hcloud prod 的两个 QA timer 均已 disabled/inactive，旧 pin 未运行。本次隔离恢复临时目录已清理，恢复回执和对象保留。

## 暂停期间的巡检

复用已有监控入口，不另建一套 QA 定时任务：

| 检查 | 已有执行入口 | 暂停期判读与处置 |
| --- | --- | --- |
| 数据盘与根盘容量 | 宿主 `tokenkey-disk-metrics.timer` 每五分钟；宿主 watcher 检查任务时效 | 核对真实挂载、用量与增长；QA 仅按已授权的本地 24 小时规则清理，不额外缩短保留期 |
| 上传保持停用 | 宿主 `tokenkey-https-watch.timer` 每两分钟 | maintenance timer disabled/inactive，service 不运行；监控显示 `intentionally paused`，历史 failed 回执仍保留 |
| 采集、当前及未来分区、过期分区 | `Ops Daily Diagnostics` 每日 UTC 02:00，复用下方 Phase 2 与 capture 检查 | 当前分区不得缺失，未来覆盖满足 owner 要求；独立 provision/cleanup timer 由宿主 watcher 检查，旧 boundary 回执不代表它们的执行结果 |
| 数据库备份 | 现行 pgBackRest 任务与宿主 watcher | 核对最近成功回执；旧 pgdump 已退役，备份范围与恢复证据见 [现网入口](../../deploy/hetzner/README.md) |

可重复执行的只读复核（仓库根目录运行）：

```bash
# 分区、暂停状态、存储身份；退出非零时保留并解释 reasons，不能写成整体健康。
python3 ops/qa/prod_phase2_live_health.py --json
# 数据卷空间与账本增长；unknown 表示证据不足，不是容量安全。
set -o pipefail
bash ops/observability/run-probe.sh --target prod \
  --script ops/observability/probe-data-layer-capacity.sh --timeout-seconds 120 \
  | python3 ops/observability/data_layer_capacity_verdict.py
```

账本增长预测不包含 QA raw 增长，不能拿它估算暂停上传后还能撑多久；需同时查看数据卷实际用量。
已有 CloudWatch 磁盘 alarm 保留，但本次读取的 AlarmActions 为空，不能把状态 `OK` 当作
告警消息送达证明。宿主脚本具备独立通知路径，凭据存在；本轮未发送合成测试通知。

2026-10-17 14:49–14:52 UTC 只读基线：数据盘使用 25%，可用约 105 GiB；capture healthy、
pending/inflight/unresolved 为 0；未来 72 小时分区无缺口。14 个过期分区仍保留，
Phase 2 返回 `boundary_host_receipt_missing` 与 `qa_records_expired_partitions_attached`，
不是因主动暂停上传而误报。宿主磁盘 timer 已 enabled/active，watcher 已识别上传暂停；
每日 workflow 为 active，最新已完成轮次早于暂停感知代码合并，不能宣称新版每日任务已跑过。

采集继续增长，所以暂停不代表可以无限期搁置。若当前/未来分区出现缺口、capture 堆积或磁盘
逼近压力阈值，应先检查独立 provision/cleanup 的失败回执、锁冲突与积压；按上述入口修复并重跑。
不要启动旧 boundary、恢复上传或解除旧 DROP pause；超出本地 24 小时策略的删除仍须另行授权。

## QA 与备份锁冲突

宿主脚本升级后，boundary、archive 与 pg_dump 共享生命周期 host.lock，排队规则和
安装/回退见 [HCloud 运维入口](../../deploy/hetzner/README.md#qa-与备份排队旧-aws-定时任务收尾)。
2026-10-14 曾观测到 boundary 与 pg_dump 同时启动，随后分区创建因 lock timeout 失败；
这说明存在时间重叠，不能据此断言 pg_dump 是唯一数据库 blocker。

排队超时以 `lifecycle_lock_busy` 失败并写 QA 回执，未获取锁不启动维护子容器。
更新代码本身不修复历史回执；安装后应观察正常 timer 的成功回执、未来分区覆盖及过期分区清理，
并继续检查当前 capture。数据库短锁超时和现有 seal/restore/DROP 门禁保持原契约。

## 持续健康检查

2026-10-13 的后续只读复查确认：新 `12:00 UTC` 小时定时归档成功、恢复校验通过、缺失 blob 为 0；
maintenance 与 boundary timer 正常，未来分区覆盖完整、无过期分区残留及热文件清理积压。
prod `/health` 与公开设置入口均为 200；活动网关 healthy、重启计数为 0。
未发现 QA worker/queue 容器或退役的导出环境变量。

七个历史 `runtime_discontinuity` 与批准基线完全一致，无新增；当前 capture runtime 快照新鲜、
pending/inflight 为 0、新完成小时已有 seal。历史警告仍保留，不能据此宣称全局 capture hard-health
恢复。Phase 2 的 `catchup_terminal_gaps_present` 则是已接受的旧补归档缺口，与这七个采集警告是两类事实。

```bash
# 现行归档、分区及维护健康；包含容器网络中的 EC2 实例角色可达性。
python3 ops/qa/prod_phase2_live_health.py --json
# 同时审计 AWS raw 桶/KMS/IAM 策略（兼容保留的 flag 名）：
python3 ops/qa/prod_phase2_live_health.py --legacy-aws --json
bash ops/observability/probe-release-control-plane.sh
```

上述检查从独立、停止的 maintenance pin 读取存储坐标并对照
`ops/stage0/qa-runtime-environment.sh` 的现行 AWS 契约；pin 缺失、坐标漂移、意外配置静态凭据或
容器网络无法取得 IMDSv2 角色身份时失败。只报告可用性，不输出 token、凭据路径或密钥。
存储检查不执行上传或恢复；归档成功与恢复校验由维护回执和数据库记录证明。
仍须只读核对主机验收回执及 capture ledger 与批准基线。
新增 failure、过时快照、捕获积压、缺少 seal 或归档/恢复失败按 raw-only 契约处理，不能清除旧警告
来获取绿灯；供应渠道状态与网关进程健康分别解释。

Useful checks:

```bash
python3 scripts/checks/qa-lifecycle-ssot.py --self-test
python3 scripts/checks/qa-lifecycle-ssot.py
python3 -m unittest ops.stage0.test_prod_component_release
```

Raw 生命周期的保留运维入口：

| 操作 | Owner | 条件 |
| --- | --- | --- |
| 单一 owner 计划与激活 | `ops/stage0/activate-qa-single-owner-via-ssm.sh` | 先 plan；核实 capture seal、restore 与 DROP pause，再使用该 plan hash 的明确确认令牌 activate |
| 暂停 DROP 与读取维护 pin | `ops/stage0/prod_release_state.py` | 仅独立迁移/接受验证，普通 gateway 发布不调用 |
| 定时维护发布 | `ops/stage0/sync-qa-maintenance-timer-via-ssm.sh` | 独立维护 pin 与受限子容器，健康验收通过后才确认配置 |
| 合成 Object Storage 兼容性探针 | `ops/qa/probe_hcloud_object_storage.py` | 显式 opt-in，仅专用合成 bucket；不证明生产迁移完成 |

普通 gateway 发布保留独立 maintenance pin 与上述存储坐标；不重做切换，也不重启旧 Bundle/worker/queue。
