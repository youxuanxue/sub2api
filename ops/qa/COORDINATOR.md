# QA 数据机协调器

代码执行入口，不代表现网已切换。QA 小时结束满 24 小时后允许丢失并强制清理热数据；
node1 已归档数据的保留期不变。现网身份、连接参数由
[舰队入口](../../deploy/hetzner/README.md) 引用的受审配置提供；不要从本文示例猜生产地址。
此任务只影响 QA 维护，不控制网关容器、请求路由、计费、备份或节点摘除。

## 配置

所有 runtime config 必须 root-owned、0600，放在 root 管理的私有目录。应用机 SSH
凭据只授权指定数据机，建议 forced-command 固定 worker/config；固定 known_hosts，不能
用用户请求或网关配置提供 SSH 端点。config/hash 是完整性检查，不是签名或跨信任域授权。

应用机配置示例（替换全部示例身份与 SHA）：

```json
{
  "schema_version": 1,
  "hostname": "example-app",
  "environment": "prod",
  "source_id": "example-stable-source",
  "source_root": "/var/lib/tokenkey/data",
  "host_lock": "/run/lock/tokenkey-qa-source.lock",
  "binary": "/opt/tokenkey/qa-local-maintenance",
  "binary_sha256": "REPLACE_WITH_64_LOWERCASE_HEX",
  "receipt_root": "/var/lib/tokenkey/qa-source-receipts",
  "remote": {
    "key": "/root/.ssh/node1",
    "jump": "root@example-jump",
    "target": "root@example-node1",
    "remote_root": "/srv/tokenkey",
    "rate_kbps": 1024,
    "timeout": 1200
  }
}
```

`source_root` 是真实 app data 根目录，含 `qa_blobs`、`qa_dlq` 和 `qa_capture_ledger`。
先解析生产挂载的真实路径，不能误指空目录。归档 transport 复用现有 node1 SSH trust
配置，需在受控安装阶段核验应用机到 jump/node1 的 host key。带宽上限 20480 KiB/s，
建议从 1024 KiB/s 开始，结合请求延迟、磁盘余量和备份负载调整。

数据机配置示例：

```json
{
  "schema_version": 1,
  "hostname": "example-data",
  "environment": "prod",
  "binary": "/opt/tokenkey/qa-local-maintenance",
  "binary_sha256": "REPLACE_WITH_64_LOWERCASE_HEX",
  "state_root": "/var/lib/tokenkey/qa-coordinator",
  "host_lock": "/run/lock/tokenkey-qa-coordinator.lock",
  "ssh_key": "/root/.ssh/qa-app",
  "known_hosts": "/root/.ssh/qa-known-hosts",
  "max_hours_per_run": 4,
  "sources": [{
    "id": "example-stable-source",
    "ssh_target": "root@example-app",
    "worker": "/usr/local/lib/tokenkey/qa-coordinator/BUNDLE/source_worker.py",
    "worker_config": "/usr/local/lib/tokenkey/qa-coordinator/BUNDLE/config.json",
    "active_from": "2026-11-12T00:00:00+00:00"
  }]
}
```

每个 source 的 `worker`/`worker_config` 取该应用机 installer 输出。
`active_from` 必须是经过核对的、实际 capture 参与的首个 UTC 整小时；`active_until`
可选，是不再参与的首个小时。部分小时运行也算参与整个小时。旧节点退役只关区间，
不能删登记来绕过历史小时门禁。克隆旧磁盘的 capture ledger 与 source 身份须先独立核验。

数据机 EnvironmentFile 为独立 root-only PG* 配置，只含数据库连接变量；使用已有数据机
PostgreSQL 可达路径和适当 TLS/认证，不复制应用 .env/cloud 凭据。先以只读 inspect 核验
正确的主库、`public.qa_records` 分区键和 DB UTC；不能把 cn-prod 指向 prod。

## 安装和交接

1. 构建对应 Linux 架构的 `backend/cmd/qa-local-maintenance`，固定 SHA。备份当前 QA
   units、bundle 路径、回执和 source 历史区间。盘点旧 v1 小时和无 ledger 数据，明确单独
   验收/保留边界；新协调器不会把旧 v1 回执当作所有 source 完整，也不删除未知旧数据。
2. 应用机运行 `install_coordinator.py --role source --config SOURCE.json --binary BINARY`；
   核对 plan 后加 `--apply`。安装不启用或停用任何任务。先安装 source，再用输出路径生成
   数据机 config。确认权限、binary SHA、ledger 根目录和 SSH 固定身份。
