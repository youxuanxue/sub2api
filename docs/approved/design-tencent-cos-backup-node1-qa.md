---
title: 腾讯 COS 数据库备份与 node1 QA 归档
status: approved
approved_by: "feng（本次会话同意三项迁移决策，并将 QA node1 保留期改为 365 天；2026-11-02 同意 prod 阶段 3 以 verify-only 结束，记为明确例外；2026-11-08 用户决定停写 AWS repo1，COS 为唯一 repository）"
approved_at: 2026-10-31
authors: [agent]
risk: high
revised: 2026-11-20（删除迁机执行/实测散文；保留现网契约）
---

# 腾讯 COS 数据库备份与 node1 QA 归档

## 决策

1. prod / cn-prod 数据库备份分别在腾讯 COS 美西 / 北京；**COS 为唯一 repository**（2026-11-08）。AWS 备份桶已停写且核对 `HeadBucket` 404（见舰队 README）。
2. QA S3 / raw S3 上传已退役；现行路径为本地 24h + node1 每 4h。不验收 QA 数据库恢复，不改在线 PostgreSQL、网关或 QA capture。
3. node1 每日从两套腾讯 COS 备份仓库拉取已完成对象，保存为最终兜底副本。node1 故障不应影响业务；副本须有独立 manifest、校验和、配额与失败告警。

历史 AWS/Hetzner QA 对象不做历史搬迁。仓库中的 Tokyo node1 reader/ingress 已退役；node1 指用户指定的现有自有服务器。

## 闭环约束

数据库：COS 全量/增量/WAL 连续可恢复。QA：UTC 小时封口→node1 receipt→才允许本地 24h cleanup。node1 只存可核验副本，不接业务恢复。

## 现网基线

| 目标 | 现网链路 | 备注 |
|---|---|---|
| prod | 腾讯 COS 美西（唯一 repo2） | — |
| cn-prod | 腾讯 COS 北京（唯一 repo2） | — |
| prod QA | 本地 capture；QA S3 已退役；本地 provision/cleanup active | 本地 24h + node1 每 4h raw copy，node1 保留 365 天 |
| cn-prod QA | 本地 capture 和热目录；QA S3 禁止 | 本地 24h + node1 每 4h raw copy，node1 保留 365 天 |

数据库 pgBackRest：每 2 小时备份、连续 WAL、每 5 分钟 archive check，full retention 7 天。物理备份覆盖 PostgreSQL cluster；Redis、主机配置、密钥和 QA raw 不因此自动进入数据库备份。旧 pgdump 不是主链路（见 [`physical-backup.md`](../../ops/stage0/physical-backup.md)）。

## 目标存储契约

### 数据库

- 每站独立 COS bucket/prefix/服务账号；prod 美西、cn-prod 北京；TLS；凭据只放 root-only 文件。
- **唯一 repository 编号保持 `repo2`**（配置键，不改号为 repo1）。改号无收益且易漏改 `--repo=2` 入口；遗留 `--repo=1` 应以明确失败暴露。
- 运行时只渲染 `repo2-*`；身份刷新（`tencent_backup_identity.py` / `cn_backup_identity.py`）不读 AWS/STS；runner `PRIMARY_REPO = 2`，无 repo2 则硬故障，不备份其他仓库。
- pgBackRest：每 2 小时增量、连续 WAL、full retention 7 天；expire 由 pgBackRest 拥有。
- COS lifecycle 只清理未完成 multipart，不与 expire 竞争。

### node1 数据库兜底

- 每日低峰从 prod 美西 COS 和 cn-prod 北京 COS 读取已完成对象，写入独立目录（如 `db-backup/prod/`、`db-backup/cn-prod/`）。
- 只读 COS 凭据、按清单增量拉取；临时目录→原子发布 manifest（bucket、prefix、对象数、字节、ETag/sha256、完成时间）。不复制 multipart 临时对象。
- node1 不运行 PostgreSQL restore，不接管业务库；验收只证明对象完整可读。
- 初始保留建议 30 天，以 7 天容量观测与配额为准；达配额或校验空间不足必须失败告警。

### QA 产品删除契约

原 `qa-raw-only` 已并入本文后删除。删除边界仍有效：

- 移除 `/users/me/qa/bundles`、bundle-exports 及旧 traj export；退役端点 404。
- 删除 API Key 页 QA 入口、管理员导出授权、`traj_export_enabled` / `traj_export_platforms`、Bundle 服务、worker、队列与 canary 发布路径。
- 不恢复 QA→S3 / raw S3 产品上传；不增加 SSE-C 作为 QA 产品能力。
- 数据库旧授权列与历史迁移保留兼容，不执行 `DROP COLUMN`。

### node1 QA

- 归档单位为已完成 UTC 小时；每 4 小时低峰运行，跳过未封口小时。只复制 `qa_blobs`、必要 `qa_dlq` 与逐小时 manifest/hash。
- prod 与 cn-prod 使用不同前缀、服务账号和锁；失败保留源数据并重试。复制完成后才允许该小时按本地 24h cleanup 进入删除窗口。
- node1 receipt 是 cleanup 硬门禁：缺 receipt / 校验失败 / 空间不足 / node1 不可达 → 源小时继续保留并告警。
- 保留期 **365 天**；QA node1 逻辑上限 **6 TiB**；数据库 COS 兜底逻辑上限 **2 TiB**。不启用 ext4 project quota；由复制程序 + 文件系统空间门禁执行。
- 空间策略：**85% 告警、90% 禁止新归档、95% 紧急告警**。
- `/storage` 单盘为可重建本地副本，不为 RAID 重构停机；跨节点保护靠 COS。不依赖 node1 恢复 QA 数据库。
- QA S3 退役后仓库只保留本地链路：`backend/cmd/qa-local-maintenance`、主机定时器 `tokenkey-qa-archive`（`ops/qa/node1_archive.py`）与 `qa_archive_shards` DROP 回执。

## 运维入口

- 舰队 / 恢复：[`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)
- 物理备份命令：[`ops/stage0/physical-backup.md`](../../ops/stage0/physical-backup.md)
- 迁机：[`ops/stage0/pgbackrest-host-migration.md`](../../ops/stage0/pgbackrest-host-migration.md) / `tokenkey-host-replacement`
- QA：[`ops/qa/README.md`](../../ops/qa/README.md)

迁机执行表、双 repo 并行实测与 AWS 桶清理回执已删；勿恢复。
