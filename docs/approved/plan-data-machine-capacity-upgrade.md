---
title: prod 数据机容量升级与配置优化方案
status: approved
approved_by: "feng（2026-11-21：同意 prod 升 4C16G 与配置；max_connections=200；补全观测告警；先不要停写切流。2026-11-21 续：先做升配备料；禁止停写，停写/重启窗口须单独审批）"
approved_at: 2026-11-21
risk: high
authors: [agent]
related:
  - docs/approved/design-data-plane-split.md
  - docs/decisions/decided.md
owners:
  - design: docs/approved/design-data-plane-split.md
  - settings_check: ops/stage0/dataplane-cutover-data.sh
  - host_alert: deploy/hetzner/tokenkey-disk-metrics-prod.sh
  - probe_memory: ops/observability/probe-data-machine-memory.sh
  - probe_prep: ops/observability/probe-data-machine-capacity-prep.sh
---

# prod 数据机容量升级与配置优化方案

**prod 按容量预留排期升 4C16G，PG 上限 12 GiB。** 套餐与参数目标已批；**2026-11-22 低峰窗已执行完成**（见下方执行结果）。备料段仍保留作审计；Redis 1 GiB 硬上限仍属单独变更。

**cn-prod 不在本方案执行范围**：继续 4C8G / PG 3 GiB。若高峰期持续出现内存回收压力，且查询物理读与延迟同时升高，另开方案评估 PG 上限 3→5 GiB；单看容器占比不足以安排重启。

## 非目标（本方案 / 本备料阶段不做）

- 不停写、不切流、不重启主机、不 `docker stop/rm` 重建 PG/Redis
- 不改 `max_connections`（prod 保持 200）
- 不改网关连接池 open/idle
- 不把 Redis 容器硬上限调到 1 GiB（设计表目标；**单独变更**，可与升配同窗或另窗）
- 不改 cn-prod 套餐或 PG 参数
- 不用 `dataplane-cutover-data.sh` 的 `check`/`finish` 验收在线主库（`check` 面向备库；`finish` 会写主库）

## prod 4C16G 推荐配置

| 项目 | 升配后目标值 |
|---|---|
| 主机 | 4 vCPU、16 GiB，推荐 180 GB SSD 入门套餐 |
| 容器内存 | PostgreSQL 上限 **12 GiB**，`shm_size=1 GiB`。Redis 容器目标上限 **1 GiB**（单独变更）。两项硬上限均生效后，按标称容量给宿主约留 **3 GiB**；实机用 MemAvailable 核验。**本窗若未设 Redis 硬上限，宿主余量按「Redis 无 cap」重算风险** |
| PG 缓存 | `shared_buffers=4GB`，`effective_cache_size=10GB`（规划器估值） |
| PG 连接与查询 | `max_connections=200`，`superuser_reserved_connections=3`，`work_mem=16MB`，`hash_mem_multiplier=2` |
| PG 维护 | `maintenance_work_mem=512MB`；`autovacuum_work_mem` / `autovacuum_max_workers` **本窗不改**（沿用现网；与 design「不改 autovacuum」一致） |
| PG 并行 | `max_parallel_workers=3`，`max_parallel_workers_per_gather=1`，`jit=off` |
| PG 存储与检查点 | `random_page_cost=1.1`，`effective_io_concurrency=200`，`min_wal_size=1GB`，`max_wal_size=4GB`，`checkpoint_timeout=15min`，`wal_compression=lz4` |
| WAL 归档 | `archive_mode=on`，`archive_timeout=5min`，pgBackRest 归档至本站 COS repo2 |
| Redis | `maxmemory 512mb`，`maxmemory-policy noeviction`，`appendonly yes`，`appendfsync everysec` |

本窗 PG 容器上限由 6 调到 12 GiB。对照 11 月 21 日 19:04 北京时间主库读数，**需要切换的 PG `-c` 参数仅四项**：`shared_buffers` 2→4GB、`effective_cache_size` 5→10GB、`maintenance_work_mem` 256→512MB、`max_parallel_workers` 2→3。连接超时、慢查询日志、时区与 autovacuum 沿用[数据面设计表](design-data-plane-split.md)。

升配完成后须设置 `TOKENKEY_DATA_HOST_PROFILE=prod-4c16g`（数据机环境 / 后续只读核对入口），否则 `dataplane-cutover-data.sh` 默认期望表仍是升配前的 2C8G / PG 6G 读数。

## 现网依据

2026-11-21 北京时间 18:18–18:28，通过两台数据机、四台网关和腾讯云实例 API 核实。两站均为 PostgreSQL 18.6 主库，Redis AOF 正常。

| 实测 | prod | cn-prod（对照，非本窗） |
|---|---:|---:|
| 主机 / PG 上限 / 数据库大小 | 2C8G / 6 GiB / 20 GiB | 4C8G / 3 GiB / 7.15 GiB |
| 主机可用内存 / PG cgroup 占比 / OOM 次数 | 4.29 GiB / 99% / 0 | 5.60 GiB / 96% / 0 |
| 11 月 15–21 日 CPU 非空闲率 P95（含 I/O 等待） | 23.7% | 14.3% |
| 两台网关连接池上限 / PG 连接上限 | 50 + 25 / 200 | 80 + 80 / 400 |

