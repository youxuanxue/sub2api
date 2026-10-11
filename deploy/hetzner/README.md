# TokenKey 舰队 · Hetzner Cloud（现网）

**SSOT：** 现网节点 / 公网 IP / CIDR / GHCR PAT 路径 / 发版入口 **只在本文件叙述**；其它文档与 skill 只留指针，勿复制数值。  
审批基线（决策锁，无 IP 表）：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

## 活舰队（五节点）

| 节点 | 正式域名 | 公网 IP | SSM Hybrid | 备注 |
|------|----------|---------|------------|------|
| **prod** | `api.tokenkey.dev` | `167.233.211.115` | `mi-033c9569c7fb8b884` · Volume `tokenkey-prod-data` | ops 控制面 `PROD_SSM_TARGET=hetzner` |
| **uk1** | `api-uk1.tokenkey.dev` | `188.245.112.147` | 见矩阵 `mi-*` | |
| **uk2** | `api-uk2.tokenkey.dev` | `2.31.27.73` | 见矩阵 `mi-*` | |
| **us4** | `api-us4.tokenkey.dev` | `188.245.14.132` | 见矩阵 `mi-*` | |
| **us5** | `api-us5.tokenkey.dev` | `91.98.83.56` | 见矩阵 `mi-*` | |

- 矩阵：`edge-targets-hetzner.json` + `prod-target-hetzner.json`（`deployable=true`）
- fan-out / probe 默认：`resolve-edge-target.py --list-deployable` → 上表四边 + prod
- 机型：`fsn1` · `cax21` · arm64；compose 主机仍单色 `tokenkey`，首发蓝绿 upgrade 经 `ensure_legacy_cutover` 迁 `tokenkey-blue`
- **不在舰队：** `us3` / `us6`（无正式 A、无 prod stub；矩阵空行 / LS `deployable=false`）。旧 LS 仅 gemini 会话 drain 前保留，勿当路由目标

## 现网不变量

| 项 | 值 |
|----|-----|
| Edge `MAIN_GATEWAY_ALLOWED_CIDR` | `167.233.211.115/32`（与 GitHub `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` 一致） |
| Edge / prod `API_DOMAIN` | 单正式名（无 `-hz` 双 SAN） |
| GHCR PAT（Hybrid） | SSM `/tokenkey/edge/fleet/ghcr/pat`（eu-west-2；`EDGE_GHCR_PAT_SSM_NAME`） |
| 探针 / sync Caddy | 直投 Hybrid `mi-*`；`sync_caddyfile` **勿**设 `EDGE_ID=`；勿对 HZ 用会解析到 LS 的 `run-probe --target edge:<id>` |
| prod stub `base_url` | 必须 `https://api-<edge>.tokenkey.dev` |
| live HZ 库 | **禁止**用旧机全量 dump 覆盖；补数只用 `request_id` 定点插入 |
| Secrets | `/tokenkey/hetzner/…`；不进 git |

AWS EIP `34.194.234.88` 与四边旧 Lightsail 仅为 ≥7d standby / 回滚，**不是**现网写路径。

## 文件

| 路径 | 作用 |
|---|---|
| `edge-targets-hetzner.json` / `prod-target-hetzner.json` | 矩阵 |
| `resolve-edge-hetzner-target.py` / `resolve-prod-hetzner-target.py` | 派生命名 + 硬门禁 |
| `render-bootstrap.sh` / `render-prod-bootstrap.sh` | user-data |
| `generated-user-data.sh` / `generated-prod-user-data.sh` | 渲染产物（与 `--check` 同步提交） |
| `provision-edge.sh` / `provision-prod.sh` | 点火（dry-run / `--confirm-paid --tag`） |

整库 DR：舰队 `tokenkey-pgdump.sh` + [`deploy/aws/RUNBOOK-disaster-recovery.md`](../aws/RUNBOOK-disaster-recovery.md)。账号级搬运（非整库）：`ops/migration/migrate-edge-accounts.py`。

换机编排：[`.cursor/skills/tokenkey-host-replacement/SKILL.md`](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 日常发版

```bash
# prod
bash scripts/stage0/dispatch-prod-deploy.sh --tag X.Y.Z
# 或 deploy-stage0 → deploy_via_ssm_bluegreen.sh（首发单色→blue）

# edge（平台 auto → HZ）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id uk1 --operation upgrade --tag X.Y.Z
```

新边 / 重建（付费）：

```bash
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id <id> --operation provision --platform hetzner \
  --allow-planned --confirm-paid --tag X.Y.Z
```

## 开放运维项（非过程史）

1. **蓝绿首跑**：下一刀 release 走上节发版；主机仍单色时由 `ensure_legacy_cutover` 迁 blue。  
2. **gemini-web re-import**：uk1/uk2 上若干 gemini 账号 `error`/不可调度（出口 IP）；修好后再开对应 prod stub。  
3. **边侧供应**：us4 anthropic / us4·us5 grok / uk CC 池按冒烟 200 再开 stub。  
4. **旧 LS / AWS prod**：≥7d 后可删实例；`deploy-qa-bundle` / `container-log-policy` 仍偏 AWS，另轨。

## 校验

```bash
python3 -m unittest deploy/hetzner/test_resolve_edge_hetzner_target.py
python3 -m unittest deploy/hetzner/test_resolve_prod_hetzner_target.py
python3 -m unittest deploy/hetzner/test_render_prod_bootstrap.py
bash deploy/hetzner/render-bootstrap.sh --check
bash deploy/hetzner/render-prod-bootstrap.sh --check
```
