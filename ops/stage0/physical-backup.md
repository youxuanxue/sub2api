# PostgreSQL 物理备份变更单

审批基线：[design-prod-physical-backup](../../docs/approved/design-prod-physical-backup.md)。
现网身份只从[基础设施入口](../../deploy/hetzner/README.md)和 resolver 取得。
生产物理备份已启用，并已完成真实 S3 最新点和历史同点全量/增量恢复验收。
用户已同意停用旧 pgdump、保留数据至少 30 天；执行与脚本清单见 [pgdump-retirement.md](pgdump-retirement.md)。
新机恢复见 [迁机工作流](pgbackrest-host-migration.md)。不做整机/卷快照常备策略，不做 AWS 临时机隔离演练（见 [`declined.md`](../../docs/decisions/declined.md)）。入口证书离机包见 `ops/archive/README.md`。

## 现网（腾讯美西）

| 项目 | 实况 |
|---|---|
| 主机 | 数据机 Lighthouse（矩阵 `node=data`），**amd64**；日常远程 TAT，Hybrid 仅 break-glass |
| PG / Redis | 数据机容器（别名 `postgres`/`redis`）；PG 18.x / pgBackRest；Redis AOF |
| 数据目录 | 明文根盘业务路径（LUKS 已退役）；无 EBS/DLM；**不做**整机/卷常备快照 |
| 活链 | COS `repo2` `/prod/postgres`；`tencent_backup_identity.py` 渲染配置。东京 timeline 1 前缀只读 |
| 操作入口 | 宿主 `tencent_pgbackrest.py backup|check`（仓库 SSOT `deploy/tencent/tencent_pgbackrest.py`）；timer `tokenkey-postgres-{backup,archive-check}`；验收 `ops/backup/cos_restore_acceptance.py` |

`prod/postgres`（Tokyo timeline 1）冻结只读；不得写入，也不作为恢复默认。
下文「固定配置」「初次上线顺序」是 Tokyo 期基线，标注为**历史**，勿当现网步骤。

## 命令入口

以下在仓库根目录执行；生产命令中的值来自已确认变更单，不复制旧节点坐标。
架构默认取目标矩阵 `target.architecture`（现网 amd64），镜像架构不一致即失败；
`--repo-path` 必填，现网活链为 `/candidates/tencent-usw-20261022/postgres`。

```bash
docker build --platform linux/amd64 -t tokenkey-postgres-backup:dev deploy/stage0/postgres-backup
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

