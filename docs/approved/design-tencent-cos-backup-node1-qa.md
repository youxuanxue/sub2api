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

1. prod 数据库备份迁到腾讯 COS 美西地域；cn-prod 数据库备份迁到腾讯 COS 北京地域。AWS S3 在迁移期保留为只读回退链，腾讯 COS 完成独立恢复验收后停止 AWS 写入和运行时身份。AWS 对象删除是单独的不可逆收尾动作。
2. QA 继续在 prod/cn-prod 本地保留至少 24 小时。每 4 小时在低峰复制已封口的 QA raw 文件到自有 node1；本方案不验收 QA 数据库恢复，也不改在线 PostgreSQL、网关或 QA capture。
3. node1 每日从两套腾讯 COS 备份仓库拉取已完成对象，保存为最终兜底副本。node1 故障不应影响业务；node1 副本必须有独立 manifest、校验和、配额与失败告警。

历史 AWS/Hetzner QA 对象按原 UTC 小时归属保留，不做历史搬迁。仓库中的 Tokyo node1 reader/ingress 已退役；本方案中的 node1 指用户指定的现有自有服务器。

## 乔布斯复审结论

方案可以成立，但必须收敛为三个清晰的闭环：

1. **数据库保护闭环**：AWS 与 COS 在迁移期必须通过 pgBackRest 双 repository 同时写入同一批 backup/WAL；不能只复制已经过期的对象，也不能把“新 full 成功”误当成连续 WAL 已保护。
2. **QA 归档闭环**：每个 UTC 小时先封口、复制、校验并写 node1 receipt；receipt 成功之前，线上 cleanup 不得删除该小时源文件。node1 不可用时延长本地保留并告警，不能为了维持 24 小时数字而删除唯一副本。
3. **兜底闭环**：node1 只保存可核验的对象和 manifest，不承担业务服务或 QA 数据库恢复。当前单盘 node1 只作为可重建本地副本，COS 与 AWS 回退链承担真正的跨节点保护；目录仍使用独立权限。

所有复制任务都必须有低优先级、带宽上限、临时目录和原子 receipt。不得抢占在线 PostgreSQL/网关资源，不得通过重启服务或清理未知文件来“修复”归档失败。

## 现网基线

| 目标 | 当前链路 | 迁移目标 |
|---|---|---|
| prod | AWS S3 `us-east-1`，活前缀 `candidates/tencent-usw-20261022/postgres` | 腾讯 COS 美西独立前缀 |
| cn-prod | AWS S3 `ap-northeast-1`（东京），前缀 `cn-prod/postgres` | 腾讯 COS 北京独立前缀 |
| prod QA | 本地 capture；S3 maintenance 当前暂停；本地 provision/cleanup active | 本地 24h + node1 每 4h raw copy，node1 保留 365 天 |
| cn-prod QA | 本地 capture 和热目录；QA S3 禁止 | 本地 24h + node1 每 4h raw copy，node1 保留 365 天 |

数据库 pgBackRest 仍使用每 2 小时备份、连续 WAL、每 5 分钟 archive check，full retention 为 7 天。物理备份覆盖 PostgreSQL cluster；Redis、主机配置、密钥和 QA raw 文件不因此自动进入数据库备份。旧 pgdump 不作为新主链路。

## 目标存储契约

### 数据库

- 每个环境使用独立 COS bucket、独立 prefix 和独立服务账号；禁止复用 AWS prefix，禁止把 prod 与 cn-prod 放入同一 pgBackRest timeline。
- prod 目标地域为腾讯 COS 美西，cn-prod 目标地域为腾讯 COS 北京。最终地域代码、bucket 名称和 endpoint 由资源清单填入，不能在脚本中猜测。
- COS 使用 S3-compatible API、TLS、服务端加密/KMS。pgBackRest 当前 `cipher=none`，迁移时必须明确 COS 侧加密和密钥轮换。
- 备份账号只拥有目标 bucket 的 list/get/put/abort/expire 所需权限；恢复账号只读。SecretId/SecretKey 只放 root-only 受保护文件或 tmpfs，不进入 `.env`、Git、SSM 输出或回执。
- 迁移开始时为每个 COS 仓库生成新的独立 full backup 和 WAL 链。AWS 旧链保持只读，直到 COS 最新点、历史点和 PITR 验收完成。
- 迁移期使用 pgBackRest 双 repository：AWS 作为现有 `repo1`，COS 作为临时 `repo2`，backup、archive-push、check 和 expire 的行为必须在隔离环境与真实低峰分别验证。当前单 repository 实现不能直接宣称支持并行保护；缺少双 repository 实现前不得切换。
- COS 验收通过后，COS 提升为主 repository；AWS 权限降为只读并保留 30 天。停止 AWS 写入前必须确认最近一个完整 backup、连续 WAL、历史恢复点和 node1 对象清单均已完成。
- pgBackRest 的 7 天链清理由 pgBackRest 负责；COS lifecycle 只处理未完成 multipart，避免与 pgBackRest expire 竞争。

