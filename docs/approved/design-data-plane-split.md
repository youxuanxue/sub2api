---
title: prod 与 cn-prod 数据面从应用机拆出
status: approved
approved_by: "feng（2026-11-09 会话：同意 #3102 的设计，要求文档与最新实测保持一致）"
approved_at: 2026-11-09
risk: high
authors: [agent]
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

2026-11-08 20:27 CST 两站只读探针实测（`ops/observability/run-probe.sh`）。这些是切流前当天的事实，不是长期规格。切流之后的事实以文首审批状态和第 6 节为准，下表不改写成切流后的布局。

| 项 | prod | cn-prod |
|---|---|---|
| 主机 | 腾讯美西 `lhins-fcwpwymt`，4 vCPU / 15.6 GiB，私网 `10.4.0.16` | 腾讯北京 `lhins-6qmb3fd6`，4 vCPU / 7.7 GiB，私网 `10.2.4.11` |
| 同机进程 | `tokenkey-blue`、`tokenkey-caddy`、`tk-cutover-prod-redis`、`tk-archive-standby`（PG） | 网关蓝绿、socat 控制代理 `tokenkey`、`tokenkey-caddy`、`tokenkey-redis`、`tokenkey-postgres` |
| 数据所在 | LUKS：根盘文件 `recovery-rebuild-20261108.luks`（64G）经 loop 挂到 `/srv/tokenkey-rehearsal`，已用 25G | 根盘普通 ext4 `/var/lib/tokenkey`，无加密 |
| 应用机重启后 | 11-08 13:03 整机重启过。当天新装 `tokenkey-luks-unlock.service`，开机从 COS 取钥解锁到 tmpfs；还没经过一次无人值守的重启。这个 unit 不在仓库里 | 自动起 |
| Postgres | 18.6，库 22 GB，`max_connections` 200，容器内存上限 4G | 18.6，库 2.9 GB，`max_connections` 100，无容器内存上限 |
| Redis | 8.10.3，AOF everysec，**无密码**，`maxmemory` 512MB + `allkeys-lru`，容器内存上限 1G，已用约 9MB | 8.10.2，AOF everysec，有密码，`maxmemory` 0 + `noeviction`，无容器内存上限，已用约 6MB |
| 网关连库 | 容器 env `DATABASE_HOST=tokenkey-postgres`、`REDIS_HOST=tokenkey-redis`。部署模板（`ops/stage0/deploy_via_ssm_bluegreen.sh`）写死这两个值 | 容器 env `DATABASE_HOST=postgres`、`REDIS_HOST=redis`。蓝绿候选色继承现役容器的 env，只覆盖 `deploy/domestic/bluegreen.py` 列出的键 |
| 每色 DB / Redis 池 | 现役 `tokenkey-blue` 是 11-08 重建时手工 `docker run` 的，env 和 `config.yaml` 都没设池参数，按代码默认 256 / 1024 运行。下次走模板的蓝绿发布会变成 50 / 1024 | 40 / 96（`compose.override.yml`） |
| 实测连接 | 网关 7 条 + 1 条 psql | 网关 5 条 + 1 条 psql |
| 蓝绿重叠期 | 两色同时在，连接数翻倍 | 同左 |
| 备份 | pgBackRest 单仓库：腾讯 COS 美西（`repo2`）。WAL 每 5 分钟归档，每 2 小时增量，全量保留 7 天；仓库状态 ok；timer 在应用机。AWS S3 `us-east-1`（`repo1`）自 2026-11-08 起不再写入（#3107） | pgBackRest 单仓库：腾讯 COS 北京（`repo2`），周期和保留期同 prod；仓库状态 ok（归档上一次失败是 11-02 的已知事故）；timer 在应用机。AWS S3 `ap-northeast-1`（`repo1`）同样自 2026-11-08 起不再写入（#3107） |
| Caddy 上游 | 现网 `/var/lib/tokenkey/caddy/Caddyfile` 只有 `reverse_proxy tokenkey-blue:8080 { flush_interval -1 }`，**没有健康检查，也没有 `lb_try_duration`**。上游不可用时请求立刻失败。2026-11-09 已补上健康检查和 `lb_try_duration 30s` / `lb_try_interval 1s` | 有健康检查和 `lb_try_duration 30s`，上游不可用时最多等 30 秒 |
| `RUN_MODE` | `standard` | `standard` |
| 网关内存 | `mem_limit 8g`、`GOMEMLIMIT=6GiB`（#3101） | `GOMEMLIMIT=2GiB`，无容器上限 |
| 应用机上的其他写库方 | pgBackRest 备份和归档检查、QA 维护三个 timer（cleanup / provision / archive）、`tokenkey-https-watch`、运维探针 | 同左 |
| 主机防火墙 | `INPUT` 默认放行，挂腾讯云 `YJ-FIREWALL-INPUT` 链；主机只监听 80 / 443，PG 和 Redis 没有发布到主机端口 | 同左 |

