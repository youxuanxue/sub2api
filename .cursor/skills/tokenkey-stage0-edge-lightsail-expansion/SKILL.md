---
name: tokenkey-stage0-edge-lightsail-expansion
description: >-
  End-to-end runbook for adding a TokenKey Stage0 Edge gateway on AWS Lightsail
  (parallel to the EC2/CFN path): register the edge in
  deploy/aws/lightsail/edge-targets-lightsail.json, ensure the one-time
  Lightsail IAM addon is in place (GHCR PAT only needed when the image is
  private — TokenKey GHCR is public so default provision uses anonymous pull),
  provision via deploy-edge-lightsail-stage0.yml, point DNS, smoke, and
  upgrade/rollback. EC2/CFN remains the default Edge path; this skill covers
  the Lightsail parallel path only.
---

# TokenKey：新增 Lightsail Edge 网关全流程

适用于 TokenKey fork of sub2api。Lightsail Edge 与 EC2 Edge **并行**（不是替代）。
权威纪律以仓库根 `CLAUDE.md` 为准（ARM 多架构镜像、release/deploy 顺序、preflight 不绕过）。
默认路径与放弃决策见 `docs/deploy/tokenkey-multiregion-egress-gateway-plan.md` §6.1 与
`docs/spec-delta-edge-lightsail.md`。

## 确定性基线（机械化 vs 真判断）

| 步骤 | 类型 | 承载 |
|---|---|---|
| 解析 edge → 区域/AZ/bundle/SSM 前缀 | 机械 | `deploy/aws/lightsail/resolve-edge-lightsail-target.py` |
| Stage0 routing / 统一 SSM（与 EC2 路径同源 primitive） | 机械 | `ops/stage0/edge_routing_matrix.py` + `ops/stage0/edge_ssm_execution.py`（admin：`edge_admin_resolve_target.py`）；可部署矩阵：`python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable` |
| 渲染 user-data（launch script） | 机械 | `deploy/aws/lightsail/render-bootstrap.sh`（drift gate 已接入 preflight） |
| Provision dispatch + watch | 机械 | `gh workflow run deploy-edge-lightsail-stage0.yml` + `gh run watch --exit-status` |
| 升级/回滚/烟测 dispatch | 机械 | 同上（operation 参数化） |
| Provision 后落盘 admin 账密 | 机械 | `bash ops/stage0/ensure-edge-admin-credentials.sh --platform lightsail <edge_id>` |
| 防火墙 443 + DNS 后 HTTPS / ACME 验收 | 机械 | `bash ops/stage0/verify-edge-lightsail-network.sh <edge_id> [--fix-443] [--renew-cert]` |
| matrix 编辑 / IAM scope / GHCR PAT 落 SSM | 判断 | prompt（成本/区域/权限是架构决定） |
| DNS A 记录指向 Lightsail Static IP | 判断 | prompt（Porkbun 手工步骤） |
| 故障定位（SSM Hybrid 注册未完成 / Lightsail 配额 / GHCR PAT 失效） | 判断 | prompt（诊断分支） |

## 调用参数

```text
/tokenkey-stage0-edge-lightsail-expansion edge_id=<id> region=<lightsail-region> operation=<prepare|provision|smoke|upgrade|rollback|full> [tag=X.Y.Z] [previous_tag=X.Y.Z] [recreate=false]
```

| 参数 | 语义 |
|---|---|
| `edge_id` | 新 Lightsail edge，如 `uk1`、`us1`、`fra1`、`sg1`。**必须**与 EC2 `edge_id` 不同名空间（同 id 不同栈不要并存活跑）。 |
| `region` | Lightsail API region（`eu-west-2` / `us-west-2` / `eu-central-1` / `ap-southeast-1` 等）。Paris 无 Lightsail，`fra1` 必须映射到 `eu-central-1`。 |
| `operation=prepare` | 仅做注册 + 一次性 IAM/SSM/PAT 配置，**不**创建实例。 |
| `operation=provision` | 创建 Lightsail 实例 + 分配 Static IP + 等 SSM Hybrid 注册完成。默认 fail-if-exists；要销毁重建须 `recreate=true`（destructive）。 |
| `operation=smoke` | 不动实例，复用 `ops/stage0/external_health.sh` + `ops/stage0/edge_post_deploy_smoke.sh`。 |
| `operation=upgrade` / `rollback` | 通过共享 `ops/stage0/deploy_via_ssm.sh` 换 tag，与 EC2 完全相同 primitive。 |
| `operation=full` | prepare → provision → admin creds → firewall 443 → DNS（手工）→ renew cert（若 DNS 晚于 provision）→ smoke 闭环。 |

