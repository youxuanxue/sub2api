# AWS 部署（tokenkey）

> 产品名 `tokenkey`，代码仓库与 GHCR 镜像名仍叫 `sub2api`（fork 关系，按 CLAUDE.md 保留）。
> 「部署侧产品身份」（stack 名、容器名、`/var/lib/tokenkey/`、systemd 单元、CW namespace、PG 用户/库默认）统一用 `tokenkey`；
> 应用环境变量名（`DATABASE_*`/`REDIS_*`/`JWT_*`）与 GHCR 镜像名 `sub2api`（`ghcr.io/<owner>/sub2api:<tag>`）是代码侧约定，**保持不变**。

本目录是 Stage 0 的可执行 IaC + 运行配置。完整方案、成本表、规格选型、备份策略、升级触发条件、所有 CFN 参数详表都在主文档：

- `**docs/deploy/aws-us-openai-gateway-deployment.md`** ← 权威，本 README 不重复

当前实现：**Stage 0**（单台 EC2 全栈，约 25–40/月，覆盖 100 同时活跃用户）。Stage 1/2/3 触发后再实施。

## 目录布局

```
deploy/aws/
├── README.md                         本文件（quick start）
├── cloudformation/
│   └── stage0-single-ec2.yaml        Stage 0 CFN：自包含（compose+Caddyfile 已 gzip+base64 内嵌进 UserData）
└── stage0/
    ├── docker-compose.yml            源真：Caddy + tokenkey + PostgreSQL + Redis
    ├── Caddyfile                     源真：LE 自动签证书 + 反代到 tokenkey:8080
    ├── .env.example                  环境变量模板（生产 .env 由 Cloud-Init 自动生成；本地调试可复制使用）
    └── build-cfn.sh                  把 docker-compose.yml + Caddyfile gzip+base64 注入 CFN 模板
```

> EC2 引导逻辑直接 inline 在 CFN 模板的 UserData 段（`stage0-single-ec2.yaml`）。无需独立的 `cloud-init.sh`；如需「不走 CFN」紧急 bootstrap，从 UserData 段 copy 出来本地化即可。

## CFN 自包含特性

CFN 模板已把 `docker-compose.yml` 与 `Caddyfile` 以 gzip+base64 内嵌进 UserData，部署时 EC2 不再外网拉这两个文件，**仓库可保持 GitHub 私仓 / 不公开**。

> **必须遵守的规则：** 编辑 `docker-compose.yml` 或 `Caddyfile` 之后，运行：
>
> ```bash
> bash deploy/aws/stage0/build-cfn.sh
> ```
>
> 否则 CFN 模板里的 base64 段会与源文件漂移。CI 上加 `bash deploy/aws/stage0/build-cfn.sh --check` 兜底。

## Quick Start

完整步骤在主文档 §3.5。最小操作如下：

```bash
REGION=us-east-1
GHCR_OWNER=<你的GitHub用户名>          # 替换
DOMAIN=api.tokenkey.dev                # 替换为你自己的对外域名
ACME_EMAIL=ops@tokenkey.dev            # 替换
ADMIN_EMAIL=admin@tokenkey.dev         # 替换

# 1) 一次性：把 GHCR Classic PAT (read:packages) 写进 SSM SecureString
#    详见主文档 §3.5 Step 0
aws ssm put-parameter --region "${REGION}" \
  --name /tokenkey/ghcr/pat --type SecureString \
  --value 'ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'

# 2) 部署栈（其余参数全用默认值；如需调整见主文档 §3.5「全部参数总表」）
#    注意 ImageTag：仅用于 stack 初始化时的首次镜像拉取；后续升级**不要**改这个参数（见 §升级 / 发版）。
#    填当下最新的 release tag（不带 v 前缀）：gh release list -L 1
INIT_IMAGE_TAG=$(gh release list -L 1 --json tagName --jq '.[0].tagName' | sed 's/^v//')
aws cloudformation deploy \
  --region "${REGION}" \
  --stack-name tokenkey-prod-stage0 \
  --template-file deploy/aws/cloudformation/stage0-single-ec2.yaml \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides \
    ApiDomain="${DOMAIN}" \
    AcmeEmail="${ACME_EMAIL}" \
    AdminEmail="${ADMIN_EMAIL}" \
    GhcrOwner="${GHCR_OWNER}" \
    GhcrPullUser="${GHCR_OWNER}" \
    ImageTag="${INIT_IMAGE_TAG}"

# 3) 取 EIP 去 Porkbun 加 A 记录
aws cloudformation describe-stacks --region "${REGION}" \
  --stack-name tokenkey-prod-stage0 \
  --query 'Stacks[0].Outputs[?OutputKey==`PublicIP`].OutputValue' --output text

# 4) DNS 生效后（1–10 min），验证
curl -sS -o /dev/null -w '%{http_code}\n' "https://${DOMAIN}/health"
# 期望 200；首次若 503 是 LE 还在签证书，等 1–2 min
```