两处文档和实测不一致：`deploy/hetzner/README.md` 把 prod 写成 4C8G，实际是 4 vCPU / 15.6 GiB；`deploy/domestic/README.md` 把 cn-prod 写成 8C，实际 `nproc` 是 4。

## 两站统一布局

- **数据机**：与应用机同地域、同账号、同私网。不跨云。数据目录 `/var/lib/tokenkey`，放在系统盘的普通 ext4 上，和 cn-prod 现在一样。磁盘大小见「数据机配置」。
- **容器**：`tokenkey-postgres`、`tokenkey-redis`。Postgres 大版本与当站现役一致，因为物理恢复要求同一大版本。
- **内存**：Postgres 和 Redis 各设容器内存上限，数值见「数据机配置」。
- **参数**：Postgres 和 Redis 用仓库里同一份 compose 启动，参数全部写成命令行 `-c`。只有和内存相关的值按站不同，从数据机 `.env` 读。
- **Redis**：AOF everysec，设 `maxmemory` 并用 `noeviction`。prod 现网是 `allkeys-lru`，内存满时会淘汰在途扣费 key；数据机统一用 cn-prod 的 `noeviction`，宁可写失败也不丢账。设 `requirepass`。cn-prod 沿用现有密码；prod 现网没有密码，切流时新生成一个，只存数据机和应用机的 `.env`。
- **防火墙**：云防火墙不开公网入站；主机 iptables 只放行本站应用机私网地址访问 5432 和 6379。
- **运维**：数据机注册自己的 SSM Hybrid（prod `mi-044ba9da6453c1557` / `tokenkey-usw-data`，cn-prod `mi-084a456b3dee71bb4` / `tokenkey-cn-data`，记在两份站点矩阵里）。备份 timer、归档检查 timer、身份续期 timer 和数据盘告警装在数据机上，应用机上的这几项停用。身份续期：`tencent_backup_identity.py` 和 `cn_backup_identity.py` 按矩阵里的数据机主机名分支，在数据机上读本机 root-only 的 `/etc/tokenkey/cos-backup.env`，原子重写容器只读挂载的 `/etc/tokenkey/pgbackrest-cos/pgbackrest.conf`（保持 `root:postgres 0640`）。轮换 COS 密钥只改数据机上的这一个文件。由 `ops/stage0/install-data-machine-identity.sh` 安装，带确认口令。
- **网关**：`DATABASE_HOST`、`REDIS_HOST`、`REDIS_PASSWORD` 从 `.env` 读，切流时填数据机私网地址。

## 数据库参数

### 现状