默认行为：
- "新增 Lightsail edge X" → `operation=full`
- "权限/配置先打通" → `operation=prepare`
- "DNS 改完继续" → `operation=smoke`

## 0) 前置

```bash
git fetch origin main --tags
git checkout main && git pull --ff-only
bash scripts/preflight.sh
```

确认：

- 本机有 `gh`、`aws`（or `aws-vault`）、`jq`。
- 仓库 var `AWS_OIDC_ROLE_ARN` 已配置；`vars.EDGE_ACME_EMAIL`、`vars.EDGE_MAIN_GATEWAY_ALLOWED_CIDR` 已在 `edge-<edge_id>` Environment 配齐（**EDGE_MAIN_GATEWAY_ALLOWED_CIDR 没有默认值**，workflow 会在缺失时 `::error::` 直接挂）。
- 若该 edge 要跑含 main-via-edge 的 smoke（当前仅 uk1/us1）：`TK_SMOKE_EDGE_CANARY_KEY` secret 已在对应 Environment 配置。

## 1) Prepare：注册新 Lightsail edge

### 1.1 更新 matrix（代码）

编辑 `deploy/aws/lightsail/edge-targets-lightsail.json`，新增或改 `<edge_id>`：

| 字段 | 说明 |
|---|---|
| `deployable` | 首次建议 `false`；准备 OK 再置 `true` |
| `lightsail_region` | Lightsail API region |
| `ec2_equivalent_region` | 用于对账与跨栈观察 |
| `availability_zone` | 该 region 的 AZ（一般 `<region>a`） |
| `domain` | `api-<edge_id>.tokenkey.dev` |
| `instance_name` | 默认 `tokenkey-edge-<edge_id>-ls`；**已有 Lightsail 实例时填 AWS 真实名称**（见 §1.1a） |
| `static_ip_name` | 默认 `tokenkey-edge-<edge_id>-ls-ip`；**已有 Static IP 时填 AWS 真实名称**（见 §1.1a） |
| `porkbun_a_ipv4` | 可选；DNS 真值锚点，与 `aws lightsail get-static-ip` 的 `ipAddress` 对齐 |
| `bundle_id` | 默认 `micro_3_0` |
| `blueprint_id` | 默认 `amazon_linux_2023` |
| `monthly_budget_usd` | 不得超过 `max_monthly_budget_usd`（默认 12） |
| `ssm_prefix` | `/tokenkey/lightsail/<edge_id>` |

#### 1.1a 已有 Lightsail 实例 + Static IP（adopt 路径）

控制台或手工预先创建的 Lightsail 资源（裸 AL2023、无 TokenKey bootstrap）走此路径。**不要**假设
`instance_name` / `static_ip_name` 遵循默认命名；必须先读 AWS 真值再写 matrix。

```bash
# 按 region 列出 tokenkey 相关 Static IP（name / ipAddress / attachedTo）
aws lightsail get-static-ips --region us-east-1 \
  --query 'staticIps[?contains(name, `tokenkey`) || contains(attachedTo, `tokenkey`)]'
# us-east-2 / us-west-2 等同理换 --region

# 解析 matrix 字段（commit 前核对）
python3 deploy/aws/lightsail/resolve-edge-lightsail-target.py \
  --edge-id <edge_id> --allow-planned
```

规则：

