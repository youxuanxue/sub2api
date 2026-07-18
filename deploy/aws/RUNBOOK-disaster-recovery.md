# TokenKey Stage0 Prod — 灾难恢复 Runbook（单节点）

> 适用对象：`api.tokenkey.dev` 的 AWS Stage0 单机栈（`deploy/aws/cloudformation/stage0-single-ec2.yaml`）。
> 目标：prod 整机 / OS / 数据卷级故障时，用**确定性步骤**把服务在分钟级恢复，而不是临场拼命令。
> 这是「冷重建」runbook，不是热备。100 用户阶段刻意选它——便宜、够用、零常驻技术债。
> prod 同机双 app 单数据层蓝绿发版见 `docs/deploy/blue-green-zero-downtime-backlog.md`。

## 恢复资产地图（先看这里——重建/恢复从这里找东西）

| 要恢复的东西 | 存在哪（去这里找） | 用哪节 |
|---|---|---|
| **PG 账本**（唯一不可再生）| ① 本机 EBS 卷 `/var/lib/tokenkey/postgres`（随机器）② DLM 快照（tag `Backup=stage0`，每日保留 7 天）③ **离机 S3** `s3://tokenkey-prod-pgdump-<acct>/prod/pgdump/`（每小时，保留 7 天，RPO ≤1h，**唯一离机副本**）| §3 / §4 / §4.4 |
| **`.env` 密钥**（POSTGRES_PASSWORD / JWT_SECRET / TOTP_ENCRYPTION_KEY）| ① 本机 EBS 卷 `/var/lib/tokenkey/.env` + DLM 快照 ② **离机 SSM SecureString** `/tokenkey/prod/stage0/env-secrets-backup`（KMS 加密，与 S3 dump 桶不同爆炸半径；运营用 `ops/stage0/backup-env-secrets-via-ssm.sh` 在激活+密钥轮转时 push，变更检测幂等）| §3 / §4 随卷带回；总损见 §4.4 |
| ↳ *push 授权*（`ssm:PutParameter`，仅离机 push 用，恢复不需要）| durable 在 prod InstanceRole（`stage0-single-ec2.yaml`，下次 prod CFN 更新生效）；当前临时为角色上手工内联策略 `EnvSecretsBackup`。**⚠️ §3 换机重建角色后，若 CFN 更新尚未落地，需在 IAM 控制台把该内联策略加回（`ssm:PutParameter` on `.../stage0/env-secrets-backup`），否则离机 push 静默失效**（已离机的旧值仍可正常 §4.4 取回）| §3 后重加 |
| **CFN 模板 / compose / 脚本 / Caddyfile** | 本仓库 `deploy/aws/cloudformation/` + `deploy/aws/stage0/`（build-cfn 嵌入 SSM Parameters，首启拉取）| §3 换机即重建 |
| **S3 备份桶本身** | CFN 栈 `tokenkey-stage0-backups`（`deploy/aws/cloudformation/stage0-backups.yaml`）| — |
| **Redis**（计数/缓存）| 不备份——可重建，新机起来自然重填 | — |

> 「哪种故障 → 用哪节」的决策树见 §0 下方。最常见是 §3（实例死、卷在 → 换机零丢失）；S3 dump（§4.4）是卷+快照都没了的最后一手。

## 0. 先理解已有的韧性能力（多数故障不需要本 runbook 的重活）

prod 栈出厂就带三层保护，**恢复前先确认能否用更轻的一层**：

| 能力 | 来源 | 覆盖的故障 |
|---|---|---|
| **持久数据卷** `DataVolume`（gp3，加密，`DeletionPolicy/UpdateReplacePolicy: Retain`） | CFN，挂 `/dev/sdf`→`/var/lib/tokenkey`，承载 PG/Redis/Caddy/app/`.env.secret` | 实例替换（ImageTag 变更等）时卷自动 detach→re-attach，数据零丢失 |
| **DLM 自动快照** `SnapshotPolicy` | CFN，针对 `Backup=stage0` tag 的实例，daily 03:00 保留 7 天（可切 hourly 保留 168=7 天） | 数据卷损坏 / 误删 / AZ 丢失，回到最近快照点 |
| **失败自动回滚** | `ops/stage0/deploy_via_ssm.sh` 的 ERR trap | 发版本身失败，自动恢复上一镜像 |

