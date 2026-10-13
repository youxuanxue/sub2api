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
- fan-out / probe 默认：`resolve-edge-target.py --list-deployable` → 上表四边；prod 单独解析
- 机型：`fsn1` · `cax21` · arm64；现网已在 1.8.284 完成蓝绿首发；后续升级使用既有 blue/green 状态机
- **不在舰队：** `us3` / `us6`（无正式 A、无 prod stub；矩阵空行 / LS `deployable=false`）。旧 LS 仅 gemini 会话 drain 前保留，勿当路由目标

## 现网不变量

| 项 | 值 |
|----|-----|
| Edge `MAIN_GATEWAY_ALLOWED_CIDR` | `167.233.211.115/32`（与 GitHub `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` 一致） |
| Edge / prod `API_DOMAIN` | 单正式名（无 `-hz` 双 SAN） |
| GHCR PAT（Hybrid） | SSM `/tokenkey/edge/fleet/ghcr/pat`（eu-west-2；`EDGE_GHCR_PAT_SSM_NAME`） |
| 探针 / sync Caddy | 直投 Hybrid `mi-*`；`sync_caddyfile` **勿**设 `EDGE_ID=`；日常探针用 `run-probe --target edge:<id>`（共享解析器只选现网 HZ） |
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

整库 DR：舰队 `tokenkey-pgdump.sh` + [恢复入口](#恢复入口)。账号级搬运（非整库）：`ops/migration/migrate-edge-accounts.py`。

换机编排：[`.cursor/skills/tokenkey-host-replacement/SKILL.md`](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 日常发版

```bash
# prod
bash scripts/stage0/dispatch-prod-deploy.sh --tag X.Y.Z
# 或 deploy-stage0 → deploy_via_ssm_bluegreen.sh

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

存量 prod 的主机告警脚本不随应用镜像发版更新。更新共享脚本与数据卷绑定使用版本化入口：

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/observability/remediate-hcloud-prod-host-alerts.sh \
  --with deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh \
  --with deploy/hetzner/prod-target-hetzner.json
```

默认只校验主机身份、Hetzner 卷挂载和现有 timer 并输出 plan；执行安装时追加
`--env APPLY=yes-hcloud-prod-host-alerts`。安装前自测共享脚本并备份旧脚本/全部 drop-ins，
输出备份目录后原子替换脚本与 volume drop-in、reload 并验证生效绑定；失败非零退出。
不主动执行告警服务，沿用现有 timer。回退时从输出的备份恢复脚本和原 drop-ins，再
`systemctl daemon-reload`。edges 继续用既有
`ops/stage0/sync-edge-host-units-via-ssm.sh`。

1. **蓝绿已验收**：1.8.284 已完成 prod + 活 Edge 的升级、排空及烟测；raw maintenance pin 的发布与存储切换仍独立。
2. **gemini-web re-import**：uk1/uk2 上若干 gemini 账号 `error`/不可调度（出口 IP）；修好后再开对应 prod stub。
3. **边侧供应**：us4 anthropic / us4·us5 grok / uk CC 池按冒烟 200 再开 stub。
4. **旧 LS / AWS prod**：≥7d 后可删实例；QA raw 的存储与资源退役条件独立，见下一项。
5. **QA 去 AWS**：仅切换新增归档；存量不搬，按 AWS 原生命周期自然到期，期间保留恢复访问。切换验收与资源退役条件见 [`docs/approved/qa-raw-only.md`](../../docs/approved/qa-raw-only.md)。

## 校验

```bash
python3 -m unittest deploy/hetzner/test_resolve_edge_hetzner_target.py
python3 -m unittest deploy/hetzner/test_resolve_prod_hetzner_target.py
python3 -m unittest deploy/hetzner/test_render_prod_bootstrap.py
bash deploy/hetzner/render-bootstrap.sh --check
bash deploy/hetzner/render-prod-bootstrap.sh --check
```

## AWS 残留与环境变量

计算节点迁移完成不等于整个系统零 AWS。以下为保留依赖与清理边界；不要按文件名或
`AWS_*` 前缀批量删除。历史审批文档记录当时的架构，现网执行以本文件和目标矩阵为准。

| 类别 | 当前用途 / owner | 清理条件 |
|---|---|---|
| SSM Hybrid / Parameter Store / STS OIDC | hcloud 远程执行、参数与 GHCR 身份；`ops/stage0/{resolve_prod_ssm_target,edge_ssm_execution}.py` | 替换发布控制面后才能移除 AWS CLI、IAM 与 OIDC |
| QA raw archive | `ops/qa/`、`deploy/aws/cloudformation/stage0-qa-raw-archive.yaml`；仅新增归档切到 Hetzner，AWS 存量按原生命周期自然到期，期间保留恢复访问 | 按 [raw-only 契约](../../docs/approved/qa-raw-only.md)验收新增归档切换、restore 与到期边界；AWS 桶、KMS 和恢复角色销毁另行审批 |
| PG dump / env-secrets / archive / media | AWS S3、SSM SecureString；`tokenkey-pgdump.sh`、`backup-env-secrets-via-ssm.sh`、archive/media 配置 | 数据与恢复能力迁移后才能移除 |
| 共享运行文件的旧目录名 | `deploy/aws/stage0/` 内 compose、Caddy、QA lifecycle、fingerprint baseline；HZ renderer 与 backend embed 仍直接消费 | 是共享 owner，路径不代表部署在 AWS；迁路径必须同时更新引用、生成产物和 sentinel |
| AWS standby 脚手架 | `deploy/aws/lightsail/`、EC2 bootstrap/CFN、`ops/lightsail/`、AWS DR 与 IP 历史 | 不进现网自动 fan-out；仅明确指定旧机恢复时使用，资产退役单独授权 |
| 第三方 AWS 协议 | Kiro/Bedrock 的 AWS endpoint、SDK/SigV4、S3 兼容客户端 | 属模型供应商/协议能力，不能随基础设施迁移删除 |

| 变量 | 语义 |
|---|---|
| `PROD_SSM_TARGET` | `auto`/未设置默认 hcloud；`aws` 仅显式选择旧 prod standby。旧 `/tokenkey/prod/control-plane-ssm-target` 参数不再参与默认解析 |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | AWS API 区域。SSM 命令必须用 resolver 返回的 `ssm_region`，不能拿 hcloud `fsn1` 或旧 EC2 region 代替；QA/S3 仍按各自存储区域 |
| `AWS_OIDC_ROLE_ARN` / `AWS_SHARED_CREDENTIALS_FILE` | 仍用于 Hybrid 控制面与现存 AWS 数据依赖；凭据内容不可打印 |
| `PROD_STACK_NAME` / `STACK_NAME` | 旧 AWS standby 或仍在使用的 QA/备份 CFN，不是 hcloud 主机身份 |
| `HCLOUD_TOKEN` | hcloud 资源管理；普通镜像升级走 SSM，不重新 provision |
| `HCLOUD_S3_Access_Key` / `HCLOUD_S3_Secret_Key` | 本机受保护的对象存储凭据；不输出、不入库 |
| `QA_RUNTIME_PROVIDER` 与 `QA_*` | QA 独立迁移契约；不得因网关迁完而直接切换默认值 |
| `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` / `EDGE_GHCR_PAT_SSM_NAME` | 仍由现网发布使用，值见本文件现网不变量 |

## 恢复入口

先用 `resolve_prod_ssm_target.py --format json` 或 `edge_ssm_execution.py` 核实身份，
再用 `run-probe.sh` 读取主机/容器状态。镜像回滚走原 dispatcher 部署 previous tag。
prod 的 `/var/lib/tokenkey` 是 Hetzner Volume；换机必须保留/核对该卷并通过 Volume mount
门禁，不能执行 AWS EC2/EBS/DLM 命令。整机恢复沿用 `tokenkey-host-replacement` 与
`hetzner-cloud-full-migration.md` 的备份、恢复对账和切流门禁。

[`AWS DR`](../aws/RUNBOOK-disaster-recovery.md) 仅保留旧机恢复和 AWS 离机备份读取步骤；
其中 EBS/DLM/CFN 实例替换不能用于现网 hcloud。先确认当前 dump、密钥备份和实际恢复点，
不要沿用旧 AWS 文档的 RPO 数字。旧机磁盘停写后的数据不能整库覆盖活库。
