# TokenKey 现网舰队 · 腾讯美西 prod 与美西出口代理

> **目录名 `deploy/hetzner/` 为历史路径。** Hetzner Cloud prod 已退役；`PROD_SSM_TARGET=hetzner` 与 `deploy-prod-hetzner-stage0` 供给均失败关闭。本文件仍是现网舰队 SSOT（腾讯美西）。

**SSOT：** 现网节点 / 公网 IP / CIDR / GHCR PAT 路径 / 发版入口 **只在本文件叙述**；其它文档与 skill 只留指针，勿复制数值。
审批基线（决策锁，无 IP 表）：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)
切流回执：[`docs/ops/reports/tencent-cutover-20261022.json`](../../docs/ops/reports/tencent-cutover-20261022.json)

## 活舰队（prod-only，2026-10-22 切流起）

| 节点 | 正式域名 | 公网 IP | 控制面身份 | 备注 |
|------|----------|---------|------------|------|
| **prod** | `api.tokenkey.dev` | `43.162.83.233` | Lighthouse `lhins-fcwpwymt` · SSM Hybrid `mi-080ed76dc6d97d8b5` · eu-west-2 | 腾讯美西 · timeline 2 · LUKS mapper；现网仍为 cutover 布局（见下） |

四正式域名 A 记录（TTL 300）均指向 `43.162.83.233`：`tokenkey.dev` / `api.tokenkey.dev` / `api-cn.tokenkey.dev` / `callmodel.io` / `api.callmodel.io`。
prod 直接使用原生账号；旧中继 stub 已软删。
**Tokyo `i-045f062ce33e1a5af` / `3.114.187.250` 已冻结写者，保留 ≥7 天；禁止解冻双写。** 不能当作现网入口。
**布局（2026-10-22 例外路径已收敛）：** LUKS 双挂仍在；Docker Root **例外**保留 `/srv/tokenkey-rehearsal/docker`；业务 `.env` 在 `/var/lib/tokenkey/.env`；网关容器 `tokenkey` / `tokenkey-caddy`；PG/Redis 名仍为 cutover 字面量但带 `postgres`/`redis` 网络别名。细则见 [`stage0-layout-convergence.md`](../tencent/stage0-layout-convergence.md)。
历史数据从 S3 隔离归档读取。退役验收与归档入口见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。

### 同机附属 / 代理验证资源

| 角色 | 地域 / 实例 ID | 公网 IP | 私网 IP | 规格 |
|---|---|---|---|---|
| prod 主机 | na-siliconvalley-2 / `lhins-fcwpwymt` | `43.162.83.233` | `10.4.0.16` | 4C8G / 120 GB SSD |
| 美西出口 `usw-eval1` | na-siliconvalley-2 / `lhins-56ogvsid` | `43.162.90.151` | `10.4.0.2` | 2C2G / 40 GB SSD |
| 美西出口 `usw-eval2` | na-siliconvalley-2 / `lhins-mtiwmfod` | `43.162.126.190` | `10.4.0.11` | 2C2G / 40 GB SSD |
| 美西出口 `usw-eval3` | na-siliconvalley-2 / `lhins-n1w2c5qr` | `43.162.116.209` | `10.4.0.5` | 2C2G / 40 GB SSD |
| 美西出口 `usw-eval4` | na-siliconvalley-2 / `lhins-h12i2d2r` | `43.162.103.103` | `10.4.0.4` | 2C2G / 40 GB SSD |

实例到期约 2026-11-21/22 UTC，`NOTIFY_AND_MANUAL_RENEW`。SSM/TAT 区域 `na-siliconvalley`；Hybrid 角色 `tokenkey-lightsail-ssm-hybrid`（eu-west-2）。
SOCKS 仅监听各机私网 `:1080`，防火墙只放行 prod `10.4.0.16`。**2026-10-22 起 `usw-eval1..4` 为原 `jp-*` 账号的生产出口**（私网 SOCKS）；`cn-ip*` 仍走国内代理。活绑定以 prod `proxies` / 账号 `proxy_id` 为准。
活舰队矩阵：[`deploy/tencent/candidate-usw-20261022.json`](../tencent/candidate-usw-20261022.json)（`deployable=true`；`resolve_prod_ssm_target.py --target auto` → 本机）。

### 活物理备份前缀（切流后）

| 项 | 值 |
|---|---|
| 活仓库 | `s3://tokenkey-prod-physical-backup-682751977094-us-east-1/candidates/tencent-usw-20261022/postgres`（timeline 2） |
| 历史只读 | 同桶 `prod/postgres`（Tokyo timeline 1；腾讯身份仅 Get） |
| 身份续期 | `tokenkey-backup-identity.timer` → `/run/tokenkey-backup-candidate/` |
| 备份/检查 | `tokenkey-postgres-backup.timer`（偶数小时 :30 UTC）+ `tokenkey-postgres-archive-check.timer`（5min） |
| 升格策略 | **不**把活链写回 `prod/postgres`（避免与冻结东京链混写）；候选前缀即现网活备份 SSOT，直至另行批准迁移路径 |

