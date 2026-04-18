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
#    注意 ImageTag：生产固定到具体版本（如 1.1.0）以可复现；测试用 latest 自动跟最新。
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
    ImageTag=1.1.0

# 3) 取 EIP 去 Porkbun 加 A 记录
aws cloudformation describe-stacks --region "${REGION}" \
  --stack-name tokenkey-prod-stage0 \
  --query 'Stacks[0].Outputs[?OutputKey==`PublicIP`].OutputValue' --output text

# 4) DNS 生效后（1–10 min），验证
curl -sS -o /dev/null -w '%{http_code}\n' "https://${DOMAIN}/health"
# 期望 200；首次若 503 是 LE 还在签证书，等 1–2 min
```

## 测试环境（轻量、低资源、与生产隔离）

测试环境与生产**完全隔离**（独立 stack / VPC / EBS / EIP / 子域名），通过不同的 `ImageTag` 决定追踪策略：

| Stack | `ImageTag` 策略 | `ApiDomain` | 升级方式 |
|---|---|---|---|
| `tokenkey-prod-stage0` | 固定 `1.2.0`（每次发版手动 bump） | `api.tokenkey.dev` | 改 CFN 参数 + `aws cloudformation deploy` |
| `tokenkey-test-stage0` | `latest`（自动跟随最新 tag） | `test-api.tokenkey.dev` | `git tag vX.Y.Z` 后在实例 `docker compose pull && up -d` |

> GoReleaser 在 `git tag vX.Y.Z && git push` 之后会同时发布 `:X.Y.Z`、`:X.Y`、`:X`、`:latest`。Release workflow **只在 `tags: v*` 触发**，`main` 分支 push 不构建镜像。

### 发版纪律（两条铁律）

1. **VERSION bump commit 不要带 `[skip ci]`** — Release workflow 由 `tag push` 触发，但
   GitHub 会读 tag 指向的 **commit message** 来决定要不要 skip。如果你 `chore: bump VERSION
   to X.Y.Z [skip ci]` 然后 `git tag vX.Y.Z`，tag push 会被静默吞掉，必须人工
   `gh workflow run release.yml -f tag=vX.Y.Z` 补救。**只有 release.yml 里 sync-version-file
   job 自动生成的回写 commit** 才需要 `[skip ci]`（防 release → sync → release 死循环）。

2. **不要随手开 `simple_release=true`** — 这个开关只构建 amd64 单架构镜像并覆盖 `:latest` /
   `:X.Y.Z` 等共享 tag，AWS Graviton (t4g/c7g/m7g) 等 ARM 主机会立即在 `exec format error`
   崩溃。生产/测试栈都跑 t4g，**默认必须 `false`**。如果手抖开了，立刻重发同 tag 的
   `simple_release=false` workflow 覆盖回 multi-arch manifest。

部署测试环境（重用同一 CFN 模板，仅改 stack 名 / 子域名 / ImageTag）：

```bash
# 复用同一 GHCR PAT（同 region 已存在 /tokenkey/ghcr/pat 即可）
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

# 取测试 EIP，去 Porkbun 加 A 记录 test-api.tokenkey.dev → <EIP>
aws cloudformation describe-stacks --region "${REGION}" \
  --stack-name tokenkey-test-stage0 \
  --query 'Stacks[0].Outputs[?OutputKey==`PublicIP`].OutputValue' --output text
```

测试栈推送新版镜像：`git tag` 触发 Release → workflow 完成后 SSM 进实例：

```bash
INSTANCE_ID=$(aws cloudformation describe-stacks --region us-east-1 \
  --stack-name tokenkey-test-stage0 \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)
aws ssm send-command --region us-east-1 \
  --document-name AWS-RunShellScript \
  --instance-ids "${INSTANCE_ID}" \
  --parameters 'commands=["cd /var/lib/tokenkey && docker compose pull && docker compose up -d"]'
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

