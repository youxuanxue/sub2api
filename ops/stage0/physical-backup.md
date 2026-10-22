# PostgreSQL 物理备份变更单

审批基线：[design-prod-physical-backup](../../docs/approved/design-prod-physical-backup.md)。
现网身份只从[基础设施入口](../../deploy/hetzner/README.md)和 resolver 取得。
生产物理备份已启用，并已完成真实 S3 最新点和历史同点全量/增量恢复验收。
用户已同意停用旧 pgdump、保留数据至少 30 天；执行与脚本清单见 [pgdump-retirement.md](pgdump-retirement.md)。
新机/腾讯云隔离演练见 [迁机工作流](pgbackrest-host-migration.md)。
腾讯美西恢复演练路线、快照与配置离机缺口见 [隔离恢复方案](../../docs/ops/tencent-isolated-restore-plan.md)。

## 现网（腾讯美西，2026-10-22 起）

| 项目 | 实况 |
|---|---|
| 主机 | 腾讯 Lighthouse `VM-0-16-ubuntu`，**amd64**，SSM Hybrid（id 见候选矩阵） |
| PG | 容器 `tk-archive-standby`（非 compose，别名 `postgres`/`tokenkey-postgres`），amd64 镜像，PG 18.6 / pgBackRest 2.58.0 |
| 数据 | `/srv/tokenkey-rehearsal/standby-pg`，位于 LUKS mapper `tokenkey_recovery`；无 EBS、无 DLM、无整机快照 |
| 活链 | `s3://<physical-backup 桶>/candidates/tencent-usw-20261022/postgres`，timeline 2；身份由 `tencent_backup_identity.py` 每分钟刷新到 tmpfs |
| 操作入口 | 宿主 `tencent_pgbackrest.py backup|check`（**尚未入仓**，无 accept/verify/status）；timer 同名 `tokenkey-postgres-{backup,archive-check}` |
| 已知偏差 | 运行时 `archive_timeout=0`（基线 300），待审批修正 |

`prod/postgres` 是 Tokyo timeline 1 的冻结只读链；不得写入，也不作为恢复默认。
本文以下"固定配置""初次上线顺序"描述的是 Tokyo 时期（arm64、EC2、EBS）的已执行基线，标注为**历史**。

## 固定配置与成本边界（历史：Tokyo 基线）

| 项目 | 执行值 |
|---|---|
| 镜像 | PostgreSQL 18.6；架构须与目标主机一致（Tokyo arm64，腾讯现网 amd64）；基础摘要在 Dockerfile 固定；pgBackRest 2.58.0；装载后用 image ID 固定运行 |
| 备份 | UTC 每个偶数小时 30 分；周日 00:30 的任务做全量，其余增量；错过周全量则下次补全量 |
| 归档 | archive_mode=on，archive_timeout=300；同步 archive-push；5 分钟一次往返及连续性检查 |
| 清理 | 专用 S3 桶，us-east-1；pgBackRest 按时间保留至少 7 天所需链；S3 仅清未完成 multipart |
| 并发 | 备份 2，恢复 4；与现有 pgdump 共用 QA host.lock；物理备份拿独占锁，避免争用 |
| 恢复资源 | 显式批准的子网/区域内独立临时机：arm64→c7g.xlarge，amd64→c7i.xlarge；100 GiB 加密 gp3，无入站端口；只读仓库角色；完成即终止，2 小时自动关机/终止兜底。腾讯现网推荐路线见隔离恢复方案 |
| 开销 | 首次验收临时机预算上限 $5；每次硬时限 2 小时。S3 流量依实际 WAL 量结算（腾讯→us-east-1 为公网上传） |

容量按「窗口边界所需全量 + 后续增量 + 连续 WAL + 新全量暂时重叠」计算，不用 84 份文件估算。
例如平均仓库 100 GB，按 us-east-1 S3 Standard 约 $0.023/GB·月估算仅存储约 $2.30/月；
请求、主机→S3 流量（历史为 Tokyo 跨区，现为腾讯公网上传）、恢复拉取另计，价格在执行前复核。
首个 48 小时与完整周必须量测这些费用；不删除用户明细以达成预算。

## 初次上线顺序（历史变更基线）

1. 先跑本地/CI 门禁和 S3 原型。构建与目标主机同架构的镜像、真实计费验收二进制；生成带 SHA-256 的部署包。
2. 在 us-east-1 创建 `stage0-physical-backups.yaml`：新私有桶、只读恢复角色/profile；
   `BackupRoleArn` 从当前 EC2 instance profile 解析。只给源主机角色新仓库所需对象权限。
   这是实例角色边界，沿用当前 IMDSv2/hop=2；网关不增加环境变量、凭据挂载或宿主网络。
   安装前复核当前实例角色/IMDS 隔离仍符合生产安全要求。