3. 数据机运行相同 installer，参数改为 `--role data --environment-file PG.env`；先 plan，
   再 apply。两类主机都会安装 `tokenkey-qa-retention` units，数据机另有 coordinator units，
   此时所有 timer 仍未启用。确认资源限制和只维护 QA 的边界。
4. 交接时仅停止/禁用旧 QA provision/cleanup/archive timers，并等待 services 空闲；不停止
   capture、网关或备份。应用机 worker 会拒绝仍启用/运行的旧 owner。交接前手动补建未来
   72h 分区，并验证；预建失败会告警，但不连带阻止独立 source 清理。
5. 数据机先运行安装 bundle 的 `coordinator_host.py --config CONFIG`，检查待归档小时，
   再人工受控单轮 `--apply`，核验 node1 实际 manifest、seal 和按源隔离。
   各主机运行 `force_retention.py --role source|data --config CONFIG` 查看待过期数据，
   确认后单轮 `--apply`。核验缺源/归档故障也能清理、未满一天的数据保留、共享 DROP
   不伪造所有应用磁盘完成，以及中断重试。禁止用“服务退出 0”代替检查数据副作用。
6. 在相应 prod `host_watch` 或 domestic 配置显式登记
   `data_qa_jobs: ["tokenkey-qa-coordinator", "tokenkey-qa-retention"]`，应用机 jobs 也登记
   `tokenkey-qa-retention`，重新安装各主机 host watcher，验证缺失、
   失败及积压告警能送达；当前实现未更改现网登记。使用默认 state_root 以匹配 watcher 路径。
   确认无旧 owner 后，才批准在所有应用机和数据机启用 `tokenkey-qa-retention.timer`，
   并仅在数据机启用 `tokenkey-qa-coordinator.timer`。不能只开协调器、漏开各主机兜底。

协调器每轮最多归档 4 个小时，轮转处理保留期内的积压；未完成小时记入 pending。
完成小时及其历史参与源集合保存在 `completed.json`，参与源集合改变时重新核验。
过期小时不重新归档，也不作为待归档积压反复告警。归档启动和完成时均检查期限，跨过期限
不宣称成功。不得手工伪造 seal/receipt。

各主机 retention timer 开机 2 分钟后执行，此后每次结束 5 分钟后重跑，独立于协调器和网络。
应用机扫描 `qa_blobs/YYYY/MM/DD/HH` 与 `qa_dlq/YYYY/MM/DD/HH`，按小时结束满 24h 判断，
不依赖 binary、数据库、node1、seal 或 archive 锁；未知旧格式不盲删，交接时单独核对。
应用机遗留旧小时的迟到文件下轮继续回收，symlink 路径拒绝并告警，正常其它小时仍继续。
归档被杀后遗留在 receipt_root 的 `.push-YYYYMMDDHH-*` staging，也按同一小时期限清理，
不依赖归档再次启动；非 staging 的回执文件不受影响。
删除意图写 journal，结果写 `SOURCE_ROOT/qa_retention_state/last-retention.json`；写审计遇到
ENOSPC 也继续释放 payload，但报告错误。该结果从不声称 archive_verified。

数据机 native `expire-shared` 每轮限量删除过期小时，记录 terminal-gap 与 DROP 状态，
不触碰应用机文件或写全站 `hot_files_cleaned_at`。复用 advisory lock、100ms lock timeout 与事务复核。
结果写 `state_root/last-retention.json`；锁竞争或备份让行后下一轮重试，不能承诺到点立即释放。
DB 写入需取得备份 operator lock，距下一次备份不足 6 分钟则让行；raw 传输不持备份锁。

## 回退

先在所有主机停止 retention timer，再停止数据机 coordinator timer，并等待服务/worker 空闲。
仅停 coordinator 不会停本地强制删除。旧单源 owner 不能直接拿 v2 回执
恢复清理；只有重新核验 source 范围、旧 v1 小时与挂载后才能恢复旧任务。切换 owner 不
重启网关。node1 已发布归档保留，已经删除的热文件/分区不能靠停 timer 自动恢复。
强制过期策略已由用户批准，不使用任意 `allow_gap` 配置；只有这里固定的一天兜底可绕过归档门禁。

## 验证

`make -C backend test-qa-local` 运行策略/worker/installer 测试、原生 ledger/lifecycle
测试和真实 PostgreSQL 18 删除验收；host watcher 测试在 observability 门禁执行。
无 UI 改动，不将这些测试称为浏览器 e2e。线上 SSH、systemd 安装与端到端交接需现场验收。
