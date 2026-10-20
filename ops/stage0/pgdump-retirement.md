# AWS prod 旧 pgdump 退役

授权：用户在本次会话同意停旧任务、保留数据至少 30 天，并要求对齐 origin/main 整理退役。
仅适用于当前 prod；[迁机工作流](pgbackrest-host-migration.md) 改用 pgBackRest。
备份验收及未完成事项见 [physical-backup.md](physical-backup.md)。

## 退役清单

| 入口 | 处置 |
|---|---|
| prod `tokenkey-pgdump.timer` | 停用；不终止正在运行的导出；保留 service 与历史文件 |
| `ops-pgdump-restore-canary.yml` | 移除周定时，仅保留历史手动入口；退役 prod 拒绝触发自动补 dump |
| `ops-stage0-pg-dump-refresh.yml` / `pg_dump_refresh_via_ssm.sh` | 标记 legacy；主机 marker 存在即拒绝重新安装/启用旧任务 |
| `deploy/aws/stage0/tokenkey-pgdump.sh` | marker 存在即失败，不能滚动清理旧文件或继续导出 |
| EC2 / Hetzner bootstrap | 默认不启用 legacy timer；明确 opt-in 且没有退役 marker 才可启用；不代表自动装好了物理备份 |
| `ops-daily-diagnostics.yml` | 使用物理备份 health，缺失/过期/损坏仍告警；沿用传输完整性校验 |
| 主机 HTTPS watcher / fleet paused_jobs | pgdump 期望 disabled/inactive；意外恢复会告警；物理备份与 WAL 继续监控 |
| 旧 S3 生命周期 | 取消数据过期，仅保留未完成 multipart 清理；共享模板中的媒体策略不变 |
| `ops-cn-pgdump.yml` / cn-prod 脚本 | 继续运行；没有已验收的物理替代路径，不在此次退役范围 |
| 旧 SQL 导入、解密、存储恢复工具 | 保留，用于历史副本的隔离读取；不再作为 prod 自动灾备主路径 |

## 执行入口与失败处理

先查当前物理备份、WAL、最新及历史恢复回执，确认 pgdump 服务空闲。以下默认只读计划：

```bash
python3 ops/stage0/preserve_legacy_pgdump.py --bucket "$LEGACY_BUCKET" \
  --receipt /tmp/pgdump-retention-plan.json
# 核对后改用新的 receipt 路径并加 --apply；原始生命周期回执禁止覆盖。
```

该工具仅支持当前账户的专用 prod pgdump 桶；移除自动过期不恢复此前已到期数据，
不设置第 30 天自动删除。保留至少 30 天，届时检查物理链/恢复能力并另行决定清理。
保留所有恢复密钥/SSM 路径；不能只保留密文却删除解密材料。

将 `retire_pgdump.py`、同目录 `postgres_backup.py` 与已 apply 的保留回执经 run-probe 投递到 prod，
指定 resolver 确认的 hostname 与当前 watcher config 路径：

```bash
python3 /tmp/retire_pgdump.py --expected-host "$EXPECTED_HOST" \
  --retention-receipt /tmp/pgdump-retention-applied.json --watch-config "$WATCH_CONFIG"
# 检查计划后重复并加 --apply。
```

应用前跑 preflight。工具使用现有 host.lock 非阻塞排他锁；任务在跑或锁忙就失败，待自然完成后重试。
只停 timer，不重启网关/PG/Redis/Caddy，也不执行数据删除。保存 before 配置、服务条件 drop-in
、旧 exporter 原文件和退役回执；同时给现役 exporter 加拒绝标记，防止绕过 systemd 直接导出。
已退役再次调用会验证 timer/service/drop-in/watcher，不能只看 marker 就宣称成功。
若 status=retiring 或配置不一致，立即检查保存的计划及实际状态，不自动删除 marker 或反向恢复任务。

取消退役是独立操作：确认仍需旧导出、空间与时段，持相同 host.lock 核对服务空闲后，
恢复 watcher 的旧配置和保存的 exporter 原文件、移除本次 drop-in/marker、daemon-reload 再恢复 timer。
不恢复旧 7 天生命周期，否则会违反已承诺的保留期。物理备份故障应先修其归档与磁盘问题；
恢复 pgdump 不会自动解决 WAL 堆积。

## 验收

分别记录 timer disabled/inactive、服务 idle、物理 health=ok、S3 无数据 Expiration、对象仍可列出、
本地历史文件未删，以及生产容器启动时间前后不变和健康探针结果。
线上工作流可先 disable，仓库取消 schedule 与诊断替换需要 PR 合并后生效；二者不能混称完成。
