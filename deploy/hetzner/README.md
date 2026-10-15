# TokenKey 舰队 · Hcloud / AWS 分批迁移

**SSOT：** 现网节点 / 公网 IP / CIDR / GHCR PAT 路径 / 发版入口 **只在本文件叙述**；其它文档与 skill 只留指针，勿复制数值。
审批基线（决策锁，无 IP 表）：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

## 活舰队（五节点）

| 节点 | 正式域名 | 公网 IP | SSM Hybrid | 备注 |
|------|----------|---------|------------|------|
| **prod** | `api.tokenkey.dev` | `167.233.211.115` | `mi-033c9569c7fb8b884` · Volume `tokenkey-prod-data` | ops 控制面 `PROD_SSM_TARGET=hetzner` |
| **uk1** | `api-uk1.tokenkey.dev` | `188.245.112.147` | 见矩阵 `mi-*` | |
| **uk2** | `api-uk2.tokenkey.dev` | `35.178.162.181` | `mi-022bedb939517c7f3` | AWS Lightsail · tokenkey-edge-uk2-aws-20261015 |
| **us4** | `api-us4.tokenkey.dev` | `188.245.14.132` | 见矩阵 `mi-*` | |
| **us5** | `api-us5.tokenkey.dev` | `91.98.83.56` | 见矩阵 `mi-*` | |

- 活矩阵：Hcloud `edge-targets-hetzner.json` + AWS `../aws/lightsail/edge-targets-lightsail.json`；每个 edge 仅一个平台 deployable。prod 仍为 Hcloud。
- fan-out / probe 默认：`resolve-edge-target.py --list-deployable` → 上表四边；prod 单独解析
- Hcloud 机型：`fsn1` · `cax21` · arm64；AWS uk2：Lightsail `xlarge_3_0`（4 vCPU / 16 GiB）。后续升级使用既有 blue/green 状态机。
- **不在舰队：** `us3` / `us6`（无正式 A、无 prod stub；矩阵空行 / LS `deployable=false`）。旧 LS 仅 gemini 会话 drain 前保留，勿当路由目标


本轮 AWS 活节点：uk2。新边仅放行现网 Hcloud prod；旧 edge 的单地址转发信任只在 DNS 过渡期间保留。
后续旧平台专用 provision、卷与告警操作仅适用于仍在 Hcloud 的节点，不能用于已切换 AWS 的 edge。

## 现网不变量

| 项 | 值 |
|----|-----|
| Edge `MAIN_GATEWAY_ALLOWED_CIDR` | `167.233.211.115/32`（与 GitHub `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` 一致） |
| Edge / prod `API_DOMAIN` | 单正式名（无 `-hz` 双 SAN） |
| GHCR PAT（Hybrid） | SSM `/tokenkey/edge/fleet/ghcr/pat`（eu-west-2；`EDGE_GHCR_PAT_SSM_NAME`） |
| 探针 / sync Caddy | 直投 Hybrid `mi-*`；`sync_caddyfile` **勿**设 `EDGE_ID=`；日常探针用 `run-probe --target edge:<id>`（共享解析器按活矩阵选择 Hcloud 或 AWS） |
| prod stub `base_url` | 必须 `https://api-<edge>.tokenkey.dev` |
| live HZ 库 | **禁止**用旧机全量 dump 覆盖；补数只用 `request_id` 定点插入 |
| Secrets | `/tokenkey/hetzner/…`；不进 git |

Stage0 单后端 Caddy 只由 `/health` 主动检查决定摘除与恢复；禁止被动冷却，避免客户端上传断连或单次业务 5xx 让其他请求等待。正式 prod 的源是 `deploy/aws/stage0/Caddyfile`；新机 prod 和 edge 均使用 `Caddyfile.edge`。修改时同时重生成 Hetzner bootstrap，`scripts/checks/caddyfile-syntax.sh` 校验 Caddy 解析后的健康策略；运行中主机通过 `ops/stage0/sync_caddyfile_via_ssm.sh` 热更新，单纯发布应用镜像不会刷新 Caddyfile。

旧 AWS 与旧 Hcloud 节点保留历史数据，不作为回滚写库；全新 AWS 节点按上表逐台接流量。

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

# edge（平台 auto → 当前活矩阵）
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
同一安装器，附加 `--with deploy/hetzner/edge-targets-hetzner.json`、
`--with ops/stage0/edge_routing_matrix.py` 与 `--env ALERT_TARGET=edge:<id>`，apply 值为
`yes-hcloud-host-alerts`；只更新告警脚本，不同步 GHCR 或重启 timer。