两站的 Postgres 基本还是安装默认值。仓库里已有调优文件 `deploy/aws/stage0/docker-compose.prod-pg.yml`（`shared_buffers` 1GB、`jit` off 等），但两站都没用上：prod 的 PG 容器启动命令只有 `postgres`，参数散落在数据目录的 `postgresql.conf` 和 `postgresql.auto.conf` 里；cn-prod 只用命令行 `-c` 设了归档相关的四项。

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
| `max_connections` | 200 / 100 | 两站都 200 | cn-prod 一台网关在蓝绿重叠时就占 80，再加运维连接已经贴近 100。备用库的这一项不能小于主库，200 两站都满足 |
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
4. 网关侧的连接池在 `.env` 里显式写出：prod `DATABASE_MAX_OPEN_CONNS=50`、`DATABASE_MAX_IDLE_CONNS=10`，cn-prod 保持 40 / 10。prod 现役 `tokenkey-blue` 是手工 `docker run` 起的，按代码默认 256 运行，切流时由模板创建新色就会改正。

### 不在这次做

- **cn-prod 的 `usage_logs` 没有分区**（1.1 GB，869 次全表扫描读了 1.07 亿行），prod 是按日分区的。分区切换有自己的工具 `ops/migration/usage_logs_daily_partition.py` 和维护窗口，与换机器无关。拆库完成后单独做，两站的表结构才算一致。
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

## 切流前的代码改动

都走正常发布。默认值保持现状，所以发布后行为不变。第 1、2、4 项已在 #3114 合并；第 5 项由 #3116 完成，备份监控由 #3117 完成。

1. prod 部署模板把两个 Host 改成 `${DATABASE_HOST:-tokenkey-postgres}` 这类写法，`ops/stage0/test_deploy_via_ssm_bluegreen.py` 同步改。
2. cn-prod 在 `compose.override.yml` 声明两个 Host，取值来自 `.env`，默认值为现值；`bluegreen.py` 把它们列入要传播的键，否则候选色会继承旧值。漏列时 `test_pool_env_probe.py` 会失败。cn-prod 的 Redis 密码不变，候选色照旧从现役容器继承，不进 override。
3. （已完成，2026-11-09 核对）prod 现网 Caddyfile 补上和 cn-prod 相同的健康检查和 `lb_try_duration 30s` / `lb_try_interval 1s`。这是停写窗口第 2 步的前提：没有它，Caddy 指向未启动的新色时请求会立刻失败。蓝绿渲染（`render_caddy_with_upstream`）只改上游地址那一行，所以补上的参数会保留到后续发布；改动同时入仓。`caddy validate` 后 `caddy reload`，用户无感。
4. `deploy/tencent/tencent_pgbackrest.py` 和 `deploy/domestic/cn_pgbackrest.py` 支持在数据机上运行，容器名和主机角色按新布局。
5. 运维探针继续在应用机上执行，经数据面客户端容器连数据机（#3116）。`ops/lib/resolve-app-container.sh` 的 SQL 和 redis-cli 入口新增候选 `tokenkey-postgres-client` / `tokenkey-redis-client`，约 70 个探针不用改。规则仍是只能有一个在运行：旧库和客户端同时运行时判为不唯一，探针直接失败。pgBackRest 只解析真库容器。客户端由 #3116 的安装脚本安装，带确认口令；它从现役色读数据机地址和密码。仍读本机的两项：容量探针对数据目录跑 `df`、日常诊断 tail PG/Redis 日志。切流后这两项要到数据机上看。
6. （已完成）准备 Redis 迁移用的私网转发：在应用机上起一个只监听私网地址的转发容器，把旧 Redis 暴露给数据机，不重建旧 Redis。防火墙只放行数据机私网地址。窗口结束后删掉。

## 每站实施步骤

两站各走一遍。先切哪站按当时的请求量定，默认先切请求量小的一站。第一站稳定后再开始第二站。稳定的标准：跑满一个完整的日流量周期，新主库产出过一份成功的全量备份，日常诊断没有新告警。

### 1. 只读准备（用户无感）

读取当站以下数据：近 7 天按小时的请求量（用来找低谷）、库体积和 WAL 速率、`max_connections`、Redis 内存和 key 数、最近一次蓝绿发布里新色从启动到就绪的耗时。这些数决定机器规格和窗口时长。

