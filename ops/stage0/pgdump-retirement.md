# AWS prod 旧 pgdump 退役（已完成）

主恢复路径：[physical-backup.md](physical-backup.md) / pgBackRest→COS。
迁机：[pgbackrest-host-migration.md](pgbackrest-host-migration.md)。

**状态（2026-11-17 收尾）**：prod/cn-prod pgdump 桶已销毁；timer/workflow 已停；
主机 marker 拒绝重新启用旧导出。舰队盘点见
[hetzner README](../../deploy/hetzner/README.md#2026-11-17-桶盘点与销毁用户批准)。

历史工具（`retire_pgdump.py`、`preserve_legacy_pgdump.py`、
`pgdump_restore_canary_remote.sh`）仅回查，不是现网灾备入口。
勿恢复 `ops-pgdump-*` / `ops-stage0-pg-dump-refresh` 自动链。