## prod 并发与内存预算

**连接上限推荐 200。** 最近七天，网关每个进程采集的在用连接数 P95 为 1，20,229 条分钟快照中有 4 条达到 50。采集未记录池等待时长，无法由此确定排队时间或全站峰值。按未来两池各 75 估算，容量是当前总池的两倍，扣掉 3 个超管保留槽后，还余 **47 个普通连接**供其他客户端使用。若再重叠一个上限 75 的进程，三池合计 225，超过 197 个普通槽；该情景需缩小重叠池或调整发布顺序。高峰若反复等连接，先看各池等待时长增量和数据库 CPU、I/O 余量，再决定扩哪个池。规划总需求逼近 197 时重算数据库上限。

**全局 `work_mem` 推荐 16MB。** 自 11 月 9 日统计重置以来，prod 的 `pg_stat_statements` 记录了约 2.13 GiB 临时写入，其中一条 SQL 占 39%。以 32 个执行进程（含并行 worker）、每进程同时运行两个哈希节点估算，按现网 `hash_mem_multiplier=2`，合计约 2 GiB。PG 容器 12 GiB 扣除共享缓冲 4 GiB、三个 autovacuum worker 最多 1.5 GiB 和这 2 GiB 后，余下 4.5 GiB 由进程、其他操作和文件缓存共享。全局提至 32MB 会把同一情景的余量压到 2.5 GiB。这是增长情景估算。对反复写临时文件且影响延迟的 SQL，先在单个事务试 `SET LOCAL work_mem='32MB'`，比较同一查询的耗时与临时写入量。

## 备料阶段（允许，不停写）

只读核验入口：

```bash
bash ops/observability/run-probe.sh \
  --target prod-data \
  --script ops/observability/probe-data-machine-capacity-prep.sh \
  --comment "prod-data capacity upgrade prep" \
  --compressed-output

bash ops/observability/run-probe.sh \
  --target prod-data \
  --script ops/observability/probe-data-machine-memory.sh \
  --comment "prod-data memory baseline" \
  --compressed-output
```

备料清单：

1. **告警与探针**：数据机 `tokenkey-disk-metrics` 含 PG cgroup **anon** 路径（cache-full 不告）；本地 `--selftest` 通过；`probe-data-machine-memory.sh` 可出 HOST/PG_CGROUP 快照。
2. **期望表门**：仓库 `ops/stage0/dataplane-cutover-data.sh` 在 `TOKENKEY_DATA_HOST_PROFILE=prod-4c16g` 时期望 SB=4GB / ECS=10GB / mwm=512MB / parallel 3/1；默认（未设 profile）仍匹配现网 2GB/5GB/256MB/2。
3. **容器定义快照**：prod 数据机 `/var/lib/tokenkey/maintenance/prod-data-4c16g/<id>/` 保存当前与目标 `docker create` 定义及校验和；目标定义须在隔离 `--name` 下 `create` 成功后立即 `rm`（不 `start`）。
4. **身份与挂载**：记录 `system_identifier`、镜像 ID、PGDATA 绑定（宿主 `/var/lib/tokenkey/postgres` → `/var/lib/postgresql/data`）、归档配置挂载、网络。正反向重建命令与旧定义同目录存放。
5. **备份（开窗前最低要求）**：`pgbackrest check`（只读）通过；归档在 15 分钟 RPO 内。**隔离恢复验收刷新已按 2026-11-21 决定跳过**（见 `decided.md`）；`restore_acceptance` 可为 `degraded` 仍允许低峰升配。升配成功/同盘回退不依赖 COS 恢复；若须从 COS 拉起则历史点链仍未证明。
6. **套餐与报价**：prod 现系统盘 100 GB；4C16G 入门型约 180 GB / 通用型约 220 GB；建议入门型，开窗前复核实例报价。轻量原地升配会重启主机——属停写窗口，另批。

### 备料核验快照（2026-11-21 20:49 CST，只读，未停写）

| 项 | 结果 |
|---|---|
| 主机 | 仍 2C8G（`nproc=2`，MemTotal≈7.5 GiB）；根盘约 31% |
| PG 现网 | 上限 6 GiB；SB 2GB / ECS 5GB / mwm 256MB / parallel 2；`system_identifier=7697434738137305123`；主库 |
| Redis | `maxmemory=512M`，Docker **无** 内存硬上限（`memory_bytes=0`） |
| 维护包 | `/var/lib/tokenkey/maintenance/prod-data-4c16g/b1d2114dcf0f56b1/` 含 original/target create + receipt；receipt 校验目标 SB/ECS/mwm/parallel |
| anon 告警 | 已装且 timer `active` |
| `pgbackrest check` | **ok**；归档推进中（采样时 last archived ≈窗口内） |
| `restore_acceptance` / `repository_verification` | **`degraded`**（历史点 vs live）；已决定不阻塞低峰升配 |