**RPO**（最坏数据丢失）：daily 快照 = ≤24h；关键发版/迁移期建议临时切 `SnapshotSchedule=hourly`（见 §5）。
**RTO**（恢复时长）：场景 A 约 5–15 min（CFN 实例替换 + bootstrap 拉镜像起容器）；场景 B 约 +5–10 min（从快照建卷）。

### 0.x 账本出机（RDS 模式）后的恢复语义 — 仅在已批准生产切换后生效

prod 当前仍是本机 PostgreSQL。未来按已审批设计迁到 RDS（`tokenkey-data-stage0` 栈，
SOP：`docs/deploy/aws-data-layer-migration.md`）之后：

| 故障 | RDS 模式下的恢复 | 替代了原文的什么 |
|---|---|---|
| **PG 数据损坏 / 误删数据** | RDS PITR（14 天窗口）：`aws rds restore-db-instance-to-point-in-time` 到新实例，逐表对账后让 app 前向指向新 endpoint | §4 从 DLM 快照恢复 PG 的部分（RPO ≤24h）——DLM 快照仍覆盖卷上其余数据（Caddy/.env.secret/Redis/pgdump） |
| **RDS 实例故障** | 推荐候选 Single-AZ：AWS 自动恢复期间服务不可用；该风险仍待设计审批。上第二 app 时同步升 `MultiAZ=true` | 无对应——本机模式下 PG 与实例同生死 |
| **app 实例坏 / 替换（§3）** | 照 §3 执行，**账本零风险**（在 RDS 上）；新机 bootstrap 从 SSM overlay `/tokenkey/prod/stage0/data-layer-env` 自动渲染外部模式（单一真相，防 split-brain） | §3 中「数据零丢失依赖卷 Retain」对 PG 的部分 |
| **整 AZ 不可用** | app 照 §3/§4 在别的 AZ 重建；RDS 单 AZ 实例若恰在故障 AZ → PITR 恢复到健康 AZ | §4 |

**双保险仍然在**：每小时 precious-class pgdump 自动改走 RDS（wrapper 读 .env），
本地滚动 6 份并上传 S3；它不包含 usage/ops/QA 表行，不能替代历史归档。
**回退边界**：切换后 14 天内保留本机 postgres 数据卷，但 RDS 开放写入后旧库已落后，
禁止直接回旧库。默认前向修复/PITR；若必须回本地，先回放 RDS 增量并逐表对账，
再经独立高风险审批切流。
**Exports 锁定**：app 栈的 `VpcId/PrivateSubnetIds/AppSecurityGroupId` 被数据栈
Import，改网络拓扑先 delete 数据栈（RDS 资源 Retain + DeletionProtection，不丢数据）。
edge 与未切换环境仍按下文原文执行（本机 PG 语义不变）。

恢复决策树（从上往下，命中即停）：

```
容器挂 / app unhealthy（实例还在）         → §1  SSM 重启 compose（非灾难）
OS 卡死但实例可 reboot                      → §2  reboot
实例彻底坏 / 被误删，但数据卷健在            → §3  CFN 替换实例（卷 Retain，数据零丢失）★最常用
数据卷也丢失 / 损坏 / 整个 AZ 不可用         → §4  从 DLM 快照恢复（丢 ≤RPO 的数据）
```

恢复前先取关键标识（下文命令引用这些变量）：

```bash
export AWS_REGION=us-east-1                 # prod 所在区，按实际
export STACK=tokenkey-prod-stage0           # 实际栈名：aws cloudformation list-stacks 查
# 常用查找：
aws cloudformation describe-stacks --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`||OutputKey==`DataVolumeId`||OutputKey==`PublicIP`].[OutputKey,OutputValue]' \
  --output text
