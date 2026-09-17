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
- `TK_SMOKE_API_KEY`（secret）— **仅**计划跑 `operation=smoke` 且显式 `smoke_phase=main-via-edge` 的 edge 需要。
  当前 prod 惯例：**只**在 `edge-uk1` / `edge-us1` 配置即可；其它 edge（如 us2/us3/us4）
  **可不配**——缺 secret 时 `edge_post_deploy_smoke.sh` 跳过 main-via-edge 段；upgrade/rollback
  默认 upgrade/rollback 走 **infra**；显式 `--smoke-phase full` 才跑容器内 edge-native OAuth 探针，不依赖该 key。GitHub secret 只写不可读，无法从 uk1 机械复制。

Smoke base URL 与 Edge 本机默认 model 清单在代码内固定（`https://api.tokenkey.dev` / `claude-sonnet-4-6`），无需 Environment var；如需覆盖，使用 `TK_SMOKE_EDGE_LOCAL_CHAT_MODELS`。

**飞书告警:不要按 edge 配。** webhook/secret 是**仓库级** secrets `TK_FEISHU_WEBHOOK_URL` / `TK_FEISHU_SIGNING_SECRET`(配一次,对所有 `edge-*` / `prod` Environment 可见),部署时由 `ops/stage0/sync-feishu-config.sh` 自动注入新边并启用 + 写后回读自验(配不齐则部署红)。别在 `edge-<id>` Environment 建同名密钥(会覆盖 repo 级)。新边 provision/upgrade 后即具备账号失效 / P0 告警能力,无手工步骤。详见 `deploy/aws/README.md` §「飞书告警自动接入」。

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

新 edge **若不跑 smoke**，可跳过 `TK_SMOKE_API_KEY`（见 §1.5 说明；uk1/us1 以外的 edge 默认不配）。

#### 1.3b Lightsail-only edge：**不要**加 EC2 CFN execution role

`us2` / `us3` / `us4`、已完成 EC2→Lightsail 的 `uk1` 等 **只在 Lightsail 矩阵 `deployable=true`** 的 edge：

- OIDC 只需 `cicd-oidc.yaml` → `AllowedSubjects` 含 `environment:edge-<id>`（§1.5a）。
- **不要**在 `cicd-oidc.yaml` 新增 `Edge<PascalCase>CloudFormationExecutionRoleArn` / `EdgeXTargetInstanceId` / 区域 SSM `ssm:SendCommand` 到 EC2 instance ARN——那是已于 2026-06-07 退役的 EC2/CFN edge 路径,不要复活。
- 运维入口：`deploy-edge-lightsail-stage0.yml` + SSM Hybrid tag `EdgeId` / `Platform=lightsail`。

若误加了 uk1 EC2 IAM：只清残留 IAM/文档；edge 已无 EC2 迁移流程，新增 edge 只用本 skill。

### 1.6 PR + 落库

```bash
git checkout -b chore/lightsail-edge-<edge_id>-register
git commit -am "feat(edge-lightsail): register <edge_id> matrix entry"
gh pr create --fill --base main
```

待 CI 全绿合 main。
