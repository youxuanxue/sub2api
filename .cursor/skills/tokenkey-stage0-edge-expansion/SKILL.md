---
name: tokenkey-stage0-edge-expansion
description: >-
  End-to-end runbook for adding a new AWS Stage0 Edge gateway beyond existing
  uk1/fra1: prepare metadata and IAM/OIDC scope, provision edge stack, set DNS,
  run smoke/upgrade/rollback via deploy-edge-stage0.yml, and report structured
  acceptance results with known failure patterns.
---

# TokenKey：新增任意 Edge 网关全流程（uk1/fra1 之外）

适用于本仓库（TokenKey fork of sub2api）。目标是把一个新 edge（例如 us1/sg1 或未来任意 `<edge_id>`）从“计划中”推进到“可 provision + 可 smoke + 可回滚”。

权威纪律以仓库根 `CLAUDE.md` 为准（ARM、多架构发布、release/deploy 顺序、禁止绕过 preflight）。

## 调用参数

```text
/tokenkey-stage0-edge-expansion edge_id=<id> region=<aws-region> operation=<prepare|provision|smoke|upgrade|rollback|full> [tag=X.Y.Z] [previous_tag=X.Y.Z] [confirm_stack=tokenkey-edge-<id>-stage0]
```

| 参数 | 语义 |
|---|---|
| `edge_id` | 新 edge 标识，如 `us1`、`sg1`。 |
| `region` | 目标 AWS 区域，如 `us-west-2`。 |
| `operation=prepare` | 只做“接入准备与权限开通”（代码/配置/IAM），不部署实例。 |
| `operation=provision` | 首次创建或更新 edge stack（`deploy-edge-stage0.yml operation=provision`）。 |
| `operation=smoke` | 只做 smoke 验收（基础设施 + SSM self-smoke）。 |
| `operation=upgrade` | 对已存在 edge 升级到指定 tag。 |
| `operation=rollback` | 回滚到 `previous_tag`。 |
| `operation=full` | 从 prepare 到 provision、DNS、smoke 的完整初始化闭环。 |
| `tag` | `provision/upgrade/rollback` 必填（无 `v` 前缀）。 |
| `confirm_stack` | 默认 `tokenkey-edge-<edge_id>-stage0`。 |

默认行为：
- 用户说“新增/初始化某 edge” → `operation=full`
- 用户说“先打通权限/配置” → `operation=prepare`
- 用户说“DNS 好了继续验收” → `operation=smoke`

## 一次性跑完（原则）

- 按顺序推进：`prepare` → `provision` → DNS → `smoke`。
- `gh run watch` 必须 `--exit-status` 跟到终态，不中途截断。
- 失败先定位根因再重跑，不做“盲目多次重试”。
- 任何新增 edge 都必须复用现有共享 primitive（`deploy-edge-stage0.yml` + `ops/stage0/*.sh`），避免分叉语义。

## 0) 前置检查

1. 同步仓库：
   ```bash
   git fetch origin main --tags
   git checkout main
   git pull origin main --ff-only
   ```
2. 读取 edge 目标定义：`deploy/aws/stage0/edge-targets.json`
3. 确认 `gh` 与 `aws` 可用，且有权限操作：
   - GitHub environments/variables/secrets
   - CloudFormation `tokenkey-cicd-oidc`
   - IAM role `tokenkey-gha-us-east-1-error-clustering`

## 1) Prepare：把新 edge 接入控制面

### 1.1 更新 edge 目标注册（代码）

编辑 `deploy/aws/stage0/edge-targets.json` 新增或更新 `<edge_id>`：
- `deployable`（首次建议先 `false`，准备就绪再置 `true`）
- `region`
- `domain`（例如 `api-<edge_id>.tokenkey.dev`）
- `stack`（`tokenkey-edge-<edge_id>-stage0`）
- `ssm_prefix`（`/tokenkey/edge/<edge_id>`）

### 1.2 更新 deploy-edge workflow 入口（代码）

