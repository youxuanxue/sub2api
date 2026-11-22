---
title: prod 与 cn-prod 数据面从应用机拆出
status: approved
approved_by: "feng（2026-11-09 会话：同意 #3102 的设计，要求文档与最新实测保持一致）"
approved_at: 2026-11-09
risk: high
authors: [agent]
revised: 2026-11-21（prod 数据机目标升 4C16G：参数表与告警口径；不停写切流）
---

# prod 与 cn-prod 数据面从应用机拆出

## 决策

prod（腾讯美西）和 cn-prod（腾讯北京）各新增一台同地域私网数据机，只跑 Postgres 主库和 Redis。应用机只留 Caddy 和网关。两站用同一套布局、同一套切流步骤，逐站实施。

- 数据机不开公网入口。5432 和 6379 只放行本站应用机的私网地址。
- Redis 设密码。Postgres 走私网明文连接，这次不加 TLS。两站都是同账号私网，入口只放行一个来源地址，密码不经过公网。
- 数据机用普通 ext4，不用加密卷，两站一致。这是用户决策（2026-11-08）：这些数据暂时不需要加密卷。
- 不换托管数据库，不加连接池中间件，不加只读副本，不自动故障转移，这次不加第二台网关。
- 两站仍是两套独立的库，互不连接。
- 数据库参数在数据机首次启动时一并改好（见「数据库参数」）。现在的应用机不改参数，也不为改参数重启。

## 审批点

每一项单独批。批了一项不代表批了下一项。

1. 本设计。
2. 每站购买数据机的预算。申请时附套餐和月价。
3. 每站一次停写窗口。申请时附当站的流量低谷、演练测得的窗口时长和预计受影响的请求数。**某站未获停写批准，该站不停写、不切流。**
4. 删除旧数据目录、旧 Redis 和 prod 的旧 LUKS 载体。

第 1–3 项批准之前不购买机器、不切流、不重启现网。第 4 项批准之前不删旧数据。

审批状态：第 1–3 项已完成（两站数据机已开通；停写切流已于 2026-11-09/10 执行，网关分别指向 `10.2.4.5` / `10.4.0.3`）。第 4 项未批，旧数据目录 / 旧 Redis / 转发器先留着。

不做：同时加网关副本、托管库、PgBouncer、只读副本、自动故障转移、第三台只跑 Redis、数据机 LUKS、切流清空 Redis、两站同窗切、切库同时改连接池默认。

**现网容量（非本设计新决策）**：cn-prod `max_connections=400`、应用池 open=80 / idle=20；prod `max_connections=200`、两台 open=50、25。数字 SSOT：`docs/decisions/decided.md`、`design-cn-prod-rolling-deploy`、`ops/stage0/dataplane-cutover-data.sh`。

## 现网基线

切流前探针快照已删；现网身份与容量以 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md) / [`deploy/domestic/README.md`](../../deploy/domestic/README.md) 为准。

## 两站统一布局

- **数据机**：与应用机同地域、同账号、同私网。不跨云。数据目录 `/var/lib/tokenkey`，放在系统盘的普通 ext4 上，和 cn-prod 现在一样。磁盘大小见「数据机配置」。
- **容器**：`tokenkey-postgres`、`tokenkey-redis`。Postgres 大版本与当站现役一致，因为物理恢复要求同一大版本。
- **内存**：Postgres 和 Redis 各设容器内存上限，数值见「数据机配置」。
- **参数**：Postgres 和 Redis 用仓库里同一份 compose 启动，参数全部写成命令行 `-c`。只有和内存相关的值按站不同，从数据机 `.env` 读。
- **Redis**：AOF everysec，设 `maxmemory` 并用 `noeviction`。prod 现网是 `allkeys-lru`，内存满时会淘汰在途扣费 key；数据机统一用 cn-prod 的 `noeviction`，宁可写失败也不丢账。设 `requirepass`。cn-prod 沿用现有密码；prod 现网没有密码，切流时新生成一个，只存数据机和应用机的 `.env`。
- **防火墙**：云防火墙不开公网入站；主机 iptables 只放行本站应用机私网地址访问 5432 和 6379。
- **运维**：数据机注册自己的 SSM Hybrid，身份记在两份站点矩阵的 `data_machine` 对象里（prod `deploy/tencent/candidate-usw-20261022.json`，cn-prod `deploy/domestic/prod-target.json`），由 `ops/observability/data_machine_target.py` 读取并在下发前核对。备份 timer、归档检查 timer、身份续期 timer 和数据盘告警装在数据机上，应用机上的这几项停用。身份续期：`tencent_backup_identity.py` 和 `cn_backup_identity.py` 按矩阵里的数据机主机名分支，在数据机上读本机 root-only 的 `/etc/tokenkey/cos-backup.env`，原子重写容器只读挂载的 `/etc/tokenkey/pgbackrest-cos/pgbackrest.conf`（保持 `root:postgres 0640`）。轮换 COS 密钥只改数据机上的这一个文件。由 `ops/stage0/install-data-machine-identity.sh` 安装，带确认口令。
- **网关**：`DATABASE_HOST`、`REDIS_HOST`、`REDIS_PASSWORD` 从 `.env` 读，切流时填数据机私网地址。

