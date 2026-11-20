---
title: prod 与 cn-prod 数据面从应用机拆出
status: approved
approved_by: "feng（2026-11-09 会话：同意 #3102 的设计，要求文档与最新实测保持一致）"
approved_at: 2026-11-09
risk: high
authors: [agent]
revised: 2026-11-20（两站切流已完成；删除停写窗口执行叙事，保留布局/参数与脚本 owner）
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

审批状态（2026-11-10）：第 1 项（本设计）已批准。第 2 项：两站数据机已开通。第 3 项：cn-prod 停写窗口已于 2026-11-09 22:05 CST 执行，网关指向数据机 `10.2.4.5`；prod 停写窗口已于 2026-11-10 07:07 CST 执行，网关指向数据机 `10.4.0.3`，新主库 timeline 4 的 WAL 已向 COS 归档。第 4 项未批，两站旧数据目录、旧 Redis 和转发器都先留着。

**现网补充（2026-11-17，非本设计新决策）**：cn-prod 数据机 PG 现网 `max_connections=400`（审批 `docs/ops/approvals/cn-prod-pg-max-connections-200-to-400.md`）；应用池 `DATABASE_MAX_OPEN_CONNS=80` / idle=20（`deploy/domestic/compose.override.yml`）。prod 仍为 `max_connections=200`、应用 open=50。切流当时「两站都 200 / cn-prod 40」的叙述见下文「数据库参数」历史建议，勿当作今日 cn-prod SSOT。

## 乔布斯复审

> 审阅标准：聚焦、简洁、端到端、设计即工作方式、精品。对一百件好事说不。

结论：方向成立。要交付的是：应用机出事时，账本和会话不再跟着消失。第二台网关是拆完之后才允许讨论的下一件事。

### 留下的

1. **一个失败域拆开。** 应用机重启、宕机或重装时，Postgres 和 Redis 还在。网关内存打满现在只会杀掉容器（#3101），但整机故障仍会带走库。
2. **一台新机器，两个进程。** Redis 和 Postgres 放在一起，用容器内存上限隔开。再买一台只跑 Redis 的机器就多一套运维，而它不对应任何已发生的故障。
3. **沿用已经在跑的备份。** 两站的 pgBackRest 全量、增量和 WAL 跟着新主库走。新主库是唯一归档者。
4. **两站一套做法。** 同样的布局、容器名、脚本和切流步骤。一站踩过的坑，另一站不再踩。
5. **应用侧只改连接地址。** 部署模板把 Host 和 Redis 密码从写死改成从 `.env` 读，不动业务代码。

### 砍掉的

| 提议 | 判决 | 理由 |
|---|---|---|
| 这次同时加网关副本 | **否** | 副本解决的是吞吐，不是「库被网关陪葬」 |
| 腾讯云托管数据库 | **否** | 要另起备份链和恢复演练。现有链今天还在用 |
| PgBouncer | **否** | 清理和迁移使用会话级 advisory lock，事务池会拿错锁 |
| 只读副本、按用户分库 | **否** | 库不是当前瓶颈。两站独立已经是地域上的水平拆分 |
| 自动故障转移 | **否** | 应用没有主库发现。一台新主库不是高可用 |
| 第三台机器只跑 Redis | **否** | 会话跟着账本走即可。单独的 Redis 集群是以后的事 |
| 数据机继续用 LUKS | **否** | 用户决策：这些数据暂时不需要加密卷，和 cn-prod 一致。加密卷让每次开机都多一个取钥依赖，取钥失败库就起不来 |
| 切换时清空 Redis | **否** | Redis 里有在途扣费预留（`billing:inflight*`）和未结算的 Grok 视频计费（`grok_video_pending:*`）。清空就是错账。用复制迁移 |
| 两站同一个窗口切 | **否** | 一站出问题时，另一站要还在。逐站切 |
| 切库的同时改连接池默认值 | **否** | 那是第二台网关的门禁，不是这次的代码 |