编辑 `.github/workflows/deploy-edge-stage0.yml`：
1. `workflow_dispatch.inputs.edge_id.options` 加入新 `<edge_id>`。
2. `Provision or update Edge stack` 步骤中的 `case "$EDGE_ID"` 新增映射：
   - `CFN_ROLE_OUTPUT_KEY=Edge<PascalCaseEdgeId>CloudFormationExecutionRoleArn`

### 1.3 更新 OIDC / IAM 模板（代码）

编辑 `deploy/aws/cloudformation/cicd-oidc.yaml`：
1. `AllowedSubjects` 默认列表加入：`repo:youxuanxue/sub2api:environment:edge-<edge_id>`。
2. 新增参数：
   - `<EdgeXRegion>`
   - `<EdgeXTargetInstanceId>`（可空，首次可留空）
3. 新增条件 `HasEdgeXInstance`。
4. 在 `ClusteringRole` 的 `SsmSendCommandStage0` 策略新增该 edge 的 `ssm:SendCommand` 语句。
5. 新增 CloudFormation execution role（命名遵循现有：`tokenkey-cfn-<region>-edge-<edge_id>-stage0`）。
6. 新增输出：`Edge<PascalCaseEdgeId>CloudFormationExecutionRoleArn`。

关键坑位（必须做对）：
- edge 的 `AWS-RunShellScript` 文档 ARN 必须用该 edge 区域：
  - `arn:aws:ssm:${EdgeXRegion}::document/AWS-RunShellScript`
- 不能写成 `${AWS::Region}`（这会在 us-east-1 OIDC 栈场景导致跨区 `ssm:SendCommand` 被拒）。

### 1.4 部署 OIDC 控制栈

```bash
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-cicd-oidc \
  --template-file deploy/aws/cloudformation/cicd-oidc.yaml \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides ...
```

说明：
- 首次可把 `<EdgeXTargetInstanceId>` 置空（只先打通权限框架）。
- edge 实例创建后再回填该参数并再次 deploy，启用精确 `ssm:SendCommand` 资源授权。

### 1.5 GitHub Environment 准备

新建环境：`edge-<edge_id>`，并配置变量（参考 `edge-uk1`/`edge-fra1`）：
- `AWS_OIDC_ROLE_ARN`
- `AWS_OIDC_STACK_NAME`
- `EDGE_ACME_EMAIL`
- `EDGE_MAIN_GATEWAY_ALLOWED_CIDR`
- `EDGE_MAIN_GATEWAY_BASE_URL`
- `EDGE_GHCR_PAT_SSM_NAME=/tokenkey/edge/<edge_id>/ghcr/pat`

注意：`EDGE_GHCR_PAT_SSM_NAME` 必须是该 edge 自己路径，不可复用别的 edge 路径。

### 1.6 区域 SSM 参数准备

在目标 region 写入（至少）：
- `/<project>/edge/<edge_id>/ghcr/pat`（SecureString）
- 以及模板/脚本依赖的 stage0 参数（compose/caddy/template 等）

## 2) Provision：首次初始化

触发 workflow：

```bash
gh workflow run deploy-edge-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=provision \
  -f tag=<X.Y.Z> \
  -f confirm_stack=tokenkey-edge-<edge_id>-stage0
```

然后 watch：

```bash
gh run watch <run-id> --exit-status
```

失败优先检查：
- OIDC assume role 是否允许 `environment:edge-<edge_id>`
- `CFN_ROLE_OUTPUT_KEY` 映射是否缺失
- `EDGE_GHCR_PAT_SSM_NAME` 是否存在于目标 region
- cloud-init 是否执行完成（`/var/lib/cloud/instance/scripts/part-001`）

## 3) 初始 admin 账密保存（首次初始化后立即执行）

### 3.1 默认账号与保存位置