1. **蓝绿已验收**：1.8.284 已完成 prod + 活 Edge 的升级、排空及烟测；raw maintenance pin 的发布与存储切换仍独立。
2. **gemini-web re-import**：uk1/uk2 上若干 gemini 账号 `error`/不可调度（出口 IP）；修好后再开对应 prod stub。
3. **边侧供应**：us4 anthropic / us4·us5 grok / uk CC 池按冒烟 200 再开 stub。
4. **旧 LS / AWS prod**：≥7d 后可删实例；QA raw 的存储与资源退役条件独立，见下一项。
5. **QA 新归档已切换 Hetzner**：定时归档与隔离恢复已验收；存量不搬，AWS 按原生命周期自然到期，期间保留恢复访问。实际归档小时分界、恢复坐标与到期边界见 [QA 运维入口](../../ops/qa/README.md#生产存储与恢复分界)；资源退役另行审批。

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
| QA raw archive | 新归档已写入 Hetzner；AWS raw 栈仅保留旧数据恢复与审计，存量按原生命周期自然到期 | 按 [QA 运维入口](../../ops/qa/README.md#生产存储与恢复分界)核查实际到期与对象清单；AWS 桶、KMS 和恢复角色销毁另行审批 |
| PG dump | prod 与活跃 Edge 已切换到私有 Hetzner 存储；AWS 旧备份保留恢复 | 切换均经过真实加密上传/下载/隔离恢复验收，不销毁旧数据 |
| env-secrets 与备份解密材料 | SSM SecureString；`backup-env-secrets-via-ssm.sh` 与 `/tokenkey/hetzner/<node>/stage0/pgdump-storage-recovery` | 随控制面另行迁移 |
| 历史 archive / dormant media 桶 | 历史数据与停用配置；媒体默认不启用 S3，只有显式 opt-in | 资产销毁另行审批，不能把停用 bucket 描述成现网迁移对象 |
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
| `QA_*` | QA 独立迁移契约；不得因网关迁完而直接切换默认值 |
| `QA_LIVE_ARCHIVE_*`（脚本内部常量） | 现行 QA raw 坐标由 `ops/stage0/qa-runtime-environment.sh` 统一持有；HCloud prod 蓝绿发布缺失或偏离这些坐标时直接失败。旧 AWS IAM/raw 检查仅显式传 `--legacy-aws` |
| `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` / `EDGE_GHCR_PAT_SSM_NAME` | 仍由现网发布使用，值见本文件现网不变量 |

媒体默认 AWS 坐标清理：`ops/stage0/retire_dormant_media_storage.py` 默认 plan；
通过 `run-probe --script ops/stage0/retire_dormant_media_storage_remote.sh`
附带 `--with ops/stage0/retire_dormant_media_storage.py` 和 prod 矩阵，追加 `--env APPLY=yes-retire-dormant-media` 前必须确认 image offload 未启用、
无显式 image storage 设置和视频任务。只删已知休眠默认值，自定义配置拒绝处理。
运行容器的旧环境变量在下一次正常蓝绿发布时消失；此入口不重启容器。

## QA 与备份排队、旧 AWS 定时任务收尾

共享脚本使用 `/var/lib/tokenkey/qa-lifecycle/host.lock`：pg_dump 持共享锁，QA boundary 与
archive 持独占锁；备份完成本地文件后立即解锁，上传不阻塞 QA。定时 QA 最多等待 600 秒，
备份最多等待 2400 秒；超时任务失败，不跳过数据库原有短锁超时、seal、restore 或 DROP 门禁。
单一 owner 的 plan/activation 仍立即报锁忙，避免把旧计划排队执行。

现有主机不会因代码合并自动更新。安装前保存三个 host 脚本的原文件、权限与哈希，核对
备份、maintenance、boundary service 都未运行，再使用同一 host.lock 排他锁安装脚本；
保持原有 timer、maintenance pin、存储身份和资源限制。任务忙则等待，不能强杀。
回退同样在任务空闲且持排他锁时恢复原文件，随后观察下一轮 timer 的回执与备份产物。
仅更新宿主脚本，不发布网关镜像或重启 gateway、Caddy、PostgreSQL/Redis。

旧 AWS prod standby 上的 QA/告警 timer 可独立停用；实例和数据继续保留。
入口默认只读 plan，必须明确旧 EC2 ID（禁止 Hybrid/HCloud）：

```bash
PROD_SSM_TARGET=aws bash ops/observability/run-probe.sh --target prod \
  --expected-instance-id <旧EC2-ID> \
  --script ops/stage0/retire-aws-standby-timers-remote.sh \
  --with ops/stage0/retire_aws_standby_timers.py \
  --env EXPECTED_STANDBY_INSTANCE_ID=<旧EC2-ID>
```

核实 plan 后同一命令追加 `--env OPERATION=apply`。入口核对 IMDSv2 身份、无运行中的
TokenKey gateway/worker、相关 service 空闲，只 disable/stop 三个 allowlist timer：
QA maintenance、QA boundary、disk metrics；不操作 PG/Redis 或删除资源。apply 输出
root-only 状态备份路径，失败尝试恢复原 timer 状态；回退用 `--env OPERATION=rollback`
及 `--env BACKUP=<输出的备份路径>`，仍须通过旧机身份与空闲检查。

普通蓝绿发布不再注入 telemetry 的 AWS 桶/region 默认值，telemetry 默认关闭。
显式 opt-in 和主机已有配置继续保留；此变更不等于已清理运行容器中的休眠变量，也不表示
telemetry 已支持 Hetzner 存储。需要启用该能力时必须单独验收其存储契约。

## 数据库备份迁移入口

审批边界：[host-only pgdump](../../docs/approved/hcloud-pgdump-storage.md)。
迁移使用的专用私有桶为 `tokenkey-pgdump-fsn1-850a09b1c6de`（fsn1，七天生命周期）。
上传 owner 仍是 `deploy/aws/stage0/tokenkey-pgdump.sh`；host-only 存储身份及加密参数
由 `ops/stage0/pgdump_storage.py` 同时供上传和恢复使用。
现网 prod、uk1、uk2、us4、us5 均已激活该身份，并逐节点完成真实 dump 的
下载哈希比对、完整 SQL 恢复、核心数据核对和临时数据清理。原两小时 timer 保持启用。
此前五节点正常定时备份已核对实际新对象、大小与 SSE-C 状态。2026-10-15 uk2 迁至新 AWS 后，使用原私有 AWS 桶 `tokenkey-prod-pgdump-682751977094/edge/uk2/pgdump/`；已验证新备份上传、下载哈希与 AES256，其他四节点保持 Hetzner 存储。旧 Hcloud uk2 的冻结整库归档隔离在同一 AWS 前缀下的 `archive-hcloud-20261015/`。
主机告警共享实现已刷新，prod 监控实际 `/var/lib/tokenkey` Hetzner 数据卷；
告警直接通知飞书，无需 S3。休眠 AWS 媒体默认值已从 prod host `.env` 清理，
运行容器的旧变量随下次正常蓝绿发布更新；未启用的 offload 不产生媒体 S3 数据。

`ops/stage0/provision_hcloud_pgdump_bucket.py --bucket <专用桶> --apply` 校验私有桶、
TLS、七天生命周期及普通/multipart SSE-C 往返。脚本读取本机受保护的
`HCLOUD_S3_Access_Key` / `HCLOUD_S3_Secret_Key`，不打印凭据。

现有主机切换用 `ops/stage0/install_hcloud_pgdump_storage.py`，提供 `--target prod|edge:<id>`、
`--bundle <root-only SSH bundle>`、`--release <id>` 和 `--operation plan|prepare|activate|rollback`。
bundle 包含共享 pgdump/helper/canary/contract、现网矩阵与 `edge_routing_matrix.py`，
并包含 `pgdump-storage.json`、`credentials`、独立的 32-byte `sse-c.key`。
私密文件用验证 host key 的 SSH 传送，**禁止**用 run-probe `--with` 或 `--env` 传密钥。

JSON schema v1 字段为 `schema_version`、`target`、`s3_uri`、`endpoint`、`region`、
`credentials_file`、`sse_c_key_file`。region 为 `fsn1`，endpoint 为
`https://fsn1.your-objectstorage.com`；URI 前缀必须为 `prod/pgdump` 或 `edge/<id>/pgdump`。
凭据文件只含 `[pgdump]` 的 `aws_access_key_id` 与 `aws_secret_access_key`；所有私密文件
必须属调用者且为 0600，安装目录在 `/etc/tokenkey/pgdump/<release>/`，网关不挂载。

prepare 上传最近已有的真实 dump。用既有 canary 的 `--storage-config <候选 storage.json>`、
`--expected-source <prepare 输出的本地文件>`、`--receipt-root /var/lib/tokenkey/pgdump-canary/<release>`
核对 SHA-256 并隔离恢复。低优先级宿主任务、内存余量至少 2GiB；设置
`TOKENKEY_PGDUMP_CANARY_IO_DEVICE` 为实际数据盘以限制恢复容器读写各 10MiB/s。
未通过磁盘容量门禁或恢复/清理失败不得 activate。activate 校验匹配回执，安装并保存回滚材料；
遇到任务运行或 timer 即将触发则等本轮结束再执行。切换后核对新备份产物：可观察正常 timer，或在容量门禁通过后低优先级执行一次同一备份脚本，
并核对远端对象。不要为验证重启任何应用/数据库 unit。

新主机 bootstrap 只装共享任务，不推断 AWS 桶；正式接流量前必须安装并验收存储配置。
灾难恢复必须取回对应节点 SSE-C 密钥；丢失该密钥的备份不可读。AWS 旧数据不会被该入口删除。

## 恢复入口

先用 `resolve_prod_ssm_target.py --format json` 或 `edge_ssm_execution.py` 核实身份，
再用 `run-probe.sh` 读取主机/容器状态。镜像回滚走原 dispatcher 部署 previous tag。
prod 的 `/var/lib/tokenkey` 是 Hetzner Volume；换机必须保留/核对该卷并通过 Volume mount
门禁，不能执行 AWS EC2/EBS/DLM 命令。整机恢复沿用 `tokenkey-host-replacement` 与
`hetzner-cloud-full-migration.md` 的备份、恢复对账和切流门禁。

[`AWS DR`](../aws/RUNBOOK-disaster-recovery.md) 仅保留旧机恢复和 AWS 离机备份读取步骤；
其中 EBS/DLM/CFN 实例替换不能用于现网 hcloud。先确认当前 dump、密钥备份和实际恢复点，
不要沿用旧 AWS 文档的 RPO 数字。旧机磁盘停写后的数据不能整库覆盖活库。