### 完成定义

每一站都满足：应用机上的网关被杀死或应用机重启之后，数据机上的 Postgres 仍接受连接，已提交的账还在，Redis 里的会话和在途扣费还在。应用机断电后重新起来的网关能正常启动，不会被旧连接留下的锁卡住。新主库自己产出过一份成功的全量备份，归档在推进。

验证不拿现网重启。先在数据机上核对结构：Postgres 和 Redis 不挂载应用机的任何目录，不依赖应用机上的任何服务。再在蓝绿发布中观察：旧色被停止时，数据机上的连接数和归档都不中断。整机重启的实证要放到另行批准的维护窗口里做。

两站都满足才算完成。不宣称多副本，不宣称数据库高可用，不宣称零停机。

## 现网基线

两站数据面切流已于 2026-11-09/10 完成（见文首审批状态）。切流前探针快照已删除；现网身份与容量以 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md) / [`deploy/domestic/README.md`](../../deploy/domestic/README.md) 为准。

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

### 现状

两站的 Postgres 基本还是安装默认值。仓库里已有调优文件 `deploy/stage0/docker-compose.prod-pg.yml`（`shared_buffers` 1GB、`jit` off 等），但两站都没用上：prod 的 PG 容器启动命令只有 `postgres`，参数散落在数据目录的 `postgresql.conf` 和 `postgresql.auto.conf` 里；cn-prod 只用命令行 `-c` 设了归档相关的四项。

2026-11-08 只读实测（prod 的统计从 11-08 15:04 重启后开始，约 6.4 小时；cn-prod 从 10-22 开始，约 17 天）：

| 指标 | prod | cn-prod | 说明 |
|---|---|---|---|
| 缓存命中率 | 92.9% | 99.2% | prod 13:03 刚重启，缓存还是冷的 |
| 临时文件 | 247 个 / 844 MB | 15,420 个 / 80 GB | `work_mem` 4MB 太小，排序和哈希落盘 |
| 被迫触发的 checkpoint | 82 次定时、10 次被迫（11%） | 4,739 次定时、287 次被迫（6%） | `max_wal_size` 1GB 偏小 |
| WAL 速率 | 约 180 MB/小时 | 约 100 MB/小时 | 全页写占比高，`wal_compression` 关着 |
| `wal_buffers_full` | 125,855 | 9,502,555 | `shared_buffers` 128MB，`wal_buffers` 跟着只有 4MB |
| 实际连接数 | 8 | 6 | 远低于 `max_connections` |
| 服务器时区 | `Asia/Tokyo`（东京遗留） | `Asia/Shanghai` | 网关在连接串里自带 `TimeZone`，不受影响；只影响 psql、运维脚本和日志 |
| PG 容器 | 内存上限 4G，用了 2.7G；`/dev/shm` 64MB | 无内存上限，用了 256MB；`/dev/shm` 64MB | 并行查询的共享内存放在 `/dev/shm` |

### 建议值

数据机首次启动（以备用库身份启动）时就用这些值，提升之后不再改。内存相关的值按 prod 8 GB、cn-prod 4 GB 给（见「数据机配置」）。

