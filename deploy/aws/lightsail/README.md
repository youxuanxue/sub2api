# Edge Stage0 on AWS Lightsail（edges 唯一路径）

> **Lightsail 是 edge 的唯一路径**（2026-06-07 起；EC2/CFN 的 edge 路径已移除）。
> **prod 主网关仍是 EC2/CFN（`tokenkey-prod-stage0`），不在本目录范围。**
> 背景见 `docs/spec-delta-edge-lightsail.md` 与 `docs/deploy/tokenkey-multiregion-egress-gateway-plan.md` §6（已标 superseded）。

Lightsail Edge 与 prod Stage0 **共用**：

- `deploy/aws/stage0/docker-compose.yml`、`Caddyfile.edge`
- `ops/stage0/verify_ghcr_manifest.sh`、`deploy_via_ssm.sh`、`edge_post_deploy_smoke.sh`

差异：无 CloudFormation；实例由 Lightsail API 创建；SSM 通过 **Hybrid Activation** 注册为 `mi-*` 节点。

## 目录

```text
deploy/aws/lightsail/
├── edge-targets-lightsail.json      # 矩阵（lightsail_region / bundle_id / instance_name）
├── resolve-edge-lightsail-target.py
├── render-bootstrap.sh              # 生成 generated-launch-script.sh
├── provision-edge.sh                # provision 主脚本
└── generated-launch-script.sh       # render-bootstrap 产物（gitignore 可选）
```

Addon IAM：`deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml`

Workflow：`.github/workflows/deploy-edge-lightsail-stage0.yml`

## 一次性 Setup

### 1) 部署 Lightsail IAM Addon

```bash
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-cicd-lightsail-addon \
  --template-file deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml \
  --capabilities CAPABILITY_NAMED_IAM
# default GitHubOidcRoleNames covers both regional OIDC roles
# (us-east-1 + eu-west-2); override only when adding new region-scoped roles.
```

### 2) 各 Edge region 写入 GHCR PAT

路径默认：`/tokenkey/lightsail/<edge_id>/ghcr/pat`（与 EC2 Edge 的 `/tokenkey/edge/<id>/ghcr/pat` 分开）。

```bash
aws ssm put-parameter --region eu-west-2 \
  --name /tokenkey/lightsail/uk1/ghcr/pat --type SecureString \
  --value 'ghp_...'
```

### 3) GitHub Environment

复用 `edge-uk1` / `edge-us1`（与 EC2 workflow 相同 Environment 名，但 **不要** 对同一 edge 混跑两种 provision）。

Variables：`EDGE_ACME_EMAIL`、`EDGE_MAIN_GATEWAY_ALLOWED_CIDR`  
Secrets：`TK_SMOKE_EDGE_CANARY_KEY`

## 初次 Provision

```bash
TAG=X.Y.Z
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=uk1 \
  -f operation=provision \
  -f tag=$TAG \
  -f confirm_instance=tokenkey-edge-uk1-ls
```

Workflow summary 会输出 Static IP → 手工配置 DNS A 记录 → 再 smoke：

```bash
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=uk1 \
  -f operation=smoke \
  -f confirm_instance=tokenkey-edge-uk1-ls
```

### 冒烟 / 本机 curl 对域名长期 `HTTP 000` 或 TCP 超时

Lightsail **实例控制台「Networking」防火墙**的硬化基线是**仅放行 TCP 443**（HTTPS + ACME TLS-ALPN-01）；
**80 与 22 关闭**（证书走 TLS-ALPN-01 无需 80；SSH 走 SSM / 控制台 browser SSH，不依赖公网 22）。仅靠正确 DNS（A → Static IP）不够；若 443 没开，`curl https://api-…/health` 会与 GitHub runner 一致：约 130s 级连接超时。

- 自检：`aws lightsail get-instance-port-states --region <region> --instance-name <instance_name>`（期望仅 443 open）
- 一次性修复 / 收口到基线（与 provision 脚本行为一致，原子覆盖整张防火墙表）：  
  `bash ops/stage0/verify-edge-lightsail-network.sh <edge_id> --enforce-ports`  
  其内部即 `aws lightsail put-instance-public-ports --region <region> --instance-name <instance_name> --port-infos fromPort=443,toPort=443,protocol=tcp,cidrs=0.0.0.0/0`（put = 替换语义，顺带关掉默认 22 与历史 80）。
- IaC：`provision-edge.sh` 会在 attach Static IP 后用 `put-instance-public-ports` 设为 **仅 443**；CI OIDC role 需在
  `cicd-oidc-lightsail-addon.yaml` 中包含 `lightsail:PutInstancePublicPorts`（及 `GetInstancePortStates`、保留 `OpenInstancePublicPorts` 供 legacy `--fix-443`）。

