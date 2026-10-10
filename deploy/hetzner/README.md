# TokenKey Edge on Hetzner Cloud

审批基线：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

**下一刀：us5 点火**（staging `api-us5-hz`，不切正式 DNS）。  
**uk1：** 正式 `api-uk1.tokenkey.dev` 已切 Hetzner `188.245.112.147`；Lightsail 实例保留作回滚（≥7 天）。

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

前置：CFN `cicd-oidc-lightsail-addon` 已含 `tokenkey-hetzner-ssm-hybrid-{uk1,us5}`。  
user-data **必须以 `#!/bin/bash` 开头**（Ubuntu cloud-init 否则忽略）；AWS CLI 走官方 awscliv2 zip（noble 无 `awscli` apt）。  
点火后把 `api-<edge>-hz.tokenkey.dev` A 指到输出的 `public_ip`，再跑 E0/E1。

## 硬门禁

- `location=fsn1` · `server_type=cax21` · `architecture=arm`
- Phase-1：合并 defaults 后不得 `deployable=true`

```bash
python3 -m unittest deploy/hetzner/test_resolve_edge_hetzner_target.py
bash deploy/hetzner/render-bootstrap.sh --check
```
