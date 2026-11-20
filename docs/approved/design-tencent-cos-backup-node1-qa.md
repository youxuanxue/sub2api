---
title: 腾讯 COS 数据库备份与 node1 QA 归档
status: approved
approved_by: "feng（本次会话同意三项迁移决策，并将 QA node1 保留期改为 365 天；2026-11-02 同意 prod 阶段 3 以 verify-only 结束，记为明确例外；2026-11-08 用户决定停写 AWS repo1，COS 为唯一 repository）"
approved_at: 2026-10-31
authors: [agent]
risk: high
---

# 腾讯 COS 数据库备份与 node1 QA 归档

## 决策

1. prod / cn-prod 数据库备份分别在腾讯 COS 美西 / 北京；**COS 为唯一 repository**（2026-11-08）。AWS 备份桶已停写且 2026-11 核对 `HeadBucket` 404；对象销毁另批。
2. QA S3 / raw S3 上传已退役（2026-11-08）；现行路径为本地 24h + node1 每 4h。QA 继续在 prod/cn-prod 本地保留至少 24 小时。每 4 小时在低峰复制已封口的 QA raw 文件到自有 node1；本方案不验收 QA 数据库恢复，也不改在线 PostgreSQL、网关或 QA capture。
3. node1 每日从两套腾讯 COS 备份仓库拉取已完成对象，保存为最终兜底副本。node1 故障不应影响业务；node1 副本必须有独立 manifest、校验和、配额与失败告警。

历史 AWS/Hetzner QA 对象按原 UTC 小时归属保留，不做历史搬迁。仓库中的 Tokyo node1 reader/ingress 已退役；本方案中的 node1 指用户指定的现有自有服务器。

## 闭环约束

数据库：COS 全量/增量/WAL 连续可恢复。QA：UTC 小时封口→node1 receipt→才允许本地 24h cleanup。node1 只存可核验副本，不接业务恢复。

## 现网基线

| 目标 | 现网链路 | 备注 |
|---|---|---|
| prod | 腾讯 COS 美西（唯一 repo2） | — |
| cn-prod | 腾讯 COS 北京（唯一 repo2） | — |
| prod QA | 本地 capture；QA S3 已退役（2026-11-08）；本地 provision/cleanup active | 本地 24h + node1 每 4h raw copy，node1 保留 365 天 |
| cn-prod QA | 本地 capture 和热目录；QA S3 禁止 | 本地 24h + node1 每 4h raw copy，node1 保留 365 天 |

数据库 pgBackRest 仍使用每 2 小时备份、连续 WAL、每 5 分钟 archive check，full retention 为 7 天。物理备份覆盖 PostgreSQL cluster；Redis、主机配置、密钥和 QA raw 文件不因此自动进入数据库备份。旧 pgdump 不作为新主链路。

## 目标存储契约

### 数据库

- 每站独立 COS bucket/prefix/服务账号；prod 美西、cn-prod 北京；TLS；凭据只放 root-only 文件。
- **唯一 repository 编号保持 `repo2`**（配置键，不改号为 repo1；见下「COS 为唯一 repository」）。
- pgBackRest：每 2 小时增量、连续 WAL、full retention 7 天；expire 由 pgBackRest 拥有。
- COS lifecycle 只清理未完成 multipart，不与 expire 竞争。

### node1 数据库兜底

- 每日低峰从 prod 美西 COS 和 cn-prod 北京 COS 读取已完成对象，写入 node1 独立目录，例如 `db-backup/prod/` 与 `db-backup/cn-prod/`。
- 复制使用只读 COS 凭据，按对象清单增量拉取；每轮先写临时目录，再原子发布 manifest。manifest 记录 bucket、prefix、对象数量、字节数、ETag/sha256、完成时间和源检查结果。只复制已完成对象，不复制 multipart 临时对象。
- node1 不运行 PostgreSQL restore，不接管业务数据库；兜底验收只证明对象完整、可读、可重新取得。
- 初始保留策略建议 30 天，实际值以 7 天容量观测和 node1 配额为准。达到配额或校验空间不足必须失败并告警，不能静默删除最新副本。

### QA 产品删除契约

原 `qa-raw-only` 审批已并入本文后删除该文件。以下删除边界仍有效：