| 参数 | 现值 | 建议 | 理由 |
|---|---|---|---|
| `shared_buffers` | 128MB | 内存 25%：prod 2GB，cn-prod 1GB | 现值是安装默认；`wal_buffers` 会随之自动升到 16MB。prod 日常热数据 2026-11-09 删除旧去重键后约 1.8 GB，重建去重索引后约 1.1 GB，2GB 装得下；再大会和页缓存重复缓存同一份数据 |
| `effective_cache_size` | 4GB | prod 5GB，cn-prod 2.5GB | 让优化器知道 PG 加页缓存一共能缓存多少。按 PG 容器上限算，不按整机内存算 |
| `work_mem` | 4MB | 16MB | 两站都有大量临时文件；prod 17 小时 922 个，平均每个 3.9 MB，16MB 能消掉大部分。实际只有十来条连接，不会把内存用满 |
| `maintenance_work_mem` | 64MB | prod 256MB，cn-prod 128MB | 加快 vacuum 和建索引 |
| `max_connections` | 200 / 100 | 切流目标：两站都 200；**cn-prod 现网 2026-11-17 已升到 400**（prod 仍 200） | 切流时：cn-prod 一台网关蓝绿重叠可占 80，贴近旧上限 100。2026-11-17 容量压测后 cn-prod 应用池升到 80×2，数据机经审批升到 400；期望值 owner 见 `ops/stage0/dataplane-cutover-data.sh` |
| `jit` | on | off | 网关都是短查询，JIT 编译只增加延迟。仓库调优文件已经是 off |
| `max_parallel_workers` / `max_parallel_workers_per_gather` | 8 / 2 | 2 / 1 | 沿用仓库调优文件的值，避免一条报表查询占满 CPU |
| `random_page_cost` | 4 | 1.1 | 两站都是 SSD 云盘 |
| `effective_io_concurrency` | 16 | 200 | 同上 |
| `max_wal_size` / `min_wal_size` | 1GB / 80MB | 4GB / 1GB | 减少被迫 checkpoint |
| `checkpoint_timeout` | 5min | 15min | 减少全页写。崩溃恢复时间会变长，按当前 WAL 速率仍在一两分钟内 |
| `wal_compression` | off | `lz4` | 镜像已编译 lz4。WAL 和备份仓库的归档量都会下降 |
| `tcp_keepalives_idle` / `interval` / `count` | 系统默认（约 2 小时） | 30s / 10s / 3 | **拆库后新出现的问题。** 应用机断电时，库端看不到连接断开，死连接会占着会话级 advisory lock（迁移锁、`scheduler_outbox_cleanup` 锁、QA 维护锁）。按系统默认要约 2 小时才释放，而网关启动时等迁移锁最多 10 分钟，到时就会启动失败。改后约 1 分钟释放 |
| `client_connection_check_interval` | 0 | 10s | 客户端断开后，正在执行的长查询也能尽快停下 |
| `idle_in_transaction_session_timeout` | 0 | 10min | 防止开着事务不提交的会话长期持锁、挡住 vacuum。会话级 advisory lock 不在事务里，不受影响 |
| `shared_preload_libraries` | 空 | `pg_stat_statements` | 镜像自带。用来找出产生临时文件和全表扫描的查询，后续调优要靠它 |
| `track_io_timing` | off | on | 开销很小，配合上一项 |
| `log_lock_waits` / `log_temp_files` / `log_min_duration_statement` | off / -1 / -1 | on / 16MB / 1s | 锁等待、大临时文件和慢查询进日志 |
| `timezone` / `log_timezone` | Tokyo / Shanghai | 两站都 `UTC` | 和网关、分区边界一致。改之前确认运维脚本里没有依赖服务器时区的日期计算 |
| 容器 `shm_size` | 64MB | 1GB | 64MB 下并行哈希可能报共享内存不足 |
| 容器内存上限 | 4G / 无 | prod 6G，cn-prod 3G | 见「数据机配置」 |

不改的：`wal_level`、`archive_mode`、`archive_command`、`archive_timeout`、`synchronous_commit`、`full_page_writes`、autovacuum 各项、`statement_timeout`、`lock_timeout`。两站的死元组比例都很低，autovacuum 不需要调；全局 `statement_timeout` 会误伤迁移和清理任务。

### 怎么落地