- `edge_id`（调度/DNS 语义，如 `us2`）可以与 `instance_name`（如 `tokenkey-edge-us-va1-ls`）**不同**。
- `confirm_instance` workflow 输入 **必须等于** matrix 的 `instance_name`，不是 `tokenkey-edge-<edge_id>-ls` 的机械推导。
- 已有 bare instance → provision 时 **`recreate=true`**（见 §2）。`provision-edge.sh` 在 recreate 时
  **只 detach、不 release** Static IP，保留已分配的 IP 地址（2026-05-28 起）。
- **禁止**在本机用普通 IAM user 跑 `provision-edge.sh`：需要 `iam:PassRole`（SSM Hybrid activation）。
  统一走 GHA `deploy-edge-lightsail-stage0.yml`（OIDC role 已授权）。

### 1.2 加 workflow choice（代码）

编辑 `.github/workflows/deploy-edge-lightsail-stage0.yml` 把 `<edge_id>` 加入 `workflow_dispatch.inputs.edge_id.options`。

### 1.3 一次性 IAM addon（每个 AWS 账户一次）

```bash
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-cicd-lightsail-addon \
  --template-file deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml \
  --capabilities CAPABILITY_NAMED_IAM
# default GitHubOidcRoleNames covers both regional OIDC roles
# (us-east-1 + eu-west-2). Override only when adding new region-scoped roles.
```

### 1.4 GHCR auth（仅在镜像私有时需要）

TokenKey 的 `ghcr.io/<owner>/sub2api` 当前是 **public**，Lightsail bootstrap 走 anonymous pull，**默认不需要 PAT**。workflow input `ghcr_pat_required` 默认 `false`。

仅当镜像未来转私有时，落 PAT 并在 dispatch 时翻位：

```bash
aws ssm put-parameter --region "<lightsail_region>" \
  --name "/tokenkey/lightsail/<edge_id>/ghcr/pat" \
  --type SecureString --value 'ghp_…'
# 然后 provision 时加 -f ghcr_pat_required=true
```

### 1.5 GitHub Environment

复用 `edge-<edge_id>` 已有的 environment（与 EC2 共用），新增/确认：

- `EDGE_ACME_EMAIL`
- `EDGE_MAIN_GATEWAY_ALLOWED_CIDR`（current prod main-gateway egress；**workflow 没有默认值**）
- `TK_SMOKE_EDGE_CANARY_KEY`（secret）— **仅**计划跑 `operation=smoke`（含 main-via-edge）的 edge 需要。
  当前 prod 惯例：**只**在 `edge-uk1` / `edge-us1` 配置即可；其它 edge（如 us2/us3/us4）
  **可不配**——缺 secret 时 `edge_post_deploy_smoke.sh` 跳过 main-via-edge 段，provision/upgrade
  的 infra 路径不受影响。GitHub secret 只写不可读，无法从 uk1 机械复制。

Smoke base URL 与 Edge 本机 model 在代码内固定（`https://api.tokenkey.dev` / `claude-sonnet-4-6`），无需 Environment var。

**US 多 region edge**（`us-east-1` / `us-east-2` / `us-west-2`）：Environment 的 `AWS_OIDC_ROLE_ARN`
与 `edge-us1` 相同（`tokenkey-gha-us-east-1-error-clustering`）；Lightsail API region 由 matrix
`lightsail_region` 决定，与 OIDC role region 无关。

### 1.5a OIDC trust：新 `edge-<id>` Environment 必做

Workflow 绑定 `environment: edge-<edge_id>` 时，OIDC token 的 `sub` 为
`repo:<owner>/<repo>:environment:edge-<edge_id>`。该 claim **必须**出现在
`deploy/aws/cloudformation/cicd-oidc.yaml` → `AllowedSubjects`，且 **live stack 必须显式 override**——
改 template Default **不会**更新已存在 stack 的参数。

