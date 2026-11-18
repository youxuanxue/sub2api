# TokenKey 现网舰队 · 腾讯美西 prod 与美西出口代理

> **目录名 `deploy/hetzner/` 为历史路径。** Hetzner Cloud prod 已退役；`PROD_SSM_TARGET=hetzner` 失败关闭，Hetzner prod 供给 workflow/脚本已删除（2026-10-23）。本文件仍是现网舰队 SSOT（腾讯美西）。

**SSOT：** 现网节点 / 公网 IP / CIDR / GHCR PAT 路径 / 发版入口 **只在本文件叙述**；其它文档与 skill 只留指针，勿复制数值。
审批基线（决策锁，无 IP 表）：[`docs/approved/hetzner-cloud-full-migration.md`](../../docs/approved/hetzner-cloud-full-migration.md)
切流回执：[`docs/ops/reports/tencent-cutover-20261022.json`](../../docs/ops/reports/tencent-cutover-20261022.json)

## 高可用与等效容灾（对外表述）

TokenKey 生产具备**多地域等效容灾**与**站点内应用高可用**：

| 层级 | 事实 | 含义 |
|---|---|---|
| 多地域 | 腾讯美西 **prod** 与腾讯北京 **cn-prod** 双站点；库、Redis、备份仓库相互独立，不共用控制面数据库 | 单地域整站故障时，另一地域仍可独立服务（等效跨地域容灾） |
| 站点内 HA | 每站「入口机 + **多应用机** WRR + 独立数据机」；入口按 `/health` 摘除不健康应用机；发版滚动不中断 | 单应用机故障或发版窗口不导致整站不可用 |

地域内入口机可不做热备（故障靠重建证书/入口），这是站内细节，**不否定**上表多地域等效容灾与多应用机 HA。国内入口见 [`deploy/domestic/README.md`](../domestic/README.md)。

## 2026-11-15/16 入口机 + 双应用机（已切 DNS）

prod 与 cn-prod 均为「入口机 + 多应用机 + 独立数据机」。设计见
[`design-prod-entry-multi-app.md`](../../docs/approved/design-prod-entry-multi-app.md)
与 [`design-cn-prod-rolling-deploy.md`](../../docs/approved/design-cn-prod-rolling-deploy.md)。

prod PG/Redis 在 `lhins-p43i2kxh` / `10.4.0.3`；cn-prod 数据身份见
[`prod-target.json`](../domestic/prod-target.json)。

| 角色 | 实例 | 公网 IP | 私网 | SSM (eu-west-2) | 备注 |
|---|---|---|---|---|---|
| **prod 入口** | `lhins-3am1i23j` | `43.162.119.210` | `10.4.0.8` | `mi-026982f3622163a1b` | Caddy 终止五域名 TLS；WRR `10.4.0.16:8080`×2 + `10.4.0.9:8080`×1；公网只开 80/443 |
| prod 应用（旧） | `lhins-fcwpwymt` | `43.162.83.233` | `10.4.0.16` | `mi-080ed76dc6d97d8b5` | 4C16G / LUKS；Phase C 单容器 `tokenkey`（`:8080`/`:18080`）；**公网 80/443 已关**，仅入口 `/32` 打 8080 |
| prod 应用（候选） | `lhins-2kf31667` | `43.162.126.44` | `10.4.0.9` | `mi-000c0168439c243a8` | 4C8G（`bundle_rs_nmc_lin_lg8_01`，2026-11-18 由 2C8G 安全滚动升配）明文根盘；Phase C 单容器同形 |
| prod 数据 | `lhins-p43i2kxh` | — | `10.4.0.3` | `mi-044ba9da6453c1557` | PG/Redis |

五域名 A（TTL 60）均指向入口 `43.162.119.210`：`tokenkey.dev` / `api.tokenkey.dev` /
`api-cn.tokenkey.dev` / `callmodel.io` / `api.callmodel.io`。国内入口
`api.galaxywaveai.com` → 腾讯北京入口 `154.8.185.224`（`10.2.4.7`），见
[`deploy/domestic/README.md`](../domestic/README.md)。