## 升级 / 发版（生产 + 测试栈共用）


| Stack                       | `ImageTag` 来源                            | `ApiDomain`             | 升级方式                                                                                                                                                                                                                                  |
| --------------------------- | ---------------------------------------- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tokenkey-prod-stage0`      | `.env` 内的 `TOKENKEY_IMAGE`（CFN 参数仅用于初始化） | `api.tokenkey.dev`      | **首选路径：SSM `docker compose pull && up -d tokenkey`**（见下方 §生产升级 SOP），原地热替换、零停机。CFN deploy 改 `ImageTag` 现在**安全**（数据在独立 `DataVolume` 上，instance replace 时 detach + 新 instance attach），但仍有 1–3 min 停机窗口（旧实例 stop → 新实例 boot + bootstrap）。 |
| `tokenkey-test-stage0`（如存在） | `.env` 同上，初始化用 `latest` 跟随               | `test-api.tokenkey.dev` | 同上 SSM 路径；`latest` 让镜像自动是最新 release，但仍要 SSM 触发 `pull && up -d` 才会真正切换。                                                                                                                                                                |


> 2026-04-21 实测：prod 栈 CFN `ImageTag=1.2.0`，但运行态 `TOKENKEY_IMAGE` 与容器实际镜像均为 `ghcr.io/youxuanxue/sub2api:1.4.1`（SSM 原地升级后形成的受控漂移）。

> **数据持久化（preflight-debt §9.b / issue #8 已修，2026-04-20）**：
> `stage0-single-ec2.yaml` 现在创建独立的 `AWS::EC2::Volume`（资源名 `DataVolume`，参数
> `DataVolumeSizeGiB`，默认 30 GiB），带 `DeletionPolicy: Retain` + `UpdateReplacePolicy: Retain`，
> 通过 `AWS::EC2::VolumeAttachment` 挂到 `/dev/sdf`，`UserData` 检测 `/dev/nvme1n1` 等候选块设备、
> 用 `UUID=<volume-uuid>` 写 `/etc/fstab` 后挂载到 `/var/lib/tokenkey/`（避免 label 重名误挂载）。所有应用数据
> （`postgres/`、`redis/`、`caddy/data`、`app/`、`pgdump/`、`.env.secret`）都在这个独立卷上。
>
> 实例被替换时（无论是改 `ImageTag` 还是 AMI/UserData 任意改动），`DataVolume` **detach** 后
> attach 到新实例，filesystem 已存在 → `UserData` 跳过 `mkfs.ext4` 直接重挂载 → 数据零丢失。
>
> 持久秘密单独写入 `/var/lib/tokenkey/.env.secret`（`POSTGRES_PASSWORD` / `JWT_SECRET` /
> `TOTP_ENCRYPTION_KEY`），首次 boot 生成后永不重写；`/var/lib/tokenkey/.env` 仍每次 boot 重生成
> 以接收 CFN 参数变化。这样 PG 用户密码、JWT 已签发会话、TOTP 记录都能跨实例替换存活。

### Stage-0 风险审计（prod 已在用）

- **R1: 首次迁移不是零停机**：旧拓扑（数据在 root EBS）第一次迁移到 `DataVolume` 必须停服导出/回灌；跳步骤 5 会造成空库启动。
- **R2: 密钥未回灌会造成逻辑性“数据不可用”**：若不把旧 `POSTGRES_PASSWORD` / `JWT_SECRET` / `TOTP_ENCRYPTION_KEY` 覆盖回 `.env.secret`，会出现 PG 认证失败或全量会话/TOTP 失效。
- **R3: 误把“安全”理解为“无中断”**：CFN 改 `ImageTag` 现在数据可保留，但实例替换仍可能产生 1-3 分钟中断；生产默认仍建议 SSM 原地升级。
- **R4: 回滚能力依赖快照与冷备**：迁移窗口前必须完成 root EBS snapshot 和 tar 冷备，不满足则不应执行 deploy。

### 现有 prod 栈迁移到 DataVolume（一次性，必须做）

> **谁需要做**：在 2026-04-20 之前用旧版模板 deploy 的栈（数据全部在 root EBS 上）。
> **谁不用做**：在 2026-04-20 之后首次 deploy 的栈（已经是新拓扑）。
>
> 判断方法：`aws cloudformation describe-stack-resources --stack-name tokenkey-prod-stage0 --logical-resource-id DataVolume`，返回 "does not exist" 即旧拓扑。

#### 执行清单（10 行，窗口前逐项勾选）

- 已公告维护窗口（预期 5-10 分钟停机），并冻结变更入口。  
- 已记录当前 `INSTANCE_ID`、`ROOT_VOL`、`ImageTag`、运行态 `TOKENKEY_IMAGE`。  
- 已完成 root EBS snapshot 且状态 `completed`。  
- 已导出 `/var/lib/tokenkey` 冷备 tar 到 S3（含校验大小/可读）。  
- 已执行 CFN deploy（创建 `DataVolume`）并拿到新 `InstanceId`。  
- 已在新实例恢复业务目录数据（不覆盖新生成 `.env` / `.env.secret` 文件本身）。  
- 已从旧备份 `.env` 回灌 `POSTGRES_PASSWORD` / `JWT_SECRET` / `TOTP_ENCRYPTION_KEY` 到新 `.env.secret`。  
- 已启动 `tokenkey`，并确认 `docker compose ps` 全部 healthy。  
- 已完成外部 `/health`、登录、TOTP、关键 API 冒烟验证。  
- 已记录回滚锚点（snapshot id + S3 备份路径）并保留至少 7 天。

迁移需要 **5–10 min 停机窗口**（取决于数据量），按以下顺序执行：

```bash
REGION=us-east-1
STACK=tokenkey-prod-stage0
INSTANCE_ID=$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)

