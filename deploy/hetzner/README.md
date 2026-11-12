# TokenKey 现网舰队 · 腾讯美西 prod 与美西出口代理

> **目录名 `deploy/hetzner/` 为历史路径。** Hetzner Cloud prod 已退役；`PROD_SSM_TARGET=hetzner` 失败关闭，Hetzner prod 供给 workflow/脚本已删除（2026-10-23）。本文件仍是现网舰队 SSOT（腾讯美西）。

**SSOT：** 现网节点 / 公网 IP / CIDR / GHCR PAT 路径 / 发版入口 **只在本文件叙述**；其它文档与 skill 只留指针，勿复制数值。
审批基线（决策锁，无 IP 表）：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)
切流回执：[`docs/ops/reports/tencent-cutover-20261022.json`](../../docs/ops/reports/tencent-cutover-20261022.json)

## 2026-11-11 应用替换准备中（未切流）

prod 和 cn-prod 均已拆分应用机与独立数据机，并非整个站点只剩应用机。
prod PG/Redis 在 `lhins-p43i2kxh` / `10.4.0.3`；cn-prod 数据身份见
[`prod-target.json`](../domestic/prod-target.json)。本次仅替换 prod 应用机，两个站点的数据服务和 cn-prod 应用不迁移、不重启。

原地 LUKS 搬盘已取消。新应用候选 `lhins-2kf31667` / `43.162.126.44` / `10.4.0.9`，
SSM `mi-000c0168439c243a8`（eu-west-2），2C8G / 80 GB SSD，95 元/月自动续费。
普通根盘 `/var/lib/docker`、`/var/lib/tokenkey`，不启动 PG/Redis 服务端。
候选储存身份：[app-parallel-20261111.json](../tencent/app-parallel-20261111.json)；
**该文件不改变默认 prod resolver**。DNS、CI 默认目标和 QA 归档 owner 仍是旧应用机。
新机 QA capture 临时关闭，不能在旧归档 owner 停用前开启新归档。

