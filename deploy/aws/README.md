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

| Stack | `ImageTag` 来源 | `ApiDomain` | 升级方式 |
|---|---|---|---|
| `tokenkey-prod-stage0` | `.env` 内的 `TOKENKEY_IMAGE`（CFN 参数仅用于初始化） | `api.tokenkey.dev` | **唯一安全路径：SSM `docker compose pull && up -d tokenkey`**（见下方 §生产升级 SOP）。**不要**用 `aws cloudformation deploy` 改 `ImageTag` —— 会触发实例 replace，root EBS 上的 PG / Redis / Caddy / pgdumps 全部变孤儿，从空 PG 起来。 |
| `tokenkey-test-stage0`（如存在） | `.env` 同上，初始化用 `latest` 跟随 | `test-api.tokenkey.dev` | 同上 SSM 路径；`latest` 让镜像自动是最新 release，但仍要 SSM 触发 `pull && up -d` 才会真正切换。 |

> **stage-0 模板限制**：`stage0-single-ec2.yaml` 的 `AWS::EC2::Instance.UserData` 把 `ImageTag`
> substitute 到 `IMAGE_TAG='${ImageTag}'`，CFN 视 UserData 为 immutable —— 任何改 `ImageTag`
> 的 deploy 都会标记 `Replacement: True`。同时模板没有独立的 `AWS::EC2::Volume`，所有持久化
> 数据都在 EC2 root EBS 上（`DeleteOnTermination: false` 保住旧 EBS 但新实例挂的是新 EBS）。
> 这两个事实合起来 = 改 `ImageTag` 走 CFN deploy 等于**销毁所有用户/配额/key 数据**。
>
> 长期方案是把 PG/Redis/Caddy 数据 volume 从 root EBS 拆到独立 `AWS::EC2::Volume`（带
> `DeletionPolicy: Retain` + `UpdateReplacePolicy: Retain`），CFN deploy 路径才能恢复安全
> 语义。在此之前，**stage-0 prod 升级一律走 SSM**，CFN deploy 仅用于初始化 stack。

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

## 详细信息

- **完整部署步骤、监控、备份、恢复演练** → 主文档 §3.5–3.8
- **实例规格选型 / 月度成本** → 主文档 §3.2、§3.3
- **应用更新 / 滚动 / 回滚** → 主文档 §3.6
- **Stage 1/2/3 升级触发条件** → 主文档 §二、§3.9
- **CFN 全部 18 个参数详表** → 主文档 §3.5「全部参数总表」