# 1) 全量备份（迁移前的最后一道保险）— 触发 DLM 立即快照 root EBS
ROOT_VOL=$(aws ec2 describe-instances --region "$REGION" --instance-ids "$INSTANCE_ID" \
  --query 'Reservations[0].Instances[0].BlockDeviceMappings[?DeviceName==`/dev/xvda`].Ebs.VolumeId' \
  --output text)
aws ec2 create-snapshot --region "$REGION" --volume-id "$ROOT_VOL" \
  --description "tokenkey pre-DataVolume-migration $(date -u +%FT%TZ)"
# 等 snapshot 进入 completed（视卷大小，通常 5–15 min）
SNAP_ID=$(aws ec2 describe-snapshots --region "$REGION" --owner-ids self \
  --filters "Name=volume-id,Values=$ROOT_VOL" --query 'Snapshots | sort_by(@,&StartTime) | [-1].SnapshotId' --output text)
aws ec2 wait snapshot-completed --region "$REGION" --snapshot-ids "$SNAP_ID"

# 2) 进实例 → 停服 → 把 /var/lib/tokenkey 打包到 /tmp/tokenkey-data.tar.gz
aws ssm start-session --region "$REGION" --target "$INSTANCE_ID"
# (在实例内)
sudo systemctl stop tokenkey
sudo docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env down
cd /var/lib/tokenkey
sudo tar -czf /tmp/tokenkey-data.tar.gz --one-file-system .
sudo ls -lh /tmp/tokenkey-data.tar.gz                  # 确认大小合理
exit                                                   # 退出 SSM session

# 3) 把 tarball 挪到本地（或 S3）作为冷备 — 万一 CFN 重启失败时手工恢复用
aws ssm send-command --region "$REGION" --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --parameters "commands=[\"aws s3 cp /tmp/tokenkey-data.tar.gz s3://<your-backup-bucket>/tokenkey-pre-migrate-$(date +%s).tar.gz\"]"

# 4) CFN deploy — 这一步会创建新 DataVolume 并替换实例。新 UserData 会发现
#    DataVolume 是空的 → mkfs.ext4 → 挂在 /var/lib/tokenkey → 生成全新 .env.secret →
#    PG / Redis 从空状态启动。**这是预期行为** —— 步骤 5 才把数据塞回去。
aws cloudformation deploy --region "$REGION" \
  --stack-name "$STACK" \
  --template-file deploy/aws/cloudformation/stage0-single-ec2.yaml \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides DataVolumeSizeGiB=30
NEW_INSTANCE_ID=$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)

# 5) 进新实例 → 停服 → 用 tarball 覆盖 → 起服
aws ssm start-session --region "$REGION" --target "$NEW_INSTANCE_ID"
# (在实例内)
sudo systemctl stop tokenkey
# 把步骤 3 留在 S3 的 tarball 拿回来（或从旧实例 EIP 上 scp，但旧实例已经被销毁，所以走 S3）
aws s3 cp s3://<your-backup-bucket>/tokenkey-pre-migrate-<ts>.tar.gz /tmp/tokenkey-data.tar.gz
cd /var/lib/tokenkey
# 保留新生成的 .env.secret 和 .env，覆盖业务数据目录
sudo cp .env.secret /tmp/.env.secret.new
sudo cp .env        /tmp/.env.new
sudo tar -xzf /tmp/tokenkey-data.tar.gz --overwrite \
  --exclude='./.env' --exclude='./.env.secret' --exclude='./.env.before-*'
# 必须把旧 .env 里的三个核心秘密覆盖回新 .env.secret，否则会出现
# PG 密码不匹配 / 已签发 JWT 全失效 / TOTP 全失效。
OLD_ENV=$(mktemp)
sudo tar -xzf /tmp/tokenkey-data.tar.gz ./.env -O > "$OLD_ENV"
for key in POSTGRES_PASSWORD JWT_SECRET TOTP_ENCRYPTION_KEY; do
  val=$(grep "^${key}=" "$OLD_ENV" | sed "s/^${key}=//")
  if [ -z "$val" ]; then
    echo "FATAL: missing $key in backup .env" >&2
    exit 1
  fi
  sudo sed -i "s|^${key}=.*|${key}=${val}|" /var/lib/tokenkey/.env.secret
