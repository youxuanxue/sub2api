# pgBackRest 新机迁移与腾讯云隔离演练

现网身份由 [舰队入口](../../deploy/hetzner/README.md) 和
`resolve_prod_ssm_target.py` 持有；备份/验收命令由 [physical-backup.md](physical-backup.md) 持有。
本文替代 prod 的全库 dump + 按表尾差迁机路径。cn-prod 的备份验收见 [domestic README](../../deploy/domestic/README.md)。

**当前交接：[腾讯迁机交接](../../docs/ops/tencent-migration-handoff.md）。**  
**状态（2026-10-23）：腾讯美西已在服（timeline 2）；Tokyo 已退役删除；出口已迁 `usw-eval*`。**  
切流踩坑与 verify 门禁见 skill [`tokenkey-host-replacement`](../../.cursor/skills/tokenkey-host-replacement/SKILL.md) 与 [playbook](../../.cursor/skills/tokenkey-host-replacement/references/playbook.md)。  
剩余：≥7 天后东京退役、JP Lightsail 关停、LUKS 自动解锁验收。飞书宿主告警（`tokenkey-https-watch.timer`）已接腾讯机。  
**规划纪律：** 新机从 `plan` 起按正式迁机终态一次写清（布局/控制面/备份告警/DNS/出口/SSOT），与 cutover **同窗**收口；禁止切后再开多轮返工停机。

## 当前可用能力与边界

真实 S3 的最新恢复与历史同点全量/增量恢复已经通过；这是数据库恢复验收，不是整站切流验收。
`run_physical_restore.py` 编排 AWS 临时机，`restore_postgres_backup.py` 在 finally 中销毁恢复容器；
两者用于验收，不能直接充当保留新库的新生产机安装器。腾讯云隔离执行已具备 TAT/SSM、短期只读身份、加密工作区和出站限制；最新点跨架构恢复及账务验收已通过（全程 31 分 17 秒，未满足 30 分钟目标），历史同点全量/增量一致且每条路径均在 30 分钟内。保留式 standby 已安装并跨夜追赶，断网/容器重启/过期凭据不晋升已实测；自动续期、重启解锁与生产运行契约仍未完成。
旧 `pgdump_restore_canary.py`、旧尾差/冻结脚本仅保留历史回查，不再作为 prod 迁机默认入口。

## 第一阶段：美国腾讯云隔离演练（无生产切流）

1. 确认地域、已有实例或按量新机、CPU 架构和预算。资源容量按备份大小、WAL、审计工作区及磁盘余量计算。当前用户已批准美西锐驰
   4 vCPU / 8 GiB、120 GB 自带系统盘，不购买独立盘；已在系统盘建立 80 GiB LUKS2 隔离工作区并完成重启锁定/人工解锁验证。此方案仅用于演练，正式运行仍须验收异机密钥托管与故障恢复。
   当前验证基线为 PostgreSQL 18.6、ARM64、pgBackRest 2.58.0，使用已验收不可变镜像。
   若美国目标仅提供 x86，先验证镜像/扩展与数据格式兼容；不能把 ARM 恢复成功当作 x86 验收。
   必要时从独立 ARM 恢复副本做逻辑导出再导入 x86，不在活库额外跑大 dump。
2. 实际资源选型、价格和基础设施验收见 [美西验证报告](../../docs/ops/reports/tencent-us-lighthouse-proxy-validation-20261021.md)，
   实例身份由舰队入口持有。用户已授权一个月手动续费的验证资源并已创建；本阶段不更改 DNS、不触碰生产卷。
   后续恢复另行核算 S3 拉取、跨云流量和新增资源费用，不把已批准实例费用当作所有迁移费用。
3. 准备只允许读取物理仓库和恢复包的短期凭据。EC2 IMDS 自动身份不能直接在腾讯云使用；
   通过受控 STS 会话和受保护文件注入 session token，凭据不进日志、git、命令参数或回执。
   不复用生产备份写角色，不授予 S3 写/删除权限。先验证对象下载、过期行为和镜像 SHA。
4. 新库写入全新 PGDATA；独立安全组，无公开数据库端口。恢复容器 archive_mode=off，
   不启动生产网关、退款/刷新/支付回调、QA 上传/DROP 等后台任务，不加载生产 Redis 副本。
   出站只放行取备份/镜像等必需路径；真实供应商、支付与回调目标保持阻断。
5. 从真实备份恢复最新点；从保留期内已记录命名点做全量/增量同点对比，复用既有
   `restore_postgres_backup.py` 中的表/分区摘要、序列、约束、计费幂等验证。
   腾讯云执行器须先补齐受控凭据、资源回收和回执验证，不能直接运行 AWS 编排器。
   PG 恢复时 max_connections 等参数不得低于源库 WAL 所要求的值；正常 startup 探针 FATAL
   不等于恢复失败，终止错误与超时必须失败。
6. 如需应用验证，使用独立域名、合成账户及受控供应商替身；先验证 worker 禁用和出站阻断。
   记录备份点/恢复点、数据校验、下载/恢复耗时、费用、清理结果。结束销毁临时容器/卷/凭据，
   保留脱敏回执。演练成功不授权正式迁机。

## 第二阶段：持续追 WAL 的迁机准备

腾讯隔离环境使用 `ops/stage0/archive_standby_rehearsal.py` 准备与检查保留数据的
archive standby。仅接受显式预期主机，要求既有加密挂载、无 swap、Docker 数据位于
加密盘、恢复出站限制已安装；发现生产容器即拒绝。`prepare` 要求不可变镜像、备份标签、
源 system identifier/timeline，不覆盖已有目录；`start` 每次启动均检查 standby.signal，
只开放容器内 Unix socket，关闭 archive-push 和业务写入。`status/restart/stop` 仅操作
带隔离演练标签的专用容器，不提供 promote 或源写隔离操作。凭据须通过现有加密交付
流程预先放入 tmpfs，工具不管理身份续期或自动解锁。当前先以单元测试和门禁验证，
真实安装/故障结果以美西报告为准，不因工具存在而认为备用库已经运行。

在目标机新盘使用 pgBackRest `--type=standby` 恢复，保留 standby.signal 与只读仓库访问，
持续从 archive 拉取 WAL；禁止默认恢复结束就自动 promote。验收进程重启后仍保持 standby、
断网不会晋升、缺 WAL 明确告警、只读身份不能 archive-push；保持目标所有写业务关闭。
显式配置当前源 timeline；记录源 system identifier、timeline、备份标签、目标 replay LSN。
不得在源机运行额外全库复制/表尾差。定期测量归档与重放滞后，评估实际切换时间。

只读观测可使用 `ops/observability/probe-pg-migration-snapshot.sh`，通过 `run-probe.sh`
及当前 `expected-instance-id` 执行。它输出 system identifier、checkpoint timeline、
current/replay LSN、归档统计和事务概况，不输出 SQL 正文、凭据或会话身份，也不执行
checkpoint、WAL switch、停写或 promote。源端两次快照只能测 WAL 生成速率；只有同时
采集并验证同源、同 timeline 的目标 recovery/replay 状态，才能讨论追赶滞后。
checkpoint timeline 不替代最终归档文件和恢复 timeline 的交叉校验；没有目标样本不能
判定追平。空闲 standby 的 last replay transaction 时间也不能单独当作延迟。

源与目标已经实际采样并比对，时间与结果见美西报告；这些采样不证明最终停写后的追平。
本地 `ops/observability/compare-pg-migration-snapshots.py source.json target.json`
消费两个快照文件，不连接云 API 或数据库。默认要求两份样本各自距执行时间不超过
120 秒、采样时间间隔不超过 120 秒，且拒绝超过 5 秒的未来时间、缺少时区、错库、
错 checkpoint timeline、非主库源和非 recovery 目标。输出有符号的
`observed_lsn_gap_bytes`；负数可能来自目标采样较晚，不能当成最终追平证明。
退出成功仅表示样本可比较，`cutover_authorized` 始终为 false。它不验证最终 WAL
入仓、源写隔离或恢复 timeline，不能作为 promote 闸门。

`ops/observability/probe-migration-readiness.sh` 是补充只读现场盘点：输出数据库
会话概况、备份/QA 等 timer 和容器状态。12:05 UTC 的现场盘点发现 QA cleanup、
QA provision timer 仍在调度，正式写者隔离计划须纳入其评估；当时瞬时事务很短
不证明 HTTP 长流已排空。这只是一次局部盘点，不能枚举或证明全部后台/直连写者已停。

后续可推进的工具按依赖顺序为：

| 工具/能力 | 现有可复用部分 | 还需完成的验证 |
|---|---|---|
| 源/目标只读采样与追赶观测 | 上述快照、`postgres_backup.py status` 的已有回执 | 同源和 timeline 比对、样本新鲜度、LSN 差与归档/重放分段耗时；不以空闲时间误报 |
| 持久 standby 安装器 | pgBackRest `--type=standby`、已验收 amd64 镜像、腾讯隔离工作区 | 独立安装器；不能复用 finally 删除库的验收流程；重启、断网、凭据过期均不晋升 |
| 最终 LSN 验证器 | source/archive/replay 数据与同点审计 | 缺 WAL、错 timeline、旧样本或未证明停写必须阻断；工具不自动 promote |
| 写者与长请求盘点/演练 | 共享 compose、后台任务和暂停清单 | 生产只读盘点；仅在隔离环境演练 drain、超时和写隔离，默认只读不能充当完整隔离 |
| 目标发布与密钥运行契约 | SSM/TAT、共享镜像/compose/Caddy | 显式候选目标、加密挂载身份、续期只读 S3 身份、异机密钥恢复；不修改默认 prod |
| 新 timeline 备份及回退演练 | 物理备份与账务恢复验收工具 | 独立仓库前缀、写入后回退保护；不修改或覆盖现有生产备份链 |

`python3 ops/stage0/verify_final_wal.py bundle.json` 提供离线最终 WAL 回执一致性检查。
输入必须包含 `fence/source/archive/target` 四份回执，每份包含 `kind=final_wal_<类别>`、
带时区的 `at`、一致的 `migration_id/system_identifier/timeline`；回执均须在 120 秒内，
时间顺序为隔离写者、最终点、真实仓库读取、目标重放。字段结构见
`ops/stage0/test_verify_final_wal.py` 中仅用于测试的合成示例，不能用其替代现场证据。
它拒绝缺少写隔离声明、非零在途/事务/可写会话、未锁发版、错误 WAL 文件或 timeline、
未验证读取/恢复点、重放落后、过期样本与目标已晋升；处理 LSN 恰好落在 WAL 段边界的情况。
通过只证明输入回执相互一致，无法认证回执或证明所有真实写者已隔离，
`cutover_authorized=false` 始终保留。真实证据采集与最终切流执行器尚未验收，不能仅凭这个工具执行 promote。

上述表格是准备顺序，不表示未实现工具已可执行。真实停写、最终归档触发、晋升、
正式入口修改以及生产代理绑定变更均不属于只读准备阶段。

独立准备应用镜像、密钥/加密材料、Caddy 状态与 active-color、主机配置、对象文件和网络。
Redis 默认新建；若会话/幂等功能有持久需求，按其 owner 单独确认，不能直接复制旧 Redis。
网络切换须保留 compose labels；Caddy upstream 对齐 tokenkey-blue/green。

## 第三阶段：正式切流（另行授权维护窗口）

1. 制定请求 drain、长连接处理、超时和回滚条件。冻结源应用、所有后台写者和直连写入入口，
   等待在途事务结束并验证无可写会话；必要时受控终止剩余会话。
   `ALTER DATABASE ... default_transaction_read_only=on` 仅是附加防护，无法约束既有会话和超级用户，
   不能单独作为写入隔离证明。禁止尚未冻结源就启动目标业务。
2. 创建最终命名恢复点、记录 LSN/timeline，切换 WAL 并等待其真实入 S3；目标必须保持 recovery，
   确认同一 timeline 的 replay LSN 不小于源最终 LSN，且最终点和审计一致。缺证据则不 promote。
3. 停止源所有写能力后才 promote 目标；安装/验证新主库备份保护，启动受控目标应用验证，
   然后切 DNS/入口，观察真实请求错误与延迟。不能只以 /health 200 判断用户体验。
4. promote 会产生新 timeline；现有连续性检查遇到 timeline 变化会拒绝健康。
   新主库使用独立仓库路径/身份并建立可恢复的备份链、WAL 及恢复验收；旧仓库只读保留。
   全新空仓库需要全量；若提前复制完整且经验证的已有备份链，可评估继承该全量并追加新 timeline
   增量。不能只复制某一份增量或只看到上传成功就宣称新链可恢复。
   在切流前准备并验证 timeline 交接方案，不能临时关闭告警假装健康。
5. 目标尚未接写时可恢复源流量；目标已经接写后，禁止直接 DNS 切回并解冻旧库。
   必须停止目标写入、保护新数据，再执行经过验证的反向迁移。旧机至少保留七天，释放另行授权。

## 演练交付清单

- 地域/实例/架构/预算和到期机制：已选美西锐驰 x86，基础设施验证完成，见美西验证报告。
- 腾讯节点 TAT/SSM 双通道、加密工作区、短期只读 S3 身份、容器出站限制已实测；真实恢复回执见美西验证报告。
- 数据库最新/历史恢复、应用隔离 smoke、资源清理回执。
- 保留式 standby、正式切流、新 timeline 备份保护（候选前缀）已完成；见切流回执。

## 腾讯云正式运行 · 切流后仍开放

- **发布目标**：`resolve_prod_ssm_target.py --target auto` → 腾讯美西。GitHub `PROD_SSM_TARGET` 必须为 `auto`。`aws-fresh`/`hetzner` 已退役，失败关闭。
- **Stage0 布局**：从 **plan/prepare** 起按「切流结束即可 `deploy-stage0`」设计；与 cutover **同窗**落地（默认例外路径：不搬 Docker Root、PG/Redis 别名）。见 [`stage0-layout-convergence.md`](../../deploy/tencent/stage0-layout-convergence.md) / skill `tokenkey-host-replacement`。**禁止**规划迁机完成后的第二次布局停机。
- **宿主存储契约**：`bluegreen_host_runtime.py` 默认 `--platform=tencent-luks`；拒绝 Hetzner volume 身份。
- **备份身份**：候选前缀 `candidates/tencent-usw-20261022/postgres` 为活链；`RuntimeDirectoryPreserve=yes`；飞书宿主告警已接（`tokenkey-https-watch.timer`，见舰队 SSOT）。
- **密钥与启动**：LUKS 异机托管已落盘；自动解锁未验收——**禁止现网整机重启试验**。
- **出口代理**：账号已迁 `usw-eval*`；日本 Lightsail 已于 2026-10-23 删除。

## 历史：切流前最短停写清单（2026-10-22 已执行）

以下为切流前准备与窗内顺序的归档，**不是**当前开放项。后续迁机仍按此顺序，并把 Stage0 例外布局写进同一窗（见 skill / [`stage0-layout-convergence.md`](../../deploy/tencent/stage0-layout-convergence.md)）。

提前完成镜像固定 digest 下载、同架构启动、证书与域名定向访问、密钥迁移、正式挂载布局、
目标日常发布、只读/备份身份续期和新 timeline 备份恢复验证，再安排停止写入。
切流前已下载并在腾讯执行 1.8.313 amd64 二进制、完成空库启动和空闲 drain 冒烟；
不能把该冒烟当作生产配置、真实账号或真实 UI 验收；源发版后须重核镜像与配置。

获批窗口顺序：先锁定发版和写者清单，关闭新业务入口并等待真实在途归零，停止后台与直连写者；
然后创建最终点、主动归档最终 WAL（不等待自然 5 分钟周期），验证其真实入仓、目标同 timeline
重放和账务一致，再晋升、启用经准备的新备份保护、应用验收、改入口、**同窗落地布局例外路径**。
QA cleanup/provision、宿主 cron、外部控制面维护任务及旧颜色应用均须盘点；
archive-check 也需确认是否会触发归档，不能与窗口内的最终归档步骤竞争。
等待中任何证据缺失或超时都停止推进，不强杀长流来迎合时间估算。

晋升前失败可保持入口原状并按事先验证的步骤恢复源写者；目标已有写入（包括后台写入）后，
禁止直接解冻旧库。新 timeline 的备份/恢复前置验收耗时必须计入实际安排；
不能承诺之前的 3–8 分钟已涵盖尚未测量的生产流程。

2026-10-21 23:39 UTC 腾讯合成实验已验证：在独立本地仓库完整复制既有备份链后，
timeline 1 的 standby 晋升到 timeline 2，可继承旧全量生成新增量并恢复出晋升后写入。
最终 WAL 归档与重放为 4.853 秒、晋升 0.518 秒、新增量 2.377 秒、恢复 7.11 秒；
这些是小型合成数据库的分段耗时，不是生产停机估计。此路径尚未验证真实 S3、权限、
生产数据量、在线预复制的一致性或跨 timeline 连续性健康检查。
后续优先评估提前复制稳定备份及依赖、窗口内补齐最终 WAL 和仓库元数据，再验收新链，
将可提前进行的传输移出停写窗口。源备份保留/清理仍在运行，不能假定一次在线复制就是一致快照。