## 数据库参数

数据机 Postgres 启动定义在仓库调优文件 / 数据机 compose：下表各项写成 `-c`，内存相关走 `.env`。命令行 `-c` 优先于恢复带来的 `postgresql.conf` / `postgresql.auto.conf`。期望值与 `max_connections` owner：`ops/stage0/dataplane-cutover-data.sh`。

### 建议值

内存相关按 **prod 目标 16 GB（升配后）**、cn-prod 8 GB（见「数据机配置」）。
prod **现网** 4C16G / PG 12G / `shared_buffers=4GB`（2026-11-22 升配）。脚本内的 `prod-4c16g` 为现网期望；默认（未设 profile）仍保留升配前 2C8G/6G 读数以便对照。`check` 适用于备库；在线主库按升配方案只读验收。

| 参数 | 安装默认 | prod 目标（4C16G） | cn-prod 现网/目标 | 理由 |
|---|---|---|---|---|
| `shared_buffers` | 128MB | **4GB** | 1GB | 约主机内存 25%；相对 12G 容器约 1/3，给 file cache 留余量 |
| `effective_cache_size` | 4GB | **10GB** | 2560MB | 规划器估算单条查询可利用的共享缓冲及 OS 缓存，不分配内存 |
| `work_mem` | 4MB | **16MB** | 16MB | 并发预算与单查询调整条件见下方升配方案 |
| `maintenance_work_mem` | 64MB | **512MB** | 128MB | 16G 主机上加快 vacuum/建索引；维护错峰 |
| `max_connections` | 100 | **200**（保留） | 400 | 高峰并发预算；与两 app 池之和 + 运维连接核算 |
| `jit` | on | off | off | 网关短查询不吃 JIT |
| `max_parallel_workers` / `per_gather` | 8 / 2 | **3 / 1** | 2 / 1 | 4C 留 1 核给 OS/Redis/checkpoint；单查询不占满 |
| `random_page_cost` | 4 | 1.1 | 1.1 | SSD |
| `effective_io_concurrency` | 16 | 200 | 200 | SSD |
| `max_wal_size` / `min_wal_size` | 1GB / 80MB | 4GB / 1GB | 同左 | 少被迫 checkpoint |
| `checkpoint_timeout` | 5min | 15min | 15min | 少全页写 |
| `wal_compression` | off | `lz4` | `lz4` | 镜像已编 lz4 |
| `tcp_keepalives_idle` / `interval` / `count` | 系统默认 | 30s / 10s / 3 | 同左 | 拆库后死连接占 advisory lock |
| `client_connection_check_interval` | 0 | 10s | 10s | 客户端断开后尽快停长查询 |
| `idle_in_transaction_session_timeout` | 0 | 10min | 10min | 防长事务挡 vacuum |
| `shared_preload_libraries` | 空 | `pg_stat_statements` | 同左 | 慢查询/临时文件归因 |
| `track_io_timing` | off | on | on | 低开销 |
| `log_lock_waits` / `log_temp_files` / `log_min_duration_statement` | off / -1 / -1 | on / 16MB / 1s | 同左 | 运维信号 |
| `timezone` / `log_timezone` | 本地 | `UTC` | `UTC` | 与网关/分区一致 |
| 容器 `shm_size` | 64MB | **1GB** | 1GB | 并行哈希 |
| 容器内存上限 | — | **PG 12G**；Redis maxmemory 512MB / Docker **1G** | PG 3G；Redis 256MB / Docker **512MB** | 主机留 ≥3–4G 给 OS+Redis+agent |

不改的：`wal_level`、`archive_mode`、`archive_command`、`archive_timeout`、`synchronous_commit`、`full_page_writes`、autovacuum 各项、`statement_timeout`、`lock_timeout`。两站的死元组比例都很低，autovacuum 不需要调（升配窗也不改）；全局 `statement_timeout` 会误伤迁移和清理任务。

网关连接池现网：prod 两台 open=50、25 / idle 各 10；cn-prod open=80 / idle=20（`deploy/domestic/compose.override.yml`）。
`usage_logs` 日分区与 `usage_billing_dedup` 保留见 [`billing-dedup-retention.md`](billing-dedup-retention.md) / `ops/migration/`。

