# 腾讯美西 prod · 隔离恢复演练与快照缺口方案

状态：**方案，待人工批准**（2026-10-22）。本文不授权创建任何云资源、修改 IAM 或 dispatch workflow。
现网身份：[舰队入口](../../deploy/hetzner/README.md)、[候选矩阵](../../deploy/tencent/candidate-usw-20261022.json)。
备份操作单：[physical-backup.md](../../ops/stage0/physical-backup.md)；加密盘：[解锁契约](tencent-unlock-contract.md)。

## 1. 只读核查事实（2026-10-22 ~12:50 UTC）

经 `run-probe.sh --target prod`（mi-080ed76dc6d97d8b5）只读探针与本机只读 AWS 查询：

| 项 | 实况 |
|---|---|
| 主机 | `VM-0-16-ubuntu`，Ubuntu 24.04，`uname -m=x86_64` |
| PG 容器 | `tk-archive-standby`，`docker run` 创建（无 compose labels），镜像 `sha256:dadb3a9c…`（tag `tokenkey-postgres-backup:usw-validation`，**amd64**），PG 18.6 / pgBackRest 2.58.0 |
| PG 数据挂载 | bind `/srv/tokenkey-rehearsal/standby-pg` → `/var/lib/postgresql/data`；`/etc/pgbackrest` ← `/run/tokenkey-backup-candidate/config`（tmpfs，ro）；`/srv/tokenkey-rehearsal` 与 `/var/lib/tokenkey` 是同一 LUKS mapper 的两个挂载 |
| PG 网络别名 | `postgres`、`tokenkey-postgres`（`tokenkey_tokenkey-network`） |
| 备份链 | `repo1-path=/candidates/tencent-usw-20261022/postgres`，桶 `tokenkey-prod-physical-backup-…-us-east-1`；timeline 2，库约 21 GB；候选前缀约 6.9 GB / 1185 对象 |
| 备份 owner | 宿主 `/usr/local/lib/tokenkey/tencent-pgbackrest/tencent_pgbackrest.py`（sha256 `f30b556d…`，**不在仓库**），只有 `backup|check`，无 `accept/verify/status` |
| 定时器 | `tokenkey-postgres-backup.timer`（2h）、`tokenkey-postgres-archive-check.timer`（5min）、`tokenkey-backup-identity.timer`（60s） |
| 最近状态 | backup ok（incr `20261022-020832F_20261022-123004I`，50 s）；archive check ok，failed_count=0 |
| `archive_timeout` | **0**（容器命令行未带该参数；文档基线是 300 s） |
| Redis | `tk-cutover-prod-redis`，`appendonly no`，RDB `save 3600 1 300 100 60 10000`，**无任何挂载**：`/data` 位于容器可写层（LUKS 上的 Docker root），容器被删除即丢失 |
| Caddy | `tokenkey-caddy`，Caddyfile/data/config 在 `/srv/tokenkey-rehearsal/formal/`（约 0.3 MB） |
| 恢复角色 | CFN `tokenkey-prod-physical-backups` 的 RestoreRole 只读 `prod/*`、`artifacts/*`，**读不到活链前缀** |
| Hybrid 角色 | `tokenkey-lightsail-ssm-hybrid`：托管 `AmazonSSMManagedInstanceCore` + S3 内联策略；**无 `ssm:PutParameter`** |
| 恢复包 | `artifacts/` 下 5 份均为 Tokyo 时期（2026-10-20，arm64，`repo1-path=/prod/postgres`）；`artifacts/tencent-validation/` 只有工具包 |
| 本机 tccli | 未安装，腾讯侧能力未用 API 查证 |

以下两项需另行决策，本 PR 未处理：
1. `archive_timeout=0` 时，WAL 只在 16 MiB 段写满后才归档。低流量时 RPO 没有上界。
   `archive_timeout` 是 sighup 级参数，`ALTER SYSTEM SET archive_timeout=300` 加 `pg_reload_conf()` 不需要重启，但属于生产变更，需审批。
2. Tencent 备份 owner 不在仓库，也无法导入恢复验收回执。
   即使恢复演练成功，`ops-physical-restore.yml` 的回执导入步骤也会明确失败（本 PR 已改为清晰报错）。应把该 operator 入仓，并补上 `accept`。

## 2. 隔离物理恢复演练

### 代码现状（本 PR）

- `run_physical_restore.py` 新增必填参数 `--architecture amd64|arm64`：amd64 → `c7i.xlarge` + AL2023 `x86_64` AMI，arm64 保留 `c7g.xlarge`。
  `--subnet` 必须是显式 `subnet-*`，`mi-*` 等值直接失败。
- `ops-physical-restore.yml` 不再对 resolver 结果调用 `ec2 describe-instances` 取 subnet，也不再写死 `ap-northeast-1`。
  改为读取 `vars.PHYSICAL_RESTORE_{REGION,SUBNET,ARCH}`，缺任何一项就在取凭据之前失败。