prod 另查：`usage_billing_dedup` 的 `n_dead_tup` 已被 autovacuum 清掉（2026-11-09 删除后约 26 万），#3110 已部署且 7–30 天的键已迁进归档表。满足后在现网执行一次 `REINDEX INDEX CONCURRENTLY idx_usage_billing_dedup_request_api_key`（不锁写入，临时多占约 0.1 GB），唯一索引从 2.6 GB 降到约 0.1 GB。备用库从之后的备份恢复，就直接带上小索引。

### 2. 开机（需预算审批，用户无感）

按统一布局装好。开机后分别从公网和同私网的其他主机测一次 5432、6379，确认都不可达。

cn-prod 注意：据 `deploy/hetzner/README.md` 2026-11-06 的记录，腾讯北京锐驰配额 5 台已被 `cn-ip1..4` 和 cn-prod 主机占满。开机前在控制台复核；仍然占满就先提配额，或者选不占这项配额的套餐。

规格按「数据机配置」。

### 3. 备用库与 Redis 副本（用户无感）

- **Postgres**：用当站 pgBackRest 恢复到数据机，作为备用库从仓库持续回放 WAL。备用库不归档。备用库第一次启动就用「数据库参数」里的新值。应用继续写旧库。
- **Redis**：数据机上的 Redis 经私网转发做旧 Redis 的副本，cn-prod 用现有密码配 `masterauth` 和 `requirepass`。两站 Redis 都只用了约 10MB，全量同步的 fork 开销可以忽略。
- **放弃**：删掉备用库和副本即可，现网不变。

### 4. 演练（用户无感）

- 在数据机上从同一份备份另起一个演练实例，用另一个目录和端口。提升它，记录提升耗时。演练实例关闭归档，不接现网仓库。
- 从应用机经私网测三项：Postgres 往返延迟、几条网关热路径查询的 p99（对演练实例只读执行）、`redis-cli --latency`。p99 不可接受就不切。
- 不起连到演练库的网关。网关后台任务会刷新上游账号凭据，演练库里的凭据会和现网分叉。
- 测完删掉演练实例。
- 汇总窗口估算：排空旧色、最后一段 WAL 归档与回放、提升、新色启动到就绪、Caddy 切换。这个数和低谷时段的请求量一起写进停写审批。

2026-11-09 两站演练实测：

| 项 | prod | cn-prod |
|---|---|---|
| 从 COS 恢复（演练实例） | 240 秒 | 143 秒 |
| 启动到追上 WAL | 12 秒 | 11 秒 |
| 提升（`pg_promote`） | 0.54 秒 | 1.08 秒 |
| 提升后第一笔写入 | 71 ms | 99 ms |
| 热路径查询比本机多出 | 不到 0.2 ms | 1–3 ms，最大约 9 ms |
| Redis 比本机多出 | 0.2–0.3 ms | 0.2–0.3 ms |
| 新色从启动到就绪 | 6 秒（看日志） | 6 秒（看日志） |

两站的 p99 都可接受。演练实例测完已删除，真正的备用库全程在回放。整窗时长还没有实测，要到窗口里才测得到。

### 5. 停写窗口（需当站停写审批）

在批准的低谷时段执行。窗口由 `ops/stage0/dataplane-cutover-app.sh` 在应用机上一次跑完，经 `run-probe.sh` 下发。`PHASE=check`（默认）只读，可以随时跑，会列出所有前提和执行计划。`PHASE=apply` 需要确认口令 `TK_DATAPLANE_CONFIRM=cutover-<站点>-to-<数据机私网 IP>`。数据机上的 `ops/stage0/dataplane-cutover-data.sh` 负责窗口前的只读检查（含「数据库参数」表）和窗口后的收尾。

现役色原地重建，不切到另一色（用户 2026-11-09 决定）：Caddyfile、`active-color` 和 Gemini 控制别名都不动。现役色停下后，新请求在 Caddy 里排队，最长等 `lb_try_duration`。

