# tokenkey AWS 美国部署方案

> 产品名 **tokenkey**；代码仓库与上游 Docker 镜像名仍为 `sub2api`（fork 关系，按 CLAUDE.md 保留）。
> 凡涉及「我们这套部署的产品身份」（CFN stack 名、容器名、systemd 单元、`/var/lib/tokenkey/` 路径、CloudWatch 命名空间、PG 用户/库默认值），统一用 `tokenkey`；
> 应用环境变量名（`DATABASE_*`/`REDIS_*`/`JWT_*`/`SERVER_*`）与 GHCR 镜像名 `sub2api`（`ghcr.io/<owner>/sub2api:<tag>`）是代码侧约定，**保持不变**。Stage 0 默认拉本仓库 fork 自己发布的 GHCR 私有镜像（**不再**使用上游 `weishaw/sub2api:latest`，那不会包含 TokenKey 的 `new-api` 集成等差异化代码）。

## 一、目标与约束

- 美国 AWS 作为唯一 OpenAI 主网关
- 对外只暴露一个域名：`api.tokenkey.dev`
- 区域：`us-east-1`（OpenAI OAuth 出口最自然、AWS 资源齐全、Team 账号场景友好）
- **当前业务规模：≈ 100 个同时活跃用户、对 OpenAI 月出站 < 50 GB**
- DNS 切换 / 跨区域容灾留作 Stage 3 选项，不在 Stage 0/1/2 内

### 1.1 OPC 运维哲学

面向 OPC（one-person company），方案遵循四条原则：

1. **托管优先** — 能交给 AWS 托管的，不自己维护
2. **组件最少** — 只保留直接支撑可用性的组件
3. **回滚简单** — 每次变更都能一个人快速判断、快速回退
4. **自动化优先于流程化** — 减少人工步骤，而不是堆更多审批和平台

> **关键判断：** 100 用户 / < 50 GB 出站对 AWS 而言是非常小的负载，原版「Multi-AZ ALB + Fargate + RDS Multi-AZ + ElastiCache Multi-AZ + NAT GW」每月 \$250+ **不是为这个负载付的钱**，而是为「Multi-AZ HA」付的钱。本方案改为 **「Stage 0 起步，按可观测触发条件升级」**，把这部分钱推迟到业务真正需要时再花。

### 1.2 文档使用方式

- **Stage 0** 是默认运行态，**所有可执行细节集中在第三节**，配置文件在 `deploy/aws/stage0/`、CFN 模板在 `deploy/aws/cloudformation/stage0-single-ec2.yaml`
- **Stage 1/2/3** 是「未来选项」，第四/五/六节只给**触发条件 + 变化点**，**不预先沉淀配置**，避免维护两套未启用方案
- 业务流程、代码调用链、`/v1/messages` 路径细节、OpenAI 分组 / 账号 / 模型映射配置见仓库根目录 `CLAUDE.md`（「Current Gateway Flow」）与 `docs/approved/newapi-as-fifth-platform.md`，本文档不重复

---

## 二、阶段总览：成本阶梯 + 升级触发

| 阶段 | 形态 | 月度估算（us-east-1, On-Demand） | 主要牺牲 |
|---|---|---|---|
| **Stage 0** | 1×EC2 t4g.small + EBS + Caddy + docker-compose（app+PG+Redis） | **≈ \$25–40** | 单 AZ；补丁/重启 5–10 min 停机；PITR 退化为「最近一次 pg_dump」 |
| Stage 1 | Stage 0 拆出 RDS PostgreSQL Single-AZ db.t4g.micro | ≈ \$45–80 | 仍单 AZ；DB 主备切换/小版本升级窗口 5–15 min 停机 |
| Stage 2 | + 第 2 台 EC2、ALB、ElastiCache cache.t4g.micro 单节点 | ≈ \$110–180 | 仍 RDS Single-AZ；AZ 级故障仍可能影响业务 |
| Stage 3 | ECS Fargate + RDS Multi-AZ + ElastiCache Multi-AZ + NAT GW + DR runbook | ≈ \$250–450+ | 钱 |

### 升级触发（满足任一即升级，不跳级）