### node1 数据库兜底

- 每日低峰从 prod 美西 COS 和 cn-prod 北京 COS 读取已完成对象，写入 node1 独立目录，例如 `db-backup/prod/` 与 `db-backup/cn-prod/`。
- 复制使用只读 COS 凭据，按对象清单增量拉取；每轮先写临时目录，再原子发布 manifest。manifest 记录 bucket、prefix、对象数量、字节数、ETag/sha256、完成时间和源检查结果。只复制已完成对象，不复制 multipart 临时对象。
- node1 不运行 PostgreSQL restore，不接管业务数据库；兜底验收只证明对象完整、可读、可重新取得。
- 初始保留策略建议 30 天，实际值以 7 天容量观测和 node1 配额为准。达到配额或校验空间不足必须失败并告警，不能静默删除最新副本。

### node1 QA

- 归档单位为已完成 UTC 小时；每 4 小时低峰运行一次，跳过当前未封口小时。只复制 `qa_blobs`、必要的 `qa_dlq` 文件和逐小时 manifest/hash，不复制在线 PostgreSQL 数据，不执行 QA DB restore。manifest 必须保留小时边界、文件相对路径、源字节数和 SHA-256，保证归档对象可独立盘点。
- prod 与 cn-prod 使用不同 node1 前缀、不同服务账号和独立锁；失败保留源数据并在下一轮重试。复制完成后才允许该小时按原本地 24h cleanup 规则进入删除窗口。
- node1 receipt 是 cleanup 的硬门禁：缺 receipt、校验失败、目标空间不足或 node1 不可达时，源小时继续保留并触发磁盘增长告警；不得回退到无门禁的 24 小时删除。
- 保留期改为 **365 天**；QA node1 逻辑上限为 **6 TiB**，prod 与 cn-prod 当前观测量级约 9.1 GB/day 和 0.7 GB/day，全年原始数据约 3.6 TB，尚未计入临时复制、校验、文件系统和保留余量。数据库 COS 兜底在 node1 的逻辑上限为 **2 TiB**。不启用 ext4 project quota，改由复制程序按已验证 receipt 累计并配合文件系统空间门禁执行。
- 空间策略为 **85% 使用率告警、90% 禁止新归档、95% 紧急告警**。当前 `/storage` 可用约 9.59 TiB，QA 6 TiB + 数据库 2 TiB 后仍保留约 1.59 TiB，用于临时文件和文件系统余量。
- 当前 `/storage` 单盘作为可重建本地副本使用，不为 RAID 重构停机或重格式化；COS 与 AWS 回退链承担跨节点保护。若未来要求 node1 单盘故障后仍立即可读，再另购同容量盘做 RAID1，实施前必须迁移并校验现有数据。
- 不依赖 node1 恢复 QA 数据库。若未来需要恢复，另立审批和验收契约；本设计不提供该能力。

## 执行阶段与门禁

1. **资源清单与演练**：确认两个 COS bucket/endpoint、服务账号、KMS、node1 SSH/同步端点、目录、quota、磁盘/RAID/加密和告警；用合成 PostgreSQL 与合成 QA 文件完成 COS PUT/GET、node1 copy、断点重试和 checksum 演练。
2. **COS 并行保护**：不停止 AWS timer、不删除 AWS 对象。为两环境启用经验证的双 repository，完成 COS full、自然增量和连续 WAL；连续观察至少两个完整业务日。
3. **数据库验收**：从 COS 恢复最新点和历史点，验证 WAL 连续性、关键表/约束/序列、账务聚合和恢复耗时；确认 archive check 新鲜且 RPO 门禁通过。失败时保持 AWS 为主。
4. **QA 验收**：完成至少两轮 4 小时复制，manifest/hash、源文件计数、目标字节数和失败重试均通过；确认 cleanup 只删除已有 node1 receipt 的小时；不把“文件复制成功”描述成 QA 数据库恢复成功。
5. **切换**：将 prod/cn-prod 主备份配置切到各自 COS，保留 AWS 只读回退和最近回执；观察一个完整备份周期。node1 每日 COS 兜底必须先有一轮成功回执。
6. **AWS 退役**：先停 AWS 写入身份和 timer，确认 COS 与 node1 连续健康；AWS bucket/object/KMS/角色删除另立收尾单，列出精确对象范围、保留截止时间和最终恢复验证，不由普通切换自动执行。

## 阶段 3 执行记录与 prod 例外（2026-11-02）

阶段 3 要求「从 COS 恢复最新点和历史点」。实际执行分成了两种结果。

