---
name: tokenkey-stage0-edge-lightsail-expansion
description: >-
  Add a TokenKey Stage0 Edge gateway on AWS Lightsail. Use for new edge registration, Lightsail provisioning, DNS pointing, smoke checks, upgrades, or rollbacks on the current edge platform.
---

# TokenKey：新增 Lightsail Edge 网关全流程

适用于 TokenKey fork of sub2api。Lightsail 是 edge 的唯一路径（EC2/CFN edge 已移除）；prod 主网关仍是 EC2/CFN。
权威纪律以仓库根 `CLAUDE.md` 为准（ARM 多架构镜像、release/deploy 顺序、preflight 不绕过）。
默认路径与放弃决策见 `docs/spec-delta/edge-lightsail.md`。

## 执行 owner

矩阵解析用 `deploy/aws/lightsail/resolve-edge-lightsail-target.py`；路由与 SSM 由 `ops/stage0/edge_routing_matrix.py`、`edge_ssm_execution.py`（admin：`edge_admin_resolve_target.py`）统一处理。可部署集合由 `python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable` 生成，bootstrap 用 `deploy/aws/lightsail/render-bootstrap.sh`。

成本、区域、IAM scope 与 DNS 是决策项；注册、provision、凭据、防火墙和迁移命令按下方对应细则执行。

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

在当前授权 checkout 运行 `bash scripts/preflight.sh`；需要切换工作树时使用 `git-worktree-submodule` 技能。发布版本与 workflow ref 须明确，不在此入口隐式切换 main。

确认：

- 本机有 `gh`、`aws`（or `aws-vault`）、`jq`。
- 仓库 var `AWS_OIDC_ROLE_ARN` 已配置；`vars.EDGE_ACME_EMAIL`、`vars.EDGE_MAIN_GATEWAY_ALLOWED_CIDR` 已在 `edge-<edge_id>` Environment 配齐（**EDGE_MAIN_GATEWAY_ALLOWED_CIDR 没有默认值**，workflow 会在缺失时 `::error::` 直接挂）。
- 若该 edge 要跑含 main-via-edge 的 smoke：`TK_SMOKE_API_KEY` secret 已在对应 Environment 配置。

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

验收：`curl -sS https://api-<edge_id>.tokenkey.dev/health` → `{"status":"ok"}`。

## 4) Smoke

```bash
bash scripts/stage0/dispatch-edge-deploy.sh --edge-id <edge_id> --operation smoke
```

dispatch 只提交 workflow；定位本次 edge / operation / ref 对应的 run 后，用 `gh run watch <run_id> --exit-status` 等待终态，不把列表第一条当成本次运行。

接 `ops/stage0/external_health.sh` + `ops/stage0/edge_post_deploy_smoke.sh`（与 EC2 共用）。

**Smoke 范围（operator 选择）**：`main-via-edge` 经 prod 中转，需该 Environment 的 `TK_SMOKE_API_KEY`；缺 key 时脚本跳过，不能报告已验证。适用目标按当前矩阵与环境配置判断，不维护 edge 名单。

`operation=upgrade` / `rollback` 默认 **infra**（workflow log：`tk_edge_post_deploy_smoke: OK phase=infra`）；首次 OAuth 拟真验收显式 `--smoke-phase full`。默认 `operation=smoke` 仍为 **full**。

## 5) Upgrade / Rollback

```bash
bash scripts/stage0/dispatch-edge-deploy.sh --edge-id <edge_id> --operation upgrade --tag <new_tag>
```

回滚用 `--operation rollback --tag <previous_prod_tag>`；需要指定 workflow ref 时传 `--ref`。按 Smoke 节匹配并等待本次 run。

## 6) 已知失败模式与定位

遇到 SSM Hybrid 注册、配额、PAT、DNS/ACME 或升级失败时读取。 见 [操作细则](references/troubleshooting.md)。

## 7) Acceptance（机械化输出）

完成后从 workflow Job summary 与 verify 脚本生成验收摘要：

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

`operation=full` 顺序见参数表；每一步都须取得对应验收证据，未完成项如实报告。