| 跃迁 | 触发条件 |
|---|---|
| **0 → 1** | (a) PostgreSQL 数据 > 5 GB；或 (b) 需要 PITR / 跨日恢复某一时刻状态；或 (c) 单机重启的 5–10 min 窗口已经引发用户投诉；或 (d) 出现 ≥ 1 次「容器内 PostgreSQL 升级失败 / 数据修复」事故 |
| **1 → 2** | (a) 需要业务期内零停机发布；或 (b) 单台 EC2 5 min CPU P95 > 60% 持续 1 周；或 (c) 内存利用率 > 70% 且 OOM 已发生；或 (d) 单机硬件/AZ 抖动直接导致用户停服；或 (e) 团队不再只有 1 人，需要灰度/多副本验证 |
| **2 → 3** | (a) 对外承诺 SLA ≥ 99.9%；或 (b) 过去 90 天内单 AZ 故障实际造成 ≥ 30 min 业务影响 ≥ 1 次；或 (c) RDS Single-AZ 主备切换的 5–15 min 停机不再可接受；或 (d) 月账单已经 \$300+ 不再是问题 |

**回退路径同样明确：** Stage 2→1 = 关掉 ALB + 第二台 EC2；Stage 1→0 = `pg_dump` 倒回容器 PG；Stage 3→2 = 回到 EC2 + ALB。

---

## 三、Stage 0：单机起步（**默认运行态**）

### 3.1 架构

```mermaid
flowchart TD
    USER[Claude Code CLI / cc switch] --> DNS[Porkbun DNS: api.tokenkey.dev A 记录]
    DNS --> EIP[AWS Elastic IP]
    EIP --> EC2

    subgraph EC2["EC2 t4g.small (公有子网, 单 AZ)"]
        CADDY[Caddy: 443/80<br/>Let's Encrypt 自动签证书]
        APP[tokenkey:8080]
        PG[(postgres:18-alpine)]
        RDS[(redis:8-alpine)]
        CADDY --> APP
        APP --> PG
        APP --> RDS
    end

    APP -- HTTPS --> OAI[auth.openai.com / chatgpt.com]
    EC2 -. 每天/每小时 .-> EBS_SNAP[(EBS Snapshot via DLM)]
    PG -. 每小时 .-> PGDUMP[(/var/lib/tokenkey/pgdump)]
```

| 项 | 值 |
|---|---|
| 实例 | EC2 **t4g.small**（ARM Graviton, 2 vCPU / 2 GiB；可降本至 `t4g.micro`，详见 3.3） |
| 存储 | 30 GiB **gp3** EBS（系统 + 数据合一，路径集中在 `/var/lib/tokenkey/`） |
| 入口 | EIP（绑定 EC2 时不计费）+ Caddy 443/80 |
| 出口 | 直连 OpenAI（**无 NAT GW**；EC2 自带公网 IP） |
| TLS | Let's Encrypt（Caddy ACME HTTP-01，自动续签） |
| 数据栈 | 同机 docker-compose：tokenkey 应用 / PostgreSQL 18 / Redis 8 / Caddy 2 |
| 备份 | 每日 EBS 整盘快照（DLM）+ 每小时 `pg_dump`（systemd timer） |
| 登录 | **AWS SSM Session Manager**（不开 SSH 密钥，免维护跳板机） |
| 日志 | 容器 stdout → `journald`；CloudWatch Agent 采集 cloud-init 日志与基础内存/磁盘指标 |

### 3.2 月度成本估算（us-east-1, On-Demand）

| 项 | 价格基准 | 月度 |
|---|---|---|
| EC2 t4g.small（730 h） | ~\$0.0168/h | **\$12.3** |
| 30 GiB gp3 root | \$0.08/GiB-month | **\$2.4** |
| EIP（attached） | 免费 | \$0 |
| EBS 快照（每日 7 份, 30 GiB 卷, 增量） | \$0.05/GiB-month × 增量 | **\$0.3–1** |
| 出站流量（< 50 GB） | 100 GB 免费额度内 | **\$0** |
| CloudWatch Logs（基础） | 摄取 \$0.50/GB | **\$1–3** |
| Route53 / DNS | 用 Porkbun | \$0 |
| **合计** | | **≈ \$25–40 / 月** |

> 切换到「每小时快照」会额外加 ~\$1–4/月（增量快照存储）。详见 3.6。

### 3.3 实例规格选择（更便宜的选项）