```

---

## Agent 协同契约（Agent 执行本 runbook 时的边界）

本 runbook 既给人读、也给 Agent 照跑。**触发**：发版 `ops/stage0/deploy_via_ssm.sh` 的自动 rollback 也救不回（SSM 日志 `node requires MANUAL intervention`）、或 external_health / smoke 失败——**单次救不回即入本 runbook**（不必等「反复 N 次」）。来路见 `.cursor/skills/tokenkey-stage0-release-rollout/SKILL.md` 的 `## rollback` 段。

| 步骤 | Agent 可否自主 |
|---|---|
| 只读诊断（`ops/observability/run-probe.sh --target prod` 看容器 / 日志 / `ops_error_logs`） | ✅ 自主 |
| §1 容器重启（`docker compose up -d`）、§2 `reboot-instances` | ✅ 自主（低风险、可逆） |
| §last 恢复后验证（`ops/stage0/post_deploy_smoke.sh` + `ops/stage0/measure_deploy_blackout.sh`） | ✅ 自主 |
| §3 CFN `execute-change-set`（换实例） | ⛔ **先 plan、等人类批**：Agent 跑 `create-change-set` + `describe-change-set` 预览（确认 `DataVolume` 不在变更列表 = 卷被保留），把变更呈给人类，**批准后**才 execute |
| §4 动数据卷（`create-volume` / `detach-volume` / `attach-volume`，含快照与 RPO 选择） | ⛔ **先 plan、等人类批**：选哪个快照、丢多少增量是判断题，Agent 列候选快照 + RPO 影响，人类拍板 |
| §DNS 切换（Porkbun A 记录） | ⛔ **人类执行**：Porkbun 凭证不在 repo |

原则：**只读与可逆步骤 Agent 自主推进；不可逆 / 高爆炸半径步骤（CFN execute、动卷、切 DNS）一律 plan → 人类批 → execute**——这是确定性自动化唯一保留的人工介入点。命令细节见下方对应章节，本契约不复述。

## 1. 容器挂 / app unhealthy（实例还在）— 非灾难

不是灾难，先排除。SSM 进实例看容器与日志：

```bash
aws ssm start-session --target <InstanceId>
# 实例内：
cd /var/lib/tokenkey
sudo docker compose ps
sudo docker logs tokenkey --since 10m 2>&1 | tail -80
sudo docker compose --env-file .env up -d   # 重新拉起缺失容器
curl -fsS http://localhost:8080/health/live && echo " live-ok"
```

迁移校验和守卫导致**全网拒绝启动**（改过已应用迁移）时，日志会明示 checksum 不匹配——这类需回滚到正确镜像，不是本 runbook 范围，见 §发版纪律。

---

## 2. OS 卡死 — reboot

```bash
aws ec2 reboot-instances --instance-ids <InstanceId>
# 等 2–3 min，UserData 不重跑（reboot 非 replace），fstab 的 nofail 挂载会自动恢复数据卷。
```

reboot 后走 §1 确认容器起来 + §last 验证。

---

## 3. 实例彻底坏 / 误删，数据卷健在 — CFN 替换实例（★最常用、数据零丢失）

**原理**：`DataVolume` 是 CFN 栈内资源且 `Retain`，**只要不删栈**，它就一直是同一个物理卷。让 CFN 重建 `Instance` 资源，新实例 UserData 会 `attach-volume` 这个现存卷并按 fstab UUID 挂回 `/var/lib/tokenkey`，PG/Redis/`.env.secret` 全部原样。

