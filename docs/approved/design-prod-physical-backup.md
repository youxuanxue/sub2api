---
title: TokenKey 生产数据库物理增量备份
status: approved
approved_by: "feng — 本次会话：同意。请继续。务必精准、高效，不要复杂化。"
risk: high
revised: 2026-11-20（AWS/EBS/pgdump 过渡叙事删除；节奏与 COS 唯一仓库见 design-tencent-cos-backup-node1-qa）
---

# TokenKey 生产数据库物理增量备份

## 决策

使用 pgBackRest 物理全量、块增量与连续 WAL 作为主数据库恢复路径。
**现网存储与唯一 repository** 见 [`design-tencent-cos-backup-node1-qa.md`](design-tencent-cos-backup-node1-qa.md)；
运维入口 [`ops/stage0/physical-backup.md`](../../ops/stage0/physical-backup.md)。

## 节奏（仍有效）

| 项目 | 值 |
|---|---|
| 工具 | pgBackRest（验证后固定版本） |
| 全量 | 每周一次，低峰 |
| 增量 | 每 2 小时 |
| WAL | 连续归档；健康 RPO ≤15 分钟 |
| 可恢复历史 | 稳态至少最近 7 天连续点 |
| 清理 | retention-full=7；由 pgBackRest expire 拥有 |
| 范围 | 整个 PostgreSQL cluster；不含 Redis/主机配置/QA blobs |

旧 pgdump 调度已退役（见 `ops/stage0/pgdump-retirement.md`）；不得把本文件读成仍写 AWS S3 主链。
