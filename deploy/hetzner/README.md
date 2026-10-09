# TokenKey Edge on Hetzner Cloud

审批基线：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

Phase-1：矩阵 + resolve + dry-run / 可选付费建机。**全 `deployable=false`，不切 DNS。** Live edge 仍是 Lightsail。

## 文件

| 路径 | 作用 |
|---|---|
| `edge-targets-hetzner.json` | 矩阵（defaults + edge id） |
| `resolve-edge-hetzner-target.py` | 派生域名/主机名 + 硬门禁 |
| `provision-edge.sh` | 默认 dry-run；`--confirm-paid` 才建机 |
| `.github/workflows/deploy-edge-hetzner-stage0.yml` | validate / provision |

## 用法

```bash
gh secret set HCLOUD_TOKEN --body "$HCLOUD_TOKEN"   # 勿贴进聊天

# 矩阵单测（GHA validate）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation validate --platform hetzner --allow-planned

# provision dry-run
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation provision --platform hetzner --allow-planned

# 付费建机（仍无 DNS）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation provision --platform hetzner --allow-planned --confirm-paid

# 本机
python3 deploy/hetzner/resolve-edge-hetzner-target.py --edge-id uk1 --allow-planned
bash deploy/hetzner/provision-edge.sh --edge-id uk1 --allow-planned
```

`auto` 路由：仅当 Hetzner 行 `deployable=true` 时优先，否则 Lightsail。

## 硬门禁

- `location=fsn1` · `server_type=cax21` · `architecture=arm`
- `ssm_prefix` 以 `/tokenkey/hetzner/` 开头
- Phase-1：合并 defaults 后不得 `deployable=true`

```bash
python3 -m unittest deploy/hetzner/test_resolve_edge_hetzner_target.py
```