```bash
# 触发 Instance 资源替换：update-stack 改任一驱动 Instance 重建的属性即可。
# 先决定新机 bootstrap 用的 ImageTag。旧实例仍能 SSM 时，机械读取运行态 tag：
RUNNING_TAG=$(bash ops/stage0/resolve-prod-running-tag-via-ssm.sh \
  --region "$AWS_REGION" --stack "$STACK")
# 若旧实例已误删 / 无法 SSM，必须人工填写 last known-good semver
# （例如最近一次成功 deploy-stage0 的 tag）；不要用陈旧 CFN ImageTag 或 mutable latest。
# RUNNING_TAG=1.8.91

# 用 change-set 预览（永远先 dry-run）：
aws cloudformation create-change-set --stack-name "$STACK" \
  --change-set-name dr-replace-instance --use-previous-template \
  --parameters ParameterKey=ImageTag,ParameterValue="$RUNNING_TAG" \
               $(其余参数用 UsePreviousValue=true) \
  --capabilities CAPABILITY_NAMED_IAM
aws cloudformation describe-change-set --stack-name "$STACK" --change-set-name dr-replace-instance \
  --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output table
# 确认 Instance=Replace、DataVolume 不在变更列表里（说明卷被保留），再执行：
aws cloudformation execute-change-set --stack-name "$STACK" --change-set-name dr-replace-instance
aws cloudformation wait stack-update-complete --stack-name "$STACK"
```

> 注意：若 EIP 关联（`EIPAssoc`）也被重建，公网 IP 不变（EIP 是 Retain/同一 AllocationId），DNS 通常无需改。若新实例拿到新 EIP，按 §DNS 切换。

完成后走 §last 验证。

---

## 4. 数据卷丢失 / 损坏 / AZ 不可用 — 从 DLM 快照恢复

数据卷没了（误删 Retain 卷、卷损坏、整个 AZ 故障）。从最近快照建新卷，挂到新实例。**会丢失「最近快照→故障」之间的数据（≤RPO）。**

### 4.1 找最近可用快照

```bash
# DLM 快照带 Backup=stage0（CopyTags）+ snapshot-by=dlm。按时间倒序取最新 completed：
aws ec2 describe-snapshots --owner-ids self \
  --filters Name=tag:snapshot-by,Values=dlm Name=status,Values=completed \
  --query 'reverse(sort_by(Snapshots,&StartTime))[:5].[SnapshotId,StartTime,VolumeId,VolumeSize]' \
  --output table
export SNAP=snap-xxxxxxxx           # 选定的最新快照
```

### 4.2 从快照建卷（建在目标实例所在 AZ）

```bash
export TARGET_AZ=$(aws ec2 describe-availability-zones --query 'AvailabilityZones[0].ZoneName' --output text)
export NEWVOL=$(aws ec2 create-volume --snapshot-id "$SNAP" --availability-zone "$TARGET_AZ" \
  --volume-type gp3 --encrypted \
  --tag-specifications 'ResourceType=volume,Tags=[{Key=Name,Value=tokenkey-prod-data-restored},{Key=Backup,Value=stage0}]' \
  --query VolumeId --output text)
aws ec2 wait volume-available --volume-ids "$NEWVOL"
echo "restored volume = $NEWVOL"
```

### 4.3 让新实例用这个恢复卷

若 §3 的 CFN 替换会新建一个**空** `DataVolume`，需把它换成恢复卷。两条路，**优先 A**：

**A. 新栈/新实例 + 手动换卷（推荐，绕过 CFN 空卷）**
```bash
# 1) 起新实例（CFN update 如 §3，或新栈），它会 attach CFN 的空 DataVolume。
# 2) SSM 进实例，停服并卸载空卷：
aws ssm start-session --target <NewInstanceId>
  cd /var/lib/tokenkey && sudo docker compose down
  sudo umount /var/lib/tokenkey
  # 退出会话
# 3) detach CFN 空卷、attach 恢复卷到同一 device：
aws ec2 detach-volume --volume-id <CFN空卷Id>
aws ec2 wait volume-available --volume-ids <CFN空卷Id>
aws ec2 attach-volume --instance-id <NewInstanceId> --volume-id "$NEWVOL" --device /dev/sdf
aws ec2 wait volume-in-use --volume-ids "$NEWVOL"
# 4) SSM 进实例重挂（恢复卷已有 ext4 label tokenkey-data + 数据），按 UUID 重写 fstab：
aws ssm start-session --target <NewInstanceId>
  DEV=$(readlink -f /dev/sdf 2>/dev/null || echo /dev/nvme1n1)
  sudo mount "$DEV" /var/lib/tokenkey
  NEWUUID=$(sudo blkid -s UUID -o value "$DEV")
  sudo sed -i "\#/var/lib/tokenkey#d" /etc/fstab
  echo "UUID=$NEWUUID /var/lib/tokenkey ext4 defaults,nofail,x-systemd.device-timeout=90 0 2" | sudo tee -a /etc/fstab
  cd /var/lib/tokenkey && sudo docker compose --env-file .env up -d
  curl -fsS http://localhost:8080/health/live && echo " live-ok"
```