| 规格 | vCPU / RAM | On-Demand（us-east-1） | 评价 |
|---|---|---|---|
| `t4g.nano` | 2 / **0.5 GiB** | ~\$3/月 | **不可行**：PG + Redis + Caddy + Go 应用四个进程同住，OS + Docker 已吃掉 ~300 MB，剩余空间会立刻 OOM |
| `t4g.micro` | 2 / **1 GiB** | ~\$6/月 | **极限可行**：必须做 swap、把 PG `shared_buffers` 调到 ~64 MB、Redis `maxmemory` 限到 ~100 MB；100 用户瞬时高峰仍有 OOM 风险，**只建议**作为「演示/灰度环境」而非生产 |
| `t4g.small`（默认） | 2 / **2 GiB** | ~\$12/月 | **舒适**：典型占用 ~1.2 GiB，留 ~700 MB 给突发；100 活跃用户绰绰有余 |
| `t4g.medium` | 2 / **4 GiB** | ~\$24/月 | 早期就过度配，留作 Stage 1 之后的备选 |

**省钱的另一条路（不动规格）：** Compute Savings Plan，按对小型 ARM 实例的常见折扣：

| 期限 | 大致折扣 | `t4g.small` 实际单价 |
|---|---|---|
| 1 年（无前付） | ~30% | ~\$8/月 |
| 1 年（部分前付） | ~35% | ~\$7.5/月 |
| 3 年（无前付） | ~50% | ~\$6/月 |

**建议路径：** 先用 `t4g.small` On-Demand 跑 1–2 个月观察实际负载与升级触发情况；如果稳定且短期内没有升 Stage 1+ 的迹象，再买 1 年 Savings Plan，把 EC2 这块从 \$12 砍到 \$8 左右。**不建议**为节省 \$6/月去用 `t4g.micro`——OOM 凌晨 3 点叫醒你一次的成本 ≫ 半年累计省下的钱。

### 3.4 前置准备

- [ ] AWS 账号 + IAM 管理用户 + MFA
- [ ] AWS CLI v2 + `aws configure` → `us-east-1`
- [ ] 域名 `tokenkey.dev` 在 Porkbun 可控
- [ ] **本仓库可保持 GitHub 私仓**（CFN 模板把 `docker-compose.yml` 与 `Caddyfile` 以 gzip+base64 内嵌进 UserData，EC2 引导时不再外网拉源码）
- [ ] **GHCR 镜像已发布**：`.github/workflows/release.yml` 推 tag 时会推 `ghcr.io/<owner>/sub2api:<tag>`，建议平时使用 `sha-<gitsha>` 形式的不可变 tag
- [ ] **GHCR 拉取 PAT 已写入 SSM SecureString**（一次性手工准备，下面 Step 0）
- [ ] OpenAI Team 账号可正常登录

> **不需要：** ACM 证书（Caddy + LE 替代）、ALB、RDS、ElastiCache、Fargate、NAT GW、Secrets Manager（机密由 Cloud-Init 在实例本地生成并写入 `/var/lib/tokenkey/.env`）、把仓库推公开（CFN 自包含）。

### 3.5 实施步骤

#### Step 0：把 GHCR PAT 写进 SSM SecureString（一次性）

在 GitHub <https://github.com/settings/tokens> 创建 **Classic PAT**（GHCR 至今对 fine-grained PAT 支持有限，官方推荐用 classic），scope 只勾 `read:packages` 一项（最小权限），expiration 设 90 天，复制生成的 token，然后：

```bash
REGION=us-east-1
aws ssm put-parameter \
  --region "${REGION}" \
  --name /tokenkey/ghcr/pat \
  --type SecureString \
  --value 'ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' \
  --description 'GHCR read:packages PAT for tokenkey EC2 stage0' \
  --tier Standard
```

> 这步是**部署生命周期独立**的：一旦写入，CFN 栈每次重建/更新都直接读，PAT 不进 git、不进 CFN 模板、不在 EC2 console 明文。轮换时只 `put-parameter --overwrite` 即可。

#### Step 1：创建 CloudFormation 栈

最小命令（4 个必填参数，其余 14 个走默认值）：