窗口前提（`PHASE=check` 全部通过才允许 apply）：
- prod 主机上的 `docker-compose.bluegreen.yml` 必须由含 #3114 的版本渲染。1.8.355 渲染出的那份把 `DATABASE_HOST=tokenkey-postgres` 写死了，只改 `.env` 不会生效。所以窗口前要先正常发一版。
- prod 的新 Redis 密码要从托管桶放到应用机的 `/var/lib/tokenkey/dataplane-redis-password`，权限 0600。cn-prod 沿用现有密码。
- cn-prod 的 override 和 `bluegreen.py` 已同步到 main（2026-11-09 核对）。

apply 的顺序：

1. 停掉应用机上其他写库方：备份、归档检查、身份续期和 QA 三个 timer。
2. 给现役色发 SIGUSR1 开始排空。**排空上限 10 秒，超时切断**（用户 2026-11-09 决定），不沿用网关默认的 120 秒。
3. 在旧库上查 `pg_stat_activity`，在旧 Redis 上查 `CLIENT LIST`。除了复制连接和本次操作的连接，必须一个都没有，有就中止。
4. 旧库执行 `pg_switch_wal()`，等 `pg_stat_archiver` 显示这一段已归档。
5. 等数据机备用库回放到这个 LSN，等 Redis 副本偏移追上旧主库。
6. 数据机 Redis 执行 `REPLICAOF NO ONE`，然后 `pg_promote`。**这一步不可回头。** 在这之前任何一步失败，脚本都会重新启动现役色和停掉的 timer，站点回到原状（退出码 3）。
7. 停止旧库和旧 Redis，并执行 `docker update --restart=no`。旧库原来是 `unless-stopped`，不改的话会被误启动，造成双写。
8. `.env` 改成数据机地址（先备份原文件；prod 同时写入新 Redis 密码），原地重建现役色，等 `/health` 就绪。Caddy 里排队的请求随即放行。cn-prod 的现役色由每次发版的 `compose.json` 起，里面是写死的 env。脚本复制一份到新目录，只改四个地址键再重建；原发版目录不改。
9. QA timer 重新启动。备份相关的 timer 在应用机上停用，`postgres-backup/` 下的回执改名保留，应用机看门狗随之不再监控备份。

窗口之后：
- 数据机执行 `PHASE=finish dataplane-cutover-data.sh`。它会重置 `postgresql.auto.conf` 里的旧值，创建 `pg_stat_statements`，强制切一次 WAL 并确认已归档到 COS，再逐项核对「数据库参数」表。
- 应用机用 #3116 的安装脚本装上数据面客户端容器，探针恢复可用。旧库停掉之前不能装：客户端和旧库同时运行时，探针会因不唯一而失败。
- 数据机启用备份 timer，并以 `prod-data` / `cn-prod-data` 角色安装看门狗（#3117）。身份续期 timer 用 `install-data-machine-identity.sh` 安装：先把 COS 密钥放到数据机的 `/etc/tokenkey/cos-backup.env`，安装后跑一次续期和归档检查；之后删掉应用机上那份。
- 发一笔真实请求，确认用量和扣费落在新库。

**排空 10 秒的影响**（2026-11-09 prod 实测）：请求平均 14.7 秒，最安静的 UTC 22 点每分钟约 42 个请求，切换时同时在途的约 10 个。10 秒内跑完的正常记账；跑不完的被切断，客户端收到连接中断。被切断的请求：
- 不写用量、不扣费，因为结算发生在请求结束时，而旧色已经停了；
- 预扣的冻结额度留在 `usage_holds`，由 `HoldReconciler` 在 30 分钟后自动退回；
- 上游已经执行的部分，TK 不向用户收费，成本由 TK 承担。

近 7 天 p99 是 143 秒，超过 5 分钟的请求共 2,343 个（每天约 330 个），长流式请求基本都会被切断。