> `.env.secret`（POSTGRES_PASSWORD / JWT_SECRET / TOTP_ENCRYPTION_KEY）就在恢复卷上，随卷一起回来——**不要**重新生成，否则旧 PG 数据解不开、所有 JWT/2FA 失效。

**B. 若必须走纯 CFN**：CFN 的 `DataVolume` 不接受 `SnapshotId` 参数（总建空卷），所以纯 CFN 无法直接从快照恢复——这是已知限制。需要从快照恢复时一律用路径 A。（若未来此场景频繁，再考虑给 CFN 加 `RestoreSnapshotId` 参数 + Condition，按「先用后建」原则现在不预先实现。）

完成后走 §last 验证。

### 4.4 兜底：从 off-box S3 pg_dump 恢复账本（RPO ≤1h，DESTRUCTIVE）

**何时用**：DLM 快照也丢了 / 太旧，或需要把账本**干净重建**进一个新 PG。`tokenkey-pgdump.sh` 每小时把逻辑 dump 推到 S3（`stage0-backups.yaml` 栈的桶，保留 7 天），所以这是比 DLM（最长 24h）更新的账本副本。**仅恢复 PostgreSQL 账本**（逻辑 dump）；Redis 可重建。丢失「最近一次 hourly dump → 故障」之间的写入（≤1h）。**总损（卷+快照都没）时**，`.env` 密钥从离机 SSM SecureString 取回（下方第 0 步）——**不要重新生成**（否则旧 PG/JWT/2FA 失效）。

> **dump 是「珍贵类」分级 dump**：含**全部表的 schema** + 除大体量可重建日志表外的全部**数据**。`tokenkey-pgdump.sh` 用 `--exclude-table-data`（保 schema、去行）排除了 `ops_system_logs* / usage_logs* / ops_error_logs* / qa_records*`（~8GB 遥测，各有自身 DELETE/分区保留）。**还原后这些日志表结构在、数据为空**，随线上流量重新累积；计费真值 `usage_billing_dedup`、账号/配置等珍贵数据**完整还原**。要带历史日志的整库副本，走 §3/§4.1-4.3 的卷/快照路径（整库物理副本，不受本 dump 分级影响）。

```bash
# 0) 总损时先取回 .env 密钥（离机 SSM SecureString，与 dump 桶不同爆炸半径）。
#    若恢复卷健在（§4.1-4.3），密钥随卷回来，跳过此步。新机/干净 PG 必须先放对密钥
#    再恢复库（PG 用 POSTGRES_PASSWORD 初始化）。
aws ssm get-parameter --region us-east-1 --name /tokenkey/prod/stage0/env-secrets-backup \
  --with-decryption --query Parameter.Value --output text | sudo tee -a /var/lib/tokenkey/.env >/dev/null
#    （上面把 3 行 KEY=VALUE 追加进 .env；若已有同名键先去重再追加。）

# 桶名 = backups 栈输出；URI 前缀 s3://<bucket>/prod/pgdump/
BUCKET=$(aws cloudformation describe-stacks --region us-east-1 \
  --stack-name tokenkey-stage0-backups \
  --query "Stacks[0].Outputs[?OutputKey=='BucketName'].OutputValue" --output text)
LATEST=$(aws s3 ls "s3://$BUCKET/prod/pgdump/" | sort | tail -1 | awk '{print $4}')
aws s3 cp "s3://$BUCKET/prod/pgdump/$LATEST" /tmp/restore.sql.gz

# SSM 进目标实例后（DESTRUCTIVE：重建 tokenkey 库）——先停 app 释放连接：
sudo docker stop tokenkey
sudo docker exec -i tokenkey-postgres psql -U tokenkey -d postgres -v ON_ERROR_STOP=1 \
  -c "DROP DATABASE IF EXISTS tokenkey;" -c "CREATE DATABASE tokenkey OWNER tokenkey;"
gunzip -c /tmp/restore.sql.gz | sudo docker exec -i tokenkey-postgres \
  psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1
sudo docker start tokenkey
curl -fsS http://localhost:8080/health/live && echo " live-ok"
```