### 日本出口代理（已迁出 · 保留待关停）

2026-10-22 账号绑定已迁到 `usw-eval*`；`jp-*` 行仍在 prod `proxies` 表，但 **绑定账号数应为 0**。Lightsail 机保留至另批关停审批。

**活公网 IP / 绑定以 prod `proxies` 表为准**（Web：`https://tokenkey.dev/admin/proxies`；
探针：`ops/observability/probe-list-proxies.sh`）。下表只记逻辑名与常见 Lightsail 名：

| prod 代理名 | Lightsail 实例（常见） | 区域 | 状态 |
|---|---|---|---|
| jp-uk1 | jp-proxy-01-micro | ap-northeast-1 | 已迁出 |
| jp-uk2 | jp-proxy-02-micro | ap-northeast-1 | 已迁出 |
| jp-us4 | jp-proxy-03-micro | ap-northeast-1 | 已迁出 |
| jp-us5 | jp-proxy-04-micro | ap-northeast-1 | 已迁出 |

名称含 uk/us 不代表物理地域。BBR 验收：`python3 scripts/checks/check-host-bbr-ssot.py --live`。

- 活 prod 身份：`../tencent/candidate-usw-20261022.json`（`deployable=true` 时由 `resolve_prod_ssm_target` 解析）。
- `../aws/fresh-migration-20261015.json` 仅保留冻结 Tokyo inventory，以及 https-watch 的 `https_watch_hostname` / `paused_jobs` / `local_qa_jobs`；不得当作活 SSM 目标。
- 换机流程：[host replacement](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 现网不变量

| 项 | 值 |
|----|-----|
| 活入口 | 腾讯美西 prod `43.162.83.233`；正式域名 A 指向该 IP（Tokyo 已冻结） |
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
4. **资源**：临时 node1 reader 与克隆盘已完成最小核查并回收，原快照和导出保留；日本 Lightsail 代理账号已迁出、机器保留待关停；
   新加坡其它项目资源不纳入 TokenKey 批量清理。详见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。
5. **告警**：腾讯机上备份/归档 timer 已装；`tokenkey-https-watch.timer` 已接到飞书（cn-prod `/health` + 备份/archive/qa-cleanup）。Stage0 布局已按例外路径收敛；日常发版走下方 `dispatch-prod-deploy.sh`。

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
| `PROD_SSM_TARGET` | `auto`/未设置 → 腾讯美西（矩阵 `deployable=true`）；显式 `tencent-usw-candidate` 同路径。`aws-fresh` 为冻结 Tokyo，默认拒绝（保留只读须 `ALLOW_FROZEN_TOKYO_TARGET=1`）。`hetzner` / `aws` 已退役身份直接拒绝。GitHub Actions 仓库变量须为 `auto`，勿钉死 `aws-fresh`。 |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | AWS API 区域。SSM 命令必须用 resolver 返回的 `ssm_region`，不能拿历史 hcloud `fsn1` 或旧 EC2 region 代替；QA/S3 仍按各自存储区域 |
| `AWS_OIDC_ROLE_ARN` / `AWS_SHARED_CREDENTIALS_FILE` | 仍用于 Hybrid 控制面与现有 AWS 计算及数据依赖；凭据内容不可打印 |
| `PROD_STACK_NAME` / `STACK_NAME` | 历史 AWS 或仍在使用的 QA/备份 CFN；不能代替当前 prod 身份 |
| `HCLOUD_TOKEN` | **已退役**；勿再用于 TokenKey prod 供给 |
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
真实恢复按需执行，下次结合腾讯云迁机演练；周定时建机已撤回。已成功但过期的验证保留每日提醒，
未验证、验证失败或当前备份链故障继续告警，分级规则见[物理备份](../../ops/stage0/physical-backup.md)。
已回收旧主机的数据从私有 S3 归档恢复到隔离库；不能再引用旧机挂载卷。

整机恢复先核对当前实例、卷、最近完成快照和当前主机的备份来源，再沿用
`tokenkey-host-replacement` 与 [AWS DR](../aws/RUNBOOK-disaster-recovery.md) 中适用的恢复步骤；
旧 CFN/实例身份不作为新机默认目标。旧机磁盘停写后的数据不能整库覆盖活库。
迁移收尾、QA 当前验收与旧机回收边界见 [收尾记录](../../docs/approved/aws-migration-closeout-20261016.md)。