旧应用机现为 4C16G / 180 GB SSD，355 元/月；旧机不停止、不销毁，LUKS 和历史 QA 保留。
并行期间增加 95 元/月；只有另行授权退役旧机后，应用机月费才净减 260 元。
验收、剩余门禁与回执见[批准方案](../../docs/approved/design-prod-app-parallel-replacement.md#实际证据)。

2026-11-11 用户后续同意保留新旧双应用节点并演练摘机，取代最终退役旧应用机的目标；
当前仍未切流，不能据此宣称 HA 已上线。入口接入方式、费用、多节点发布与 QA 集中管理
边界见[后续决定](../../docs/approved/design-prod-app-parallel-replacement.md#2026-11-11-后续决定保留双应用节点)。

## 活舰队（prod-only，2026-10-22 切流起）

| 节点 | 正式域名 | 公网 IP | 控制面身份 | 备注 |
|------|----------|---------|------------|------|
| **prod** | `api.tokenkey.dev` | `43.162.83.233` | Lighthouse `lhins-fcwpwymt` · SSM Hybrid `mi-080ed76dc6d97d8b5` · eu-west-2 | 腾讯美西 · timeline 2 · LUKS mapper；Stage0 例外布局（见下） |

四个长期正式域名 A 记录（TTL 300）指向 `43.162.83.233`：`tokenkey.dev` / `api.tokenkey.dev` / `callmodel.io` / `api.callmodel.io`。`api-cn.tokenkey.dev` 仍暂时指向同一地址，日后退役。国内长期入口 `api.galaxywaveai.com` 指向腾讯北京 `154.8.214.179`，站点见 [`deploy/domestic/README.md`](../domestic/README.md)。
prod 直接使用原生账号；旧中继 stub 已软删。
**Tokyo `i-045f062ce33e1a5af` / `3.114.187.250` 已于 2026-10-23 完全退役**（实例、栈、VPC、卷、EIP、快照均已删；仅留 S3 历史链 `prod/postgres` timeline 1，只含数据库）。不能当作现网入口。
**布局（2026-10-22 例外路径已收敛）：** LUKS 双挂仍在；Docker Root **例外**保留 `/srv/tokenkey-rehearsal/docker`；业务 `.env` 在 `/var/lib/tokenkey/.env`；网关容器 `tokenkey` / `tokenkey-caddy`；PG/Redis 名仍为 cutover 字面量但带 `postgres`/`redis` 网络别名；Redis 自 2026-10-22 起 AOF（everysec）持久化，数据在 `/srv/tokenkey-rehearsal/redis`。默认可 `deploy-stage0`。细则 [`stage0-layout-convergence.md`](../tencent/stage0-layout-convergence.md)；unlock [`tencent-unlock-contract.md`](../../docs/ops/tencent-unlock-contract.md)。
历史数据从 S3 隔离归档读取。退役验收与归档入口见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。

### 同机附属 / 代理验证资源

| 角色 | 地域 / 实例 ID | 公网 IP | 私网 IP | 规格 |
|---|---|---|---|---|
| prod 旧应用机（仍接流） | na-siliconvalley-2 / `lhins-fcwpwymt` | `43.162.83.233` | `10.4.0.16` | 4C16G / 180 GB SSD |
| 美西出口 `usw-eval1` | na-siliconvalley-2 / `lhins-56ogvsid` | `43.162.90.151` | `10.4.0.2` | 2C2G / 40 GB SSD |
| 美西出口 `usw-eval2` | na-siliconvalley-2 / `lhins-mtiwmfod` | `43.162.126.190` | `10.4.0.11` | 2C2G / 40 GB SSD |
| 美西出口 `usw-eval3` | na-siliconvalley-2 / `lhins-n1w2c5qr` | `43.162.116.209` | `10.4.0.5` | 2C2G / 40 GB SSD |
| 美西出口 `usw-eval4` | na-siliconvalley-2 / `lhins-h12i2d2r` | `43.162.103.103` | `10.4.0.4` | 2C2G / 40 GB SSD |

实例到期约 2026-11-21/22 UTC，续费标志 `NOTIFY_AND_AUTO_RENEW`（2026-10-22 用户决定按月自动续费；账户余额不足仍会停机）。SSM/TAT 区域 `na-siliconvalley`；Hybrid 角色 `tokenkey-lightsail-ssm-hybrid`（eu-west-2）。
SOCKS 仅监听各机私网 `:1080`，防火墙只放行 prod `10.4.0.16`。
`usw-eval1..4` 曾承接原 `jp-*` 账号；2026-11-11 核对 prod 活配置已无该组，不能按历史表推断当前账号出口。
prod 实际活出口为腾讯北京四台、阿里北京四台和 AWS Oregon 四台，存在 default 别名重复行；
新应用候选已通过全部 16 条活配置的认证连通测试。Oregon 主机仍是保留的 AWS 计算依赖，不能描述成零 AWS。
活绑定以 prod / cn-prod `proxies` 为准。

#### 国内出口 `cn-ip1..4`（腾讯北京锐驰，公网 SOCKS）

| 角色 | 实例 | 公网 IP | 放行来源 |
|---|---|---|---|
| `cn-ip1` | `lhins-a3h0vmh0`（`tokenkey-cn-proxy-canary-20261022`） | `120.53.25.245` | 美西 prod `43.162.83.233`、cn-prod 公网 `154.8.214.179`（iptables `TK_CN_PROXY`）。UCloud `106.75.29.139` 已于 2026-10-27 退役；防火墙上若仍有这条源地址，没有主机在用，改规则时再删，不要顺手改掉北京地址 |
| `cn-ip2` | `lhins-lqhqjbke`（`tokenkey-cn-proxy-02-20261022`） | `140.143.236.199` | 同上 |
| `cn-ip3` | `lhins-bg2vs1uo`（`tokenkey-cn-proxy-03-20261022`） | `49.233.210.77` | 同上 |
| `cn-ip4` | `lhins-0ds0p3c2`（`tokenkey-cn-proxy-04-20261022`） | `140.143.229.164` | 同上 |

2026-11-06 现网核对：`cn-ip1..4` 的 iptables `TK_CN_PROXY` 只放行 `43.162.83.233`、`154.8.214.179` 和 `106.75.29.139`，没有私网 `10.2.4.11`。cn-prod 实际从公网 `154.8.214.179` 拨入，每台累计十几 GB 流量都记在这条规则上，`ss` 也只看到这两个公网来源。以前写的"北京应用从私网 `10.2.4.11` 拨 SOCKS"与现网不符。真正的强约束是：放行名单里必须有 `154.8.214.179`，漏了国内出口会 502。

`cn-ip5`（`192.144.148.117` / `lhins-leojyzyi`）已于 2026-10-22 退还并销毁。账号已迁走。cn-prod 应用在 `154.8.214.179`（`lhins-6qmb3fd6`），与 `cn-ip1..4` 同于 **2026-11-22** 到期，续费标志 `NOTIFY_AND_AUTO_RENEW`（2026-10-23 改）。到期自动扣费；账户余额不足仍会停机。UCloud `uhost-1wgyn7gu2f6a` / EIP `106.75.29.139` / 数据盘 / `uvnet-1w9lkxuadj3m` 已于 2026-10-27 销毁，SSM `mi-0efdf781205cff512` 已注销。`api-cn.tokenkey.dev` 仍暂时指向美西，日后退役；退役前不要把这条解析改到北京。

UCloud 原 `cn-ip*` UHost+EIP 已退服；上表为现网国内出口。

#### 国内出口 `cn-ip5..8`（阿里云北京轻量 SWAS，公网 SOCKS，2026-11-06 起）

腾讯北京锐驰配额上限 5 台，已被 `cn-ip1..4` 与 cn-prod 主机占满，所以扩容放在阿里云。阿里云主账号 `1598035194530033`。套餐 `swas.s.c2m1s30b1.linux`：2C1G、30 GB ESSD、200 Mbps 峰值，包月 40 元/台，自动续费，首期到期 **2026-12-06**。系统 Ubuntu 24.04。

| 角色 | cn-prod 代理名 | 实例 ID | 公网 IP | 私网 IP |
|---|---|---|---|---|
| `cn-ip5` | `ip5`（id 6） | `8e8fc5390cad43719d9daa7dcdf99321` | `47.94.148.90` | `172.24.15.8` |
| `cn-ip6` | `ip6`（id 7） | `23c73fc6c4604e229adfed3ec7fd93dd` | `47.95.11.119` | `172.24.15.9` |
| `cn-ip7` | `ip7`（id 8） | `c96d3742937f4003963df40c8c28a94a` | `47.95.14.76` | `172.24.15.10` |
| `cn-ip8` | `ip8`（id 9） | `e7c1865981ba47519aa86f495fb04e82` | `39.106.196.249` | `172.24.15.11` |

这里的 `cn-ip5` 是新机。上文已销毁的腾讯 `cn-ip5`（`192.144.148.117`）是另一台，名字重复使用了。

装机入口是 [`deploy/tencent/install-cn-socks-proxy.sh`](../tencent/install-cn-socks-proxy.sh)。它负责安装 GOST、写入 `tokenkey-proxy` 和 `tokenkey-proxy-firewall` 两个 unit、配置 BBR，最后装主机告警并跑 `--verify` 验收；GOST 版本、校验和与 1080 端口的放行来源都写在这个脚本里。生成的 iptables 规则和 `cn-ip1..4` 现网逐字一致。每台 SOCKS 密码独立，只存在主机和 cn-prod `proxies` 表里。阿里云实例防火墙额外开了 TCP 1080/8080 全网，和腾讯 `cn-ip1..4` 的宽松规则保持一致。远程执行走阿里云「云助手」SWAS `RunCommand`，不走 TAT 或 SSM。

#### 代理主机告警（强制）

每台 SOCKS 出口机（`usw-eval*` 与 `cn-ip*`）**新机/换机/重装后必须**安装 `tokenkey-disk-metrics.timer` + `tokenkey-proxy-watch.timer`，并以 `install-proxy-host-alerts.sh --verify` 验收。  
安装 owner：[`deploy/tencent/proxy-host-alerts.md`](../tencent/proxy-host-alerts.md)。凭据仅主机 `/opt/tokenkey/host-alerts.env`，不进 git。  
另有 prod 应用侧 `proxy_unreachable_count` P0（网关拨测），不能替代本机 timer。

活舰队矩阵：[`deploy/tencent/candidate-usw-20261022.json`](../tencent/candidate-usw-20261022.json)（`deployable=true`；`resolve_prod_ssm_target.py --target auto` → 本机）。

### 活物理备份仓库（2026-11-08 起 COS 唯一）

2026-11-08 用户决定停写 AWS repo1，COS 为唯一 repository（[设计](../../docs/approved/design-tencent-cos-backup-node1-qa.md#2026-11-08-决定停写-aws-repo1cos-为唯一-repository)）。

| 项 | 值 |
|---|---|
| 活仓库 | pgBackRest `repo2`：COS `tokenkey-prod-backup-usw-1320330536`（`cos.na-siliconvalley.myqcloud.com`）`/prod/postgres` |
| 配置渲染 | 数据机上的 `tokenkey-backup-identity.timer`（15min）→ `tencent_backup_identity.py refresh`，读数据机 root-only `/etc/tokenkey/cos-backup.env`，重写 `/etc/tokenkey/pgbackrest-cos/pgbackrest.conf`（`root:postgres 0640`，容器只读挂载）。由 `ops/stage0/install-data-machine-identity.sh` 安装。应用机上的 identity timer 停用；不读 AWS 会话、不调 STS |
| 备份/检查 | 数据机上的 `tokenkey-postgres-backup.timer`（偶数小时 :30 UTC）和 `tokenkey-postgres-archive-check.timer`（5min）。应用机上这两项已停用 |
| 已停写（保留待清理） | AWS `s3://tokenkey-prod-physical-backup-682751977094-us-east-1/candidates/tencent-usw-20261022/postgres`（timeline 2）与同桶 Tokyo 历史 `prod/postgres` |

### 日本出口代理（已关停 · 2026-10-23 删除）

2026-10-22 账号绑定已迁到 `usw-eval*`；`jp-*` 行已于 2026-10-23 在 prod `proxies` 表软删（绑定账号数为 0）。2026-10-22 经用户批准，Lightsail 机（含 `tokenkey-node1-ingress-jp-20261016-vm`）已于 2026-10-23 **删除并释放静态 IP**（不可逆）；最终快照也已于同日删除（[runbook §6](../../docs/ops/tokyo-retirement-runbook.md)）。

**活公网 IP / 绑定以 prod `proxies` 表为准**（Web：`https://tokenkey.dev/admin/proxies`；
探针：`ops/observability/probe-list-proxies.sh`）。下表只记逻辑名与常见 Lightsail 名：

| prod 代理名 | Lightsail 实例（常见） | 区域 | 状态 |
|---|---|---|---|
| jp-uk1 | jp-proxy-01-micro | ap-northeast-1 | 已删除（2026-10-23） |
| jp-uk2 | jp-proxy-02-micro | ap-northeast-1 | 已删除（2026-10-23） |
| jp-us4 | jp-proxy-03-micro | ap-northeast-1 | 已删除（2026-10-23） |
| jp-us5 | jp-proxy-04-micro | ap-northeast-1 | 已删除（2026-10-23） |

名称含 uk/us 不代表物理地域。BBR 验收：`python3 scripts/checks/check-host-bbr-ssot.py --live`。

- 活 prod 身份：`../tencent/candidate-usw-20261022.json`（`deployable=true` 时由 `resolve_prod_ssm_target` 解析）。
- 宿主 watcher owner：`../tencent/candidate-usw-20261022.json` 的 `host_watch`（`hostname` / `domains` / `paused_jobs` / `local_qa_jobs`）。
- Tokyo 已于 2026-10-23 完全退役（实例/栈/VPC/卷/快照已删，仅留 S3 历史链），`aws-fresh` 解析路径与 `fresh-migration-20261015.json` 已删除（[runbook](../../docs/ops/tokyo-retirement-runbook.md)）。
- watcher 不随仓库合并更新：主机按安装时的内容寻址副本运行（`/usr/local/lib/tokenkey/https-watch/<hash>/config.json`）。改 `host_watch` 后须**经审批**重装：把 `install-host-https-watch.sh`、`host_https_watch.py`、`https_health.py`、`feishu_delivery.py`、`deploy/tencent/candidate-usw-20261022.json`、`deploy/domestic/prod-target.json` 按 basename 放进 `WATCH_BUNDLE_DIR`，先不带 `APPLY` 跑一遍（只核身份并打印配置），与现装 config 比对后再以 `APPLY=yes-host-https-watch` 安装；prod 与 cn-prod（`WATCH_ROLE=cn-prod`）都要装。2026-10-22 起 `local_qa_jobs` 新增 `tokenkey-qa-provision`（迁机漏装）；provision 已于 2026-10-22 13:29 UTC 补装（[runbook §8](../../docs/ops/tokyo-retirement-runbook.md#8-迁机漏装tokenkey-qa-provision已补装-2026-10-22)）；prod watcher 已于 2026-10-22 15:24 UTC 重装纳入（bundle `95998671d971c8ce`）。
- 换机流程：[host replacement](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 现网不变量

| 项 | 值 |
|----|-----|
| 活入口 | 腾讯美西 prod `43.162.83.233`；正式域名 A 指向该 IP（Tokyo 已退役） |
| prod `API_DOMAIN` | 单正式名（无 `-hz` 双 SAN） |
| 探针 / sync Caddy | prod 解析至腾讯 Hybrid `mi-*`（`resolve_prod_ssm_target.py`）；冻结/退役身份拒绝执行 |
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

日常发版仅 prod。2026-10-22 起已在腾讯机走完首次蓝绿（`tokenkey` 旧名现为 socat 控制代理，活动色 `tokenkey-blue/green`）。共享 compose、Caddy、QA 和 SSM 实现仍由现网使用；pgdump 转入历史恢复保留。

## 开放运维项（非过程史）

1. **账号**：prod 原生账号承接请求；错误、不可调度状态由运营处理，不自动恢复中继或旧副本。
2. **QA**：capture 沿现有配置；QA S3 已退役，2026-11-08 用户决定；现行路径为本地 24h + node1 每 4h（主机定时器 `tokenkey-qa-archive`）。
   旧 `tokenkey-qa-maintenance`/`tokenkey-qa-boundary` 已从仓库与 watcher 删除，主机上已 mask 的单元
   留待单独清理。巡检见 [QA 运维入口](../../ops/qa/README.md)。
3. **监控**：腾讯机无 EBS/DLM；Tokyo 已退役，快照已全部删除（见 [runbook](../../docs/ops/tokyo-retirement-runbook.md)）。宿主 HTTPS/job watcher 读取暂停 owner，
   要求所列 timer 停用且 service 不在运行；意外恢复会告警。其他任务（含 `local_qa_jobs`）按成功时效监控。
   主机脚本不随网关镜像自动更新；安装/刷新独立 watcher 后才生效，仓库合并不代表已部署。
4. **资源**：临时 node1 reader 与克隆盘已完成最小核查并回收，原快照和导出保留；日本 Lightsail 代理已于 2026-10-23 删除；
   新加坡其它项目资源不纳入 TokenKey 批量清理。详见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。
5. **告警**：腾讯机上备份/归档 timer 已装；`tokenkey-https-watch.timer` 已接到飞书（cn-prod `/health` + 备份/archive/qa-cleanup）。Stage0 布局已按例外路径收敛；日常发版走下方 `dispatch-prod-deploy.sh`。

## AWS 残留与环境变量

计算节点迁移完成不等于整个系统零 AWS。以下为保留依赖与清理边界；不要按文件名或
`AWS_*` 前缀批量删除。历史审批文档记录当时的架构，现网执行以本文件和目标矩阵为准。

| 类别 | 当前用途 / owner | 清理条件 |
|---|---|---|
| SSM Hybrid / Parameter Store / STS OIDC | 腾讯美西 prod/出口机与北京 cn-prod 远程执行、参数与 GHCR 身份；`ops/stage0/resolve_prod_ssm_target.py` | 替换发布控制面后才能移除 AWS CLI、IAM 与 OIDC |
| QA raw archive | QA S3 已于 2026-11-08 退役；仓库模板与恢复 CLI 已删除，历史对象按七天生命周期到期 | 删除 `tokenkey-prod-qa-raw-archive` CFN 栈（含桶、KMS、恢复角色）由单独收尾执行，见 [QA 运维入口](../../ops/qa/README.md#生产存储与恢复分界) |
| 数据库备份 | prod 主路径为 pgBackRest 全量/增量/WAL；旧 pgdump 按用户授权退役，旧数据保留至少 30 天 | [物理备份验收](../../ops/stage0/physical-backup.md)与[旧任务退役](../../ops/stage0/pgdump-retirement.md)。cn-prod 逻辑 dump 已停，见 [domestic README](../domestic/README.md) |
| env-secrets 与备份解密材料 | SSM SecureString；`backup-env-secrets-via-ssm.sh` 与 `/tokenkey/hetzner/<node>/stage0/pgdump-storage-recovery` | 随控制面另行迁移 |
| 历史 archive / dormant media 桶 | 历史数据与停用配置；媒体默认不启用 S3，只有显式 opt-in | 资产销毁另行审批，不能把停用 bucket 描述成现网迁移对象 |
| 共享运行文件的旧目录名 | `deploy/aws/stage0/` 内 compose、Caddy、QA lifecycle、fingerprint baseline；HZ renderer 与 backend embed 仍直接消费 | 是共享 owner，路径不代表部署在 AWS；迁路径必须同时更新引用、生成产物和 sentinel |
| 第三方 AWS 协议 | Kiro/Bedrock 的 AWS endpoint、SDK/SigV4、S3 兼容客户端 | 属模型供应商/协议能力，不能随基础设施迁移删除 |

| 变量 | 语义 |
|---|---|
| `PROD_SSM_TARGET` | `auto`/未设置 → 腾讯美西（矩阵 `deployable=true`）；显式 `tencent-usw-candidate` 同路径。`aws-fresh`（Tokyo）已退役拒绝。`hetzner` / `aws` 已退役身份直接拒绝。GitHub Actions 仓库变量须为 `auto`，勿钉死 `aws-fresh`。 |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | AWS API 区域。SSM 命令必须用 resolver 返回的 `ssm_region`，不能拿历史 hcloud `fsn1` 或旧 EC2 region 代替；QA/S3 仍按各自存储区域 |
| `AWS_OIDC_ROLE_ARN` / `AWS_SHARED_CREDENTIALS_FILE` | 仍用于 Hybrid 控制面与现有 AWS 计算及数据依赖；凭据内容不可打印 |
| `PROD_STACK_NAME` / `STACK_NAME` | 历史 AWS 或仍在使用的 QA/备份 CFN；不能代替当前 prod 身份 |
| `HCLOUD_TOKEN` | **已退役**；勿再用于 TokenKey prod 供给 |
| `HCLOUD_S3_Access_Key` / `HCLOUD_S3_Secret_Key` | 本机受保护的对象存储凭据；不输出、不入库 |
| `QA_*` | QA 独立迁移契约；不得因网关迁完而直接切换默认值 |
| `QA_ARCHIVE_*` | QA S3 归档 2026-11-08 退役；prod 蓝绿发布不再注入或校验这些键，compose 也不再传入容器（网关缺省关闭归档）。node1 副本由主机定时器 `tokenkey-qa-archive` 负责 |

媒体默认 AWS 坐标清理：`ops/stage0/retire_dormant_media_storage.py` 默认 plan；
通过 `run-probe --script ops/stage0/retire_dormant_media_storage_remote.sh`
附带 `--with ops/stage0/retire_dormant_media_storage.py` 和 prod 矩阵，追加 `--env APPLY=yes-retire-dormant-media` 前必须确认 image offload 未启用、
无显式 image storage 设置和视频任务。只删已知休眠默认值，自定义配置拒绝处理。
运行容器的旧环境变量在下一次正常蓝绿发布时消失；此入口不重启容器。

## QA 与备份排队、旧 AWS 定时任务收尾

共享脚本使用 `/var/lib/tokenkey/qa-lifecycle/host.lock`：pg_dump 持共享锁，本地 QA
provision/cleanup 持独占锁；备份完成本地文件后立即解锁，上传不阻塞 QA。本地 QA 最多等待 600 秒，
备份最多等待 2400 秒；超时任务失败，不跳过数据库短锁超时。

QA S3 退役（2026-11-08）后，旧 maintenance/boundary 宿主脚本已从仓库删除。主机上已 mask 的
单元与 `/usr/local/bin/tokenkey-qa-{maintenance,boundary}.sh` 留待单独的主机清理；清理前保持 mask，
`local_maintenance_host.py` 的 `require_legacy_paused()` 会在它们被意外启用时拒绝运行本地 QA 任务。

普通蓝绿发布不再注入 telemetry 的 AWS 桶/region 默认值，telemetry 默认关闭。
显式 opt-in 和主机已有配置继续保留；此变更不等于已清理运行容器中的休眠变量，也不表示
telemetry 已支持 Hetzner 存储。需要启用该能力时必须单独验收其存储契约。

## 数据库备份迁移入口

prod 主路径为 pgBackRest 全量/增量与连续 WAL；最新与历史同点恢复已通过。
执行命令、配置和剩余验证见 [物理备份变更单](../../ops/stage0/physical-backup.md)。
旧 pgdump 调度按用户授权退役，历史数据至少保留 30 天，不删除恢复材料；
脚本/工作流处置见 [退役清单](../../ops/stage0/pgdump-retirement.md)。
cn-prod 逻辑 dump 已停。验收是北京本机 pgBackRest 能恢复，见 [domestic README](../domestic/README.md)。

## 恢复入口

先用 `resolve_prod_ssm_target.py --format json` 核实当前 prod 身份，
再用 `run-probe.sh` 读取主机/容器状态。镜像回滚走原 dispatcher 部署 previous tag。
数据库从 pgBackRest 仓库恢复到新盘，绝不能覆盖活库；跨云/新机流程见
[pgBackRest 迁机工作流](../../ops/stage0/pgbackrest-host-migration.md)。
腾讯机目前**没有**整机/卷快照覆盖（Tokyo 的 DLM 不适用）。物理备份不包含 Redis、配置密钥及外部对象。
旧 SQL 副本仍可用于隔离逻辑恢复，须核实所选备份的 usage 覆盖与加密材料。
真实恢复按需执行，下次结合腾讯云迁机演练；周定时建机已撤回。已成功但过期的验证保留每日提醒，
未验证、验证失败或当前备份链故障继续告警，分级规则见[物理备份](../../ops/stage0/physical-backup.md)。
已回收旧主机的数据从私有 S3 归档恢复到隔离库；不能再引用旧机挂载卷。

整机恢复先核对当前实例、卷、最近完成快照和当前主机的备份来源，再沿用
`tokenkey-host-replacement` 与 [AWS DR](../aws/RUNBOOK-disaster-recovery.md) 中适用的恢复步骤；
旧 CFN/实例身份不作为新机默认目标。旧机磁盘停写后的数据不能整库覆盖活库。
迁移收尾、QA 当前验收与旧机回收边界见 [收尾记录](../../docs/approved/aws-migration-closeout-20261016.md)。