- 默认邮箱：`admin@<api-domain>`（若 CFN 未显式传 `AdminEmail`）。
- `AUTO_SETUP` 首次创建 admin 时，若 `ADMIN_PASSWORD` 为空，会生成一次性随机密码并写入日志。
- 初始和重置 admin 账密必须保存到 `$HOME/Codes/keys/tokenkey-<edge_id>-admin-password.txt`，格式与 `tokenkey-uk1-admin-password.txt` 一致：`email=...`、`password=...`。
- 禁止在终端、PR、issue、日志摘要或聊天中打印密码；只报告保存路径和状态。

### 3.2 线上日志保存（优先）

```bash
EDGE_ID=<edge_id>
REGION=<edge-region>
STACK=tokenkey-edge-<edge_id>-stage0
KEYS_DIR=$HOME/Codes/keys
CREDENTIAL_FILE="$KEYS_DIR/tokenkey-$EDGE_ID-admin-password.txt"
INSTANCE_ID=$(aws cloudformation describe-stacks \
  --region "$REGION" \
  --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' \
  --output text)

CMD_ID=$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --parameters 'commands=[
    "set -euo pipefail",
    "sudo grep \"^ADMIN_EMAIL=\" /var/lib/tokenkey/.env || true",
    "sudo docker logs tokenkey 2>&1 | grep -E \"Generated admin password\" || true",
    "sudo journalctl -u tokenkey.service --no-pager | grep -E \"Generated admin password\" || true",
    "sudo grep -E \"Generated admin password|ADMIN_EMAIL\" /var/log/tokenkey-edge-bootstrap.log || true"
  ]' \
  --query 'Command.CommandId' --output text)

aws ssm wait command-executed \
  --region "$REGION" \
  --command-id "$CMD_ID" \
  --instance-id "$INSTANCE_ID" || true

RAW_OUTPUT=$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$CMD_ID" \
  --instance-id "$INSTANCE_ID" \
  --query 'StandardOutputContent' --output text)

ADMIN_EMAIL=$(printf '%s\n' "$RAW_OUTPUT" | grep -Eo 'ADMIN_EMAIL=[^[:space:]]+' | tail -n 1 | cut -d= -f2-)
ADMIN_PASSWORD=$(printf '%s\n' "$RAW_OUTPUT" | sed -nE 's/.*Generated admin password \(one-time\): ([^[:space:]]+).*/\1/p' | tail -n 1)
if [ -z "$ADMIN_EMAIL" ] || [ -z "$ADMIN_PASSWORD" ]; then
  echo "[warn] initial admin credential not found in logs; run reset script instead" >&2
else
  umask 077
  {
    printf 'email=%s\n' "$ADMIN_EMAIL"
    printf 'password=%s\n' "$ADMIN_PASSWORD"
  } >"$CREDENTIAL_FILE"
  chmod 600 "$CREDENTIAL_FILE"
  echo "[ok] admin credentials saved to $CREDENTIAL_FILE"
fi
unset RAW_OUTPUT ADMIN_PASSWORD
```

### 3.3 若查不到初始密码（常见）

直接重置：

```bash
bash ops/stage0/reset-edge-admin-password.sh edge-<edge_id>
```

脚本只打印状态和 `CREDENTIAL_FILE`，不会打印密码。登录后立即改为长期密码。

## 4) DNS

从 stack 输出拿到公网 IP（或 EIP），配置：
- `api-<edge_id>.tokenkey.dev` → 对应 A 记录

DNS 生效后继续 smoke。

## 5) Smoke 验收

触发：

```bash
gh workflow run deploy-edge-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=smoke \
  -f confirm_stack=tokenkey-edge-<edge_id>-stage0
```

验收标准：
1. `GET https://api-<edge_id>.tokenkey.dev/health` 为 200。
2. public runner `GET /v1/models` 为 403（allowlist 生效）。
3. SSM self-smoke 成功（容器与本地 health 正常）。
4. 若配置 `MAIN_GATEWAY_EDGE_SMOKE_API_KEY`，则 main-gateway-via-edge 业务 smoke 也通过。

