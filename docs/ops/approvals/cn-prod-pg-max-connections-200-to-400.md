# 审批：cn-prod 数据机 Postgres `max_connections` 200 → 400

状态：**已执行**（2026-11-17 13:46 BJT / 05:46 UTC；用户批准「批准重启 pg」）

日期：2026-11-17  
范围：cn-prod 数据机 `lhins-odyx5eis` / `10.2.4.5` / 容器 `tokenkey-postgres`  
依据：`docs/ops/reports/cn-prod-deepseek-flash-pool-perf-20261117.md`；`docs/decisions/decided.md`（用户确认：低峰须发审批）

## 为什么

- 400 并发正式压测时，两应用 `DATABASE_MAX_OPEN_CONNS=40` 合计已贴满 ~81 活跃连接。
- 现网应用池已升到 **80/应用**（合计 160 open）。数据机现网 `SHOW max_connections` = **200**，对 160 应用池 + 备份/运维/监控余量偏紧。
- `max_connections` 由容器启动参数 `-c max_connections=200` 注入，**改值必须重建/重启 Postgres 进程**（短中断）。

## 变更

| 项 | 现状 | 目标 |
|---|---|---|
| `max_connections` | 200 | **400** |
| 应用侧 `DATABASE_MAX_OPEN_CONNS` | 80（已上线） | 不变 |
| 其他 PG 调参（shared_buffers 等） | 保持 | 本审批**不改** |

## 影响与窗口

- **中断**：Postgres 容器 recreate/restart 期间，两台应用机短暂无法建新连接；在途请求可能失败；入口可能出现 5xx。
- **窗口要求**：**低峰**（建议业务 RPM 显著低于白天峰值；避开发版/备份高峰）。
- **预计停机**：约 30–90 秒（含健康检查回流）；若 WAL/启动校验慢，按实际日志延长。
- **回滚**：把启动参数改回 `max_connections=200` 再 restart（仍有一次短中断）。

## 执行步骤（批准后才做）

1. 再次确认低峰：入口/应用近期 RPM、`pg_stat_activity` 连接数。
2. 备份当前 `docker inspect tokenkey-postgres` 的 `Config.Cmd`。
3. 将启动参数 `-c max_connections=200` 改为 `-c max_connections=400`（与现有启动入口一致；**不**改 archive/pgbackrest 相关项）。
4. recreate/restart `tokenkey-postgres`；等 `pg_isready` + `SHOW max_connections` = 400。
5. 抽检两应用 `/health=200`、抽样写路径；确认 `pg_stat_activity` 正常。
6. 在本文件与压测报告执行状态表标记 **已完成** + 实际窗口时间。

## 明确不做

- 批准前不重启、不 recreate。
- 本审批不含 Redis、应用规格、入口权重（那些已单独落地）。

## 执行回执

| 项 | 结果 |
|---|---|
| 方式 | `docker stop` → rename 旧容器 → `docker create --entrypoint postgres --user 70:70`（仅改 `-c max_connections=400`）→ start |
| `SHOW max_connections` | **400** |
| `pg_is_in_recovery` | `f` |
| `archive_mode` | `on` |
| 就绪 | start 后约 **1s** `pg_isready` |
| 应用/公网 | app-a/app-b `/health=200`，`https://api.galaxywaveai.com/health=200` |
| 旧容器保留 | `tokenkey-postgres.before-maxconn2-20261117T054640Z`（已停止；确认现网 PG `max_connections=400` 健康后可 `docker rm`，**勿**动运行中的 `tokenkey-postgres`） |

说明：同日首次 recreate 因未保留 `Entrypoint=postgres` 误走镜像 entrypoint 失败，已立刻 rollback 旧容器；第二次按上表成功。首次失败窗口约数分钟；成功切换本身约 1 秒级。

## 审批栏

- [x] 批准执行（用户 2026-11-17：「批准重启 pg」；当时流量偏低但非凌晨名义低峰）
- [ ] 拒绝 / 改值（请注明目标连接数或改期）

审批人：用户会话确认  
窗口：2026-11-17 ~13:46 BJT（即时）  
签字/确认日期：2026-11-17