## 数据机配置

| | prod | cn-prod |
|---|---|---|
| 身份 | `lhins-p43i2kxh` / `10.4.0.3` | `lhins-odyx5eis` / `10.2.4.5` |
| 套餐 | **现网 4C16G**（`bundle_starter_nmc_lin_lg16_01`，2026-11-22 低峰升配） | 北京轻量；**现网 4C8G**（`bundle_starter_mc_lg8_01`） |
| PG / Redis 容器上限 | **现网 PG 12G**；Redis maxmemory 512MB / Docker 硬上限 **1G**（2026-11-22 `docker update`） | PG 3G；Redis maxmemory 256MB / Docker 硬上限 **512MB**（2026-11-22 `docker update`） |

现网主机矩阵与 Hybrid 身份见舰队 / 国内 README。数据在系统盘 ext4；安全靠 pgBackRest→本站 COS（repo2）。不开公网入站；5432/6379 只放行本站应用机私网。轻量可原地升配（**需重启窗口，另批；本修订不停写切流**）。

### 数据机观测 / 告警口径（精准）

owner：`deploy/hetzner/tokenkey-disk-metrics-prod.sh`（data 机 timer 每 5min）。

| 信号 | 告警？ | 口径 |
|---|---|---|
| 主机 `MemAvailable` 使用率 | 是（已有） | warn 连续两 tick ≥90%；critical 单 tick ≥96%；带恢复配对 |
| 根盘 / 数据卷 df | 是（已有） | 磁盘将满 P0 |
| PG cgroup `memory.current/max` 单纯偏高 | **否** | file cache 顶满是稳态，不告 |
| PG cgroup **`anon`** | **是（新增）** | 不可回收压力：warn 连续两 tick ≥阈值 MiB；critical 单 tick；带恢复。默认 warn=1536MiB / critical=3072MiB（可用环境变量覆盖） |
| 只读探针 | 运维/发版后 | `ops/observability/probe-data-machine-memory.sh`；升配备料 `probe-data-machine-capacity-prep.sh`（均经 `run-probe.sh --target prod-data`） |

期望设置核对 owner：`ops/stage0/dataplane-cutover-data.sh`（`TOKENKEY_DATA_HOST_PROFILE=prod-4c16g` 启用升配后期望表；升配完成前勿设）。

升配目标、备料、非目标与停写另批：[`plan-data-machine-capacity-upgrade.md`](plan-data-machine-capacity-upgrade.md)。

## 切流与代码改动（已完成）

两站停写切流、客户端容器、备份/看门狗迁数据机均已完成。执行细则已删。
只读核对与应急重跑仍可用：`ops/stage0/dataplane-cutover-app.sh` / `dataplane-cutover-data.sh`（默认 `PHASE=check`；`apply` 需确认口令）。
删除旧应用机数据目录 / 旧 Redis / 转发器仍须单独批准（审批点第 4 项）。
prod 应用机 LUKS 已由 [`design-prod-entry-multi-app.md`](design-prod-entry-multi-app.md) 完成销毁。

## 回退

切流提升之后旧库不得再写成主库；已有新写入时只能从备份链恢复，不能把 Host 指回旧库。
两个主库同时往一个仓库归档视为故障。

## 第二台网关的门禁

两站都拆完之前，不部署第二台网关。拆完之后要在某站加副本，先满足下面三条，缺一条就不加：

- 现网 `RUN_MODE` 不是 `simple`。`simple` 会跳过清理锁，每台网关都会做清理。
- 网关台数 × 每色连接上限 × 2（蓝绿重叠），再加上备份和运维连接，放得进 `max_connections`。切流当时两站 `max_connections` 都是 200，每色上限 prod 50、cn-prod 40。**现网（2026-11-17）**：prod 仍 200 / open 50；cn-prod 为 400 / open 80（单容器滚动，无蓝绿重叠翻倍）。加副本前仍须按「台数×上限×重叠 + 运维」核算，不够先调上限或升 `max_connections`。Redis 连接池一并核算。
- `model_pricing.json` 和插件目录确认是每台机器自己的缓存，不是要在副本之间互相覆盖的事实来源。

账号调度和用量已经在同一个库里，多台网关共用这份状态。上游配额才是容量上限。

## 风险

停写排队/长流切断、跨机延迟、防火墙误配、旧库双写、去加密卷后快照可读、死连接占 advisory lock
（靠 `tcp_keepalives_*`）、冷 `shared_buffers`、cn-prod 配额满。审批与回退见上文。
