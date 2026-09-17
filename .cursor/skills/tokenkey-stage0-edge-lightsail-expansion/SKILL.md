---
name: tokenkey-stage0-edge-lightsail-expansion
description: >-
  Add a TokenKey Stage0 Edge gateway on AWS Lightsail. Use for new edge registration, Lightsail provisioning, DNS pointing, smoke checks, upgrades, or rollbacks on the current edge platform.
---

# TokenKey：新增 Lightsail Edge 网关全流程

适用于 TokenKey fork of sub2api。Lightsail 是 edge 的唯一路径（EC2/CFN edge 已移除）；prod 主网关仍是 EC2/CFN。
权威纪律以仓库根 `CLAUDE.md` 为准（ARM 多架构镜像、release/deploy 顺序、preflight 不绕过）。
默认路径与放弃决策见 `docs/spec-delta/edge-lightsail.md`。

## 确定性基线（机械化 vs 真判断）

| 步骤 | 类型 | 承载 |
|---|---|---|
| 解析 edge → 区域/AZ/bundle/SSM 前缀 | 机械 | `deploy/aws/lightsail/resolve-edge-lightsail-target.py` |
| Stage0 Lightsail routing / 统一 SSM | 机械 | `ops/stage0/edge_routing_matrix.py` + `ops/stage0/edge_ssm_execution.py`（admin：`edge_admin_resolve_target.py`）；可部署矩阵：`python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable` |
| 渲染 user-data（launch script） | 机械 | `deploy/aws/lightsail/render-bootstrap.sh`（drift gate 已接入 preflight） |
| Provision dispatch + watch | 机械 | `gh workflow run deploy-edge-lightsail-stage0.yml` + `gh run watch --exit-status` |
| 升级/回滚/烟测 dispatch | 机械 | 同上（operation 参数化） |
| Provision 后落盘 admin 账密 | 机械 | `bash ops/stage0/ensure-edge-admin-credentials.sh --platform lightsail <edge_id>` |
| 跨 edge 搬账号+凭据（admin UI 进不去的 live blob，如 kiro OAuth grant）+ groups，server→S3→server | 机械 | `ops/migration/migrate-edge-accounts.py extract\|build\|load`（写侧默认 dry-run，`--execute` 落地；详见 §2.3） |
| 防火墙收口 443-only + DNS 后 HTTPS / ACME 验收 | 机械 | `bash ops/stage0/verify-edge-lightsail-network.sh <edge_id> [--enforce-ports] [--renew-cert]` |
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
| `operation=upgrade` / `rollback` | 通过共享 `ops/stage0/deploy_via_ssm_bluegreen.sh` 换 tag，与 prod 完全相同 primitive。 |
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
- 若该 edge 要跑含 main-via-edge 的 smoke（当前仅 uk1/us1）：`TK_SMOKE_API_KEY` secret 已在对应 Environment 配置。

## 1) Prepare：注册新 Lightsail edge

仅 operation=prepare/full 时读。先明确区域/成本/权限选择，再注册矩阵及 IAM/SSM/PAT；此阶段不创建实例。 见 [操作细则](references/prepare.md)。

## 2) Provision：创建实例

仅 provision/full 或授权迁移账号时读。默认 fail-if-exists；`recreate=true` 是销毁重建，须明确授权；凭据不回显。按细则处理 admin creds、443 防火墙与账号迁移。 见 [操作细则](references/provision.md)。

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

**Smoke 范围（operator 选择）**：可选 `main-via-edge`（经 prod 中转，需 `TK_SMOKE_API_KEY`）目前只对 **uk1 / us1** 启用。
其它 Lightsail edge provision 完成后以 DNS + 可选 `curl https://api-<id>.tokenkey.dev/health` 验收；
`operation=upgrade` / `rollback` 默认 **infra**（workflow log：`tk_edge_post_deploy_smoke: OK phase=infra`）；首次 OAuth 拟真验收显式 `--smoke-phase full`。默认 `operation=smoke` 仍为 **full**。

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

遇到 SSM Hybrid 注册、配额、PAT、DNS/ACME 或升级失败时读取。 见 [操作细则](references/troubleshooting.md)。

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
4. §2.2 防火墙收口 443-only（`verify-edge-lightsail-network.sh --enforce-ports`）
5. §3 DNS A 记录 → Static IP
6. §3 ACME（若 TLS 失败：`--renew-cert`）
7. §4 Smoke（或 uk1/us1 以外 edge 仅 `curl /health`）
8. §7 Acceptance 输出

数据来自 workflow Job summary + verify 脚本，不要在 SKILL 里手抄常量。