候选储存身份：[app-parallel-20261111.json](../tencent/app-parallel-20261111.json)；
**主机诊断**：`run-probe.sh --target prod` 默认按 `apps[]` **扇出**到每台有
`ssm_instance_id` 的应用机；单台用 `--node app-old|app-cand`。不要再把
`resolve_prod_ssm_target` 的单机默认当成「整站诊断」（改 resolver 默认另批）。
**2026-11-16 Phase C 代码/矩阵已收敛；2026-11-17 线上布局收口完成**：两台均为单容器
（`private:8080` + `loopback:18080`→200）。
2026-11-17：prod 两台应用机与 cn-prod app-a 上已停用的 `tokenkey-blue/green/caddy`
容器已删除（仅在确认 `tokenkey` healthy 后 `docker rm`；未动 `*-client`）。
矩阵 `layout.kind=single-container`；
日常发版走 `rolling.py` + `prod-rolling-deploy.yml`（确认词 `rolling-prod-<tag>`，
默认 `--via tat` / `tencent-ops`；SSM 仅应急）。**真换版本 rolling receipt**：[run 44058916390](https://github.com/youxuanxue/sub2api/actions/runs/44058916390)
（`1.8.381`；`app-cand`/`app-old` 均为 `mode=full` / `outcome=rolled`）。
`prepare_single_container.py` 仅作历史/重铺参考。旧机脱离 LUKS 是阶段 D
（须 ≥24h 稳定 + 单台门禁 + 再批准）。
舰队矩阵形状门禁：`python3 deploy/tencent/check_prod_fleet_matrix.py`。

**Tokyo `i-045f062ce33e1a5af` / `3.114.187.250` 已于 2026-10-23 完全退役**（实例、栈、VPC、卷、EIP、快照均已删；仅留 S3 历史链 `prod/postgres` timeline 1，只含数据库）。不能当作现网入口。
旧应用机布局例外：LUKS 双挂；Docker Root 保留 `/srv/tokenkey-rehearsal/docker`；业务 `.env` 在 `/var/lib/tokenkey/.env`。细则 [`stage0-layout-convergence.md`](../tencent/stage0-layout-convergence.md)；unlock [`tencent-unlock-contract.md`](../../docs/ops/tencent-unlock-contract.md)。

SSM/TAT 区域 `na-siliconvalley`；Hybrid 角色 `tokenkey-lightsail-ssm-hybrid`（eu-west-2）。
SOCKS 仅监听各机私网 `:1080`；放行来源见下方 `ALLOWED_SOURCES`（**应用机公网 IP**，不是入口 IP）。
`usw-eval1..4` 已于 2026-11-15 销毁。Oregon 主机仍是保留的 AWS 计算依赖。活绑定以 prod / cn-prod `proxies` 为准。
**2026-11-17 核实**：prod 仍有 active `or-1..4`（SOCKS `35.160.218.198` / `54.186.89.199` / `35.165.11.247` / `32.191.95.59`），
`live_accounts` 分别为 32 / 31 / 28 / 33（合计 124）；fallback/backup 引用为 0。
cn-prod `proxies` **无** `or-*`，仅 Aliyun `ip5..8` + `cn-ip9..12`。Oregon **不可退役**。

#### 国内出口 `cn-ip9..12`（阿里云北京轻量 SWAS，公网 SOCKS，2026-11-15 起）

原腾讯北京锐驰出口 `cn-ip1..4`（`lhins-a3h0vmh0` / `lhins-lqhqjbke` / `lhins-bg2vs1uo` / `lhins-0ds0p3c2`）已于 2026-11-15 迁走全部账号，02:02 UTC 隔离，08:05 UTC 经用户批准销毁（`TerminateInstances`）。prod `cn-ip1..4`（id 14–17）与 cn-prod `ip1..4`（id 2–5）已于 08:02 UTC 软删。退还目的是腾出腾讯北京锐驰配额，供后续入口机与应用机使用。

阿里云主账号 `1598035194530033`，套餐 `swas.s.c2m1s30b1.linux`（2C1G / 30 GB ESSD / 200 Mbps 峰值 / 不限流量，40 元/台/月），到期 **2026-12-15**，自动续费。

| 角色 | 实例 ID | 公网 IP | prod `proxies.id` | cn-prod `proxies.id` |
|---|---|---|---|---|
| `cn-ip9` | `2d143188cce448b1b38ccf513a92b568` | `59.110.104.56` | 33 | 14 |
| `cn-ip10` | `c2e58ded0b8848c385fd22ce381c7933` | `59.110.105.229` | 34 | 15 |
| `cn-ip11` | `3e42ea87a00e430fa4c411ed5cea1bc8` | `101.201.123.226` | 35 | 16 |
| `cn-ip12` | `f6ddf117c12c4e29ac106fecce2658c2` | `39.107.40.6` | 36 | 17 |

迁移方式是换绑：在两个站点各新增 `cn-ip9..12` 记录，再把 `cn-ip1..4` 的账号分批改绑过去，旧记录不删不改 host，随时可回滚。迁移后已验证：两端 `cn-ip1..4` 绑定为 0，新代理承接真实流量，代理相关错误为 0。

**1080 放行来源**（唯一 owner 是 `deploy/tencent/install-cn-socks-proxy.sh` 的 `ALLOWED_SOURCES`）：`43.162.83.233`（prod 旧应用机）、`43.162.126.44`（prod 候选应用机）、`154.8.214.179`（cn-prod app-a）、`82.156.89.64`（cn-prod app-b）。入口机公网 IP **不要**加入此名单。UCloud `106.75.29.139` 已于 2026-10-27 销毁，不再放行。云防火墙要与这份名单一致，不要对 `0.0.0.0/0` 开放 22、8080 或 1080；新建实例必须先删掉控制台默认的公网 22 规则。

#### 历史出口（已退役）

- `cn-ip1..4`：腾讯北京锐驰，2026-11-15 退还。
- 旧 `cn-ip5`（`192.144.148.117` / `lhins-leojyzyi`）：2026-10-22 退还并销毁。与下文现役 `cn-ip5` 同名但不是同一台。
- UCloud `uhost-1wgyn7gu2f6a` / EIP `106.75.29.139` / `uvnet-1w9lkxuadj3m`：2026-10-27 销毁，SSM `mi-0efdf781205cff512` 已注销。

#### 国内出口 `cn-ip5..8`（阿里云北京轻量 SWAS，公网 SOCKS，2026-11-06 起）

腾讯北京锐驰配额上限 5 台。2026-11-06 首次扩容时配额已被当时的 `cn-ip1..4` 与 cn-prod 主机占满，因此把出口扩容放在阿里云；2026-11-15 `cn-ip1..4` 退还后配额已释放，但出口继续留在阿里云，不再回迁。阿里云主账号 `1598035194530033`。套餐 `swas.s.c2m1s30b1.linux`：2C1G、30 GB ESSD、200 Mbps 峰值，包月 40 元/台，自动续费，首期到期 **2026-12-06**。系统 Ubuntu 24.04。

| 角色 | cn-prod 代理名 | 实例 ID | 公网 IP | 私网 IP |
|---|---|---|---|---|
| `cn-ip5` | `ip5`（id 6） | `8e8fc5390cad43719d9daa7dcdf99321` | `47.94.148.90` | `172.24.15.8` |
| `cn-ip6` | `ip6`（id 7） | `23c73fc6c4604e229adfed3ec7fd93dd` | `47.95.11.119` | `172.24.15.9` |
| `cn-ip7` | `ip7`（id 8） | `c96d3742937f4003963df40c8c28a94a` | `47.95.14.76` | `172.24.15.10` |
| `cn-ip8` | `ip8`（id 9） | `e7c1865981ba47519aa86f495fb04e82` | `39.106.196.249` | `172.24.15.11` |

这里的 `cn-ip5` 是新机。上文已销毁的腾讯 `cn-ip5`（`192.144.148.117`）是另一台，名字重复使用了。

装机入口是 [`deploy/tencent/install-cn-socks-proxy.sh`](../tencent/install-cn-socks-proxy.sh)。它负责安装 GOST、写入 `tokenkey-proxy` 和 `tokenkey-proxy-firewall` 两个 unit、配置 BBR、收紧 sshd，最后装主机告警并跑 `--verify` 验收。1080 的放行来源只在这个脚本的 `ALLOWED_SOURCES`。主机 iptables 只放行这些来源的 1080，并丢弃入站 22 和 8080。云防火墙要配成同一份 1080 来源名单，不要对 `0.0.0.0/0` 开放 22、8080 或 1080；系统重装不会清掉已有云防火墙，新建实例不能用控制台默认的公网 22。每台 SOCKS 密码独立，只存在主机和 cn-prod `proxies` 表里。远程执行走阿里云「云助手」SWAS `RunCommand`，不走 TAT 或 SSM。

#### 代理主机告警（强制）

每台 SOCKS 出口机（`usw-eval*` 与 `cn-ip*`）**新机/换机/重装后必须**安装 `tokenkey-disk-metrics.timer` + `tokenkey-proxy-watch.timer`，并以 `install-proxy-host-alerts.sh --verify` 验收。  
安装 owner：[`deploy/tencent/proxy-host-alerts.md`](../tencent/proxy-host-alerts.md)。凭据仅主机 `/opt/tokenkey/host-alerts.env`，不进 git。  
另有 prod 应用侧 `proxy_unreachable_count` P0（网关拨测），不能替代本机 timer。

活舰队矩阵：[`deploy/tencent/candidate-usw-20261022.json`](../tencent/candidate-usw-20261022.json)
（`deployable=true`）。`resolve_prod_ssm_target.py` 在有 `apps[]` 时**必须**显式
`--node entry|app-old|app-cand|data`（或 `PROD_SSM_NODE`）；不再静默默认旧应用机。
整站诊断用 `run-probe --all-apps`。

### 活物理备份仓库（2026-11-08 起 COS 唯一）

2026-11-08 用户决定停写 AWS repo1，COS 为唯一 repository（[设计](../../docs/approved/design-tencent-cos-backup-node1-qa.md#2026-11-08-决定停写-aws-repo1cos-为唯一-repository)）。

| 项 | 值 |
|---|---|
| 活仓库 | pgBackRest `repo2`：COS `tokenkey-prod-backup-usw-1320330536`（`cos.na-siliconvalley.myqcloud.com`）`/prod/postgres` |
| 配置渲染 | 数据机上的 `tokenkey-backup-identity.timer`（15min）→ `tencent_backup_identity.py refresh`，读数据机 root-only `/etc/tokenkey/cos-backup.env`，重写 `/etc/tokenkey/pgbackrest-cos/pgbackrest.conf`（`root:postgres 0640`，容器只读挂载）。由 `ops/stage0/install-data-machine-identity.sh` 安装。应用机上的 identity timer 停用；不读 AWS 会话、不调 STS |
| 备份/检查 | 数据机上的 `tokenkey-postgres-backup.timer`（偶数小时 :30 UTC）和 `tokenkey-postgres-archive-check.timer`（5min）。应用机上这两项已停用 |
| 已停写并清理 | AWS physical-backup 桶/栈已删：CFN `tokenkey-prod-physical-backups`=`DELETE_COMPLETE`（2026-11-11）；`tokenkey-prod-physical-backup-682751977094-us-east-1` 与 cn-prod 对应桶 `HeadBucket` 404（2026-11-17 核对）。活链只在 COS `repo2` |

### C4 装机 / 恢复证据（主机 + COS，不读 SecureString）

| 证据 | 日期 / 回执 | 说明 |
|---|---|---|
| prod COS 真实恢复（全量+增量） | 2026-11-08；见 [design-tencent-cos-backup §prod COS](../../docs/approved/design-tencent-cos-backup-node1-qa.md#prod-cos-真实恢复证据2026-11-08) | 隔离主机从 COS `repo2` 恢复；不经 Parameter Store |
| SecureString C5 删除 | 2026-11-18；`docs/ops/reports/securestring-c5-delete-20261118T084419Z.txt` | 活 env 备份参数已删；写脚本 tombstone |
| env 恢复入口 | `deploy/hetzner/restore-prod-env-secrets.sh` | 默认拒 SSM SecureString；`--from-file` 读主机/离机材料；`--allow-generate` 仅显式批准 |
| 新机 bootstrap | `deploy/stage0/stage0-ec2-bootstrap.sh` | 不再钉 SSM 参数名；活源为主机 `.env` |
| 物理恢复 GHA | `ops-physical-restore.yml` refuse-only（A13） | 隔离恢复本地跑 `run_physical_restore.py`；活库验收 `probe-physical-restore-acceptance.sh` + TAT |

装机或换机时：在目标机放好 root-only `.env`（或 `--from-file` 材料）后再起 compose；数据库从 COS
`repo2` 恢复，不回退 SecureString。日常备份身份见上表 `tokenkey-backup-identity.timer`。

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
- 宿主 watcher owner：`../tencent/candidate-usw-20261022.json` 的 `host_watch`（`hostname` / `domains` / `paused_jobs` / `local_qa_jobs` / `data_qa_jobs`）。
- Tokyo 已于 2026-10-23 完全退役（实例/栈/VPC/卷/快照已删，仅留 S3 历史链），`aws-fresh` 解析路径与 `fresh-migration-20261015.json` 已删除（[runbook](../../docs/ops/tokyo-retirement-runbook.md)）。
- watcher 不随仓库合并更新：主机按安装时的内容寻址副本运行（`/usr/local/lib/tokenkey/https-watch/<hash>/config.json`）。改 `host_watch` / `entry` / `apps` 后须**经审批**重装：把 `install-host-https-watch.sh`、`host_https_watch.py`、`https_health.py`、`feishu_delivery.py`、`deploy/tencent/candidate-usw-20261022.json`、`deploy/domestic/prod-target.json` 按 basename 放进 `WATCH_BUNDLE_DIR`，先不带 `APPLY` 跑一遍（只核身份并打印配置），与现装 config 比对后再以 `APPLY=yes-host-https-watch` 安装。应用机：`WATCH_ROLE=prod WATCH_NODE=app-old|app-cand`（私网身份 + 本机 `:18080` + `apps[].jobs`；primary=`app-old` 另保留对 cn-prod 的 peer 观测）；**入口机** `WATCH_ROLE=prod-entry`（私网身份 + 五域名证书 + 两台 `:8080`，凭据 `/var/lib/tokenkey/host-alerts.env`）；数据机 `WATCH_ROLE=prod-data`；cn-prod 见 domestic README。2026-11-16 入口机已装 `prod-entry`；2026-11-17 QA 协调器交接后已重装旧应用机 / 数据机 watcher；同日收尾补装 `app-cand` 对称 watcher（jobs 含 `tokenkey-qa-retention`；data 另含 `tokenkey-qa-coordinator`）。
- 换机流程：[host replacement](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 现网不变量

| 项 | 值 |
|----|-----|
| 活入口 | 腾讯美西入口机 `43.162.119.210`（`lhins-3am1i23j` / `mi-026982f3622163a1b`）；五域名 A 指向该 IP。旧应用机公网 80/443 已关 |
| prod `API_DOMAIN` | 单正式名（无 `-hz` 双 SAN） |
| 探针 / sync Caddy | `resolve_prod_ssm_target.py --node app-old|app-cand|entry`（无默认）；入口机同步也可显式 `INSTANCE_ID` + `PROD_CADDY_ROLE=entry`；冻结/退役身份拒绝执行 |
| live 库 | **禁止**用旧机全量 dump 覆盖；补数只用 `request_id` 定点插入 |
| Secrets | `/tokenkey/hetzner/…`；不进 git |

正式 prod 的 Caddy 源是 `deploy/stage0/Caddyfile`，同一模板按角色渲染两套策略：
应用机 `PROD_CADDY_ROLE=app` 保持单容器 `tokenkey:8080`，只由 `/health` 主动检查决定摘除与恢复；
入口机 `PROD_CADDY_ROLE=entry` 从活矩阵生成两台应用机的私网 WRR 池（当前 `10.4.0.16`×2 +
`10.4.0.9`×1），3s 健康检查、连续 2 次失败摘除。两种策略都禁止被动冷却。修改模板时同时
重生成 Hetzner bootstrap，`scripts/checks/caddyfile-syntax.sh` 校验 Caddy 解析后的健康策略；
运行中主机通过 `ops/stage0/sync_caddyfile_via_ssm.sh` 热更新，单纯发布应用镜像不会刷新 Caddyfile。

旧主机均不再提供服务或回滚写库；历史整库保留在 S3 独立归档前缀。只有上表 prod 接收网关流量。

## 文件

生产目标解析由 `ops/stage0/resolve_prod_ssm_target.py` 持有；共享部署文件位于 `deploy/stage0/`。
换机编排：[host replacement](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。

## 日常发版与恢复

prod 的日常发布入口是 `.github/workflows/prod-rolling-deploy.yml`，确认词为
`rolling-prod-<tag>`。形状与 cn-prod 对齐：`prod` Environment 只做审批门，
执行 job 在 `tencent-ops` 上用 TAT（`--via tat`，区域 `na-siliconvalley`）驱动
`deploy/domestic/rolling.py`，按矩阵逐台 drain、换镜像、健康检查和回流。
入口机持续承担公网流量。warm（`warm-image-stage0.yml`）同样在 `tencent-ops`
上固定 `--channel tat`，不再依赖 AWS OIDC 作为默认路径。

不要把旧 Stage0 蓝绿脚本作为日常 prod 发布入口，也不要用
`resolve_prod_ssm_target.py` 默认的旧应用机身份绕过 rolling 矩阵。

```bash
# Canonical dispatch (normal path — TAT):
bash scripts/stage0/dispatch-prod-deploy.sh \
  --operation warm --tag X.Y.Z --revision FULL_SHA
bash scripts/stage0/dispatch-prod-deploy.sh \
  --operation deploy --tag X.Y.Z --revision FULL_SHA
# deploy derives confirm=rolling-prod-X.Y.Z and runs prod-rolling-deploy.yml
```

**SSM 应急路径**（仅 TAT/CAM 不可用且已单独批准时）：本地持有 AWS OIDC/Hybrid
凭据后，对同一 tag/revision 显式走 `--via ssm` / `--channel ssm`（例如
`python3 deploy/domestic/rolling.py deploy --manifest deploy/tencent/candidate-usw-20261022.json --via ssm ...`，
或 `warm_fanout.py warm-all ... --channel ssm`）。不要把 SSM 写回 workflow 默认，
也不要在 `tencent-ops` job 上半吊着 AWS OIDC。

日常发布入口是上面的 `dispatch-prod-deploy.sh --operation warm|deploy`
（cn-prod 对应 `cn-deploy`，同样要求完整 40 位 revision）。
`legacy-deploy|replay|smoke-only` 与节点内蓝绿实现才是兼容/恢复路径，
只用于 rolling 不可用、需要恢复旧布局或经过单独批准的故障处理；
不能借此绕过入口机、多应用机和单容器布局门禁。

发版前须确认 `tencent-ops` 的 `TENCENTCLOUD_*` 对美西 `na-siliconvalley` TAT/CAM
可用；权限未核通时 CI 绿也不可当作已能发版。

## 开放运维项（非过程史）

1. **账号**：prod 原生账号承接请求；错误、不可调度状态由运营处理，不自动恢复中继或旧副本。
2. **QA**（2026-11-17 已交接数据机协调器）：capture 沿现有配置；QA S3 已退役。应用机跑独立 `tokenkey-qa-retention`（满 24h 可丢）；
   数据机跑 `tokenkey-qa-coordinator` + retention。应用机 v1 `provision/cleanup/archive` 已停用并拆除 unit（worker not-found 可接受；node1 不可达时协调器只记 pending/告警，不阻塞 retention）。巡检见 [QA 运维入口](../../ops/qa/README.md) /
   [COORDINATOR.md](../../ops/qa/COORDINATOR.md)。
3. **监控**：腾讯机无 EBS/DLM；Tokyo 已退役，快照已全部删除（见 [runbook](../../docs/ops/tokyo-retirement-runbook.md)）。宿主 HTTPS/job watcher 读取暂停 owner，
   要求所列 timer 停用且 service 不在运行；意外恢复会告警。其他任务（含 `local_qa_jobs` / `data_qa_jobs`）按成功时效监控。
   主机脚本不随网关镜像自动更新；安装/刷新独立 watcher 后才生效，仓库合并不代表已部署。
4. **资源**：临时 node1 reader 与克隆盘已完成最小核查并回收，原快照和导出保留；日本 Lightsail 代理已于 2026-10-23 删除；
   新加坡其它项目资源不纳入 TokenKey 批量清理。详见 [资源收尾](../../docs/ops/aws-retirement-20261017.md)。
   AWS 冷归档桶 `tokenkey-prod-archive-682751977094` lifecycle 30 天自删已启用；东京 physical-backup 与 `cfn-deploy` 镜像桶已不存在（`HeadBucket` 404）。
5. **告警**：腾讯机上备份/QA retention/coordinator timer 已装；`tokenkey-https-watch.timer` 已接到飞书。Stage0 布局已按例外路径收敛；日常发版走上方 `prod-rolling-deploy.yml`。

## AWS 残留与环境变量

计算节点迁移完成不等于整个系统零 AWS。以下为保留依赖与清理边界；不要按文件名或
`AWS_*` 前缀批量删除。历史审批文档记录当时的架构，现网执行以本文件和目标矩阵为准。

### 2026-11-17 清点（可清 vs 必须留）

| 类别 | 状态 | 动作 |
|---|---|---|
| SSM Hybrid / OIDC / Parameter Store | **禁新增**；OIDC→SSM workflow 已抽壳/拒绝（`oidc_ssm_workflow_paths=[]`）；SecureString 已 C5 | 日常 warm+rolling+`run-probe`/`ssm_execution` 默认 TAT。allowlist 仅剩 break-glass SSM 字面量与 AWS 隔离恢复（`run_physical_restore`）。Hybrid/OIDC **角色注销**另批。门牙见 `ops/tencent-ops/`；清单 [design-tencent-ops-closeout](../../docs/approved/design-tencent-ops-closeout.md) |
| Oregon 保留计算 | **必须留** | 2026-11-17：prod `or-1..4` 合计 124 live accounts；cn-prod 无 Oregon 行 |
| 旧 physical-backup CFN/S3 | **已清** | 2026-11 核对 `DELETE_COMPLETE` / `HeadBucket` 404 |
| Tokyo EC2 / `aws-fresh` | **已清** | resolver 失败关闭 |
| `deploy/stage0/` 共享 owner | **已迁出 `deploy/aws/`** | 共享 compose/Caddy/QA/fingerprint；路径不再暗示 AWS 部署面 |
| 供应商 AWS 协议（Kiro/Bedrock 等） | **必须留** | 模型能力，不是基础设施残留 |
| `deploy_via_ssm_bluegreen.sh` | **已退役入口** | live `layout.kind=single-container` 默认 exit 2；仅 `STAGE0_ALLOW_LEGACY_BLUEGREEN=1` 应急。正文与契约测试刻意保留，**不再单列开放债** |

| 类别 | 当前用途 / owner | 清理条件 |
|---|---|---|
| SSM Hybrid / Parameter Store / STS OIDC | 存量旁路仍服务诊断/部分 ops workflow/GHCR 身份与应急 `--via ssm`；`resolve_prod_ssm_target.py` | 默认消费者清零后再批拆 Hybrid/OIDC；门牙见 `ops/tencent-ops/` |
| QA raw archive | QA S3 已于 2026-11-08 退役；仓库模板与恢复 CLI 已删除 | CFN `tokenkey-prod-qa-raw-archive`=`DELETE_COMPLETE`（2026-11-09）；桶 `HeadBucket` 404；主机 maintenance/boundary 脚本与 unit 已不存在（2026-11-17 核对）。见 [QA 运维入口](../../ops/qa/README.md#生产存储与恢复分界) |
| 数据库备份 | prod 主路径为 pgBackRest 全量/增量/WAL；旧 pgdump 按用户授权退役，旧数据保留至少 30 天 | [物理备份验收](../../ops/stage0/physical-backup.md)与[旧任务退役](../../ops/stage0/pgdump-retirement.md)。cn-prod 逻辑 dump 已停，见 [domestic README](../domestic/README.md) |
| env-secrets 与备份解密材料 | **主机 root-only `.env` 为活源**；SecureString 已 C5 删除（2026-11-18） | `restore-prod-env-secrets.sh` 默认拒 SSM；`--from-file` / `--allow-generate`；删参 owner `ops/stage0/delete-legacy-securestring-params.sh`；写脚本 tombstone；C4 证据见下节 |
| 历史 / 休眠 S3 桶 | 见下方「2026-11-17 桶盘点」；Tokyo `cfn-deploy` 镜像中转已改 COS；AWS `archive` 已开 30 天过期 | 休眠桶与 Tokyo 镜像桶已清；`archive` 桶待对象过期后自然空桶再删 |
| 共享运行文件目录 | `deploy/stage0/` 内 compose、Caddy、QA lifecycle、fingerprint baseline；HZ renderer 与 backend embed 直接消费 | 共享 owner；目录名不再含 `aws/`；改路径须同步引用、生成产物和 sentinel |
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
| `QA_ARCHIVE_*` | QA S3 归档 2026-11-08 退役；rolling/蓝绿发布不再注入或校验这些键，compose 也不再传入容器（网关缺省关闭归档）。现网副本归档走数据机 `tokenkey-qa-coordinator`（node1 jump）；应用机不再跑 v1 `tokenkey-qa-archive` |

媒体默认 AWS 坐标清理入口：`ops/stage0/retire_dormant_media_storage.py`（plan 默认；
`APPLY=yes-retire-dormant-media` 才写主机 `.env`，不重启容器）。2026-11-17 现网已是
`DRIVER=local`，无需 APPLY（见下方 QA 收尾节）。

### 2026-11-17 桶盘点与销毁（用户批准）

账户 `682751977094`。此前已 404：**physical-backup**、**qa-raw(-archive)**、**telemetry**。

**已销毁**（`HeadBucket` 404；SSM 恢复材料未动）：

| 桶 | 说明 |
|---|---|
| `tokenkey-cn-prod-pgdump-682751977094` | 空桶 |
| `tokenkey-prod-media-682751977094` | 休眠媒体（2 个 `public/` 对象 + 版本残留已清） |
| `tokenkey-prod-cfn-deploy-682751977094-us-east-1` | 美东 CFN/hotfix 残留 |
| `tokenkey-prod-pgdump-682751977094` | 7 个旧逻辑 dump（最新 2026-10-20）；保留 `/tokenkey/hetzner/*/stage0/pgdump-storage-recovery` 与 `/tokenkey/prod/stage0/pgdump.b64` |
| `tokenkey-prod-cfn-deploy-682751977094-ap-northeast-1` | 东京镜像中转；cn-prod 已改北京 COS（[design-cn-image-cos](../../docs/approved/design-cn-image-cos.md)）。2026-11-17 核对 `HeadBucket` 404 |

**仍保留（自删进行中）**：

| 桶 | 区域 | 角色 |
|---|---|---|
| `tokenkey-prod-archive-682751977094` | us-east-1 | lifecycle `expire-all-30d` **已启用**（2026-11-17 核对）；**禁止新写**。最新对象约 2026-11-15（`host-config/prod-entry-…`），按 LastModified+30d 过期。活目的地：腾讯 COS `tokenkey-prod-ops-archive-1320330536`（[design-cos-object-archive](../../docs/approved/design-cos-object-archive.md)）。空桶后再删桶本身 |

后续债（门牙已合入，旁路/密钥逐 PR）：运维旁路与密钥材料收敛到腾讯 TAT/COS——见
[`design-tencent-ops-control-plane.md`](../../docs/approved/design-tencent-ops-control-plane.md)
与 [`ops/tencent-ops/`](../../ops/tencent-ops/)。共享 Stage0 路径已迁至 `deploy/stage0/`。

## QA 与备份排队、旧 AWS 定时任务收尾

共享脚本使用 `/var/lib/tokenkey/qa-lifecycle/host.lock`：pg_dump 持共享锁，本地 QA
provision/cleanup 持独占锁；备份完成本地文件后立即解锁，上传不阻塞 QA。本地 QA 最多等待 600 秒，
备份最多等待 2400 秒；超时任务失败，不跳过数据库短锁超时。

QA S3 退役（2026-11-08）后，旧 maintenance/boundary 宿主脚本已从仓库删除。
**2026-11-17 主机核对（prod `app-old`/`app-cand`，cn-prod `app-a`/`app-b`）**：
`tokenkey-qa-maintenance`/`boundary` 的 unit、mask 链接与 `/usr/local/bin` 脚本均已不存在（A 收尾完成）。
协调器交接后现行应用机单元是 `tokenkey-qa-retention`（+ 数据机 `tokenkey-qa-coordinator`）；
v1 `provision`/`cleanup`/`archive` 已停用拆除，不是「现行保留」。

媒体默认 AWS 坐标（B）：2026-11-17 plan 在 prod 两台应用机均见
`MEDIA_STORAGE_DRIVER=local` 且 `BUCKET`/`REGION` 为空，**不是**休眠 S3 三元组；
`retire_dormant_media_storage.py` 正确返回 `custom_or_partial_media_configuration`，**无需 APPLY**。
（同日修复该脚本在 run-probe `/tmp` 交付下 `Path.parents[1]` 误抛错，否则 plan 无法到达配置裁决。）

普通 rolling 发版与经批准的蓝绿恢复都不再注入 telemetry 的 AWS 桶/region 默认值，telemetry 默认关闭。
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
