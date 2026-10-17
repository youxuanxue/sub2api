# TokenKey 现网舰队 · Tokyo prod 与日本出口代理

**SSOT：** 现网节点 / 公网 IP / CIDR / GHCR PAT 路径 / 发版入口 **只在本文件叙述**；其它文档与 skill 只留指针，勿复制数值。
审批基线（决策锁，无 IP 表）：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

## 活舰队（prod-only，2026-10-17 起）

| 节点 | 正式域名 | 公网 IP | SSM 身份 | 备注 |
|------|----------|---------|------------|------|
| **prod** | `api.tokenkey.dev` | `3.114.187.250` | `i-045f062ce33e1a5af` · ap-northeast-1 | EC2 c7g.xlarge · tokenkey-prod-tokyo-20261017 |

**Edge 已退役并回收。** prod 直接使用原生账号（含原 edge 复制账号），旧中继 stub 已软删。
AWS / Hcloud 旧主机与公网 IP 不能用作回滚目标；历史数据从 S3 隔离归档读取。
退役验收与归档入口见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。
`dispatch-edge-deploy` / edge deploy workflow / 付费 `provision-edge` 另需字面量 `I_APPROVE_EDGE_RESTORE`（`ops/stage0/require_edge_restore_approval.py`；`--allow-planned` 不够）。

### 日本出口代理（现役）

这些 Lightsail 机器仅提供账号出口，不能作为 edge 网关部署或退役：

| prod 代理名 | Lightsail 实例 | 公网 IP | 区域 |
|---|---|---|---|
| jp-uk1 | Ubuntu-1-1 | 46.51.238.48 | ap-northeast-1 |
| jp-uk2 | Ubuntu-1-2 | 52.68.179.121 | ap-northeast-1 |
| jp-us4 | Ubuntu-3-1 | 35.77.89.227 | ap-northeast-1 |
| jp-us5 | Ubuntu-3-2 | 52.68.182.0 | ap-northeast-1 |

账号绑定以 prod 管理 API 的实际 endpoint 为准；名称含 uk/us 不代表物理地域。

- 活 prod 身份：`../aws/fresh-migration-20261015.json` 的 `prod`；其旧源与 `edges` 坐标仅供历史取证。
- 旧 edge 矩阵：`../aws/lightsail/edge-targets-lightsail.json` 与 `edge-targets-hetzner.json`。
  `retired=true` 在 dispatch、provision 和 SSM 解析时拒绝，`--allow-planned` 不能覆盖。
- `resolve-edge-target.py --list-deployable` 与 `EXPECTED_LIVE_EDGE_IDS` 均为空。
  新建 edge 是独立架构决策：提供新身份、更新矩阵及契约；不能重新启用已删除实例或已释放 IP。