- `prepare_physical_backup.py` 新增必填参数 `--repo-path`，架构从 `--architecture` 或目标矩阵 `target.architecture` 取得。这样可以打出指向活链的 amd64 恢复包。

### 路线对比

| | A. 腾讯侧临时 CVM（硅谷，按量） | **B. AWS us-east-1 临时 amd64 EC2（推荐）** |
|---|---|---|
| 资源 | 4C8G 按量 CVM + 100 GB 云盘；Lighthouse 为包月预付，不适合一次性演练 | `c7i.xlarge`（4C8G）+ 100 GiB 加密 gp3，默认 VPC 公有子网，无入站 |
| 计算费用 | 量级约 ¥1–3/小时（**未用 tccli 询价**，执行前用 `InquiryPriceRunInstances` 复核） | 按需约 $0.18/小时；gp3/IPv4 2 小时合计 < $0.05 |
| 数据出网 | S3 us-east-1 → 公网 → 腾讯：一次全量 + 增量 + WAL 约 7–10 GB，按 ~$0.09/GB 计约 $1 | 同区 S3 → EC2 **免流量费** |
| 数据驻留 | 生产数据副本进入第二家云的新主机 | 数据本来就在 AWS S3，不新增驻留面 |
| 凭据/权限 | 新增腾讯 API 子账号（cvm/vpc/cbs 创建与销毁），本机需配置 tccli；AWS 侧需为活链前缀签发短期只读 STS；命令通道需 TAT 或新的 SSM Hybrid 激活 | CI OIDC 角色需在 us-east-1 有 `ec2:RunInstances/CreateSecurityGroup/...`、`iam:PassRole`（RestoreInstanceProfile）和 SSM send-command 权限；RestoreRole 读权限需加上 `candidates/tencent-usw-20261022/postgres/*`（CFN 变更） |
| 自动销毁 | 需新写编排：RunInstances 的定时销毁（ActionTimer，**需核实**）+ 宿主 `shutdown`；当前仓库没有腾讯编排器 | 已实现：`shutdown -h +120`、`instance-initiated-shutdown-behavior=terminate`、按 client-token 认领清理并确认卷已删除 |
| 代码改动 | 新写编排器、资源回收与测试 | 本 PR 已完成参数化；剩余为配置和 IAM |
| 架构一致性 | x86，与现网一致 | x86，与现网一致 |

推荐 **B**：成本最低，同区不产生流量费，不引入新的云凭据，自动销毁已经过实测；剩余工作只有 IAM 和配置。
A 只在明确要求"恢复演练必须在腾讯侧证明"时才值得做。

### B 路线执行前需人工批准

1. **IAM / CFN**（改 `deploy/aws/cloudformation/stage0-physical-backups.yaml`，不在本 PR 范围）：
   RestoreRole 的 `s3:GetObject` 增加 `${Repository.Arn}/candidates/tencent-usw-20261022/postgres/*`；
   CI OIDC 角色在 us-east-1 获得 EC2 临时资源、PassRole 与 SSM 权限，范围限定 `tokenkey-purpose=physical-restore` 标签。审批人：账号/IAM 管理员。
2. **恢复包**：用 amd64 构建 `backup-billing-check`，运行
   `prepare_physical_backup.py --architecture amd64 --repo-path /candidates/tencent-usw-20261022/postgres`，上传到 `artifacts/<sha256>.tar.gz`（新对象，不覆盖旧包）。
3. **仓库变量**：`PHYSICAL_RESTORE_REGION=us-east-1`、`PHYSICAL_RESTORE_SUBNET=<默认 VPC 子网>`、`PHYSICAL_RESTORE_ARCH=amd64`，同时更新 `PHYSICAL_BACKUP_BUNDLE_SHA/IMAGE_ID`，并设置 `PHYSICAL_BACKUP_ENABLED=true`。
4. **预算**：单次硬上限 **$5**（预计 < $1），实例寿命 2 小时；超时、失败或清理不完整都不产生成功回执。审批人：业务负责人。
5. **验收导入**：Tencent operator 需先补 `accept`，或者把回执作为工件人工留存；在此之前 workflow 最后一步会明确失败。

## 3. 快照缺口

### 现状

腾讯机没有整机或卷快照，Tokyo 的 DLM 不适用。离机副本只有 PG（pgBackRest → S3）。
以下内容只存在于这台主机：

| 数据 | 位置 | 丢失影响 |
|---|---|---|
| `.env`（含 POSTGRES_PASSWORD/JWT_SECRET/TOTP_ENCRYPTION_KEY 等） | `/var/lib/tokenkey/.env` | TOTP 密钥丢失会使已加密字段不可解；JWT 轮换导致全员重新登录 |
| Redis RDB | 容器可写层 `/data`（无挂载） | 会话、限流、缓存；按迁机文档"Redis 默认新建"可接受，但要注意：容器重建也会清空它 |
| Caddy 配置与证书 | `/srv/tokenkey-rehearsal/formal/caddy*`、`tls/` | 证书可经 ACME 重签；Caddyfile 有仓库来源 |
| 蓝绿状态、compose、布局回执 | `/var/lib/tokenkey/{active-color,docker-compose.bluegreen.yml,layout-staging/receipts}` | 部分可由仓库重建 |
| Tencent 备份 operator、身份刷新脚本、systemd 单元 | `/usr/local/lib/tokenkey{,-migration}/`、`/etc/systemd/system/tokenkey-*` | 不在仓库，丢失后无法原样复建（见 §1） |
| LUKS 解锁材料 | 已异机托管在操作员本机（见解锁契约） | — |