3. 将部署包传到新桶 `artifacts/<sha256>.tar.gz`。SHA-256 对齐后在源主机解包、`docker load`。
   刷新既有 HTTPS watcher，使其支持物理备份健康检查；原有 QA 暂停清单继续有效。
4. **安排 10 分钟维护窗口**。安装器默认只输出计划；核对主机、旧/新 image ID、PG 版本、挂载路径后执行 `--apply`。
   唯一预期服务中断来自一次 PostgreSQL 容器重建；窗口内可能连接失败、请求重试，不能承诺零中断。
   60 秒仍未就绪，或出现持续连接错误，应立即执行回退。全量生成异步执行，不计入数据库重启耗时。
5. 验证首份全量、远端 WAL 和自然两小时增量；检查凭据从容器自动获取并经过一次轮换。
   观察相近流量下请求错误/延迟、CPU、磁盘和 WAL 积压，持续回归则停新 backup timer 并排障。
6. 创建命名恢复点（位于一份已完成增量之后），完成归档检查，记录名称和 UTC 时间。
   独立机先恢复最新，再按记录点执行完整/增量双路径审计。二者均通过后才导入验收回执。
7. 并行观察至少 48 小时。**此 PR 不切换 pgdump**：真实恢复及性能达标后才将它改为每日，
   同时调整旧 pgdump 告警/恢复 canary 的时效阈值；每日 EBS 快照继续。
   新仓库实际建立满七天窗口后，验收最老边界并确认连续 WAL 后才能报告七天保护。

## 命令入口

以下在仓库根目录执行；生产命令中的值来自已确认变更单，不复制旧节点坐标。
架构默认取目标矩阵 `target.architecture`（现网 amd64），镜像架构不一致即失败；
`--repo-path` 必填，现网活链为 `/candidates/tencent-usw-20261022/postgres`。

```bash
docker build --platform linux/amd64 -t tokenkey-postgres-backup:dev deploy/aws/stage0/postgres-backup
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go -C backend build -trimpath -ldflags='-s -w' \
  -o /tmp/backup-billing-check ./cmd/backup-billing-check
docker save --output /tmp/postgres-backup.tar tokenkey-postgres-backup:dev
python3 ops/stage0/prepare_physical_backup.py --image-tar /tmp/postgres-backup.tar \
  --billing-check /tmp/backup-billing-check --bucket "$BACKUP_BUCKET" --region us-east-1 \
  --repo-path /candidates/tencent-usw-20261022/postgres --output /tmp/physical-backup.tar.gz
```

部署包是恢复镜像来源，不依赖生产机还活着或私人 GHCR 登录状态；新桶保留部署包。
备份目录中不存静态 AWS 密钥。包的 image ID、SHA-256 和审批 commit 要写入执行记录。
Docker classic 与 containerd 的 image ID 表达不同；包同时列出 OCI 和 config 身份。
源机与恢复机装载后核对各自 `docker image inspect` 的 ID，执行参数使用实际装载的不可变 ID；
恢复机须使用与源机相同的 Docker 存储格式；身份不一致时修正恢复机，不能跳过镜像一致性检查。
安装器在唯一运行中的 PG 容器（`tokenkey-postgres` 或 `tk-archive-standby`）上核对数据挂载，
并要求候选镜像、运行中 PG 镜像与宿主 `uname -m` 三者架构一致。
腾讯 `tk-archive-standby` 不是 compose 管理，安装器只输出计划，拒绝 `--apply`（备份由宿主 Tencent operator 负责）。
安装时将包内 `install_postgres_backup.py`、`postgres_backup.py` 和 compose 放同一目录：

```bash
python3 install_postgres_backup.py --bucket "$BACKUP_BUCKET" --region us-east-1 \
  --image "$IMAGE_ID" --expected-host "$EXPECTED_HOST"      # 只读计划
# 在已批准窗口内，重复同一命令并加 --apply。
# 回退：python3 install_postgres_backup.py --rollback --apply
```

安装器保留旧 .env/compose/image ID，回退重建原数据库容器，保留原数据目录和远端仓库。
回退后保护能力退回旧 pgdump/EBS；不得继续宣称 15 分钟 RPO。不得手工删除 pg_wal 缓解压力。