done
rm -f "$OLD_ENV"
sudo systemctl start tokenkey
sudo docker compose ps                                 # 全 healthy
exit

# 6) 外部验证
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' https://api.tokenkey.dev/health
# 用一个已知账号试着登录，确认 PG 数据 / JWT 会话 / TOTP 都正常

# 7) 清理 — 旧 root EBS 因为 DeleteOnTermination=false 留着，确认新栈跑稳后再删
#    (snapshot 也建议留 7 天再删，DLM 自动管)
```

> **失败回退**：步骤 4 之前任何一步出问题，CFN 还没真的 deploy，旧实例 / 旧 EBS 都在，
> 重启 `systemctl start tokenkey` 即可恢复；步骤 4 之后出问题，从步骤 1 的 snapshot
> 创建新 EBS、建一台同规格 EC2、attach、`mount /dev/xvda1 /mnt`、把数据再 `tar` 回来即可。
>
> **简化路径**：如果可以接受 5–10 min 停服 + 重置 admin 密码 + 所有用户重新登录 + 重置
> TOTP，跳过步骤 5 的"复制旧密钥"动作，直接让新栈起空 PG，再用 admin 账号导入业务数据
> 备份（`pg_restore` 之前要 `DROP DATABASE tokenkey; CREATE DATABASE tokenkey;`）。
> 选哪个看历史数据规模 + 用户数。

### 发版 SOP（开发者侧 — 创建 release）

```bash
# 在 main 分支上、VERSION 文件已经是目标版本号（通常通过 PR + squash merge 完成）
bash scripts/release-tag.sh vX.Y.Z
# helper 会校验：HEAD commit message 不含任何 skip-marker / VERSION 文件匹配 tag /
#                tag 不存在 / main 与 origin 同步；全过才创建 annotated tag 并 push。
# release.yml 会在 tag push 后几秒内 fire；监控：
gh run watch $(gh run list --workflow=release.yml --limit 1 --json databaseId -q '.[0].databaseId')
```

> 不要 `git tag vX.Y.Z && git push origin vX.Y.Z` 手敲 —— 跳过 helper 就跳过了 §发版纪律
> 第 1 条的 mechanical enforcement，v1.3.0 / v1.4.0 两次事故都是这个绕过路径造成的。

### 生产升级 SOP（运维侧 — 拉新版本到 prod 栈）

Release workflow 全绿后（`gh run list --workflow=release.yml --limit 1` 看 `success`），
GHCR 已经有 `:X.Y.Z` 多架构镜像。在 prod 实例上：

```bash
TAG=X.Y.Z   # 不带 v 前缀
INSTANCE_ID=$(aws cloudformation describe-stacks --region us-east-1 \
  --stack-name tokenkey-prod-stage0 \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)

aws ssm send-command --region us-east-1 \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --parameters "commands=[
    \"sudo cp -a /var/lib/tokenkey/.env /var/lib/tokenkey/.env.before-${TAG}\",
    \"sudo sed -i 's|sub2api:[0-9.]*|sub2api:${TAG}|' /var/lib/tokenkey/.env\",
    \"cd /var/lib/tokenkey && sudo docker compose --env-file .env pull tokenkey\",
    \"cd /var/lib/tokenkey && sudo docker compose --env-file .env up -d --no-deps tokenkey\",
    \"for i in 1 2 3 4 5 6 7 8 9 10 11 12; do s=\\$(sudo docker inspect tokenkey --format '{{.State.Health.Status}}'); echo \\\"try \\$i: \\$s\\\"; [ \\\"\\$s\\\" = healthy ] && break; sleep 5; done\",
    \"cd /var/lib/tokenkey && sudo docker compose ps\",
    \"sudo docker logs tokenkey --since 2m 2>&1 | tail -20\"
  ]"
```

外部健康验证：

```bash
curl -sS -o /dev/null -w 'HTTP %{http_code} | %{time_total}s\n' https://api.tokenkey.dev/health
# 期望：HTTP 200 / <2s
```

回滚（v1.4.0 起 `.env` 自动有 `.env.before-X.Y.Z` 备份）：

```bash
aws ssm send-command --region us-east-1 \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --parameters "commands=[
    \"sudo cp -a /var/lib/tokenkey/.env.before-${TAG} /var/lib/tokenkey/.env\",
    \"cd /var/lib/tokenkey && sudo docker compose --env-file .env up -d --no-deps tokenkey\"
  ]"
