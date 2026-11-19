# Redis 角色持久化与重启回归

cn-prod 2026-11-18 的历史 503 根因是迁移只执行了运行时 `REPLICAOF NO ONE`，容器启动参数仍有 `--replicaof`，数据机重启后恢复为只读副本。并发槽 Lua 写入被拒绝，最终返回 503。

## 生产核查与本 PR 边界

2026-11-19 17:23:39 北京时间（09:23:39 UTC），prod 数据机只读核查：`role:master`，容器 Entrypoint/Cmd 无 `replicaof/slaveof`；创建于 2026-11-19 07:21:55 北京时间。17:32:57（09:32:57 UTC）补查 Redis 实际配置文件，未发现副本启动配置。TAT 回执 `inv-xbcpwdgr9d`、`inv-xbcq74guvs`。

本 PR 只修改后续迁移与监控代码，不改现网容器、不部署、不重启、不发业务请求。当前 prod/cn-prod 的历史修复已经清除了旧启动参数；不需要为了使用本 PR 而替换当前健康主库。告警代码需在后续明确授权的运维安装中生效，本次不安装、不发送测试通知。

## 后续迁移的启动契约

仅在新建、尚未承接请求的数据机副本上运行数据机脚本的 `PHASE=prepare-redis`，以 `TK_DATAPLANE_CONFIRM=prepare-redis-<SITE>-<本机私网IP>` 明确确认。该阶段只把 `dataplane-redis-entrypoint.sh` 安装到 `/etc/tokenkey/redis-runtime/entrypoint.sh`；发现已有 Redis 容器或非空数据目录就拒绝，不创建、替换或重启容器。随后在创建新副本时使用该入口：

- `/data` 是该副本独立的可写持久卷，初始为空；现网数据目录禁止用于初始化。
- 将此脚本只读挂载到容器，设为 `sh /runtime-entrypoint.sh` 启动。
- `/run/requirepass`、`/run/masterauth` 为现有凭据保管流程交付的只读密码文件。不得将密码写进命令或日志。
- 初次启动传 `REDIS_INITIAL_MASTER_HOST`（旧主私网 IPv4）与 `REDIS_INITIAL_MASTER_PORT`。该地址、镜像和卷由该站迁移计划确定，现网身份入口仍为 `deploy/hetzner/README.md`。
- 性能、AOF 等参数按现有数据机参数提供，例如 `--appendonly yes --appendfsync everysec --maxmemory-policy noeviction`。复制角色只能来自 `/data/tokenkey-redis.conf`，禁止再传 `--replicaof/--slaveof/--include`。
- 入口只在全新空数据卷上创建副本配置。已存在配置时复用文件，不受初始主库环境变量影响；已有数据却丢失配置时拒绝启动，避免静默重新复制旧主。
- 配置文件必须可由 Redis 原子重写，因此持久挂载整个 `/data` 目录，不能只把配置文件以只读单文件挂载。`CONFIG REWRITE` 会保存认证值，文件权限保持 `0600`，不得分享原文。

两侧切换前检查仍按 `design-data-plane-split.md` 执行：先数据机 `PHASE=check`（检查启动参数及持久卷），再应用机 `PHASE=check/apply`。旧式无配置文件的副本会被应用机检查拒绝；启动参数覆盖角色或缺少持久卷会被数据机检查拒绝。不要绕过检查去操作健康主库。

应用机提升 Redis 后必须 `CONFIG REWRITE` 成功，才继续 Postgres 提升与应用切流。Postgres 提升失败时，重新挂回 Redis 旧主也必须保存配置。角色变更或配置保存失败均返回失败并保持写入方停止，避免错误地恢复业务；需要按停写窗口的故障恢复流程处理。数据机 `finish` 再确认主库角色并保存配置，才完成 Postgres 收尾。

## 只读告警

数据机现有 `host_https_watch.py` 定时任务复用原通知、去重、恢复机制：

- P0：Redis 非主库，或无法确认主库状态。
- P1：运行时已是主库，但启动命令或实际配置文件仍有 `replicaof/slaveof`，存在重启退回副本风险。
- 两次连续失败触发，恢复发送一次；P1 已告警后转为运行故障直接升级 P0。

探测只调用 Docker inspect、Redis INFO 和读取配置文件；不写 Redis、不访问用户请求路径、不自动修复。Redis 与配置读取子进程超时 5 秒，容器名解析沿用共享 resolver 的超时，配置及凭据原文不会进入通知。公网 `/health` 与应用摘除策略保持原语义，共享 Redis 故障不能靠摘除全部应用解决。

## 隔离验证

本机 Docker 即可执行，不需要压测机或任何生产凭据：

```bash
TK_REDIS_REHEARSAL=1 python3 -m unittest discover -s ops/stage0 -p test_redis_promotion_restart.py -v
python3 -m unittest discover -s ops/stage0 -p test_dataplane_cutover.py -v
python3 -m unittest discover -s ops/observability -p test_redis_primary_watch.py -v
python3 -m unittest discover -s ops/observability -p test_host_https_watch.py -q
```

真实 Redis 测试创建随机命名的容器、全新卷和 `--internal` 网络，不发布端口、不连接外部网络、不挂载现网数据，完成后只清理本次资源。镜像默认 `redis:8-alpine`，可通过 `TK_REDIS_REHEARSAL_IMAGE` 指定待验证版本。

验证覆盖旧故障复现（运行时提升后重启退回副本）、持久提升后重启、删除重建容器、原有及提升后写入的数据保留、复制回退后重启仍为副本、配置保存失败拒绝继续、禁止复制启动参数覆盖、已有数据丢配置时拒绝启动，以及告警去重/升级/恢复。CI 在 ops 变更时强制执行真实 Redis 测试；普通无 Docker 的 preflight 仅运行离线测试并显式跳过容器测试。