**cn-prod：完整验收通过。** 从北京 COS 的 repo2 恢复了最旧全量
（`20261102-202410F`）和最新点（`...203119I`），两点都走完恢复流程并可查询：
schema 与线上一致（409 表 / 96 外键 / 281 主键 / 86 序列），行数与金额均落后线上
但不超过，`usage_logs` 与 `usage_billing_dedup` 的行数差在两点和线上都是 26。
恢复 80.3s / 75.3s，重放启动约 2.4s。两份回执齐备。

**prod：只做了 verify，恢复验收未完成 —— 这是一条明确的例外，不是通过。**

| | 数值 |
|---|---|
| 库大小 | 21 GB |
| 根卷可用 | 28 GB（恢复后剩 6 GB，约 95% 使用率） |
| 演练卷可用 | 42 GB，但与生产库本体和 docker root **同在一个 LUKS loop 文件上** |
| 主机 | 腾讯 Lighthouse，无云 CLI，挂盘需控制台操作 |

两个卷都没有安全余量：根卷写满会影响整机（含网关），演练卷写满会直接打挂生产库。
`formal/` 7.5G 一度被误判为历史目录，实为活跃生产目录（三个容器挂载，`sub2api`
正在写日志），不可清理。可安全回收的空间合计约 1.4 GB，远不足 20.6 GB。

因此 prod 执行的是 `--verify-only`：19 份备份全部 `status: valid`，
`missing / checksum invalid / size invalid / other` 全为 0，WAL `...3335` → `...3719`
单一 timeline，耗时 288 秒。只写 `repository_verification.json`。

**这两者证明的东西不同**：verify 证明对象完整未损坏，完整恢复证明数据库真的能起来
并查询。cn-prod 的完整验收说明 COS 恢复**方法**可行，但不替代 prod 自己那条链
（美西地域、独立 bucket、timeline 2、19 份备份）的恢复证明。

**该例外的后果，以及一个必须知道的事实**：prod 与 cn-prod 的 Tencent runner 写的是
`archive_check.json`，它**不产生** `restore_acceptance_missing_or_stale`
—— 那两个 reason 属于 Stage0 的 `health.json` 路径（Tokyo 时期 EC2 基线）。
所以 prod 的恢复验收缺口**当前没有任何门禁报警**，`archive_check.json` 读作
`status: ok`。这个缺口只存在于本记录中。

**解除该例外的条件**（任一）：

1. 给 prod 挂一块 ≥30 GB 临时盘（控制台操作），跑
   `ops/backup/cos_restore_acceptance.py --repo 2 --apply` 的完整验收，完成后卸载；
2. 扩容根卷或演练卷，使恢复后余量不低于 15 GB；
3. 在独立主机上拉取 prod 的 COS 链做恢复验收。

在上述任一完成前，**不得按阶段 6 退役 AWS 的 prod 写入身份** ——
阶段 6 要求「确认 COS 与 node1 连续健康」，而 prod 的 COS 链尚未被证明可恢复，
AWS repo1 仍是 prod 唯一经过恢复验证的链。

## 2026-11-08 决定：停写 AWS repo1，COS 为唯一 repository

**2026-11-08 用户决定停写 AWS repo1，COS 为唯一 repository。** prod 与 cn-prod 同时执行。
该决定覆盖上节「不得按阶段 6 退役 AWS 的 prod 写入身份」的限制，由用户在知情下作出：
prod 的 COS 链仍只有 `--verify-only` 证据（2026-11-02），尚未做过真实恢复。

| 项 | 决定 |
|---|---|
| repository 编号 | COS 保持 `repo2`，不改编号为 `repo1` |
| 运行时配置 | 只渲染 `repo2-*`，无任何 `repo1-*`、AWS endpoint 或 STS token |
| 身份刷新 | prod `tencent_backup_identity.py`、cn-prod `cn_backup_identity.py` 不再读 `/root/.aws/credentials`、不调 STS；缺 COS 凭据即失败并保留现配置，绝不回退 AWS |
| runner | `PRIMARY_REPO = 2`；配置里没有 repo2 时写 `primary_repository_missing` 硬故障回执，不备份其他仓库 |
| AWS 对象 | 原样保留（不 expire、不删）；桶/角色清理是单独的 infra 步骤 |

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

**仍然成立的风险**：prod COS 尚未做过真实恢复。建议在独立主机或临时挂盘上补一次
`cos_restore_acceptance.py --repo 2 --apply`（见上节解除例外的三种方式），但不阻塞本决定。

## 当前阻塞输入

- prod COS 美西和 cn-prod COS 北京的最终地域代码、bucket 名称、endpoint；
- 两套 COS 服务账号及 KMS 策略 owner；
- node1 的稳定主机名/IP、SSH 或同步方式、归档根目录、告警接收方；QA raw 365 天、QA 6 TiB、数据库 2 TiB，以及 85%/90%/95% 空间阈值已获批准。
- pgBackRest 双 repository 的实现与隔离验收结果；

在上述输入进入资源清单并完成 dry-run 前，不创建付费 COS 资源、不写生产备份配置、不启动 node1 归档 timer、不删除 AWS 数据。