### Lighthouse 快照：能力与限制

依据[腾讯云 Lighthouse 快照文档](https://www.tencentcloud.com/document/product/1103/41394)，未用 tccli 实测：
- 免费额度：每地域快照数 = 实例数 × 2，且不超过 20。美西现有 2 台（主机 + 代理），因此约 4 份。超额收费与定期快照策略在该文档中未写明，**执行前需用 `DescribeSnapshots`/控制台复核**。
- 只针对系统盘，创建不需要停机，一般 5 分钟内完成；回滚覆盖整个系统盘且不可逆，运行中的实例会被自动关机。
- **实例销毁时，它的所有快照一起删除**：快照不是离机或异账户副本，挡不住实例丢失、账号问题或误销毁，只能防"误操作 / 升级失败回滚"。
- 快照与实例同地域，不能当跨地域灾备。

LUKS 盘上的快照意味着：
- LUKS 是系统盘上的 80 GiB loop 文件，快照里只有密文。回滚后仍需人工交付解锁材料到 tmpfs，按 `unlock → mount → Docker → app` 启动。快照本身不泄露数据，但离开异机密钥就无法使用。
- 快照是块级崩溃一致：PG 回滚后走崩溃恢复；回滚点之后的写入会丢，而且回滚后的 timeline 会与 S3 活链分叉。
  **数据库恢复仍以 pgBackRest 为准**，快照回滚后不得直接接写，必须先核对与活链的 LSN/timeline。
- 快照包含 `.env` 明文所在的密文盘；保留期内任何能回滚该快照的人都需要具备解锁材料才能读出内容。

建议：
- 只用作变更前保护：deploy-stage0、宿主升级、布局变更前手动建一份，保留 2 份滚动，在免费额度内。
- 日常不开定期快照。如需定期，频率为每天一次、保留 2–3 份，前提是确认免费额度与费用。
- 费用：额度内为 0。超额单价未查证，只给量级（同类云盘快照按 GB·月计费，120 GB 系统盘每份每月约为个位数美元）。
- 还原步骤：
  1. 停写并确认活链最新 WAL 已入仓；
  2. 控制台或 API 回滚（实例会关机）；
  3. 开机后按解锁契约交付密钥、挂载，再启动 Docker；
  4. 先启动 PG，核对 timeline/LSN 与活链：落后时以 pgBackRest 恢复为准，不直接接写；
  5. 跑只读解锁探针、发布 smoke 后才放流量。
- 创建快照或策略需人工批准，本 PR 没有创建。

### 最小可行"配置/密钥离机备份"

**能否复用 `backup-env-secrets-via-ssm.sh`**：只能部分复用。
- 脚本已支持 Hybrid（`REGION` 参数、dash→bash 包装），但 Hybrid 角色**没有 `ssm:PutParameter`**，现有参数 `/tokenkey/prod/stage0/env-secrets-backup`（us-east-1，2026-10-17 Tokyo 时期写入）无法由腾讯机更新。
- 它只备份 3 个密钥，不覆盖其余 `.env`、Caddy、systemd 与 operator。
- 若沿用此脚本：给 Hybrid 角色加一条仅限该参数 ARN（或腾讯专用新参数名）的 `ssm:PutParameter/GetParameter` 内联策略，在 `REGION=us-east-1` 下运行一次。之后每次轮换都要重跑。需 IAM 审批。

**推荐的最小方案（不需要改 IAM）**：
1. 宿主把 `.env`、`docker-compose.bluegreen.yml`、`active-color`、Caddyfile、`/etc/systemd/system/tokenkey-*`、`/usr/local/lib/tokenkey{,-migration}/` 打成 tar。
2. 用**操作员公钥**（age 或 openssl RSA/OAEP 包装随机 AES 密钥）加密后，上传到现有可写前缀 `candidates/tencent-usw-20261022/host-config/<UTC>.tar.enc`。Hybrid 角色在该前缀已有 Put 权限。
3. 私钥与 LUKS 包装密钥一样留在操作员本机，宿主只持有公钥，被入侵也读不出历史备份。
4. 变更后触发即可，或每天一次并做变更检测，保留 14 份。运行回执只写对象 key、sha256 与文件清单，不写内容。
5. 验收：在操作员本机下载、解密，与宿主 sha256 比对。

已知取舍：该前缀同时允许 `DeleteObject`，宿主被入侵后可以删掉这些备份，可选用桶版本控制缓解。
这个方案需要另开 PR 实现并审批首次运行；本 PR 只写方案。
