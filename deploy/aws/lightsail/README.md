# Edge Stage0 on AWS Lightsail（并行路径）

> **默认路径仍是 EC2/CFN**（`deploy-edge-stage0.yml`）。本目录是 Lightsail 实验/降本并行栈；
> 规划依据见 `docs/deploy/tokenkey-multiregion-egress-gateway-plan.md` §6.1 与
> `docs/spec-delta-edge-lightsail.md`。

Lightsail Edge 与 EC2 Edge **共用**：

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
  --parameter-overrides GitHubOidcRoleName=tokenkey-gha-us-east-1-error-clustering \
  --capabilities CAPABILITY_NAMED_IAM
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

Variables：`EDGE_ACME_EMAIL`、`EDGE_MAIN_GATEWAY_ALLOWED_CIDR`、`EDGE_MAIN_GATEWAY_BASE_URL`  
Secrets：`MAIN_GATEWAY_EDGE_SMOKE_API_KEY`

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
| diagnostics | `ops-daily-diagnostics.yml` 矩阵 | `ops-daily-diagnostics.yml` 矩阵自动接入（按 `platform` 分支；error_clustering installer 暂仍 EC2 only） |
| 成本 | ~$10–16/月（t4g.micro） | ~$12/月（micro bundle，因区域而异） |

## 本地校验

```bash
bash deploy/aws/lightsail/render-bootstrap.sh --check       # 漂移门禁（已接入 preflight）
python3 -m unittest discover -s deploy/aws/lightsail -p 'test_*.py' -t deploy/aws/lightsail
python3 -m unittest deploy.aws.stage0.test_resolve_edge_target_prod_ops_matrix_lightsail -v
PREFLIGHT_BASE=origin/main bash scripts/preflight.sh
```

实机 provision/smoke 需账户内 Lightsail 权限与 PAT；首跑由
`tokenkey-stage0-edge-lightsail-expansion` skill 走 `operation=full` 驱动。

## IP 污染轮换

```bash
bash ops/lightsail/rotate-static-ip.sh <edge_id>          # 计划（dry run）
bash ops/lightsail/rotate-static-ip.sh <edge_id> --apply  # 三步 swap（destructive）
```

详见 `tokenkey-stage0-edge-lightsail-ip-rotation` skill。
