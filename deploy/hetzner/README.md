# TokenKey 现网舰队 · Tokyo prod 与日本出口代理

**SSOT：** 现网节点 / 公网 IP / CIDR / GHCR PAT 路径 / 发版入口 **只在本文件叙述**；其它文档与 skill 只留指针，勿复制数值。
审批基线（决策锁，无 IP 表）：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)

## 活舰队（prod-only，2026-10-17 起）

| 节点 | 正式域名 | 公网 IP | SSM 身份 | 备注 |
|------|----------|---------|------------|------|
| **prod** | `api.tokenkey.dev` | `3.114.187.250` | `i-045f062ce33e1a5af` · ap-northeast-1 | EC2 c7g.xlarge · tokenkey-prod-tokyo-20261017 |

prod 直接使用原生账号；旧中继 stub 已软删。
AWS / Hcloud 旧主机与公网 IP 不能用作回滚目标；历史数据从 S3 隔离归档读取。
退役验收与归档入口见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。

### 日本出口代理（现役）

这些 Lightsail 机器仅提供账号出口。

**活公网 IP / 绑定以 prod `proxies` 表为准**（Web：`https://tokenkey.dev/admin/proxies`；
探针：`ops/observability/probe-list-proxies.sh`）。下表只记逻辑名与常见 Lightsail 名，
避免第二份 IP 清单漂移：

| prod 代理名 | Lightsail 实例（常见） | 区域 |
|---|---|---|
| jp-uk1 | jp-proxy-01-micro | ap-northeast-1 |
| jp-uk2 | jp-proxy-02-micro | ap-northeast-1 |
| jp-us4 | jp-proxy-03-micro | ap-northeast-1 |
| jp-us5 | jp-proxy-04-micro | ap-northeast-1 |

名称含 uk/us 不代表物理地域。BBR 验收：`python3 scripts/checks/check-host-bbr-ssot.py --live`。

