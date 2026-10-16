# QA Operations

QA is raw-only. Production capture is sealed, archived to the private raw object
store, restore-verified, and cleaned by `tokenkey-qa-maintenance.timer`.

The lifecycle owner is `deploy/aws/stage0/tokenkey-qa-maintenance.sh`; policy is
`ops/qa/policy.yaml` and rollout state is `ops/qa/deploy_rollout.yaml`. Edge
capture and archive remain disabled. Raw records are never exposed through the
product API or user interface.

The approved contract is
[`docs/approved/qa-raw-only.md`](../../docs/approved/qa-raw-only.md). Production
switched new archive hours to Hetzner on 2026-10-13 and completed scheduled-archive
and isolated-restore acceptance. Existing AWS raw data was not migrated; it expires
under its original seven-day lifecycle with recovery access preserved.
AWS resource destruction requires separate approval.

## 生产存储与恢复分界

以下坐标来自生产切换验收回执，按 **UTC 归档小时**选择存储，不能按恢复当天或网关镜像版本选择。

| 归档小时 | 存储 | 恢复坐标 |
| --- | --- | --- |
| 截至 `2026-10-13T08:00:00Z` | AWS 旧 raw | bucket `tokenkey-prod-qa-raw-archive-682751977094`，region `us-east-1`，prefix `raw/v1` |
| 从 `2026-10-13T09:00:00Z` 起 | Hetzner 新 raw | bucket `tokenkey-qa-raw-fsn1-dd2ca9019b22`，region `fsn1`，endpoint `https://fsn1.your-objectstorage.com`，prefix `raw/v1` |

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

AWS 最后一次已核实写入为 `2026-10-13T09:18:49Z`，最后小时 commit 的 S3 `Expiration`
为 **`2026-10-21T00:00:00Z`**。这是已观测对象的到期边界，不是删桶时间：到期删除异步进行，
之后须重新只读核查对象清单、版本及未完成上传，并确认无新写入和其他依赖，再另行审批资源退役。

## AWS 新 prod 迁移后的维护恢复

2026-10-16 在现役 AWS prod 上恢复了原 boundary 与 maintenance timer，继续使用上述
Hetzner raw 存储和独立 maintenance pin（1.8.288），没有搬运旧主机 QA raw，也未启动退役 worker。
首次 GetObject 遇 503；再次执行在 02:28 UTC 完成新 `2026-10-16T00:00Z` 小时归档，
2,333 条记录、9,332 个 blob 引用，缺失 0，恢复验证通过；补偿旧小时也通过。
之后解除本次临时 DROP pause 并恢复 hourly timer；boundary 已验证未来 72 小时覆盖。
源端未迁入的原始文件不因恢复 timer 而补齐；历史 terminal gap 保留，不能宣称整个 QA 历史无缺口。
主机身份、备份验收及后续状态见 [AWS 迁移收尾](../../docs/approved/aws-migration-closeout-20261016.md)。

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
prod 与四个现役 edge 的 `/health`、prod 公开设置入口均为 200；各节点活动网关 healthy、重启计数为 0。
Edge capture 关闭，未发现 QA worker/queue 容器或退役的导出环境变量。

七个历史 `runtime_discontinuity` 与批准基线完全一致，无新增；当前 capture runtime 快照新鲜、
pending/inflight 为 0、新完成小时已有 seal。历史警告仍保留，不能据此宣称全局 capture hard-health
恢复。Phase 2 的 `catchup_terminal_gaps_present` 则是已接受的旧补归档缺口，与这七个采集警告是两类事实。

```bash
# 现行归档、分区及维护健康；默认只检查 Hetzner，不访问旧 AWS IAM。
python3 ops/qa/prod_phase2_live_health.py --json
# 需要审计历史 AWS raw 权限时显式追加：
python3 ops/qa/prod_phase2_live_health.py --legacy-aws --json
bash ops/observability/probe-release-control-plane.sh
```

上述检查从独立、停止的 maintenance pin 读取存储坐标并对照
`ops/stage0/qa-runtime-environment.sh` 的现行 Hetzner 契约；pin 缺失、坐标漂移或
未配置凭据文件时失败。凭据字段仅报告是否配置，不输出路径或密钥。
存储检查不执行上传或恢复；归档成功与恢复校验由维护回执和数据库记录证明。
仍须只读核对主机验收回执及 capture ledger 与批准基线。
新增 failure、过时快照、捕获积压、缺少 seal 或归档/恢复失败按 raw-only 契约处理，不能清除旧警告
来获取绿灯。`scan-edge-health.sh` 默认只看 Anthropic 账号池，空直连池不能代表整个 edge 宕机；
供应渠道状态与网关进程健康分别解释。

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