若失败：
- `ssm:SendCommand` AccessDenied：
  - 检查 OIDC 栈参数 `<EdgeXTargetInstanceId>` 是否已回填实例 ID。
  - 检查策略中文档 ARN 是否为 `arn:aws:ssm:<edge-region>::document/AWS-RunShellScript`。

## 6) Upgrade / Rollback（日常）

### upgrade

```bash
gh workflow run deploy-edge-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=upgrade \
  -f tag=<X.Y.Z> \
  -f confirm_stack=tokenkey-edge-<edge_id>-stage0
```

### rollback

```bash
gh workflow run deploy-edge-stage0.yml \
  -f edge_id=<edge_id> \
  -f operation=rollback \
  -f tag=<PREVIOUS_X.Y.Z> \
  -f confirm_stack=tokenkey-edge-<edge_id>-stage0
```

回滚后必须重新 smoke。

## 7) `operation=full` 执行清单

按以下顺序执行：
1. 完成 Prepare（1.1~1.6）
2. 执行 Provision
3. 配置并确认 DNS 生效
4. 执行 Smoke
5. 输出结构化结果：
   - edge_id / region / stack / domain
   - provision run id + status
   - smoke run id + status
   - external health / 403 path / SSM self-smoke
   - 是否覆盖 main-gateway-via-edge（若缺密钥明确标注“未覆盖”）

## 8) 提交前自检

若本次改了仓库文件（workflow/template/json）：

```bash
bash scripts/preflight.sh
```

通过后再提交。

## 故障速查

| 现象 | 根因 | 处理 |
|---|---|---|
| `AssumeRoleWithWebIdentity` 拒绝 | AllowedSubjects 缺 `environment:edge-<edge_id>` | 更新 `cicd-oidc.yaml` 并 deploy |
| provision 报找不到 CFN role output | workflow 未加 `CFN_ROLE_OUTPUT_KEY` 映射 | 更新 `.github/workflows/deploy-edge-stage0.yml` |
| cloud-init 拉镜像失败 | `EDGE_GHCR_PAT_SSM_NAME` 错路径或参数缺失 | 在目标 region 写入 `.../edge/<edge_id>/ghcr/pat` |
| smoke 外部 health 000 | DNS 未生效或服务未起来 | 先查 DNS，再查实例 `tokenkey.service` / compose |
| smoke 报 `ssm:SendCommand` 拒绝 | 实例 ARN/文档 ARN 策略不匹配 | 回填 `<EdgeXTargetInstanceId>`，并确保文档 ARN 用 `<EdgeXRegion>` |

## 扩展阅读

- `.github/workflows/deploy-edge-stage0.yml`
- `deploy/aws/cloudformation/cicd-oidc.yaml`
- `deploy/aws/stage0/edge-targets.json`
- `deploy/aws/cloudformation/stage0-edge-ec2.yaml`
- `ops/stage0/edge_post_deploy_smoke.sh`
- `deploy/aws/README.md`

## 工具脚本：重置 Edge admin 密码（随机）

已提供脚本：`ops/stage0/reset-edge-admin-password.sh`

用法：

```bash
bash ops/stage0/reset-edge-admin-password.sh edge-fra1
# 或
bash ops/stage0/reset-edge-admin-password.sh fra1
```

行为：
- 自动从 `deploy/aws/stage0/edge-targets.json` 解析 `region/stack`
- 自动从 stack 输出解析 `InstanceId`
- 通过 SSM 在实例上读取 `ADMIN_EMAIL`
- 随机生成新密码并重置 admin（bcrypt/pgcrypto）
- 保存 `email=...` / `password=...` 到 `$HOME/Codes/keys/tokenkey-<edge_id>-admin-password.txt`
- 最终只打印状态与 `CREDENTIAL_FILE`，不打印密码

注意：该脚本禁止明文打印新密码；执行后从本地 keys 文件读取并登录，再立即改成你的长期密码。