1. 把仓库调优文件扩成数据机用的 Postgres 启动定义，上表每一项都写成 `-c`，内存相关的值走 `.env` 变量，沿用该文件已有的变量名。两站共用这一份。
2. pgBackRest 恢复会把旧库的 `postgresql.conf` 和 `postgresql.auto.conf` 一起带过来。命令行 `-c` 的优先级高于这两个文件，所以新值直接生效。提升之后用 `ALTER SYSTEM RESET` 清掉 `postgresql.auto.conf` 里的旧值（prod 有 `max_connections`、归档、`restore_command`），只留一个参数来源。
3. 提升之后执行 `CREATE EXTENSION pg_stat_statements`。
4. 网关侧的连接池在 `.env` / override 里显式写出：prod `DATABASE_MAX_OPEN_CONNS=50`、`DATABASE_MAX_IDLE_CONNS=10`；cn-prod 切流时为 40 / 10，**现网 2026-11-17 为 80 / 20**（`deploy/domestic/compose.override.yml`）。prod 现役 `tokenkey-blue` 是手工 `docker run` 起的，按代码默认 256 运行，切流时由模板创建新色就会改正。

### 不在这次做

- **cn-prod 的 `usage_logs` 已在 2026-11-10 切成日分区**（`usage_logs_daily_partition.py --target cn-prod`，上界 `2026-11-12T00:00:00Z`，历史行留在 `usage_logs_legacy`，并预建之后 8 个日分区）。切换前它是 1.1 GB 的整表。prod 原本就是日分区。两边之后由同一套每晚维护补未来分区。
- **`usage_billing_dedup` 已单独处理**（[`billing-dedup-retention.md`](billing-dedup-retention.md)，#3110）：热表 7 天，含归档共 30 天。2026-11-09 已在线删除 1,446 万行超过 30 天的键，热表从 1,822 万行降到 376 万行，删除期间服务无影响。表文件仍是 5.1 GB 数据加 3.3 GB 索引；空出的数据页由后续写入复用，不做 `VACUUM FULL`。唯一索引要在 #3110 部署并迁完归档后用 `REINDEX INDEX CONCURRENTLY` 重建一次，见「每站实施步骤」第 1 步。

## 数据机配置

### 建议

| | prod | cn-prod |
|---|---|---|
| 套餐 | 美西轻量入门型 `bundle_starter_nmc_lin_med8_02`，月付 90 元 | 北京轻量入门型 `bundle_starter_mc_med4_02`，月付 75 元 |
| CPU / 内存 | 2 vCPU / 8 GB | 2 vCPU / 4 GB |
| 磁盘 | SSD 系统盘 100 GB | SSD 系统盘 70 GB |
| 位置 | 与应用机同地域同可用区，同一个轻量私网 | 同左 |
| PG 容器内存上限 | 6 GB | 3 GB |
| Redis | `maxmemory` 512MB，容器上限 1GB | `maxmemory` 256MB，容器上限 512MB |

prod 数据机：`lhins-p43i2kxh`，私网 `10.4.0.3`，2026-11-09 开通，自动续费。不开任何公网入站端口，远程操作走腾讯云 TAT。2026-11-08 先开过一台通用型 150 GB 的 `lhins-rbjrqukb`，去重表清理后改用入门型，旧机器已在无理由退款期内退还。轻量只能升配不能降配，换小只能退了重买。

cn-prod 数据机：`lhins-odyx5eis`，私网 `10.2.4.5`，2026-11-09 开通，自动续费。原计划等 prod 稳定后再开，实际提前开通，用来同步搭建备用库和副本；切流仍按逐站顺序。

两台数据机的现状（2026-11-09 只读核对）：
- 系统时区 UTC。
- SSM Hybrid 已注册：prod `mi-044ba9da6453c1557`，cn-prod `mi-084a456b3dee71bb4`。
- 腾讯云防火墙只放行本站应用机访问 5432 和 6379，另放行私网 ICMP。从公网和同私网的代理机测试，22、5432、6379 都不可达。
- pgBackRest 配置里 COS 的编号是 `repo2`，和 `tencent_pgbackrest.py` / `cn_pgbackrest.py` 的 `PRIMARY_REPO = 2` 一致；`restore_command` 也是 `--repo=2`。