```bash
# 1) 改 cicd-oidc.yaml Default（或记下完整列表）
# 2) 部署 — 必须带 --parameter-overrides AllowedSubjects=...
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-cicd-oidc \
  --template-file deploy/aws/cloudformation/cicd-oidc.yaml \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides AllowedSubjects="repo:youxuanxue/sub2api:ref:refs/heads/main,repo:youxuanxue/sub2api:environment:prod,repo:youxuanxue/sub2api:environment:edge-uk1,...,repo:youxuanxue/sub2api:environment:edge-<new_id>"

# 3) 机械验收 — sub 列表含新 environment
aws iam get-role --role-name tokenkey-gha-us-east-1-error-clustering \
  --query 'Role.AssumeRolePolicyDocument.Statement[0].Condition."ForAnyValue:StringLike"."token.actions.githubusercontent.com:sub"'
```

`AssumeRoleWithWebIdentity` / `Not authorized` 且 provision 步骤未开始 → 先查此 trust，不要猜 Lightsail 权限。

新 edge **若不跑 smoke**，可跳过 `TK_SMOKE_EDGE_CANARY_KEY`（见 §1.5 说明；uk1/us1 以外的 edge 默认不配）。

#### 1.3b Lightsail-only edge：**不要**加 EC2 CFN execution role

`us2` / `us3` / `us4`、已完成 EC2→Lightsail 的 `uk1` 等 **只在 Lightsail 矩阵 `deployable=true`** 的 edge：

- OIDC 只需 `cicd-oidc.yaml` → `AllowedSubjects` 含 `environment:edge-<id>`（§1.5a）。
- **不要**在 `cicd-oidc.yaml` 新增 `Edge<PascalCase>CloudFormationExecutionRoleArn` / `EdgeXTargetInstanceId` / 区域 SSM `ssm:SendCommand` 到 EC2 instance ARN——那是 EC2/CFN 路径（`tokenkey-stage0-edge-expansion`）。
- 运维入口：`deploy-edge-lightsail-stage0.yml` + SSM Hybrid tag `EdgeId` / `Platform=lightsail`。

若误加了 uk1 EC2 IAM 又已迁移到 Lightsail，Phase 5 收尾见 `tokenkey-stage0-edge-platform-migration` §5。

### 1.6 PR + 落库

```bash
git checkout -b chore/lightsail-edge-<edge_id>-register
git commit -am "feat(edge-lightsail): register <edge_id> matrix entry"
gh pr create --fill --base main
```

待 CI 全绿合 main。

## 2) Provision：创建实例

先从 matrix 取 `confirm_instance`（不要硬编码 `tokenkey-edge-<edge_id>-ls`）：

```bash
CONFIRM=$(python3 deploy/aws/lightsail/resolve-edge-lightsail-target.py \
  --edge-id <edge_id> | awk -F= '/^instance_name=/{print $2}')
TAG=X.Y.Z   # 当前 prod tag（不带 v）；读 backend/cmd/server/VERSION
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=provision \
  -f tag=$TAG \
  -f confirm_instance="$CONFIRM"
gh run watch --exit-status $(gh run list -w deploy-edge-lightsail-stage0.yml -L 1 --json databaseId -q '.[0].databaseId')
```

matrix 变更在 feature branch 上时，dispatch 加 `--ref <branch>`（workflow checkout 该 ref 的 matrix）。

观察点：

- Job summary / log 行 `provision complete edge=… ip=…` 的 **ip 必须等于** matrix `porkbun_a_ipv4`（或 adopt 前查到的 Static IP）；
- SSM managed instance 在 ≤15 分钟内拿到 `mi-*`。

**销毁重建**（bare instance、换 bundle、误装坏栈；**保留 Static IP 地址**）：

```bash
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=provision \
  -f tag=$TAG \
  -f confirm_instance="$CONFIRM" \
  -f recreate=true
```

workflow 仍打印 “Static IP will be DESTROYED” 警告语，但 `provision-edge.sh` 实际 **detach-only**（不 release）。
默认 `recreate=false` → 实例已存在则 `::error::` 直接挂；不会被静默销毁。

**批量 US edge**（如 us2/us3/us4）：按 edge 顺序逐个 dispatch + watch（concurrency 按 edge_id 分组，可并行，但 prepare/OIDC 未完成前不要并发）。

### 2.1 Admin 账密落盘（provision 后必做，禁止打印密码）

GHA workflow **不会**写 operator 本机的 `~/Codes/keys/`。每个新 edge provision 成功后，在本机执行（**stdout 不输出 password**）：

