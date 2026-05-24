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
| 渲染 user-data（launch script） | 机械 | `deploy/aws/lightsail/render-bootstrap.sh`（drift gate 已接入 preflight） |
| Provision dispatch + watch | 机械 | `gh workflow run deploy-edge-lightsail-stage0.yml` + `gh run watch --exit-status` |
| 升级/回滚/烟测 dispatch | 机械 | 同上（operation 参数化） |
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
| `operation=full` | prepare → provision → DNS（手工）→ smoke 闭环。 |

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
- `MAIN_GATEWAY_EDGE_SMOKE_API_KEY` secret 已在该 Environment 配置。

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
| `instance_name` | `tokenkey-edge-<edge_id>-ls` |
| `static_ip_name` | `tokenkey-edge-<edge_id>-ls-ip` |
| `bundle_id` | 默认 `micro_3_0` |
| `blueprint_id` | 默认 `amazon_linux_2023` |
| `monthly_budget_usd` | 不得超过 `max_monthly_budget_usd`（默认 12） |
| `ssm_prefix` | `/tokenkey/lightsail/<edge_id>` |

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
- `EDGE_MAIN_GATEWAY_BASE_URL`
- `MAIN_GATEWAY_EDGE_SMOKE_API_KEY`（secret）

### 1.6 PR + 落库

```bash
git checkout -b chore/lightsail-edge-<edge_id>-register
git commit -am "feat(edge-lightsail): register <edge_id> matrix entry"
gh pr create --fill --base main
```

待 CI 全绿合 main。

## 2) Provision：创建实例

```bash
TAG=X.Y.Z   # 当前 prod tag（不带 v）
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=provision \
  -f tag=$TAG \
  -f confirm_instance=tokenkey-edge-<edge_id>-ls
gh run watch --exit-status $(gh run list -w deploy-edge-lightsail-stage0.yml -L 1 --json databaseId -q '.[0].databaseId')
```

观察点：

- Job summary 输出 Static IP；
- "SSM managed instance registration" 步骤在 ≤10 分钟内拿到 `mi-*`（fail-fast 已接入 `render-bootstrap.sh`，10 分钟还拿不到必有日志可看）。

**销毁重建**（如换 bundle、误装坏栈）：

```bash
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=provision \
  -f tag=$TAG \
  -f confirm_instance=tokenkey-edge-<edge_id>-ls \
  -f recreate=true
```

默认 `recreate=false` → 实例已存在则 `::error::` 直接挂；不会被静默销毁。

## 3) DNS

手工把 `api-<edge_id>.tokenkey.dev` A 记录指到上一步输出的 Static IP（Porkbun）。等 1 分钟生效。

## 4) Smoke

```bash
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=smoke \
  -f confirm_instance=tokenkey-edge-<edge_id>-ls
gh run watch --exit-status $(gh run list -w deploy-edge-lightsail-stage0.yml -L 1 --json databaseId -q '.[0].databaseId')
```

接 `ops/stage0/external_health.sh` + `ops/stage0/edge_post_deploy_smoke.sh`（与 EC2 共用）。

## 5) Upgrade / Rollback

```bash
TAG=<new_tag>
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=<edge_id> -f operation=upgrade -f tag=$TAG \
  -f confirm_instance=tokenkey-edge-<edge_id>-ls
```

回滚把 `operation=rollback` + 把 `tag` 设成上一个 prod tag。

## 6) 已知失败模式与定位

| 现象 | 根因候选 | 第一步 |
|---|---|---|
| "EDGE_MAIN_GATEWAY_ALLOWED_CIDR not set" | Environment 漏配 | 加到 `edge-<edge_id>`；不要给默认值 |
| `provision` 步骤 fail，managed instance 未注册 | SSM Hybrid activation expired / 网络不通 | Lightsail 浏览器 SSH 看 `/var/log/tokenkey-lightsail-bootstrap.log`；`BOOTSTRAP_FAIL: ` 行即根因 |
| `external_health` 报 5xx | Caddy 还在签证书 / docker compose 起不来 | `ssh` 进实例 `docker compose -f /var/lib/tokenkey/docker-compose.yml ps` |
| Static IP 已分配但 attach 失败 | 旧 instance 还在持有该 Static IP | `aws lightsail detach-static-ip` 再重 attach；或 `recreate=true` 重来 |
| GHCR pull 401 | PAT 过期 / 写错 SSM 路径 | 用 1.4 重新 put-parameter |

## 7) Acceptance（机械化输出）

完成 1 个 Lightsail edge expansion 后，给一个 5 行 acceptance：

```text
edge_id        : <id>
lightsail_region: <region>
domain         : api-<id>.tokenkey.dev
managed_instance: mi-XXXXXXXXXXXXXXXXX
last_smoke_run : <gh run URL>
```

数据来自 workflow Job summary，不要在 SKILL 里手抄常量。