```

> **这条路径不会改 CFN ImageTag 参数**，因此 `aws cloudformation describe-stacks` 显示的
> `ImageTag` 会与实际运行版本漂移。这是 stage-0 模板限制下的**有意 trade-off**：drift 可侦测
> （`aws cloudformation describe-stacks --query Drift`），数据丢失不可逆。CFN 参数视为
> "初始化默认值"，实际版本以 `.env` 内 `TOKENKEY_IMAGE` 为准。

> GoReleaser 在 `bash scripts/release-tag.sh vX.Y.Z` 之后会同时发布 `:X.Y.Z`、`:X.Y`、`:X`、`:latest`
> 多架构（amd64+arm64）镜像。Release workflow **只在 `tags: v*` 触发**，`main` 分支 push 不构建镜像。

## 测试环境（轻量、低资源、与生产隔离 — 可选）

测试环境与生产**完全隔离**（独立 stack / VPC / EBS / EIP / 子域名），`ImageTag=latest` 自动跟随最新 release tag。

### 发版纪律（两条铁律）

1. **VERSION bump commit 整段消息任何位置都不能出现 skip-marker 字面**，并且发版**必须**走
  `bash scripts/release-tag.sh vX.Y.Z` —— 不要手敲 `git tag` + `git push origin vX.Y.Z`。
   背景：Release workflow 由 `tag push` 触发，GitHub 会扫描 tag 指向 commit 的**整段消息**
   （subject + body + 代码块 + 反引号）寻找 `[skip ci]` / `[ci skip]` / `[no ci]` /
   `[skip actions]` / `[actions skip]` 字面。任意位置命中 → release.yml 被静默吞掉 →
   不构建镜像 → prod / test 栈拿不到新版本 → 唯一恢复路径是
   `gh workflow run release.yml -f tag=vX.Y.Z -f simple_release=false`。
   两次踩坑（v1.3.0 / v1.4.0）的共同模式都是：commit body 把 `[skip ci]` 当成示例字符串
   讨论"不要带 [skip ci]"，结果 GitHub 不区分上下文一律识别为 skip。**讨论这个标记字面
   等同于携带它**。helper `scripts/release-tag.sh` 在打 tag 前会 `git log -1 --format=%B`
   做精确 grep 拦截、校验 `backend/cmd/server/VERSION` 与 tag 一致、确认 `main` 与
   `origin/main` 同步，**全过才创建 annotated tag 并 push**。CLAUDE.md §9.2 是该 helper
   的权威说明。
   **唯一允许带 skip-marker 的 commit** 是 release.yml 自己的 `sync-version-file` job
   生成的回写 commit（避免 release → sync → release 死循环），这条 commit 不是人手写的。
2. **不要随手开 `simple_release=true`** — 这个开关只构建 amd64 单架构镜像并覆盖 `:latest` /
  `:X.Y.Z` 等共享 tag，AWS Graviton (t4g/c7g/m7g) 等 ARM 主机会立即在 `exec format error`
   崩溃。生产/测试栈都跑 t4g，**默认必须 `false`**。如果手抖开了，立刻重发同 tag 的
   `simple_release=false` workflow 覆盖回 multi-arch manifest。

初始化测试栈（重用同一 CFN 模板，仅改 stack 名 / 子域名）：

```bash
aws cloudformation deploy \
  --region "${REGION}" \
  --stack-name tokenkey-test-stage0 \
  --template-file deploy/aws/cloudformation/stage0-single-ec2.yaml \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides \
    Environment=test \
    ApiDomain=test-api.tokenkey.dev \
    AcmeEmail=forsurexue@gmail.com \
    AdminEmail=admin@tokenkey.dev \
    GhcrOwner=youxuanxue \
    GhcrPullUser=youxuanxue \
    ImageTag=latest

aws cloudformation describe-stacks --region "${REGION}" \
  --stack-name tokenkey-test-stage0 \
  --query 'Stacks[0].Outputs[?OutputKey==`PublicIP`].OutputValue' --output text
# 去 Porkbun 加 A 记录 test-api.tokenkey.dev → <EIP>
```

测试栈推新版镜像 — **走与生产相同的 SSM SOP**（见上方 §生产升级 SOP），仅替换 `INSTANCE_ID`：

```bash
INSTANCE_ID=$(aws cloudformation describe-stacks --region us-east-1 \
  --stack-name tokenkey-test-stage0 \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)
# 之后 aws ssm send-command 与 prod 段完全一致；测试栈 `.env` 默认 `TOKENKEY_IMAGE=...:latest`，
# pull 即拿到最新；如要 pin 到具体版本，把上方 SOP 的 sed 换成你想要的 tag 即可。
```

测试环境用完销毁（彻底清零，不留 EIP/EBS 计费）：

```bash
aws cloudformation delete-stack --region us-east-1 --stack-name tokenkey-test-stage0
# 等 stack 真的删干净
aws cloudformation wait stack-delete-complete --region us-east-1 --stack-name tokenkey-test-stage0
```

## 进入实例排错（不需要 SSH 私钥）

```bash
INSTANCE_ID=$(aws cloudformation describe-stacks --region us-east-1 \
  --stack-name tokenkey-prod-stage0 \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)