## 执行结果（2026-11-22 低峰窗，已完成）

开窗原话见 `docs/decisions/decided.md`（「同意升配。尽快完成。」）。实例 `lhins-p43i2kxh` → `bundle_starter_nmc_lin_lg16_01`。

| 项 | 结果 |
|---|---|
| 主机 | `nproc=4`，MemTotal≈15.2 GiB；套餐升配含重启 |
| 写中断 | 约 UTC 23:49 停 PG/Redis → 23:51 主机 RUNNING → 中间 PG 重建两次失败（root entrypoint / 默认 entrypoint 误判未初始化）→ **UTC 23:59:30** `ACCEPT_PG_OK` |
| PG | 上限 12 GiB；SB 4GB / ECS 10GB / mwm 512MB / parallel 3/1；`system_identifier=7697434738137305123`；主库；`--user 70:70 --entrypoint postgres`（与原 shape 一致，避免官方 entrypoint 按空 `PGDATA` 初始化） |
| Redis | 升配窗未改；**同日稍后**已设 Docker 硬上限 1 GiB（`ACCEPT_REDIS_CAP_OK`）；进程内仍 `maxmemory=512mb` / `noeviction` |
| profile | `/etc/tokenkey/data-host-profile.env` → `TOKENKEY_DATA_HOST_PROFILE=prod-4c16g` |
| 归档 | `pg_switch_wal` 后 `archived_count` 8064→8065，`failed_count=0` |
| 网关 | `app-old` / `app-cand`：`tokenkey` healthy；私网 TCP `10.4.0.3:5432` / `:6379` ok |
| 窗后 memory | HOST used≈9%（by MemAvailable）；PG cgroup fill≈10%，oom_events=0 |
| 真实请求/扣费 | UTC 00:11:39 `probe_account_model`（`--via ssm --node app-old`）`ACCOUNT_ID=621` `deepseek-chat` → `verdict=servable`；`usage_logs.id=19997302` `account_id=621` `total_cost=actual_cost=0.0000150746`；`X-Request-ID=c8095e21-d1c6-4e6a-ab1d-af1be29a10d0`。user_id=1 同期有其它大额用量，不能用整余额差单独对这一笔 |
| 维护包回写 | `apply-after-resize.sh` / `target-create.json` / `receipt.json` 已写入 `--user 70:70`；`window-20261122/SHAPE.md` 记录禁忌（root entrypoint / 空 PGDATA 官方 entrypoint） |
| Redis 硬上限（另批续做） | prod **1 GiB**（UTC 00:25:43）；cn-prod **512 MiB**（UTC 00:25:54）；均 `docker update`、未停服；重建须带同 `--memory` |
| 未做 | 隔离 COS 恢复刷新；Redis recreate 定义尚未写入 compose/维护包 create（仅 runtime update + 主机 `REDIS_CAP.md`） |

维护日志：`/var/lib/tokenkey/maintenance/prod-data-4c16g/window-20261122/`（`stop.log` / `apply*.log` / `SHAPE.md`）。

## 执行窗口（历史检查单；本窗已执行）

审批范围至少覆盖：主机套餐升配（重启）、PG 容器按目标 `-c`/内存上限重建、写中断上限与回退时点。Redis 1 GiB 硬上限若同窗执行须写明；否则窗内按无 Redis cap 验收宿主 MemAvailable。

1. **窗前确认可恢复。** 重跑备料探针；硬门见上。窗口记录最长写中断及为回退预留的时间。[物理备份与恢复](../../ops/stage0/physical-backup.md)
2. **选套餐，备好旧启动定义。** 用现有镜像 ID、启动参数和网络备好正反向重建命令；核对挂载与归档。
3. **升级并验收。** 升配后核实 4 vCPU、实机内存和文件系统容量。正常关闭旧 PG，确认已停止后，用原镜像的 `pg_controldata` 只读核对目标目录集群标识，与窗前一致才启动新容器。验收：`system_identifier` 一致、`pg_is_in_recovery()=false`、四项参数 + 容器 12G 已生效、Redis 主角色与 AOF、两台网关重连、一笔真实请求用量与扣费一致、WAL 归档继续推进、`TOKENKEY_DATA_HOST_PROFILE=prod-4c16g` 已写入、只读 `expected_settings` 核对通过、再跑 memory/prep 探针与 disk-metrics selftest（确认 cache-full 仍不误报）。
4. **回退与复核。** 到约定回退时点仍无法恢复正常写入：确认集群与数据挂载无误后停止新容器，用原镜像、原挂载和旧参数恢复，并复验同一写路径。标识不符或疑似数据损坏时停止写入并走隔离核查。窗后比较热点 SQL 平均耗时与临时写入；池等待须先有两台网关基线。