**窗口估算**：排空 10 秒、确认无连接约 2 秒、最后一段 WAL 归档与回放约 5 秒、Redis 切主不到 1 秒、提升约 1 秒（实测 prod 0.54 秒、cn-prod 1.08 秒）、新色就绪约 6 秒，合计约 25 秒，在 `lb_try_duration 30s` 内。这段时间里新请求在 Caddy 排队变慢，不返回 502。WAL 和排空两项要到窗口里才能实测；整窗实测超过 30 秒，就先调大 `lb_try_duration`。

从第 2 步到第 8 步要在 `lb_try_duration` 内完成。演练估算超过这个上限时，停写审批要写明超出的部分会返回 502，或者先申请临时调大上限。窗口结束的标志是新库接受写入并且归档在推进，不是容器启动成功。

### 6. 观察与收尾

- 新主库产出第一份成功的全量备份之前，旧数据目录不动。两站切流后到 2026-11-10 为止，数据机上只有增量备份（prod 最新一份在 timeline 4，cn-prod 在 timeline 2），依附的全量还是切流前旧主库做的。`backup_kind` 按 UTC 周日 00:30 切周，所以第一份新 timeline 上的全量是 2026-11-15（周日）之后的第一轮，也可以经单独批准提前手动跑一份。
- cn-prod 切流后的第一轮备份（16:30–20:30 UTC）报 `[106] new backup label is not later than latest`。原因：旧应用机的 pgBackRest 标签是北京时间，数据机是 UTC。22:30 UTC 那一轮起恢复正常，期间 WAL 归档没有中断。prod 旧标签本来就是 UTC，不受影响。
- 切流后读数据机的探针：`run-probe.sh --target prod-data / cn-prod-data` 直连数据机，SSM Name 不符就拒绝。每日容量探针在数据机上跑 `df`，跑道按数据机系统盘计算；`log_dump` 的 `tokenkey-postgres` / `tokenkey-redis` 读数据机上的日志。CI 角色要能对这两个 `mi-*` 执行 `SendCommand`，授权在栈外 inline policy `tokenkey-lightsail-edge-addon`（见 `docs/ops/tencent-cutover-checklist.md`）。应用机上的容量探针仍会把本机 `df` 判为「无结论」（#3122），作为兜底。
- 删除旧数据目录、旧 Redis 和转发容器需要单独批准。
- `usage_billing_dedup` 唯一索引的 `REINDEX CONCURRENTLY` 要等 7–30 天的键迁完归档（约 3 个晚上）再做，不挡切流。
- prod 的 LUKS 载体和异机密钥要等应用机也迁出 LUKS 之后才能删（见下节）。

## prod 应用机的加密卷

prod 应用机的 Docker 数据目录和 `.env` 也在 LUKS 卷上。11-08 新装的自动解锁还没经过无人值守的重启；它失败时网关起不来，但拆库之后库不受影响。

把应用机迁出 LUKS 要移动 Docker 数据目录，需要重启 Docker，因此是另一次窗口。本设计只记下这件事，不排进这次。目标布局和 cn-prod 一致。

## 回退

- **提升之前**：重启旧色，回到原状。删掉备用库和副本。
- **提升之后**：旧库不能再启动成可写主库。已有新写入时，回退是从备份链恢复，不是把 Host 指回去。两个主库同时往一个仓库归档视为故障。

## 第二台网关的门禁

两站都拆完之前，不部署第二台网关。拆完之后要在某站加副本，先满足下面三条，缺一条就不加：

- 现网 `RUN_MODE` 不是 `simple`。`simple` 会跳过清理锁，每台网关都会做清理。
- 网关台数 × 每色连接上限 × 2（蓝绿重叠），再加上备份和运维连接，放得进 `max_connections`。拆库后两站的 `max_connections` 都是 200，每色上限 prod 50、cn-prod 40。prod 两台网关加蓝绿重叠正好占满 200，cn-prod 是 160。加副本前必须先把每色上限调低。Redis 连接池一并收小。
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
