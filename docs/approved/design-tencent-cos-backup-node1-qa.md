---
title: 腾讯 COS 数据库备份与 node1 QA 归档
status: approved
approved_by: "feng（本次会话同意三项迁移决策，并将 QA node1 保留期改为 365 天）"
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
3. **兜底闭环**：node1 只保存可核验的对象和 manifest，不承担业务服务或 QA 数据库恢复。node1 本身必须使用冗余磁盘和独立权限；否则只是把单点从云桶换成另一块盘。

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
- 保留期改为 **365 天**并设置独立 quota；prod 与 cn-prod 当前观测量级约 9.1 GB/day 和 0.7 GB/day，全年原始数据约 3.6 TB，尚未计入临时复制、校验、文件系统和保留余量。node1 扩盘后建议至少提供 **4 TB 可用空间**，更稳妥为 **6 TB 可用空间**；quota 在 24–72 小时试复制和连续 7 天增长观测后最终确定。
- 扩盘必须先完成 node1 现有数据清单、校验副本、磁盘/RAID 健康检查和回滚方案；建议 6 TB **可用**空间由 RAID1/RAIDZ 等冗余布局提供，而不是一块更大的单盘。不得通过重格式化、删除现有目录或直接覆盖挂载点来扩容。扩容期间不启动 QA 归档任务。
- 不依赖 node1 恢复 QA 数据库。若未来需要恢复，另立审批和验收契约；本设计不提供该能力。

## 执行阶段与门禁

1. **资源清单与演练**：确认两个 COS bucket/endpoint、服务账号、KMS、node1 SSH/同步端点、目录、quota、磁盘/RAID/加密和告警；用合成 PostgreSQL 与合成 QA 文件完成 COS PUT/GET、node1 copy、断点重试和 checksum 演练。
2. **COS 并行保护**：不停止 AWS timer、不删除 AWS 对象。为两环境启用经验证的双 repository，完成 COS full、自然增量和连续 WAL；连续观察至少两个完整业务日。
3. **数据库验收**：从 COS 恢复最新点和历史点，验证 WAL 连续性、关键表/约束/序列、账务聚合和恢复耗时；确认 archive check 新鲜且 RPO 门禁通过。失败时保持 AWS 为主。
4. **QA 验收**：完成至少两轮 4 小时复制，manifest/hash、源文件计数、目标字节数和失败重试均通过；确认 cleanup 只删除已有 node1 receipt 的小时；不把“文件复制成功”描述成 QA 数据库恢复成功。
5. **切换**：将 prod/cn-prod 主备份配置切到各自 COS，保留 AWS 只读回退和最近回执；观察一个完整备份周期。node1 每日 COS 兜底必须先有一轮成功回执。
6. **AWS 退役**：先停 AWS 写入身份和 timer，确认 COS 与 node1 连续健康；AWS bucket/object/KMS/角色删除另立收尾单，列出精确对象范围、保留截止时间和最终恢复验证，不由普通切换自动执行。

## 当前阻塞输入

- prod COS 美西和 cn-prod COS 北京的最终地域代码、bucket 名称、endpoint；
- 两套 COS 服务账号及 KMS 策略 owner；
- node1 的稳定主机名/IP、SSH 或同步方式、归档根目录、quota 和告警接收方；
- 是否接受 node1 数据库兜底初始保留 30 天、QA raw 保留 365 天，以及扩盘后至少 6 TB 可用空间（冗余盘后的 usable capacity）。
- pgBackRest 双 repository 的实现与隔离验收结果；

在上述输入进入资源清单并完成 dry-run 前，不创建付费 COS 资源、不写生产备份配置、不启动 node1 归档 timer、不删除 AWS 数据。
