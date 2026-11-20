# PostgreSQL 物理备份变更单

审批基线：[design-prod-physical-backup](../../docs/approved/design-prod-physical-backup.md)。
现网身份只从[基础设施入口](../../deploy/hetzner/README.md)和 resolver 取得。
新机恢复见 [迁机工作流](pgbackrest-host-migration.md) 与 `tokenkey-host-replacement`。
不做整机/卷快照常备策略，不做 AWS 临时机隔离演练（见 [`declined.md`](../../docs/decisions/declined.md)）。
入口证书离机包见 `ops/archive/README.md`。

## 现网（腾讯美西）

| 项目 | 实况 |
|---|---|
| 主机 | 数据机 Lighthouse（矩阵 `node=data`），**amd64**；日常远程 TAT，Hybrid 仅 break-glass |
| PG / Redis | 数据机容器（别名 `postgres`/`redis`）；PG 18.x / pgBackRest；Redis AOF |
| 数据目录 | 明文根盘业务路径（LUKS 已退役）；无 EBS/DLM；**不做**整机/卷常备快照 |
| 活链 | COS `repo2` `/prod/postgres`；`tencent_backup_identity.py` 渲染配置 |
| 操作入口 | 宿主 `tencent_pgbackrest.py backup|check`（仓库 SSOT `deploy/tencent/tencent_pgbackrest.py`）；timer `tokenkey-postgres-{backup,archive-check}`；验收 `ops/backup/cos_restore_acceptance.py` |
| 旧 pgdump | 调度已退役；AWS pgdump 桶已销毁（见舰队 README「2026-11-17 桶盘点」与 [pgdump-retirement.md](pgdump-retirement.md)） |

`prod/postgres`（Tokyo timeline 1）冻结只读；不得写入，也不作为恢复默认。
Tokyo/AWS 临时恢复百科已删。以下 basename **仅历史回查 / 契约测试**，不是现网主路径：
`prepare_physical_backup.py`、`install_postgres_backup.py`、`run_physical_restore.py`、
`retire_pgdump.py`、`preserve_legacy_pgdump.py`、`pgdump_restore_canary_remote.sh`。

## 日常命令

```bash
# 宿主（经 run-probe / TAT 到数据机）
python3 deploy/tencent/tencent_pgbackrest.py check
python3 deploy/tencent/tencent_pgbackrest.py backup   # 仅批准窗口

# 离机验收（COS）
python3 ops/backup/cos_restore_acceptance.py --help
```

整机/跨云恢复：`tokenkey-host-replacement` playbook + [pgbackrest-host-migration.md](pgbackrest-host-migration.md)。
节奏与 RPO 不变量见审批基线；勿把本文件读成仍写 AWS S3 主链。