- 活 prod 身份：`../aws/fresh-migration-20261015.json` 的 `prod`；其旧源坐标仅供历史取证。
- 换机流程：[host replacement](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 现网不变量

| 项 | 值 |
|----|-----|
| 活入口 | 仅 Tokyo prod `3.114.187.250`；四正式域名 A 指向该 IP |
| prod `API_DOMAIN` | 单正式名（无 `-hz` 双 SAN） |
| 探针 / sync Caddy | prod 解析至当前 EC2 `i-*`；已退役身份拒绝执行 |
| live 库 | **禁止**用旧机全量 dump 覆盖；补数只用 `request_id` 定点插入 |
| Secrets | `/tokenkey/hetzner/…`；不进 git |

Stage0 单后端 Caddy 只由 `/health` 主动检查决定摘除与恢复；禁止被动冷却，避免客户端上传断连或单次业务 5xx 让其他请求等待。正式 prod 的源是 `deploy/aws/stage0/Caddyfile`；新机 prod 复用同一 Caddy 源文件。修改时同时重生成 Hetzner bootstrap，`scripts/checks/caddyfile-syntax.sh` 校验 Caddy 解析后的健康策略；运行中主机通过 `ops/stage0/sync_caddyfile_via_ssm.sh` 热更新，单纯发布应用镜像不会刷新 Caddyfile。

旧主机均不再提供服务或回滚写库；历史整库保留在 S3 独立归档前缀。只有上表 prod 接收网关流量。

## 文件

生产目标解析由 `ops/stage0/resolve_prod_ssm_target.py` 持有；共享部署文件位于 `deploy/aws/stage0/`。
换机编排：[host replacement](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 日常发版

```bash
bash scripts/stage0/dispatch-prod-deploy.sh --tag X.Y.Z
```

日常发版仅 prod。共享 compose、Caddy、QA 和 SSM 实现仍由现网使用；pgdump 转入历史恢复保留。

## 开放运维项（非过程史）

1. **账号**：prod 原生账号承接请求；错误、不可调度状态由运营处理，不自动恢复中继或旧副本。
2. **QA**：capture 沿现有配置；用户已暂停向 S3 上传。停用期望 owner 为当前 prod 清单的
   `paused_jobs`：`tokenkey-qa-maintenance`（上传暂停）与 `tokenkey-qa-boundary`
   （历史归档验收问题保持停用，DROP pause 保留）。capture 与备份继续运行。
   暂停期巡检与恢复见 [QA 运维入口](../../ops/qa/README.md)。
3. **监控**：东京 DLM 与 CPU/磁盘告警保留。宿主 HTTPS/job watcher 读取暂停 owner，
   要求所列 timer 停用且 service 不在运行；意外恢复会告警。daily diagnostics 复用同一暂停清单，
   暂停期不要求归档回执刷新，boundary 与存储检查保留。其他任务仍按成功时效监控。
   主机脚本不随网关镜像自动更新；安装/刷新独立 watcher 后才生效，仓库合并不代表已部署。
4. **资源**：临时 node1 reader 与克隆盘已完成最小核查并回收，原快照和导出保留；日本代理保留；
   新加坡其它项目资源不纳入 TokenKey 批量清理。详见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。

## AWS 残留与环境变量

计算节点迁移完成不等于整个系统零 AWS。以下为保留依赖与清理边界；不要按文件名或
`AWS_*` 前缀批量删除。历史审批文档记录当时的架构，现网执行以本文件和目标矩阵为准。

| 类别 | 当前用途 / owner | 清理条件 |
|---|---|---|
| SSM Hybrid / Parameter Store / STS OIDC | Lightsail / 历史 Hcloud 远程执行、参数与 GHCR 身份；`ops/stage0/resolve_prod_ssm_target.py` | 替换发布控制面后才能移除 AWS CLI、IAM 与 OIDC |
| QA raw archive | 存储配置为 AWS S3，当前上传已暂停；历史恢复按小时来源选择 | 按 [QA 运维入口](../../ops/qa/README.md#生产存储与恢复分界)核查实际到期与对象清单；AWS 桶、KMS 和恢复角色销毁另行审批 |
| 数据库备份 | prod 主路径为 pgBackRest 全量/增量/WAL；旧 pgdump 按用户授权退役，旧数据保留至少 30 天 | [物理备份验收](../../ops/stage0/physical-backup.md)与[旧任务退役](../../ops/stage0/pgdump-retirement.md)；cn-prod 独立 pgdump 保留 |
| env-secrets 与备份解密材料 | SSM SecureString；`backup-env-secrets-via-ssm.sh` 与 `/tokenkey/hetzner/<node>/stage0/pgdump-storage-recovery` | 随控制面另行迁移 |
| 历史 archive / dormant media 桶 | 历史数据与停用配置；媒体默认不启用 S3，只有显式 opt-in | 资产销毁另行审批，不能把停用 bucket 描述成现网迁移对象 |
| 共享运行文件的旧目录名 | `deploy/aws/stage0/` 内 compose、Caddy、QA lifecycle、fingerprint baseline；HZ renderer 与 backend embed 仍直接消费 | 是共享 owner，路径不代表部署在 AWS；迁路径必须同时更新引用、生成产物和 sentinel |
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

普通蓝绿发布不再注入 telemetry 的 AWS 桶/region 默认值，telemetry 默认关闭。
显式 opt-in 和主机已有配置继续保留；此变更不等于已清理运行容器中的休眠变量，也不表示
telemetry 已支持 Hetzner 存储。需要启用该能力时必须单独验收其存储契约。

## 数据库备份迁移入口

prod 主路径为 pgBackRest 全量/增量与连续 WAL；最新与历史同点恢复已通过。
执行命令、配置和剩余验证见 [物理备份变更单](../../ops/stage0/physical-backup.md)。
旧 pgdump 调度按用户授权退役，历史数据至少保留 30 天，不删除恢复材料；
脚本/工作流处置见 [退役清单](../../ops/stage0/pgdump-retirement.md)。
cn-prod 继续使用独立 pgdump，尚未获得物理替代路径验收。

## 恢复入口

先用 `resolve_prod_ssm_target.py --format json` 核实当前 prod 身份，
再用 `run-probe.sh` 读取主机/容器状态。镜像回滚走原 dispatcher 部署 previous tag。
数据库从 pgBackRest 仓库恢复到新盘，绝不能覆盖活库；跨云/新机流程见
[pgBackRest 迁机工作流](../../ops/stage0/pgbackrest-host-migration.md)。
每日 DLM 卷快照继续覆盖主机文件。物理备份不包含 Redis、配置密钥及外部对象。
旧 SQL 副本仍可用于隔离逻辑恢复，须核实所选备份的 usage 覆盖与加密材料。
真实恢复按需执行，下次结合腾讯云迁机演练；周定时建机已撤回，现有恢复证据过期告警保留。
已回收旧主机的数据从私有 S3 归档恢复到隔离库；不能再引用旧机挂载卷。

整机恢复先核对当前实例、卷、最近完成快照和当前主机的备份来源，再沿用
`tokenkey-host-replacement` 与 [AWS DR](../aws/RUNBOOK-disaster-recovery.md) 中适用的恢复步骤；
旧 CFN/实例身份不作为新机默认目标。旧机磁盘停写后的数据不能整库覆盖活库。
迁移收尾、QA 当前验收与旧机回收边界见 [收尾记录](../../docs/approved/aws-migration-closeout-20261016.md)。