```bash
bash ops/stage0/ensure-edge-admin-credentials.sh --platform lightsail <edge_id>
# 或等价：capture 失败 (exit 3) 时再跑 reset-edge-admin-password.sh
ls -l "$HOME/Codes/keys/tokenkey-<edge_id>-admin-password.txt"   # chmod 600，含 email= / password=
```

验收：`email=admin@api-<edge_id>.tokenkey.dev` 与 uk1/us1 文件格式一致。**禁止**在 PR、GHA log、聊天里粘贴 `password=` 行。

**prod 主网关同脚本**：目标传字面量 `prod`（同一套脚本解析到固定 EC2 栈 `tokenkey-prod-stage0`/`us-east-1`，`--platform` 忽略），落盘 `tokenkey-prod-admin-password.txt`。prod 的 bootstrap 日志通常已滚动，`ensure prod` 会自动 fallback 到 reset（轮换）；要直接轮换用 `bash ops/stage0/reset-edge-admin-password.sh prod`。

### 2.2 防火墙 TCP 443（provision 后必验）

`provision-edge.sh` 会尝试开放 80/443，但 **443 可能未生效**（us2 实案：公网 HTTPS 超时、实例内 curl 正常）。

```bash
bash ops/stage0/verify-edge-lightsail-network.sh <edge_id> --fix-443
# 机械验收：Lightsail get-instance-port-states 中 TCP 80 与 443 均为 open
```

**禁止**在 443 未开时进入 DNS/smoke——现象是连接超时，不是应用 5xx。

### 2.3 新 edge Anthropic baseline（OAuth 账号就绪后）

DNS cutover 且 admin 可登录后，按 `tokenkey-anthropic-oauth-config` 对新 edge 跑 tier baseline + concurrency mirror verify（Lightsail 矩阵 `deployable=true` 的 edge 已纳入双矩阵 domain 链接）。

## 3) DNS 与 ACME 时序

手工把 `api-<edge_id>.tokenkey.dev` A 记录指到 Static IP（Porkbun）。等 `dig +short @1.1.1.1` 指向该 IP（常见约 1 分钟）。

**Adopt 路径常见坑**：provision **早于 DNS** → Caddy ACME 对 NXDOMAIN 失败 → DNS 生效后公网 TLS handshake 仍失败。

```bash
bash ops/stage0/verify-edge-lightsail-network.sh <edge_id> --renew-cert
# 等价：SSM docker restart tokenkey-caddy，等 ~15s 后 curl https://api-<id>.tokenkey.dev/health
```

验收：`curl -sk https://api-<edge_id>.tokenkey.dev/health` → `{"status":"ok"}`。

## 4) Smoke

```bash
CONFIRM=$(python3 deploy/aws/lightsail/resolve-edge-lightsail-target.py \
  --edge-id <edge_id> | awk -F= '/^instance_name=/{print $2}')
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=smoke \
  -f confirm_instance="$CONFIRM"
gh run watch --exit-status $(gh run list -w deploy-edge-lightsail-stage0.yml -L 1 --json databaseId -q '.[0].databaseId')
```

接 `ops/stage0/external_health.sh` + `ops/stage0/edge_post_deploy_smoke.sh`（与 EC2 共用）。

**Smoke 范围（operator 选择）**：全量 smoke + main-via-edge 目前只对 **uk1 / us1** 启用（需
`TK_SMOKE_EDGE_CANARY_KEY`）。其它 Lightsail edge  provision 完成后以 DNS + 可选
`curl https://api-<id>.tokenkey.dev/health` 或 `operation=smoke`（无 canary 时仅 infra 段）验收即可。

## 5) Upgrade / Rollback

```bash
TAG=<new_tag>
CONFIRM=$(python3 deploy/aws/lightsail/resolve-edge-lightsail-target.py \
  --edge-id <edge_id> | awk -F= '/^instance_name=/{print $2}')
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> -f operation=upgrade -f tag=$TAG \
  -f confirm_instance="$CONFIRM"
```

