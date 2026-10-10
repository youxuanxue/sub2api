# TokenKey Edge on Hetzner Cloud

审批基线：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

**us5：** 正式 `api-us5.tokenkey.dev` → Hetzner `91.98.83.56`（`mi-00586252d85c7632e`；Caddy 双域名含 `api-us5-hz`）；E5 10min soak 绿；Lightsail 保留回滚（≥7 天）。  
**uk1：** 正式 `api-uk1.tokenkey.dev` → Hetzner `188.245.112.147`；Lightsail 保留回滚（≥7 天）。  
**下一台（未点火）：** 其余 edge 需先合入/部署 Hybrid IAM（见下），再按 L0→E5；**禁止**在上一台 E5 未绿时付费点火。

## 文件

| 路径 | 作用 |
|---|---|
| `edge-targets-hetzner.json` | 矩阵（defaults + edge id） |
| `resolve-edge-hetzner-target.py` | 派生命名 + 硬门禁 |
| `render-bootstrap.sh` | 生成 Ubuntu user-data（SSM + compose） |
| `generated-user-data.sh` | 渲染产物（须与 `--check` 同步提交） |
| `provision-edge.sh` | dry-run 或 `--confirm-paid --tag` 点火 |

## 用法

```bash
gh secret set HCLOUD_TOKEN --body "$HCLOUD_TOKEN"

# dry-run
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation provision --platform hetzner --allow-planned

# 付费点火（GHA：需 AWS OIDC + EDGE_ACME_EMAIL + EDGE_MAIN_GATEWAY_ALLOWED_CIDR）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation provision --platform hetzner --allow-planned \
  --confirm-paid --tag X.Y.Z

# 本机 dry-run / 渲染
python3 deploy/hetzner/resolve-edge-hetzner-target.py --edge-id uk1 --allow-planned
bash deploy/hetzner/render-bootstrap.sh --check
bash deploy/hetzner/provision-edge.sh --edge-id uk1 --allow-planned
```

前置：CFN `cicd-oidc-lightsail-addon` 提供 `tokenkey-hetzner-ssm-hybrid-<edge>`（uk1/us5 已部署；uk2/us3/us4/us6 角色定义在模板中，**需 CFN 更新后**才能点火——更新 CFN 本身不算 edge 切流，但仍是 AWS 变更，需明确批准）。  
user-data **必须以 `#!/bin/bash` 开头**（Ubuntu cloud-init 否则忽略）；AWS CLI 走官方 awscliv2 zip（noble 无 `awscli` apt）。  
点火后把 `api-<edge>-hz.tokenkey.dev` A 指到输出的 `public_ip`，再跑 E0/E1。

**探针：** 勿对 Hetzner 用 `run-probe --target edge:<id>`（会解析到 Lightsail）。对 Hybrid `mi-*` 直投 SSM，并带齐 `probe-contracts.json` companions（含 `smoke_anthropic_realistic.py`）。

## 正式 DNS 切流（复用 uk1/us5）

1. Staging E0–E4 + 短 soak 绿。  
2. Porkbun：`api-<edge>.tokenkey.dev` A → Hetzner IP（Lightsail IP 先留着）。  
3. 主机 `/var/lib/tokenkey/.env`：  
   `API_DOMAIN="api-<edge>.tokenkey.dev, api-<edge>-hz.tokenkey.dev"`  
   （**必须加引号**；逗号未引号时 `source .env` 会炸）。  
4. 热渲染 Caddy（`ops/stage0/sync_caddyfile_via_ssm.sh edge <mi-…>`，Hybrid 已包 `bash -c`）→ ACME 出正式证书。  
5. 外网 health 200 + OAuth probe `servable` → E5 短 soak（本迁移约定 10 min）。

回滚：A 指回 Lightsail；Caddy 仍双域名时 staging/formal 可并存。

## 下一 edge 准备清单（仓库侧，未 apply）

| 项 | 状态 |
|---|---|
| Hybrid IAM uk1/us5 | 已在 AWS |
| Hybrid IAM uk2/us3/us4/us6 | CFN 已 apply（`tokenkey-cicd-lightsail-addon`） |
| 矩阵 `deployable=true` | Phase-1 仍禁止 |
| Porkbun staging `api-*-hz` | 按台临点再改 |
| `sync_caddyfile` dash/pipefail | 已修为 `bash -c` |

## 硬门禁

- `location=fsn1` · `server_type=cax21` · `architecture=arm`
- Phase-1：合并 defaults 后不得 `deployable=true`

```bash
python3 -m unittest deploy/hetzner/test_resolve_edge_hetzner_target.py
python3 -m unittest ops/stage0/test_sync_caddyfile_via_ssm.py
bash deploy/hetzner/render-bootstrap.sh --check
```