```bash
REGION=us-east-1
GHCR_OWNER=<你的GitHub用户名>          # 例如 youxuanxue；同时作为 GhcrPullUser
DOMAIN=api.tokenkey.dev                # 必须是你能在 DNS 上控制的域名
ACME_EMAIL=ops@tokenkey.dev            # LE 证书过期通知收件人

aws cloudformation deploy \
  --region "${REGION}" \
  --stack-name tokenkey-prod-stage0 \
  --template-file deploy/aws/cloudformation/stage0-single-ec2.yaml \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides \
    ApiDomain="${DOMAIN}" \
    AcmeEmail="${ACME_EMAIL}" \
    GhcrOwner="${GHCR_OWNER}" \
    GhcrPullUser="${GHCR_OWNER}"
```

> **CFN 模板自包含**：`docker-compose.yml` 与 `Caddyfile` 已 gzip+base64 内嵌在 UserData。
> 编辑这两个文件后**必须** `bash deploy/aws/stage0/build-cfn.sh` 重新刷新 base64 段，
> 否则上线的会是旧版。CI 上加 `bash deploy/aws/stage0/build-cfn.sh --check` 兜底。

##### 全部参数总表（18 个）

| 类别 | 参数 | 默认 | 必填 | 何时改 |
|---|---|---|---|---|
| **必填** | `ApiDomain` | — | ✅ | 你的对外域名 |
| | `AcmeEmail` | — | ✅ | LE 证书账户邮箱 |
| | `GhcrOwner` | — | ✅ | GHCR 镜像所属 GitHub 用户/组织 |
| | `GhcrPullUser` | — | ✅ | `docker login ghcr.io` 用的用户名（通常 = GhcrOwner） |
| **可选** | `AdminEmail` | `''` → `admin@<ApiDomain>` | | 管理员邮箱与 ApiDomain 域不同时 |
| | `ImageTag` | `latest` | | **生产**固定到具体版本（如 `1.1.0`）实现可复现部署；**测试**保持 `latest` 自动跟最新 release。Release workflow 仅在 `tags: v*` 触发，`main` 不构建镜像，故 `:main` 不存在。 |
| | `SnapshotSchedule` | `daily` | | 改 `hourly` 把整机 RPO 从 24h 压到 1h（每月加 \$1–4），见 §3.7 |
| | `InstanceType` | `t4g.small` | | 改 `t4g.medium`（\$24/月）/ `t4g.large` 应对扩容；见 §3.3 |
| | `RootVolumeSizeGiB` | `30` | | PG 数据 + pg_dump 接近 75% 时扩 |
| | `Timezone` | `UTC` | | 一般不动 |
| | `AdminCidr` | `0.0.0.0/0` | | 改 `127.0.0.1/32` 彻底关 SSH 22 端口（推荐用 SSM 替代） |
| | `GhcrPatSsmName` | `/tokenkey/ghcr/pat` | | 多环境共用栈时改路径，例如 `/tokenkey/ghcr/pat-prod` |
| **基本不改** | `GhcrImageName` | `sub2api` | | fork 不重命名 image，保持 |
| | `ProjectName` | `tokenkey` | | 全部 CFN 资源命名前缀，改了会引起栈级 rename |
| | `Environment` | `prod` | | 与 ProjectName 一起拼资源名（`tokenkey-prod-vpc` 等） |
| | `VpcCidr` | `10.0.0.0/16` | | 与已有 VPC peering 冲突时改 |
| | `PublicSubnetCidr` | `10.0.1.0/24` | | 同上 |
| | `AmazonLinux2023Arm64Ami` | SSM Public Parameter | | 走默认即可（永远是当区最新 AL2023 ARM64） |

栈创建大约 3–5 分钟。完成后取 EIP：

```bash
aws cloudformation describe-stacks --region "${REGION}" --stack-name "${STACK}" \
  --query 'Stacks[0].Outputs' --output table
```

#### Step 2：在 Porkbun 设置 DNS

`api.tokenkey.dev` 加 **A 记录** → CFN 输出的 `PublicIP`，TTL 60–300s。