- 移除 `/users/me/qa/bundles`、bundle-exports 及旧 traj export；退役端点返回 404。
- 删除 API Key 页面 QA 入口、管理员导出授权、`traj_export_enabled` / `traj_export_platforms`、Bundle 服务、worker、队列与 canary 发布路径。
- 不恢复 QA→S3 / raw S3 产品上传；不增加 SSE-C 作为 QA 产品能力。
- 数据库旧授权列与历史迁移保留兼容，不执行 `DROP COLUMN`。

### node1 QA

- 归档单位为已完成 UTC 小时；每 4 小时低峰运行一次，跳过当前未封口小时。只复制 `qa_blobs`、必要的 `qa_dlq` 文件和逐小时 manifest/hash，不复制在线 PostgreSQL 数据，不执行 QA DB restore。manifest 必须保留小时边界、文件相对路径、源字节数和 SHA-256，保证归档对象可独立盘点。
- prod 与 cn-prod 使用不同 node1 前缀、不同服务账号和独立锁；失败保留源数据并在下一轮重试。复制完成后才允许该小时按原本地 24h cleanup 规则进入删除窗口。
- node1 receipt 是 cleanup 的硬门禁：缺 receipt、校验失败、目标空间不足或 node1 不可达时，源小时继续保留并触发磁盘增长告警；不得回退到无门禁的 24 小时删除。
- 保留期改为 **365 天**；QA node1 逻辑上限为 **6 TiB**，prod 与 cn-prod 当前观测量级约 9.1 GB/day 和 0.7 GB/day，全年原始数据约 3.6 TB，尚未计入临时复制、校验、文件系统和保留余量。数据库 COS 兜底在 node1 的逻辑上限为 **2 TiB**。不启用 ext4 project quota，改由复制程序按已验证 receipt 累计并配合文件系统空间门禁执行。
- 空间策略为 **85% 使用率告警、90% 禁止新归档、95% 紧急告警**。当前 `/storage` 可用约 9.59 TiB，QA 6 TiB + 数据库 2 TiB 后仍保留约 1.59 TiB，用于临时文件和文件系统余量。
- 当前 `/storage` 单盘作为可重建本地副本使用，不为 RAID 重构停机或重格式化；COS 与 AWS 回退链承担跨节点保护。若未来要求 node1 单盘故障后仍立即可读，再另购同容量盘做 RAID1，实施前必须迁移并校验现有数据。
- 不依赖 node1 恢复 QA 数据库。若未来需要恢复，另立审批和验收契约；本设计不提供该能力。
- QA S3 退役后（2026-11-08）仓库只保留本地链路：`backend/cmd/qa-local-maintenance`（provision/cleanup）、主机定时器 `tokenkey-qa-archive`（`ops/qa/node1_archive.py`）与 `qa_archive_shards` 中的 DROP 回执。网关 maintenance/boundary 子命令、`qa-archive` 恢复 CLI、raw archive CloudFormation 与 Phase 2/IAM 巡检均已删除；主机上被 mask 的 `tokenkey-qa-maintenance`/`tokenkey-qa-boundary` 单元留待单独的主机清理。

## 迁移执行（已完成）

资源清单、双 repo 并行、验收、切主、停写 AWS 等阶段已于 2026-11 完成；逐步执行表已删。
prod COS 真实恢复证据（2026-11-08，独立数据机）支持停写 AWS；桶/栈清理为独立 infra 步骤
（2026-11 核对 physical-backup 桶 `HeadBucket` 404）。

## 2026-11-08 决定：停写 AWS repo1，COS 为唯一 repository

**2026-11-08 用户决定停写 AWS repo1，COS 为唯一 repository。** prod 与 cn-prod 同时执行。
该决定覆盖上节「不得按阶段 6 退役 AWS 的 prod 写入身份」的限制。决定依据是 2026-11-08 在独立数据机上完成的 prod COS 真实恢复（见下节）。