轻量套餐支持 `ModifyInstancesBundle` 原地升配。切流后缓存命中率长期低于 99% 时再升，不提前买大。

数据放在系统盘上，和 cn-prod 现在一样，不要求独立云硬盘。数据安全靠 pgBackRest：WAL 每 5 分钟归档一次，每 2 小时做一次增量备份，每站只推到本站的腾讯 COS 仓库（AWS S3 仓库自 2026-11-08 起不再写入，#3107）。磁盘坏了或系统盘被重装，最多丢最后几分钟还没归档的 WAL。独立云硬盘只能缩短恢复时间。2026-11-08 在 prod 数据机上实测，从 COS 恢复 prod 全库（23 GB）用 271 秒，回放和启动 2 秒，所以不需要独立云硬盘。

### 依据

2026-11-09 在 prod 只读实测（PG 已运行 17 小时）：

| 指标 | 实测 |
|---|---|
| 60 秒采样缓存命中率 | 99.85%，每秒读盘 3.3 块（约 26 KB/s） |
| 事务 | 约 83 次/秒，每秒新增 5 行、更新 8 行 |
| 请求量 | 近一周每天 5 万到 18 万次，峰值小时约 8,800 次 |
| PG 容器 CPU / 内存 | 1.5% / 2.6 GB |

日常真正被访问的数据，2026-11-09 删除旧去重键后约 1.8 GB：核心表约 60 MB、最近 3 天 `usage_logs` 分区约 300 MB、最近的 ops 和 QA 日志约 200 MB、`usage_billing_dedup` 唯一索引里仍被写入的部分约 1.2 GB（重建索引后约 0.1 GB）。当天下午 60 秒采样：命中率 98.48%，每秒读盘约 450 KB，PG CPU 11.5%，连接 11 条。

- **CPU**：PG 只用了 1.5%，2 核足够。
- **内存**：8 GB 里给 `shared_buffers` 2 GB、页缓存约 4 GB，装得下约 1.8 GB 的热数据。COS 恢复验收要在同一台机器上再起一个 PG（默认 1 GB），8 GB 留得出这份余量，4 GB 不够。cn-prod 库只有 2.9 GB，4 GB 内存能把整个库放进缓存。
- **磁盘**：
  - prod：`usage_logs` 近 14 天平均每天约 123 MB，约 3.7 GB/月；去重表已降到稳态，不再增长。100 GB 下：正式库 23 GB，加上 COS 恢复验收临时多恢复的一份（实测 2026-11-09 恢复 23 GB 用 291 秒，恢复后剩 66 GB），约 10 个月后会变紧。届时升配或把验收挪到别的机器。
  - cn-prod：合计约 1.7 GB/月。70 GB 下库 2.7 GB，系统盘剩 57 GB，够用两年以上。
- **要盯的风险**：磁盘（见上）。切流后命中率长期低于 99% 时升配；轻量升配需要重启实例，要找窗口。

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

- 停写窗口内的新请求要排队，超过 Caddy 等待上限的会失败；超过排空上限的长流会被切断。审批时附预估数量。
- 跨机后每次查询多一次私网往返。演练测得的 p99 不可接受就不切。
- 私网放行写错会把库暴露出去，或者切断应用。开机后按第 2 步测端口。
- 旧库被重新启动成可写主库会造成双写。提升之后保持停止。
- 去掉加密卷后，拿到数据机的磁盘快照就能读到库。这是已接受的取舍。
- 应用机断电后，库端死连接会占着会话级 advisory lock，新网关可能等不到迁移锁。靠 `tcp_keepalives_*` 约 1 分钟释放；切流后要在蓝绿发布中实测一次。
- 新参数让 `shared_buffers` 变大，切流后头几个小时缓存是冷的，查询会比平时慢。
- cn-prod 锐驰配额可能已满，开机会被卡住。