aws ssm start-session --region us-east-1 --target "${INSTANCE_ID}"

# 实例内常用：
sudo tail -f /var/log/tokenkey-bootstrap.log              # 首次启动看引导
sudo systemctl status tokenkey
sudo docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps
sudo journalctl -u tokenkey -n 200 --no-pager
sudo systemctl list-timers tokenkey-pgdump.timer
ls -lh /var/lib/tokenkey/pgdump/ 2>/dev/null || echo '(no dumps yet — first dump runs ~1h after boot)'
sudo cat /var/lib/tokenkey/.env                           # 含明文密码，慎查
```

## 旧栈清理（如果之前 deploy 过 Phase 1）

仓库里**不再保留** Phase 1（NAT + 多子网）等 Stage 3 目标态模板。如你有旧栈在跑，先删掉，否则 NAT/EIP 会持续计费：

```bash
aws cloudformation delete-stack --region us-east-1 --stack-name <旧栈名>
```

## CI 通过 OIDC 调度 SSM（Error Clustering Daily 等）

GitHub Actions 不再用长期 AWS 凭证，**OIDC 临时换 STS** → `ssm:SendCommand` → EC2 内
`docker run --network=tokenkey_default` 临时容器跑 `/usr/local/bin/error_clustering` →
连 `tokenkey-postgres` 走 docker 内网 → reports base64 通过 SSM stdout 回传 → workflow
解码出 `report.{json,md}`。**不暴露 PG 端口、无长期密钥、无 SSH key**。

### 一次性 setup（admin 操作）

1. **创建 GitHub OIDC provider**（每个 AWS 账户一次，跨仓库共享）：

   ```bash
   aws iam create-open-id-connect-provider \
     --url https://token.actions.githubusercontent.com \
     --client-id-list sts.amazonaws.com \
     --thumbprint-list 6938fd4d98bab03faadb97b34396831e3780aea1
   ```

   如已存在（其它项目创建过）跳过此步即可。

2. **部署 OIDC role 子栈**（IaC 在 `deploy/aws/cloudformation/cicd-oidc.yaml`）：

   ```bash
   aws cloudformation deploy \
     --region us-east-1 \
     --stack-name tokenkey-cicd-oidc \
     --template-file deploy/aws/cloudformation/cicd-oidc.yaml \
     --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides \
       GitHubRepo=youxuanxue/sub2api \
       AllowedSubjects="repo:youxuanxue/sub2api:ref:refs/heads/main" \
       TargetInstanceId=$(aws cloudformation describe-stacks --region us-east-1 \
         --stack-name tokenkey-prod-stage0 \
         --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text) \
       CreateOIDCProvider=false   # 已在 step 1 手动创建
   ```

   首次 PR 自验时可临时把 `AllowedSubjects` 加上当前分支 ref，验完后**用同样的命令** redeploy 把它去掉即可（`AllowedSubjects` 是普通参数，可以反复改；只有 `CreateOIDCProvider` 不能反复 flip，见下方坑位）。

   > **坑位（2026-04-22 真实事故）**：`CreateOIDCProvider` 参数**只在第一次 deploy 时选定一次**，之后每次 redeploy 都必须保持同值。
   > - 如果首次用 `true`（让本栈创建 provider），之后所有 redeploy 都必须保持 `true`，否则 CFN 会把 provider 当成「资源被移除」直接删除（已加 `DeletionPolicy: Retain` 兜底，但仍会脱离栈管理，下一次再 flip 回 `true` 会因为 provider 已存在而 `EntityAlreadyExists` 失败）。
   > - 如果首次用 `false`（手动 step 1 已创建 provider），之后保持 `false` 即可。
   > 简言之：**首次定一次，后续不改这个参数**。要改 `AllowedSubjects` / `TargetInstanceId` / `GitHubRepo` 都没问题。

3. **GitHub repo variables（Settings → Secrets and variables → Actions → Variables）**：

   | 名称 | 值 | 说明 |
   |---|---|---|
   | `AWS_OIDC_ROLE_ARN` | `arn:aws:iam::682751977094:role/tokenkey-gha-us-east-1-error-clustering` | step 2 输出的 `RoleArn` |
   | `AWS_REGION` | `us-east-1` | 可选，默认即此值 |
   | `PROD_STACK_NAME` | `tokenkey-prod-stage0` | 可选，默认即此值 |

   `AWS_OIDC_ROLE_ARN` 留空时 workflow 会走 graceful skip 路径（`exit 0`），与
   `qa_records` 缺失场景一致。

4. **首次部署 binary 到 EC2**：

   ```bash
   bash scripts/deploy-error-clustering-binary.sh   # 用本机 AWS 凭证 + SSM
   ```

   binary 落到 `/usr/local/bin/error_clustering`（arm64 静态、CGO=0、~2MB）。
   每次 `scripts/error_clustering/main.go` 改动后，操作员需重跑此脚本把新版本推到
   prod。**TODO（后续 PR）**：将 binary 烤进 release Docker 镜像，让镜像升级即更新；
   届时 binary 来源切到 `docker exec tokenkey ...`，本节脚本只在引导期使用。

### Workflow 行为

- 触发：每天 02:00 UTC cron + 手动 `workflow_dispatch`（可指定 `since_hours`）
- Graceful degradation：缺 `AWS_OIDC_ROLE_ARN` / 缺 `qa_records` → 都返回 `summary:"skip:..."` 的空 report 并 exit 0
- 验证手动跑：`gh workflow run error-clustering-daily.yml -f since_hours=24 -R youxuanxue/sub2api`

### Cloud Agent 拉取 error-clustering 报告

让 **Cursor Cloud Agent** 也能查 prod 错误聚类时，**不要**复制 AWS 凭证到 Agent
secrets——Cloud Agent 既无 AWS key 也无 GHA OIDC token，复制凭证会扩大长期密钥的攻击面，
与 §"无长期 AWS 凭证"约束冲突。改用：让 Agent 通过 `gh workflow run` 触发**已有的**
`error-clustering-daily.yml`，再下载 artifact。AWS OIDC → SSM → EC2 链路完全不动。

**配置（Cursor Dashboard → Cloud Agents → Secrets）**：

| 名称 | 值 | 说明 |
|---|---|---|
| `GH_TOKEN` | GitHub PAT（fine-grained） | 仅授权 `youxuanxue/sub2api` 仓库，scopes：`actions:write`（dispatch）、`actions:read`（poll/download）、`contents:read`（`gh run download` 需要）。**不要**勾其他权限——这把 token 永远不接触 AWS。 |

**用法**（在 Cloud Agent 会话里）：

```bash
bash scripts/fetch-prod-error-clusters.sh                   # 默认 24h，输出到 ./.error-clusters/
SINCE_HOURS=72 bash scripts/fetch-prod-error-clusters.sh    # 自定义窗口
bash scripts/fetch-prod-error-clusters.sh --check           # 只校验 env + 工具，不 dispatch
```

脚本会：dispatch workflow → 轮询新 run id（默认 10 min 超时）→ `gh run watch` 等待完成
→ `gh run download` 取 artifact → 打印 `report.json` 的 `summary` 字段。`gh` CLI 由
`.cursor/cloud-agent-install.sh` 在会话引导时 best-effort 安装；缺失时脚本会报错并指出
安装方式。

**会话引导自检**：`.cursor/cloud-agent-install.sh` 在每次 Cloud Agent 启动时会检查
`GH_TOKEN`：未设置 → 静默跳过；已设置 → 同时跑 `fetch-prod-error-clusters.sh --check`
+ `fetch-prod-logs.sh --check` 验证 `gh` + token + 工具链 + 各自的参数校验矩阵。失败时
打印 `[cloud-agent] WARNING: prod-data fetch self-test FAILED ...` 但**不让 install 失败**
（避免一个过期 token 把整个 agent 会话堵死）。看到 WARNING 即知该轮换 token 或修 scope。

### Cloud Agent 按需拉取 prod 容器日志

`fetch-prod-error-clusters.sh` 拿的是聚合趋势（24h 内错误聚类）；要查**具体某个事故**
（"刚才 5 分钟内 user X 报的 500 traceback 是啥？"），用 `fetch-prod-logs.sh` 触发
`prod-log-dump.yml` workflow 拉**原始** `docker logs`。

两者**复用同一个 `GH_TOKEN` + 同一个 OIDC role + 同一个 SSM 链路**——`prod-log-dump.yml`
没有要求任何新 IAM 权限，因为 `cicd-oidc.yaml` 里的 `ssm:SendCommand` +
`AWS-RunShellScript` 已经覆盖。

**用法**：

```bash
# 默认：tokenkey 容器最近 10 分钟、最多 1000 行、不过滤
bash scripts/fetch-prod-logs.sh

