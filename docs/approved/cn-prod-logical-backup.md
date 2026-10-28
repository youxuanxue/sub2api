---
title: cn-prod logical backup activation
status: approved
approved_by: "feng — 2026-10-19：同意建议执行顺序：1，2，cn-prod适配pgdump，3，4"
risk: high
---

# cn-prod logical backup

> **状态（2026-10-23）：退役中。** cn-prod 已迁至腾讯北京并运行 pgBackRest
> （stanza `tokenkey`，repo `/cn-prod/postgres`；现网核验 WAL 归档正常、
> `failed_count=0`、`backup_count=4`），逻辑 dump 不再是备份路径。
>
> 本文件描述的 `ops-cn-pgdump.yml` 已改名 `ops-cn-backup-watch.yml`：**保留**每 2 小时
> 的调度，但改为核验 pgBackRest 回执。保留调度是刻意的——它曾是 cn-prod 唯一的备份
> 监控入口（`ops-daily-diagnostics.yml` 只覆盖 prod），直接删掉会让一份健康的备份
> 失去全部告警。
>
> 回执由 `tokenkey-cn-postgres-archive-check.service` 每 5 分钟产出
> （`deploy/domestic/cn_pgbackrest.py`）。5 分钟不是偏好而是契约：
> `physical_backup` 门禁要求回执在 900 秒内，而备份每 2 小时才跑一次。
>
> **已完成（2026-10-23）**：`CN_PGDUMP_ENABLED=false` 已关闭，逻辑 dump 链路停止。
> cn-prod 主机本就没有 pgdump timer（备份由 Actions 远程触发），所以关开关即完全停链。
>
> 刻意**保留**的东西：
> - `CN_PGDUMP_BUCKET` 与 S3 历史副本——退役不删既有数据；
> - `/var/lib/tokenkey/pgdump/tokenkey-20261022T115847Z.sql.gz` 及两份 restore 日志——
>   那是 10-22 迁机时实际用于恢复的源，是这次迁移唯一的逻辑回滚点。磁盘占用 7%
>   （106G 可用），没有清理压力，删掉的代价远大于收益；
> - `/opt/tokenkey/cn-backup-run.py`（旧 runner）——已无任何 unit 引用它，无法被 systemd
>   触发，留作回滚路径。
>
> 新监控上线前已验证稳定：archive-check 连续 31 次成功、`NRestarts=0`、回执 137 秒新鲜
> （门禁要求 ≤900s）、`backup_count` 4→8，且 `backup.json` 已由新 runner 在 02:30 与
> 08:30 两次真实备份后产出（`status=ok`）。本文件自此仅作历史记录。
>
> **文末步骤不再执行。** 下面是 2026-10-19 的逻辑 dump 说明。逻辑 dump 已于 2026-10-23 停止。不要再按这些步骤跑 `tokenkey-pgdump.sh`。现行备份是 pgBackRest，监控是 `ops-cn-backup-watch.yml`。

Reuse `deploy/aws/stage0/tokenkey-pgdump.sh` for the independent domestic
PostgreSQL database, including usage details. No database restart, gateway
cutover, production restore or change to AWS prod backup frequency is required.

`deploy/domestic/pgdump.py` runs the shared dump through the existing cn-prod
SSM target, then grants one object upload through a 30-minute signed S3 URL.
The host receives no permanent AWS credentials. `pgdump-stack.yaml` owns the
private encrypted domestic bucket, seven-day object retention and narrowly
scoped runner access. The bucket is retained when the stack is deleted.

`.github/workflows/ops-cn-pgdump.yml` schedules backups every two hours at minute
17 after `CN_PGDUMP_ENABLED=true`. GitHub scheduling can be delayed; recovery
freshness follows the last verified object, never the nominal schedule. Six
local rolling copies use the shared retention owner. A failed upload keeps the
local copy and fails the workflow; only a successful remote size/checksum
comparison writes `offbox_verified`. S3 validates the signed SHA-256 on upload.
This is backup transfer evidence,
not a restore acceptance claim.

Before enabling scheduling, create one real backup, download it from S3,
restore into an isolated PostgreSQL instance and compare usage rows, timestamps
and exact costs against the dump snapshot using `pgdump_usage_evidence.py`.
Never compare a historical snapshot with the changing live database. Preserve
the receipt and verify destruction of the temporary restore instance.

Rollback disables `CN_PGDUMP_ENABLED`; retain local and S3 copies. No live
database replacement or deletion is part of rollback. The weekly-full and
two-hour-incremental pgBackRest rollout remains AWS-only under
`design-prod-physical-backup.md`.