回滚把 `operation=rollback` + 把 `tag` 设成上一个 prod tag。

## 6) 已知失败模式与定位

| 现象 | 根因候选 | 第一步 |
|---|---|---|
| OIDC `AssumeRoleWithWebIdentity` / `Not authorized`，provision 未开始 | 新 `edge-<id>` Environment 未加入 `cicd-oidc` AllowedSubjects，或 stack 未 `--parameter-overrides` | §1.5a：查 IAM role trust `sub` 列表 |
| 本机 `provision-edge.sh` → `iam:PassRole` denied | 普通 IAM user 无 Hybrid activation 权限 | 改走 GHA workflow，不要本地 provision |
| "EDGE_MAIN_GATEWAY_ALLOWED_CIDR not set" | Environment 漏配 | 加到 `edge-<edge_id>`；不要给默认值 |
| `provision` 步骤 fail，managed instance 未注册 | SSM Hybrid activation expired / 网络不通 | Lightsail 浏览器 SSH 看 `/var/log/tokenkey-lightsail-bootstrap.log`；`BOOTSTRAP_FAIL: ` 行即根因 |
| GHA `smoke` / SSM 步骤失败，本机同 tag 却正常 | **`aws` / workflow 用了错的 `--region`**（误用 `ec2_equivalent_region`）或 job **工作目录**与脚本相对路径不一致 | 用 `python3 ops/stage0/edge_ssm_execution.py --repo-root . --edge-id <id> --format env` 核对输出的 **`REGION`**；workflow 里凡 SSM 调用必须与之一致；检查 `defaults.run.working-directory` / `cd` |
| `external_health` 报 5xx | Caddy 还在签证书 / docker compose 起不来 | `ssh` 进实例 `docker compose -f /var/lib/tokenkey/docker-compose.yml ps` |
| 公网 `curl https://api-<id>.tokenkey.dev` **连接超时** | Lightsail 防火墙 **缺 TCP 443**（80 可能已开） | §2.2 `verify-edge-lightsail-network.sh --fix-443` |
| DNS 已指 Static IP 但 **TLS handshake 失败** | provision 时 DNS 为 NXDOMAIN，ACME 未签成功 | §3 `--renew-cert` 重启 `tokenkey-caddy` |
| Static IP 已分配但 attach 失败 | 旧 instance 还在持有该 Static IP | `aws lightsail detach-static-ip` 再重 attach；或 `recreate=true` 重来 |
| GHCR pull 401 | PAT 过期 / 写错 SSM 路径 | 用 1.4 重新 put-parameter |
| Squash 合并后 `git branch -d feature/...` 拒绝删除 | Squash 不产生「分支 tip 是 main 祖先」关系 | `git checkout main && git pull --ff-only` 后 `git branch -D feature/...`；`git remote prune origin` |

## 7) Acceptance（机械化输出）

完成 1 个 Lightsail edge expansion 后，给一个 5 行 acceptance：

```text
edge_id        : <id>
lightsail_region: <region>
domain         : api-<id>.tokenkey.dev
managed_instance: mi-XXXXXXXXXXXXXXXXX
firewall_443   : open (verify-edge-lightsail-network.sh)
https_health   : ok | pending-dns | tls-renew-required
admin_credentials_file: ~/Codes/keys/tokenkey-<id>-admin-password.txt (§2.1; password not printed)
last_smoke_run : <gh run URL or skipped>
```

## 8) `operation=full` 编号清单

1. §1 Prepare（matrix + workflow choice + OIDC §1.5a + lightsail addon §1.3）
2. §2 Provision（GHA + watch）
3. §2.1 Admin 账密（`ensure-edge-admin-credentials.sh`）
4. §2.2 防火墙 443（`verify-edge-lightsail-network.sh --fix-443`）
5. §3 DNS A 记录 → Static IP
6. §3 ACME（若 TLS 失败：`--renew-cert`）
7. §4 Smoke（或 uk1/us1 以外 edge 仅 `curl /health`）
8. §7 Acceptance 输出

数据来自 workflow Job summary + verify 脚本，不要在 SKILL 里手抄常量。