#### Step 3：等 Caddy 自动签证书（约 1–3 分钟）

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://api.tokenkey.dev/health
# 第一次可能 503（LE 还在签），等 1–2 分钟，正常应返回 200
```

#### Step 4：进入实例核对（不需要 SSH 私钥）

```bash
INSTANCE_ID=$(aws cloudformation describe-stacks --region "${REGION}" --stack-name "${STACK}" \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' --output text)
aws ssm start-session --region "${REGION}" --target "${INSTANCE_ID}"

# 实例内：
sudo systemctl status tokenkey
sudo docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps
sudo cat /var/lib/tokenkey/.env      # 自动生成的 POSTGRES_PASSWORD / JWT_SECRET / TOTP_ENCRYPTION_KEY
sudo systemctl list-timers tokenkey-pgdump.timer
```

#### Step 5：管理员登录 + 完成 OpenAI OAuth

打开 `https://api.tokenkey.dev`，账号 = `AdminEmail`。如初始密码留空，看一次容器日志：

```bash
sudo docker logs tokenkey 2>&1 | grep -i 'admin password' | head
```

后续按 `CLAUDE.md` 网关说明与管理后台完成「加 OpenAI 账号 → group → API Key → cc switch」。

#### Step 6：端到端验证

```bash
# 用一个生成的 sub2api API Key 走主链路
curl -X POST https://api.tokenkey.dev/v1/messages \
  -H 'Authorization: Bearer <API_KEY>' \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.4-medium","messages":[{"role":"user","content":"ping"}]}'
```

确认请求成功、用量正确记录在管理面。

### 3.6 应用更新与回滚

两种方式，**优先用 A**（状态可追溯、PAT 自动重新登录）：

**A. 通过 CFN 改 `ImageTag`（推荐）**

```bash
NEW_TAG=sha-abc1234        # 已在 GHCR 构建好的 tag

aws cloudformation deploy \
  --region us-east-1 --stack-name tokenkey-prod-stage0 \
  --template-file deploy/aws/cloudformation/stage0-single-ec2.yaml \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides ImageTag="${NEW_TAG}"
```

- CFN 检测到 `ImageTag` 变化 → EC2 替换或 UserData 重跑（取决于 CFN 的 update behavior），停机约 1–3 min（含 dnf update / docker pull）。
- 重跑会重新 `aws ssm get-parameter` 拿 PAT 并 `docker login`，所以**当 SSM 里的 PAT 已轮换或被删，这步会失败**。在 `/var/log/tokenkey-bootstrap.log` 里可看到具体错误；先 `aws ssm put-parameter --overwrite` 续上 PAT 再重试即可。

**B. SSH/SSM 进实例热更（停机约 10–30 秒，CFN 状态不再准确）**

只在「想分钟内紧急回滚 / CFN 这条路被堵」时用。同时**绕开了 CFN 的 ImageTag 跟踪**——下次再走 A 方案时，CFN 会以为你还在跑旧 tag，可能不重启 EC2。

```bash
# 把 <owner> 和 sha-XXXX 替换成实际值
NEW_IMAGE='ghcr.io/<owner>/sub2api:sha-XXXX'

sudo bash -lc "
  cd /var/lib/tokenkey &&
  sed -i \"s|^TOKENKEY_IMAGE=.*|TOKENKEY_IMAGE=${NEW_IMAGE}|\" .env &&
  docker compose --env-file .env pull &&
  docker compose --env-file .env up -d
"
```

回滚：把 `NEW_IMAGE` 换成上一版的 sha tag 重跑同一段。

> **升级路径触发：** 当「10–30 秒停机」开始造成业务影响（即 Stage 1→2 的触发条件 a），就该升 Stage 2（ALB + 双副本，零停机滚动）。

### 3.7 备份与恢复

#### 两层备份并行

| 类型 | 工具 | 默认频率 | 保留 | 一致性 | 用途 |
|---|---|---|---|---|---|
| **整盘快照** | DLM → EBS Snapshot | 每天 1 次 03:00 UTC | 7 份 | crash-consistent | 实例丢失 / 整机回滚 |
| **逻辑备份** | systemd timer + `pg_dump` | **每小时 1 次** | **168 份（7 天）** | application-consistent | 想回到任一小时 / 迁移到 RDS |

#### 「每日快照 → 每小时快照」的影响

在 `aws cloudformation deploy` 时把 `SnapshotSchedule=daily` 改成 `hourly`（详见 README）：

| 维度 | 影响 |
|---|---|
| **RPO（整机灾难）** | 24 h → **1 h** |
| **额外存储成本** | EBS 快照按**增量块**计费（\$0.05/GiB-month）。30 GiB 卷在轻写场景，每天增量通常仅几百 MB。每日 7 份 ≈ \$0.1–0.3/月；每小时 168 份 ≈ **\$1–4/月**。重写多的卷会拉高，需 Cost Explorer 复核。 |
| **API 调用** | 每实例每小时 1 次，远低于 EBS 快照速率上限 |
| **RTO** | 不变（10–30 min 创建新卷 + 改 EIP 关联） |
| **应用一致性** | 不变。EBS 快照本身始终是 crash-consistent；想「应用一致地回到任一小时」要用 `pg_dump` 那条线（已默认每小时 1 份） |
| **前台 IO 影响** | 几乎可忽略，EBS 快照后台异步、写时复制 |

**结论：** 默认 `daily` 已经足够（pg_dump 已经每小时一份覆盖应用一致需求）；如果你愿意每月多花 \$1–4 把「整机 RPO」也压到 1 小时，把参数改 `hourly` 即可。

#### 恢复演练（每季度 1 次）

- **只丢了表数据**：`docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey < <(zcat /var/lib/tokenkey/pgdump/tokenkey-<TS>.sql.gz)`
- **整机/卷损坏**：从最新 EBS 快照 → 创建新卷 → 启新 EC2（同 SG/Subnet）→ attach 卷 → EIP 重新关联到新实例
- 演练日期填回附录 A.S0 的「最近一次恢复演练日期」

### 3.8 监控与告警（基础四项）

Stage 0 不上 SNS 也不上专门告警栈，只用 CloudWatch Agent 已经在采集的指标 + AWS 控制台告警：

| 指标 | 阈值（建议初始） | 处理 |
|---|---|---|
| EC2 `CPUUtilization` 5min P95 | > 60% 持续 1 周 | 触发 **Stage 1→2 升级评估** |
| EC2 内存 `mem_used_percent`（CW Agent） | > 70% 持续 30 min | 同上 |
| `/var/lib/tokenkey` 磁盘 `used_percent` | > 75% | 扩 gp3 卷（在线扩容） |
| `https://api.tokenkey.dev/health` | 5 min 内 ≥ 3 次失败 | P1：实例可能挂了 |

业务类（OAuth refresh、上游 4xx/429）暂不建专门告警，靠每天扫一次容器日志（`docker logs tokenkey --since 24h | grep -iE 'oauth|429|401|403'`）兜底。Stage 2/3 再上完整 CloudWatch Alarms + SNS。

### 3.9 触发升级（→ Stage 1）

满足以下任一即评估升级，**不要为「未来可能」提前花钱**：

- PostgreSQL 数据 > 5 GB
- 想要 PITR（精确到秒级回滚）
- 单机重启的 5–10 min 窗口已经引发用户投诉
- 出现 ≥ 1 次「容器内 PostgreSQL 升级失败 / 数据修复」事故

---

## 四、Stage 1：托管 PostgreSQL（**未来选项，触发后再实施**）

**变化点（相对 Stage 0）：**

- **新增** RDS PostgreSQL Single-AZ db.t4g.micro，开启自动备份（7–14 天）+ Performance Insights
- **新增** 一个**私有子网**（仅放 RDS）；EC2 仍在公有子网
- **新增** 安全组规则：RDS SG 仅允许 EC2 SG `5432`
- **EC2 上**关停 `postgres` 容器，改 `DATABASE_HOST` 指向 RDS endpoint，密码进 **Secrets Manager**（应用通过 IAM Role 拉取）
- **保留** 容器内 Redis、Caddy、应用本身

**额外月费：≈ \$15–40**（RDS db.t4g.micro Single-AZ + 20 GiB gp3 + Performance Insights free tier + Secrets Manager \$0.4/secret）

**收益：** PITR、自动备份、版本升级托管。**牺牲：** 仍是单 AZ，RDS 小版本升级 / 主备切换会有 5–15 min 停机。

---

## 五、Stage 2：双副本 + ALB（**未来选项，触发后再实施**）

**变化点（相对 Stage 1）：**

- **新增** 第 2 台 EC2，跨第 2 个 AZ；同时新增第 2 个公有子网
- **新增** ALB（443，绑定 ACM 证书）替换 Caddy 做入口；管理面路径开 ALB **粘性**（与 OAuth `SessionStore` 进程内状态约束一致，详见 5.3 → 七、代码相关约束）
- **新增** ElastiCache cache.t4g.micro **单节点**；EC2 上关停 Redis 容器（**必须**：两台 EC2 之间 sticky session / refresh lock 必须共享）
- RDS 仍 Single-AZ，**不上** Multi-AZ；**不上** NAT GW（两台 EC2 都在公有子网各带 EIP）
- 应用部署改 ALB Target Group + 滚动发布（旧机器停一台 → 新机器拉新镜像 → 进 LB → 下另一台）

**额外月费：≈ \$60–100**（ALB ~\$22 + LCU、第二台 t4g.small ~\$12、ElastiCache cache.t4g.micro ~\$12、第二个 EIP/卷/快照）

**收益：** 零停机滚动发布、单机故障切换、Redis 共享态正确。**牺牲：** AZ 级故障仍可能影响 RDS。

---

## 六、Stage 3：完整 HA / Multi-AZ（**未来选项**）

**变化点（相对 Stage 2）：**

- **应用层**改 ECS Fargate `desiredCount=2`，跨 2 AZ；删 EC2
- **RDS** 升 Multi-AZ；**ElastiCache** 升 Multi-AZ auto failover
- **网络**新增私有应用子网 + NAT GW；ECS 不再有公网 IP
- **CI/CD**：GitHub Actions + AWS OIDC + ECR + ECS rolling deployment（替换 Stage 0/1/2 的「SSM 拉镜像」）
- **DR**：跨区域冷备 runbook、IaC 模板、DNS 切换预案

**额外月费：≈ \$100–250**（NAT GW 时长 + 处理费、Fargate 2 task、RDS Multi-AZ ×2 计算/存储、ElastiCache 副本节点、ALB LCU 上升、CloudWatch Alarms + SNS）

**适用场景：** 对外承诺 SLA ≥ 99.9%、收入规模能消化月账单 \$300+、需要无人值守跨 AZ/跨区域容灾。

---

## 七、代码相关约束

给修改代码的人的提醒：本部署方案对代码状态有如下耦合，触及对应模块时需同步评审本文档。代码事实以 `CLAUDE.md`「Current Gateway Flow」与 `docs/approved/newapi-as-fifth-platform.md` 为纲；本表只列部署联动后果。

| 代码事实 | 部署联动 | 影响阶段 |
|---|---|---|
| OAuth `SessionStore` 是进程内状态 | 单实例（Stage 0/1）天然满足；多实例（Stage 2+）必须 ALB 粘性 | Stage 2 起 |
| Redis 承载 sticky session / token cache / refresh lock | 单实例时容器内 Redis OK；多实例必须独立 ElastiCache（不可降级为可选） | Stage 2 起 |
| WS mode 有进程本地状态 | 不作为主生产链路 | 全阶段 |

主链路固定为：

```text
Claude Code CLI → cc switch → api.tokenkey.dev/v1/messages → sub2api OpenAI group → OpenAI OAuth account
```

---

## 附录 A：Stage 0 节点信息登记表

> 部署完成后填回；机密类（DB 密码、JWT/TOTP 密钥）由 Cloud-Init 自动生成在 `/var/lib/tokenkey/.env`，**本表不登记明文**。Stage 1+ 启用后再扩展登记 RDS / ALB / ElastiCache 等字段。

### A.S0 Stage 0 资源

| 字段 | 值 |
|---|---|
| AWS 区域 | `us-east-1` |
| CloudFormation Stack 名 | `tokenkey-prod-stage0` |
| VPC ID | `vpc-________` |
| 公有子网 ID | `subnet-________` |
| 安全组 ID | `sg-________` |
| EC2 Instance ID | `i-________` |
| EC2 实例规格 | `t4g.small` |
| EBS Root 卷 ID | `vol-________` |
| EBS Root 卷大小 | `30 GiB gp3` |
| Elastic IP | `__.__.__.__` |
| EIP Allocation ID | `eipalloc-________` |
| API 域名 | `api.tokenkey.dev` |
| Porkbun A 记录 TTL | `60` / `120` / `300` |
| LE 证书签发时间 | (Caddy 日志) |
| 应用容器监听端口 | `8080`（容器内，不暴露宿主） |
| DLM Lifecycle Policy ID | `policy-________` |
| 快照频率 | `daily` / `hourly` |
| 最近一次 EBS 恢复演练日期 | |
| 最近一次 pg_dump 恢复演练日期 | |
| 备注（首次部署日期 / 镜像 tag） | |