# 查最近 30 分钟的 5xx / panic / deadline
SINCE=30m GREP_PATTERN='5[0-9]{2}|panic|deadline' bash scripts/fetch-prod-logs.sh

# 查 PostgreSQL 慢查询
CONTAINER=tokenkey-postgres SINCE=2h GREP_PATTERN='duration: [0-9]{4,}' \
  bash scripts/fetch-prod-logs.sh

# 查 Caddy access log 中某个 IP
CONTAINER=tokenkey-caddy SINCE=1h GREP_PATTERN='1\.2\.3\.4' \
  bash scripts/fetch-prod-logs.sh

# 仅校验环境，不真正 dispatch
bash scripts/fetch-prod-logs.sh --check
```

**输入参数（均为可选 env）**：

| 变量 | 默认 | 约束 |
|---|---|---|
| `CONTAINER` | `tokenkey` | enum：`tokenkey` / `tokenkey-postgres` / `tokenkey-caddy` / `tokenkey-redis` |
| `SINCE` | `10m` | 正则 `^[0-9]+[smhd]$`（直接传给 `docker logs --since`）|
| `GREP_PATTERN` | `""`（不过滤） | ERE，最长 512 字符；引号 / 反斜杠在 workflow 入口被剥除以防 shell 注入 |
| `TAIL_LINES` | `1000` | 正整数，硬上限 10000（防止 artifact 过大）|
| `OUT_DIR` | `./.prod-logs` | artifact 落盘目录 |

**安全 / 风险增量**：

- 没有新增 IAM 权限。攻击者拿到 `GH_TOKEN` 能做的事和已有 workflow 同：触发预定义流程，**无法注入任意 shell 命令**：
  - `container` 是 GHA `type: choice` enum
  - `since` / `tail_lines` 在 script 和 workflow 两处都做正则/数值校验
  - `grep_pattern` 在 runner 上 base64 编码 → 经 SSM 送到 EC2 → 解码到 `/tmp/prod-log-dump/pattern` → `grep -E -f` 直接读文件，**全程不经过 shell 解析**，因此正则可以包含任意字节（`\d` / `\(` / `'` / `"` / `$` 等），既不需剥离也无注入面
- workflow 不接受任意 `docker exec` 或任意命令字符串，只暴露"读容器日志 + 可选 grep"这一种行为。
- workflow 在 EC2 端用 `trap ... EXIT` 在 SSM 命令结束时清理 `/tmp/prod-log-dump`，敏感日志内容不在主机间投递周期残留。
- 日志内容可能包含敏感信息（user id、prompt 片段、PG 查询参数）——GHA artifact 默认保留 90 天，必要时手动 `gh run delete` 清理。

**已知限制：SSM 24KB stdout 上限**

AWS SSM `GetCommandInvocation` 返回的 `StandardOutputContent` 硬上限 24000 字符。即使 workflow 已经 gzip + base64 压缩（典型日志压缩比 5-10x），如果实际匹配的行数过多仍会触顶。workflow 检测到末尾 marker 缺失时**显式失败**，提示"Tighten GREP_PATTERN, lower TAIL_LINES, or shorten SINCE"，不会返回静默截断的乱码。

要彻底解开这个上限需要：给 OIDC role 加 `s3:PutObject`、新建/复用一个 ops bucket、改 workflow 用 `--output-s3-bucket-name` + `aws s3 cp` 取全量。这是 CFN/IAM 改动，不在本 PR 范围；当前的 grep + tail 组合对绝大多数事故定位场景已够用。

**信任面边界**：

- Cloud Agent 拿到的 `GH_TOKEN` 只能 dispatch + 读 artifact，不能改源码/合并 PR/改 Actions secrets/改 IAM。
- 真正的 AWS 凭证仍由 GHA runner 通过 OIDC 临时换取，1h 自动过期，sub claim 锁 repo+branch。
- Cloud Agent 异常或 token 泄露 → 攻击者最多能反复触发同一个 read-only workflow，**不能**绕过 OIDC 信任策略去拿 AWS 凭证。

### 端到端原理

```
GHA runner ── OIDC token ──▶ AWS STS (sts:AssumeRoleWithWebIdentity)
              │
              ▼ (1h 临时凭证, sub claim 锁定 repo+branch)
GHA ── ssm:SendCommand ──▶ EC2 (i-04a8afd18c997b8ac)
                             │
                             ▼
                      docker run --rm --network tokenkey_default \
                        -v /usr/local/bin/error_clustering:/binary:ro \
                        -e PG_DSN=postgres://tokenkey:***@tokenkey-postgres:5432/...
                        alpine:3.21 /binary --since-hours 24 ...
                             │
                             ▼  base64 stdout
GHA ◀── ssm:GetCommandInvocation ── EC2
   │
   ▼ awk + base64 -d
report.{json,md} → upload-artifact → 决策 issue/draft-PR
```

IAM 信任面：

- OIDC provider 仅信任 `token.actions.githubusercontent.com:aud=sts.amazonaws.com`
- Role trust 锁定 `sub` claim 到指定 repo + branch（默认仅 `main`）
- Role 权限只允许 `ssm:SendCommand` 到 prod EC2 实例 + `AWS-RunShellScript` document
- 不能 SSH、不能 RunInstances、不能改 IAM、不能读 secrets

## 详细信息

- **完整部署步骤、监控、备份、恢复演练** → 主文档 §3.5–3.8
- **实例规格选型 / 月度成本** → 主文档 §3.2、§3.3
- **应用更新 / 滚动 / 回滚** → 主文档 §3.6
- **Stage 1/2/3 升级触发条件** → 主文档 §二、§3.9
- **CFN 全部 18 个参数详表** → 主文档 §3.5「全部参数总表」