> 优先级：数据卷/快照健在时一律走 §3/§4.1-4.3（带回 `.env.secret`、Redis、完整状态）。§4.4 是「卷+快照都没了」或「需要干净账本」的最后一手。

完成后走 §last 验证。

---

## DNS 切换（仅当公网 IP 变了）

EIP 是 Retain，多数恢复后 IP 不变。若新实例拿到新 EIP：

1. 取新 IP：`aws cloudformation describe-stacks --stack-name "$STACK" --query 'Stacks[0].Outputs[?OutputKey==\`PublicIP\`].OutputValue' --output text`
2. Porkbun 控制台把 `api.tokenkey.dev` 的 A 记录指向新 IP（TTL 生效 1–10 min）。
3. 等解析生效：`dig +short api.tokenkey.dev` 命中新 IP。

---

## §last 恢复后验证（强制）

```bash
# 1) 健康
curl -fsS https://api.tokenkey.dev/health && echo " health-ok"

# 2) 端到端 smoke（与发版同一套）
export TK_SMOKE_GITHUB_ENV=prod
export TK_SMOKE_API_KEY=sk-...   # 从 GitHub prod environment secrets
TOKENKEY_BASE_URL=https://api.tokenkey.dev bash ops/stage0/post_deploy_smoke.sh

# 3) 真空/抖动确认（恢复期外网视角无客户端可见失败）
TOKENKEY_BASE_URL=https://api.tokenkey.dev DURATION_SECONDS=60 \
  bash ops/stage0/measure_deploy_blackout.sh
```

数据完整性抽查：登录 admin，确认账号池数量、用户/API key、最近用量记录与故障前一致（场景 B 下允许缺失最近 ≤RPO 的增量）。

---

## 5. 演练与 RPO 调优

- **季度演练（非生产）**：在测试栈跑一遍 §4.1→§4.3 路径 A（从快照建卷→新实例 attach→起服务→smoke），**不切 prod DNS**。把实测 RTO 记到本文件 §0 表格。
- **关键发版/大迁移期收紧 RPO**：临时把 `SnapshotSchedule` 切到 `hourly`（保留 168=7 天），发版稳定后切回 `daily`：
  ```bash
  aws cloudformation update-stack --stack-name "$STACK" --use-previous-template \
    --parameters ParameterKey=SnapshotSchedule,ParameterValue=hourly $(其余 UsePreviousValue=true) \
    --capabilities CAPABILITY_NAMED_IAM
  ```
- **手动即时快照**（高风险操作前，不等 DLM 周期）：
  ```bash
  aws ec2 create-snapshot --volume-id <DataVolumeId> \
    --description "pre-change manual $(date -u +%FT%TZ)" \
    --tag-specifications 'ResourceType=snapshot,Tags=[{Key=snapshot-by,Value=manual},{Key=Backup,Value=stage0}]'
  ```

---

## 范围外

- 不涉及 edge 节点恢复（edge 是无状态资源出口，重新 provision 即可，见 `tokenkey-stage0-edge-lightsail-expansion` / `tokenkey-stage0-edge-lightsail-ip-rotation` skills）。
- 不涉及多区 active-active / 热备 / 自动 failover（100 用户阶段不做；触发阈值见蓝绿 backlog）。