源主机统一入口为已安装的 `postgres_backup.py`：`backup`、`check`、`verify`、`status`、`accept --receipt`。
`verify` 是整仓校验，安排在独立恢复机执行，避免日常重复扫描整个仓库。
`check` 每五分钟检查真实 WAL 往返、连续段、剩余空间及 WAL 增长；旧或缺失恢复证据会显示 degraded。
首次独立验收前出现 `restore_acceptance_missing_or_stale` 属真实待验收状态，不应静音其他故障。

命名点示例（会写极小 WAL，不修改业务表）：

```sql
SELECT pg_create_restore_point('tk_accept_YYYYMMDD_HHMMSS'), clock_timestamp();
```

记录返回时间，跑一次 `postgres_backup.py check` 后再开始恢复：

```bash
python3 ops/stage0/run_physical_restore.py --region "$RESTORE_REGION" \
  --bucket "$BACKUP_BUCKET" --key "artifacts/$BUNDLE_SHA.tar.gz" --sha256 "$BUNDLE_SHA" \
  --image "$IMAGE_ID" --subnet "$RESTORE_SUBNET" --architecture amd64 --profile "$RESTORE_PROFILE" \
  --receipt /tmp/restore-latest.json                      # 只读计划
# 子网须为显式批准的 EC2 subnet-*；不能从腾讯 mi-* 推导。
# 已批准资源预算后加 --apply；历史验收另加 --target-name 与 --target-time。
```

恢复副本只开放本地 socket，archive_mode=off；全表叶分区行数/摘要、usage 精确金额与时间范围、
索引/约束和序列安全先比对，再调用仓库真实计费实现检查热表/归档重试与退款。
不启动网关、退款 worker、QA 上传或 DROP。比较以同一恢复点为准，不拿 live 的当前余额比较过去副本。
超时/损坏/缺 WAL/账务不符/清理失败均不产生成功回执；30 分钟目标失败也不能导入正式验收。
每次恢复计时包含仓库预校验、数据还原、WAL 重放和账务检查；双路径分别计时并各自计入共享预校验耗时。
云端回执须确认临时实例终止、其数据卷删除和安全组回收；API 响应丢失时按本次唯一标识查找清理。

首次必须同时导入 latest 和历史双路径回执。后续真实恢复按需执行，下次结合腾讯云迁机演练，
不启用每周自动建机。日常继续检查备份、WAL 和磁盘；成功的恢复/仓库验证记录超过 8 天后，
主机监控每天最多提醒一次，日报标为 warning，不作为备份故障自动报修。
缺少成功记录、验证失败、备份/WAL/磁盘异常仍报故障，混合出现时故障优先。
原始 health 回执仍保留 degraded 和过期原因，避免把旧证据当成新的恢复证明；
展示分级统一由 `physical_backup_fact` 处理。需要较老点验证时，从保留期内已记录的恢复点传入 name/time。

## 历史验证边界（2026-10-20，Tokyo 链）

真实独立恢复已通过：最新路径约 1656 秒；同一历史点的全量路径约 1785 秒、增量路径约 1623 秒，
包括仓库预校验、WAL 重放、完整数据审计与计费幂等校验。最新验收 SSM
`03f79c89-c229-4fa4-bdb2-9beb90721feb`，历史验收 SSM `1df51a98-c16a-441a-8f96-875881943a69`。
临时实例、数据盘和安全组已清理，验收回执已导入源主机，status=ok。
恢复参数已修正为与源库一致的 max_connections=200；等待时忽略正常 startup 探针错误，
真实恢复进程失败仍立即终止。

原计划要求先观察 48 小时再降为每日；用户在真实恢复成功后更新决策为停旧任务并保留数据。
本次没有声称已完成 48 小时负载或完整七天覆盖验收。七天窗口、费用/性能趋势与身份轮换仍需持续核验。
数据库恢复成功不等于整站恢复、腾讯云兼容或正式切流通过。

按用户简化要求，周定时恢复已从工作流移除，自动建机/IAM 扩权草稿撤回。
`ops-physical-restore.yml` 仅保留手动入口，当前配置与权限未就绪，不代表可直接运行：
使用前须配置 `PHYSICAL_BACKUP_ENABLED`、`PHYSICAL_RESTORE_{REGION,SUBNET,ARCH}`、恢复包/镜像/桶/profile 等变量，
缺失恢复网络变量会在取凭据前失败；验证 CI 的 EC2/SSM 及 EC2 PassRole 权限与执行时长。
现网活链前缀尚不在 RestoreRole 读权限内，Tencent operator 也无 `accept`，详见隔离恢复方案。已有恢复角色可读取备份，无需另建仓库或身份；
这不是当前必须推进的 IAM 扩权任务，也不触发生产服务变更。
