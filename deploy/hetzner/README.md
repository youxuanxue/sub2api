# TokenKey Edge / Prod on Hetzner Cloud

> **审批基线：** [`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)  
> **Phase-1：** 骨架与矩阵门禁。矩阵内全部 `deployable=false`。  
> **Live edge 仍是 Lightsail**（`deploy/aws/lightsail/`），直至 Phase-2 单台 canary 翻矩阵并接线 dispatch。

## 目录

```text
deploy/hetzner/
├── edge-targets-hetzner.json       # edge 矩阵 SSOT（fsn1 / cax21 / arm）
├── prod-target-hetzner.json        # prod 目标（deployable=false）
├── resolve-edge-hetzner-target.py  # 解析 + 硬门禁（location/SKU/arch）
├── test_resolve_edge_hetzner_target.py
├── provision-edge.sh               # 默认 dry-run；--confirm-paid 才建机
└── README.md
```

Workflow stub：`.github/workflows/deploy-edge-hetzner-stage0.yml`（validate-only）。

## GitHub Secret / dispatch

```bash
# 仓库级 Secret（Environment edge-* 的 job 也可读）
# 勿把 token 贴进聊天；从本机环境注入：
gh secret set HCLOUD_TOKEN --body "$HCLOUD_TOKEN"

# Phase-1 validate（无建机、无 DNS）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation validate --platform hetzner --allow-planned

# provision dry-run（GHA 打印 hcloud plan）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation provision --tag 0.0.0 \
  --platform hetzner --allow-planned

# 付费建机（仍无 DNS；需 HCLOUD_TOKEN）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation provision --tag 0.0.0 \
  --platform hetzner --allow-planned --confirm-paid
```

`auto` 路由：Hetzner 矩阵 `deployable=true` 优先；否则 Lightsail（现网默认）。

## 本机 CLI（类 aws）

```bash
export HCLOUD_TOKEN=…          # 或写入 ~/.zshrc；勿提交
hcloud server list
python3 deploy/hetzner/resolve-edge-hetzner-target.py --edge-id uk1 --allow-planned
bash deploy/hetzner/provision-edge.sh --edge-id uk1 --allow-planned   # dry-run
```

付费建机（仍不改 DNS）：

```bash
bash deploy/hetzner/provision-edge.sh --edge-id uk1 --allow-planned --confirm-paid
```

## 硬门禁（与审批文档一致）

- `location` 仅 `fsn1`
- `server_type` 仅 `cax21`
- `architecture` 仅 `arm`（E0）
- `ssm_prefix` 必须以 `/tokenkey/hetzner/` 开头
- Phase-1：`deployable` 必须全为 `false`

## 测试

```bash
python3 -m unittest deploy/hetzner/test_resolve_edge_hetzner_target.py
```