- 换机流程：[host replacement](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 现网不变量

| 项 | 值 |
|----|-----|
| 活入口 | 仅 Tokyo prod `3.114.187.250`；四正式域名 A 指向该 IP |
| Edge 部署 | **关闭**（退役行 `retired=true`；无 prod→edge stub） |
| Edge `MAIN_GATEWAY_ALLOWED_CIDR`（历史机若仍开机） | `3.114.187.250/32` |
| prod `API_DOMAIN` | 单正式名（无 `-hz` 双 SAN） |
| GHCR PAT（Hybrid） | SSM `/tokenkey/edge/fleet/ghcr/pat`（eu-west-2；仅历史 edge 排障） |
| 探针 / sync Caddy | prod 解析至当前 EC2 `i-*`；已退役 edge 身份拒绝执行 |
| live 库 | **禁止**用旧机全量 dump 覆盖；补数只用 `request_id` 定点插入 |
| Secrets | `/tokenkey/hetzner/…`；不进 git |

Stage0 单后端 Caddy 只由 `/health` 主动检查决定摘除与恢复；禁止被动冷却，避免客户端上传断连或单次业务 5xx 让其他请求等待。正式 prod 的源是 `deploy/aws/stage0/Caddyfile`；新机 prod 和 edge 均使用 `Caddyfile.edge`。修改时同时重生成 Hetzner bootstrap，`scripts/checks/caddyfile-syntax.sh` 校验 Caddy 解析后的健康策略；运行中主机通过 `ops/stage0/sync_caddyfile_via_ssm.sh` 热更新，单纯发布应用镜像不会刷新 Caddyfile。

旧主机均不再提供服务或回滚写库；历史整库保留在 S3 独立归档前缀。只有上表 prod 接收网关流量。

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
bash scripts/stage0/dispatch-prod-deploy.sh --tag X.Y.Z
```

日常发版仅 prod。`edge-health-watch` 仅保留手动诊断，空 edge 集合会退出；
prod 日常监控由宿主 watcher 和 daily diagnostics 承担。
Hcloud 建机、edge 发布和 Gemini Web Worker workflow 属历史/待建模板：
当前已退役身份一律拒绝；prod 不运行 Gemini worker。共享 compose、Caddy、pgdump、
QA 和 SSM 实现仍由现网使用，不按目录名批量删除。

## 开放运维项（非过程史）

1. **账号**：prod 原生账号承接请求；错误、不可调度状态由运营处理，不自动恢复中继或旧副本。
2. **QA**：capture 沿现有配置；用户已暂停向 S3 上传。暂停 owner 为当前 prod 清单的
   `paused_jobs`，只包含 `tokenkey-qa-maintenance`。boundary、备份与 capture 不随之停用。
   历史 raw 不搬，恢复见 [QA 运维入口](../../ops/qa/README.md)。
3. **监控**：东京 DLM 与 CPU/磁盘告警保留。宿主 HTTPS/job watcher 读取暂停 owner，
   要求归档 timer 停用且 service 不在运行；意外恢复会告警。daily diagnostics 复用同一暂停清单，
   暂停期不要求归档回执刷新，boundary 与存储检查保留。其他任务仍按成功时效监控.
   主机脚本不随网关镜像自动更新；安装/刷新独立 watcher 后才生效，仓库合并不代表已部署。
4. **资源**：临时 node1 reader/入口在独立任务验收后回收；日本代理保留；
   新加坡其它项目资源不纳入 TokenKey 批量清理。详见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。

## 历史 OAuth 账号回滚记录

历史恢复说明：现已没有可直接回滚的旧 edge。以下凭据纪律仅适用于另行批准的新建隔离节点。

冻结副本不刷新；prod 可能已轮换 refresh token，不能直接重新启用旧副本。
按 [集中后审计与修复](../../docs/approved/oauth-consolidation-gateway-safety.md) 核对残留风险。
回滚是独立生产操作，先给出具体源/目标账号映射及目标代理，再按此顺序执行：

1. 保留目标 edge inactive、schedulable=false。先导出目标旧状态到私有文件，禁止将凭据打印到工单/PR。
2. 暂停对应 prod 账号的新调度，等待在途请求及 refresh 结束；将源设为 inactive，确认刷新任务不再写入后才取最终凭据快照。只设 schedulable=false 不阻止后台刷新。
3. 用现有账号导入/管理流程更新目标副本的最新凭据、模型映射和必要健康状态，保持目标禁用；不能复制 prod 本地 ProxyID 到另一数据库，按真实出口重新解析绑定。
4. 私有比较凭据版本和源/目标映射，确认来源仅有一个刷新者。若 prod 已不可读，只能先验证备份凭据或重新授权，不能假定 edge 冻结 token 有效。
5. 单个目标先启用并做有界文本/工具/图片/多轮验证，再恢复对应路由；源 prod 保持 inactive。任何阶段失败，先停目标并取回其最新轮换凭据，才恢复源，避免双刷新者。

代理名称是逻辑账号池，不作为停机依据。维护历史代理记录前，从管理 API 读实际 endpoint、
所绑定账号及历史矩阵；已退役美国逻辑名不能重新作为出口使用。

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
| SSM Hybrid / Parameter Store / STS OIDC | Lightsail / 历史 Hcloud 远程执行、参数与 GHCR 身份；`ops/stage0/{resolve_prod_ssm_target,edge_ssm_execution}.py` | 替换发布控制面后才能移除 AWS CLI、IAM 与 OIDC |
| QA raw archive | 存储配置为 AWS S3，当前上传已暂停；历史恢复按小时来源选择 | 按 [QA 运维入口](../../ops/qa/README.md#生产存储与恢复分界)核查实际到期与对象清单；AWS 桶、KMS 和恢复角色销毁另行审批 |
| PG dump | 仅 Tokyo prod 定时备份；旧 edge/prod 全量冻结库保留独立 S3 归档 | 当前节点的隔离恢复与定时任务验收见 [收尾记录](../../docs/approved/aws-migration-closeout-20261016.md)；不销毁旧数据 |
| env-secrets 与备份解密材料 | SSM SecureString；`backup-env-secrets-via-ssm.sh` 与 `/tokenkey/hetzner/<node>/stage0/pgdump-storage-recovery` | 随控制面另行迁移 |
| 历史 archive / dormant media 桶 | 历史数据与停用配置；媒体默认不启用 S3，只有显式 opt-in | 资产销毁另行审批，不能把停用 bucket 描述成现网迁移对象 |
| 共享运行文件的旧目录名 | `deploy/aws/stage0/` 内 compose、Caddy、QA lifecycle、fingerprint baseline；HZ renderer 与 backend embed 仍直接消费 | 是共享 owner，路径不代表部署在 AWS；迁路径必须同时更新引用、生成产物和 sentinel |
| 历史 AWS 脚手架 | `deploy/aws/lightsail/`、EC2 bootstrap/CFN、`ops/lightsail/`、AWS DR 与 IP 历史 | 不进现网自动 fan-out；旧身份已拒绝，新建恢复节点需独立清单 |
| 第三方 AWS 协议 | Kiro/Bedrock 的 AWS endpoint、SDK/SigV4、S3 兼容客户端 | 属模型供应商/协议能力，不能随基础设施迁移删除 |

| 变量 | 语义 |
|---|---|
| `PROD_SSM_TARGET` | `auto`/未设置默认 aws-fresh；`hetzner` / `aws` 的已退役 prod 身份直接拒绝。旧 `/tokenkey/prod/control-plane-ssm-target` 参数不再参与默认解析 |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | AWS API 区域。SSM 命令必须用 resolver 返回的 `ssm_region`，不能拿 hcloud `fsn1` 或旧 EC2 region 代替；QA/S3 仍按各自存储区域 |
| `AWS_OIDC_ROLE_ARN` / `AWS_SHARED_CREDENTIALS_FILE` | 仍用于 Hybrid 控制面与现有 AWS 计算及数据依赖；凭据内容不可打印 |
| `PROD_STACK_NAME` / `STACK_NAME` | 历史 AWS 或仍在使用的 QA/备份 CFN；不能代替当前 prod 身份 |
| `HCLOUD_TOKEN` | hcloud 资源管理；普通镜像升级走 SSM，不重新 provision |
| `HCLOUD_S3_Access_Key` / `HCLOUD_S3_Secret_Key` | 本机受保护的对象存储凭据；不输出、不入库 |
| `QA_*` | QA 独立迁移契约；不得因网关迁完而直接切换默认值 |
| `QA_LIVE_ARCHIVE_*`（脚本内部常量） | 现行 QA raw 坐标由 `ops/stage0/qa-runtime-environment.sh` 统一持有；prod 蓝绿发布缺失或偏离这些坐标、意外配置静态凭据时直接失败。AWS IAM/raw 策略检查兼容保留 `--legacy-aws` 参数 |
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

旧主机已经删除，停用旧机 timer 的脚本只作为历史实现保留；不得向当前 prod 套用旧退役步骤。
历史取证入口 `ops/stage0/retire-aws-standby-timers-remote.sh` 保留其旧机身份/空闲门禁；
`ops/observability/remediate-hcloud-prod-host-alerts.sh` 保留 Hcloud 专属卷/主机门禁。
这些工具用于解释旧安装/回退回执，不是当前 prod 的安装或清理命令。


普通蓝绿发布不再注入 telemetry 的 AWS 桶/region 默认值，telemetry 默认关闭。
显式 opt-in 和主机已有配置继续保留；此变更不等于已清理运行容器中的休眠变量，也不表示
telemetry 已支持 Hetzner 存储。需要启用该能力时必须单独验收其存储契约。

## 数据库备份迁移入口

当前 prod 使用 AWS 私有 pgdump 桶，源配置与权限以主机验收为准；归档回查见
[资源收尾](../../docs/ops/aws-retirement-20261017.md)。历史 Hcloud 存储迁移过程见
[审批记录](../../docs/approved/hcloud-pgdump-storage.md)，不再作为现网安装流程。
历史密钥、SSM 路径与共享存储实现不能按 `hetzner` 字样删除，须先核对恢复依赖。

## 恢复入口

先用 `resolve_prod_ssm_target.py --format json` 核实当前 prod 身份，
再用 `run-probe.sh` 读取主机/容器状态。镜像回滚走原 dispatcher 部署 previous tag。
现网 prod 的 `/var/lib/tokenkey` 是独立 EBS 数据卷；每日 DLM 对实例的根盘和数据盘建立
一致时间点的多卷快照，按既有策略保留七份。仓库中的 pg_dump 每两小时备份核心数据与
仍在库内的 `usage_logs` 使用明细（含所有分区），在同一数据库快照内保留账本和对账依据；
仅排除运维日志和 QA raw 的行数据，不能代替全盘快照或延长在线明细的保留期。
此范围变更须按上文宿主脚本安装流程落地；旧备份不会补回使用明细。
上线前核对新增体积下本地滚动副本、临时文件及恢复库的空间，观察备份/上传耗时，并从
新 S3 备份隔离恢复，核对 usage 日分区及 default 分区的记录、时间范围和费用汇总。
现有 restore canary 的核心表计数不包含 usage，不能单凭其成功宣称明细已恢复。
完成新脚本安装、成功上传及明细恢复验收前，使用明细仍依赖每日快照，RPO 约 24 小时；
验收后按最近成功且包含明细的异地备份计算实际 RPO，正常目标约两小时。
已回收旧主机的数据从私有 S3 归档恢复到隔离库；不能再引用旧机挂载卷。

整机恢复先核对当前实例、卷、最近完成快照和当前主机的备份来源，再沿用
`tokenkey-host-replacement` 与 [AWS DR](../aws/RUNBOOK-disaster-recovery.md) 中适用的恢复步骤；
旧 CFN/实例身份不作为新机默认目标。旧机磁盘停写后的数据不能整库覆盖活库。
迁移收尾、QA 当前验收与旧机回收边界见 [收尾记录](../../docs/approved/aws-migration-closeout-20261016.md)。