| 项 | 决定 |
|---|---|
| repository 编号 | COS 保持 `repo2`，不改编号为 `repo1` |
| 运行时配置 | 只渲染 `repo2-*`，无任何 `repo1-*`、AWS endpoint 或 STS token |
| 身份刷新 | prod `tencent_backup_identity.py`、cn-prod `cn_backup_identity.py` 不再读 `/root/.aws/credentials`、不调 STS；缺 COS 凭据即失败并保留现配置，绝不回退 AWS |
| runner | `PRIMARY_REPO = 2`；配置里没有 repo2 时写 `primary_repository_missing` 硬故障回执，不备份其他仓库 |
| AWS 对象 | 停写后原样保留；桶/栈清理为单独 infra 步骤。2026-11-11 CFN `tokenkey-prod-physical-backups`=`DELETE_COMPLETE`；2026-11-17 核对 prod/cn physical-backup 桶 `HeadBucket` 404 |

**为什么保持 repo2，不改成 repo1。** pgBackRest 不在仓库内记录编号：`archive.info` /
`backup.info` 只记 stanza 与 PG system-id/版本，编号只是配置键（`repoN-*`）和 `--repo=N`
选择器。所以改号不会破坏 COS 上的链，但它不带来任何收益，反而有成本：

1. 现有 `--repo=2` 的所有入口（`cos_restore_acceptance.py --repo 2`、runbook、回执里的
   `stanza_status["2"]`）都要跟着改，改漏一处就静默读到别的仓库；
2. 保持编号时，切换就是「删掉所有 `repo1-*` 行」，`repo2-*` 行逐字不变，可逐字 diff 审核；
3. 遗留的 `--repo=1` 调用会以 `[032] key '1' is not valid for 'repo' option` 明确失败，
   而不是悄悄落到另一个仓库上。

**单仓库下的 pgBackRest 2.58 行为（2026-11-08 本地 postgres:18-alpine + pgBackRest
2.58.0 实测，从双仓库热切到仅 repo2，PG 持续运行）：**

| 命令 | 结果 |
|---|---|
| `archive-push`（`archive_command` 无 `--repo`） | 只推送到配置中的 repo2；切换后 `pg_switch_wal()` 立即归档，`failed_count` 0 |
| `check`（不接受 `--repo`） | 校验唯一的 repo2，通过 |
| `info`（不带 `--repo`） | 只列 repo2，`status.code` 0 |
| `backup`（带或不带 `--repo=2`） | 都写 repo2；runner 继续显式传 `--repo=2` |
| `expire --repo=2` | 只按 repo2 的 `retention-full=7` 清理 repo2 的备份及其 WAL |
| `--repo=1` | 报 `[032]` 失败 |
| `restore --repo=2` | 仅凭 repo2 恢复成功 |
| 被移除的 repo1 | 对象保持原状，不被 expire 或改写 |

**WAL 链为何不断。** COS 自 2026-11-02 起就与 AWS 接收同一条 `archive-push` 扇出，WAL
连续（2026-11-08 只读核对：prod repo2 `...330041` → `...45AE`，cn-prod repo2
`...16B7` → `...240063`，max 与 repo1 一致）。移除 repo1 只是缩小扇出，不新建 stanza，
也不需要原先 cn-prod 那套「先 staging 再上线」的 repo2 预检（那套预检防的是无 stanza 的
新仓库拖住 AWS-only 的 `archive_command`），因此已删除。配置写入是原子替换，切换瞬间
正在执行的 `archive-push` 读的是旧配置，仍会成功写入两个仓库。

**留给单独 infra 步骤的 AWS 资源（不在本变更内删除）：**

- prod：S3 桶 `tokenkey-prod-physical-backup-682751977094-us-east-1`（活前缀
  `candidates/tencent-usw-20261022/postgres`，Tokyo 历史只读前缀 `prod/postgres`）；
- cn-prod：S3 桶 `tokenkey-cn-prod-physical-backup-682751977094-ap-northeast-1`（前缀 `cn-prod/postgres`）；
- 两台主机共用的 SSM Hybrid 角色 `tokenkey-lightsail-ssm-hybrid`（RoleId
  `AROAZ55ZQM2DC6BZQWQ4B`）上的 S3 备份权限。角色本身**不能删**：两台主机的 SSM 运维
  通道（run-probe、发布、诊断）都靠它，只撤销 S3 备份相关的 policy。


**现网**：只渲染 `repo2-*`；身份刷新不读 AWS/STS；runner `PRIMARY_REPO=2`。
遗留 AWS 桶/Hybrid 角色 S3 备份权限若仍存在，只作只读/收尾，不恢复写入。