### Admin 登录 / 忘记密码

- 抓首次启动生成的一次性口令（落盘 `~/Codes/keys/tokenkey-<edge_id>-admin-password.txt`）：  
  `bash ops/stage0/ensure-edge-admin-credentials.sh --platform lightsail <edge_id>`（先试 capture，日志缺失则 reset；**禁止打印 password**）。
- 重置随机密码（同上 keys 文件）：  
  `bash ops/stage0/reset-edge-admin-password.sh <edge_id>` 或 `--platform lightsail <edge_id>`。

### 其它运维脚本（与 EC2 Edge 对齐）

Lightsail Edge 启用后，沿用同一 **`edge_routing_matrix`/`edge_ssm_execution` 路由**：

- **`python3 ops/stage0/edge_ssm_execution.py --repo-root . --edge-id uk1 [--format env|json]`**  
  输出 SSM **`REGION`** + **`INSTANCE_ID`**（`mi-*` 来自 Parameter Store；EC2 为 `i-*`）。与 **`ops/stage0/edge_admin_resolve_target.py` auto** 规则一致。
- **`ops/observability/run-probe.sh --target edge:<id>`**（默认 `ALLOW_PLANNED` 未设置）走上述脚本；若要探 **planned 仅 EC2 矩阵条目**，仍可设 **`ALLOW_PLANNED=1`** 走 **`resolve-edge-target.py` + CFN**。
- **`ops/anthropic/manage-anthropic-config.py`**、**`rebalance-anthropic-priority.py`**、`snapshot`/`apply`/`check` **与 `check-edge-oauth-stability.py`**：**snapshot** / **护栏**在多矩阵下按 Lightsail **`deployable=true` 优先**解析实例（与 **`edge_routing_matrix.py`** 一致）。
- **`python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable`**：列出 **EC2 ∪ Lightsail 有效 deployable** 的 edge id（可选 **`--lightsail-matrix`**）。

## 升级 / 回滚

```bash
gh workflow run deploy-edge-lightsail-stage0.yml \
  -f edge_id=uk1 \
  -f operation=upgrade \
  -f tag=$TAG \
  -f confirm_instance=tokenkey-edge-uk1-ls
```

## 与 EC2 Edge 的关键差异

| 维度 | EC2 Edge | Lightsail Edge |
|------|----------|----------------|
| IaC | `stage0-edge-ec2.yaml` | Lightsail API + SSM 参数状态 |
| 运维入口 | EC2 SSM（原生 instance profile） | SSM Hybrid（`mi-*`） |
| 静态 IP | EIP + CFN Retain/IMPORT | Lightsail Static IP |
| IP 污染轮换 | `tokenkey-stage0-edge-ip-rotation` skill（EIP） | `tokenkey-stage0-edge-lightsail-ip-rotation` skill + `ops/lightsail/rotate-static-ip.sh` |
| 区域 | 与 EC2 region 一一对应 | **Paris 无 Lightsail**；fra1 映射 Frankfurt |
| 架构 | Graviton arm64（t4g） | Lightsail bundle 多为 x86（multi-arch 镜像仍可用） |
| Swap | CFN user-data 配 **2 GiB** `/swapfile` | matrix `swap_gib` → `provision-edge.sh` → bootstrap `SWAP_SIZE_GIB`（默认 2 GiB）；已运行实例可经 SSM 跑 `ops/lightsail/ensure-edge-swap.sh` |
| diagnostics | `ops-daily-diagnostics.yml` 矩阵 | `ops-daily-diagnostics.yml` 矩阵自动接入（按 `platform` 分支；error_clustering installer 暂仍 EC2 only） |
| 成本 | ~$10–16/月（t4g.micro） | ~$12/月（micro bundle，因区域而异） |

## 复盘：Lightsail 侧常见出错点（避免沿用 EC2 心智）

以下来自线上/CI 与研发流程中的反复踩坑，**与「实例在 Lightsail、控制面在 SSM Hybrid」这一组合强相关**：

1. **SSM / AWS CLI 的 `--region`**：凡针对该 edge 的 `aws ssm`（读 `/tokenkey/lightsail/<edge_id>/…`、后续 SendCommand 的前置解析），必须落在矩阵里的 **`lightsail_region`**。字段 **`ec2_equivalent_region` 只用于对账/文档**，不能拿来当 SSM region（否则会出现「实例与参数在同一区，但 CLI 跑到另一区」——包括 **GHA smoke** 里曾出现的 **region / 工作目录** 不一致类失败）。
2. **GitHub Actions 里跑 smoke / compose**：确保 job 的 **`working-directory`**（或显式 `cd`）与脚本里相对路径一致（仓库根 vs `deploy/aws/stage0`）。错目录会表现为找不到 `docker-compose.yml`、或子脚本解析到错误 `REPO_ROOT`。
3. **合并 PR 后的本地清理**：若使用 **squash merge**，`feature` 分支 tip **不是** `main` 的祖先，`git branch -d feature/...` 可能提示「未合并到 HEAD」。应先 `git checkout main && git pull --ff-only`，再对已确认进 `main` 的分支使用 **`git branch -D …`**（并 `git remote prune origin` 清 stale 跟踪分支）。
4. **preflight 的 `silent-error-swallow` 警告**：`capture-edge-admin-credentials.sh`、`run-probe.sh` 等处对多路日志源 / 可选 `aws` 探测使用 `|| true`、`2>/dev/null` 属于**刻意**「多源尝试、缺一不必失败」。若新增类似写法且确属合法吞错，在对应行加 **`# preflight-allow: swallow`** 并写清理由，避免审查时误判。

## 本地校验

```bash
bash deploy/aws/lightsail/render-bootstrap.sh --check       # 漂移门禁（已接入 preflight）
python3 -m unittest discover -s deploy/aws/lightsail -p 'test_*.py' -t deploy/aws/lightsail
python3 -m unittest deploy.aws.stage0.test_resolve_edge_target_prod_ops_matrix_lightsail -v
PREFLIGHT_BASE=origin/main bash scripts/preflight.sh
```

实机 provision/smoke 需账户内 Lightsail 权限与 PAT；首跑由
`tokenkey-stage0-edge-lightsail-expansion` skill 走 `operation=full` 驱动。

### Provision：`SSM managed instance not registered within timeout`

- **常见假阴性（已修复）**：`DescribeInstanceInformation` 不允许把「标签过滤器」与其它过滤器混在一起；也不得依赖 `ComputerName` 默认等于 Lightsail `instance_name`（AL2023 常为 DHCP hostname）。provision 脚本现以 **`ActivationIds` = 本次 Hybrid activation** 作为主查询，并在 bootstrap 里 `hostnamectl set-hostname` 对齐实例名。
- **仍超时**：Lightsail 浏览器 SSH 查看 `/var/log/tokenkey-lightsail-bootstrap.log`；失败行以 `BOOTSTRAP_FAIL:` 开头。Workflow 末尾会打印 `describe-activations` 帮助判断 activation / 配额是否用尽。

## uk1：`api-uk1.tokenkey.dev` 权威平台（Lightsail）

矩阵基线：**EC2 `uk1.deployable=false`**（`deploy/aws/stage0/edge-targets.json`），**Lightsail `uk1.deployable=true`**（本目录 `edge-targets-lightsail.json`）。
同名域名只能由一个平台对外服务；两边的 `deployable` 同时为 `true` 会被 **`scripts/checks/edge-platform-exclusivity.py`** 拦下。

**Porkbun（prod，`api-uk1.tokenkey.dev`）：** A 记录必须等于 Lightsail Static IP（`aws lightsail get-static-ip --static-ip-name tokenkey-edge-uk1-ls-ip` 的 `ipAddress`）。当前矩阵记在 `edge-targets-lightsail.json` → **`targets.uk1.porkbun_a_ipv4`**（**`13.134.80.182`**），作为人工 DNS **唯一真值**。

**不能与 EC2 EIP 混用：** 旧 EC2 Edge 的 Elastic IP（例如历史 **`16.61.87.51` / `eipalloc-03b2653ddd57b9c93`**）**无法挂到 Lightsail 实例**。若 Porkbun 仍指向已游离的 EC2 EIP，公网会超时。迁到 Lightsail 后必须把 A 记录改到 **Lightsail Static IP**；不再需要的老 EIP 可通过 `release-address` 回收。

核对顺序：先以 **`aws lightsail get-static-ip`** 为真，再改 Porkbun 与本仓库 `porkbun_a_ipv4`，不得在未核对前提下漂移。

端到端实操（provision、旁路校验、DNS 核对、Anthropic OAuth 重建、Smoke）：**`.cursor/skills/tokenkey-stage0-edge-lightsail-expansion/SKILL.md`**（等价副本在 `.claude/skills/…`）。
Lightsail **首次**拉起实例用 `deploy-edge-lightsail-stage0.yml` **`operation=provision`**；镜像 tag 轮转用 **`upgrade` / `rollback`**。

## IP 污染轮换

```bash
bash ops/lightsail/rotate-static-ip.sh <edge_id>          # 计划（dry run）
bash ops/lightsail/rotate-static-ip.sh <edge_id> --apply  # 三步 swap（destructive）
```

详见 `tokenkey-stage0-edge-lightsail-ip-rotation` skill。
