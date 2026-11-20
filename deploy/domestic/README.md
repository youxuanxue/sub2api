# 腾讯北京 cn-prod（domestic）

当前站点是腾讯北京 `tokenkey-cn-prod`，已采用入口机 + 多应用机 + 数据机的生产布局。
与美西 **prod** 组成**多地域等效容灾**（站点库与控制面独立）；本站入口 WRR 多应用机提供**站点内应用高可用**。对外表述见 [`deploy/hetzner/README.md`](../hetzner/README.md#高可用与等效容灾对外表述)。
`lhins-6qmb3fd6`（`154.8.214.179`）只是历史 app-a 身份，当前节点、私网和数据面
以 [`prod-target.json`](prod-target.json) 的 `entry`、`apps[]` 和数据机配置为准。
原始实例与 `cn-ip1..4` 到期 **2026-11-22**，续费标志
`NOTIFY_AND_AUTO_RENEW`（2026-10-23 改）；到期自动扣费，账户余额不足仍会停机。
UCloud 转发 `106.75.29.139` 已于 2026-10-27 退役。国内长期入口是
`https://api.galaxywaveai.com`，站点定义在 [`entry/Caddyfile.tmpl`](entry/Caddyfile.tmpl)。
`api-cn.tokenkey.dev` 仍暂时指向美西，日后退役；退役前不要把它解析到北京，也不要写成
`public_origin` 或 `SERVER_FRONTEND_URL`。现网节点只看 [`prod-target.json`](prod-target.json)。

逻辑 dump 调度已改为 `ops-cn-backup-watch.yml`，本变更不恢复它。2026-11-09 停写窗口之后，`tokenkey-cn-postgres-backup` 和 `tokenkey-cn-postgres-archive-check` 在数据机 `VM-4-5-ubuntu`（`10.2.4.5`）上跑，应用机上的对应 timer 已停用。数据机上的 `tokenkey-cn-backup-identity.timer`（15min）运行 `cn_backup_identity.py`，读数据机 root-only `/etc/tokenkey/cos-backup.env`，重写 `/etc/tokenkey/pgbackrest-cos/pgbackrest.conf`；由 `ops/stage0/install-data-machine-identity.sh` 安装。轮换 COS 密钥只改数据机上的这一个文件。2026-11-08 起唯一仓库是北京 COS `repo2`（`tokenkey-cnprod-backup-bj-1320330536` `/cn-prod/postgres`），东京 AWS `repo1` 已停写且桶/栈已清理（2026-11 核对 `HeadBucket` 404）；详见[设计](../../docs/approved/design-tencent-cos-backup-node1-qa.md#2026-11-08-决定停写-aws-repo1cos-为唯一-repository)。备份验收到此为止，不再另开恢复机，也不从东京仓库再导一份库。

这是独立于美西 prod 的国内站点；只读观测可通过独立注册的 AWS SSM Hybrid agent 下发（日常发版走 TAT）。
站点身份与滚动契约见 [`prod-target.json`](prod-target.json) 与
[cn-prod 多应用机滚动发版](../../docs/approved/design-cn-prod-rolling-deploy.md)。

## 历史上线（已归档）

UCloud 首发（2026-10-14）与迁机收尾回执已删除。现网身份与续费以文首与
[`prod-target.json`](prod-target.json) 为准；换机走 `tokenkey-host-replacement`，
勿按已删历史表重建账号或资源。

## 入口机与多应用机（2026-11-15 起）

| 角色 | 实例 | 私网 | 公网 | 说明 |
|---|---|---|---|---|
| 入口机 | `lhins-j89o28j0` | `10.2.4.7` | `154.8.185.224` | Caddy 终止 `api.galaxywaveai.com` 的 TLS，按 **1:1** 分发到两台应用机；公网只开 80/443 |
| app-a | `lhins-6qmb3fd6` | `10.2.4.11` | `154.8.214.179` | 应用机下限 **4C8G**；现仍为 4C16G（`bundle_rs_mc_lg16_01`，Lighthouse 不支持原地降内存），单容器 `tokenkey`，私网 `10.2.4.11:8080`；云防火墙只放行入口 `10.2.4.7` |
| app-b | `lhins-0kxx7cco` | `10.2.4.13` | `82.156.89.64` | 4C8G（`bundle_rs_mc_lg8_01`），单容器 `tokenkey`，无公网入站；监听 `10.2.4.13:8080`，同样只放行 `10.2.4.7` |
| 数据机 | `lhins-odyx5eis` | `10.2.4.5` | （无公网业务入口） | **4C8G**（`bundle_starter_mc_lg8_01`，2026-11-18 短窗停机由 2C4G 升配）；PG `max_connections=400`、Redis；见 `dataplane-cutover-data.sh` 期望值 |
| 临时压测 client（已销毁） | `ins-7ltl2zv3` | — | — | 北京按量 CVM（升配后 `SA2.2XLARGE16`）；tag `expire_after=2026-11-19`；**2026-11-19 用户授权 `TerminateInstances` 销毁**（不进 `apps[]`，无业务流量） |

节点身份只看 [`prod-target.json`](prod-target.json) 的 `entry` 与 `apps[]`。`apps[]` 的顺序就是滚动顺序，金丝雀位在前（现为 app-b；应用机下限 4C8G）。设计见 [cn-prod 多应用机滚动发版](../../docs/approved/design-cn-prod-rolling-deploy.md)。应用池现网：`DATABASE_MAX_OPEN_CONNS=80` / idle=20、`REDIS_POOL_SIZE=96`、`GATEWAY_MAX_IN_FLIGHT_REQUESTS=480`（`compose.override.yml`）。
- 入口机健康检查每 3 秒访问各节点 `/health`，连续 2 次失败即摘除。drain 让节点 `/health` 返回 503，入口机先停派新请求，节点再等在途请求清零。
- 日常发版由 `rolling.py` 逐台 drain、换镜像、健康检查和回流，入口机持续把新流量分给另一台。**节点内蓝绿与 `tokenkey-entry-origin` socat 代码已删除**；若主机上仍有停用的 blue/green/socat 容器，只作历史残骸，rolling 会拒绝把它们当成在服布局。
- 入口机 Caddyfile 在 `/var/lib/tokenkey/caddy/etc/Caddyfile`，挂载的是目录而不是单个文件：单文件挂载在 `sed -i` 换 inode 后会继续读旧内容。入库模板为 [`entry/Caddyfile.tmpl`](entry/Caddyfile.tmpl)，`python3 entry_caddy.py --diff <线上文件>` 只读比对（忽略注释和空白），`--out PATH` 渲染。`to` 列表按私网 IP 排序，权重跟随各自节点。
- 应用机的节点覆盖由 `python3 node_compose.py --node app-b --out /opt/tokenkey/compose.node.yml` 渲染：`<private_ip>:8080` 与 `127.0.0.1:18080`、`memory_limit`、`TOKENKEY_NODE_NAME=cn-prod-<name>`、`GATEWAY_MAX_IN_FLIGHT_REQUESTS`（取自 `compose.override.yml`）和节点 `env`。`--diff EXISTING` 只读，有差异时退出码 1。
- 证书是 2026-11-15 从 app-a 复制的同一张（有效期至 2027-01-21），入口机用 TLS-ALPN-01 续期。**2026-11-20**：应用机 app-edge 已收敛——无 `tokenkey-caddy`、无 80/443；`/var/lib/tokenkey/caddy` 已清（备份在主机 `/var/backups/tokenkey-edge-debris/`）；`compose.override.yml` 将 `caddy` 放入 `legacy-edge` profile，防止 `compose up` 复活。

### 看门狗

| 主机 | 安装参数 | 监控内容 |
|---|---|---|
| 入口机 | `WATCH_ROLE=cn-prod-entry` | `https://<entry.domain>/health`；证书剩余不足 20 天；每个 `apps[]` 节点直接探测 `http://<private_ip>:8080/health`，503 持续超过 15 分钟告警，全部不健康立即告警 |
| app-a | `WATCH_ROLE=cn-prod WATCH_NODE=app-a` | 本机 `127.0.0.1:18080/health`、磁盘、`tokenkey-qa-retention`；并保留对 prod 与 `public_origin` 的观测 |
| app-b | `WATCH_ROLE=cn-prod WATCH_NODE=app-b` | 本机 `127.0.0.1:18080/health`、磁盘、`tokenkey-qa-retention` |
| 数据机 | `WATCH_ROLE=cn-prod-data` | 磁盘、`tokenkey-qa-coordinator`、`tokenkey-qa-retention`，以及本机 pgBackRest 证据文件触发的备份 jobs |

带 `WATCH_NODE` 或入口机角色时，installer 用 `hostname -I` 核对 `prod-target.json` 的私网 IP。不带 `WATCH_NODE` 的 `cn-prod` 配置保持不变。三台都读 `/opt/tokenkey/host-alerts.env`。

### 加减应用机清单

加一台（减一台反向执行，先从入口机摘除再停机）：

1. `prod-target.json` 的 `apps[]` 追加节点（`name`、`lighthouse_instance_id`、`private_ip`、`public_ip`、`weight`、`memory_limit`，可选 `env`）。
2. 数据机 `dataplane_source_acl.py --phase add-source`；云防火墙放行 5432/6379。
3. 8 台出口机 `proxy_source_add.py`，阿里云 1080 规则。
4. 新机 `/opt/tokenkey/.env` 的凭据与线上一致（`POSTGRES_PASSWORD` 见下文），用 `node_compose.py --out` 写 `compose.node.yml`，以 drain 状态起容器，跑 smoke 后再放行。
5. 入口机 `entry_caddy.py --diff` 确认只多出新节点，`--out` 写入后 `caddy reload`。
6. 装 QA timer（见「QA 归档」）与看门狗（`WATCH_NODE=<name>`），并重装入口机看门狗，让它探测新节点。

### QA 归档（数据机协调器）

cn-prod 已切到 `ops/qa/COORDINATOR.md` 路径，不再跑应用机 v1 `provision/cleanup/archive`：

| 主机 | bundle / 要点 |
|---|---|
| app-a | source `ce91ce7ee87925712507`（`source_id=cn-prod-app-a`）；v1 timers 已停；`tokenkey-qa-retention` 已启用 |
| app-b | source `229d76e166be3062d24d`（`source_id=cn-prod-app-b`）；`tokenkey-qa-retention` 已启用 |
| data | coordinator `e9795f9a278425bc383d`；`PGHOST=10.2.4.5`；`tokenkey-qa-coordinator` + `tokenkey-qa-retention` 已启用 |

矩阵：`apps[].jobs` 含 `tokenkey-qa-retention`；`data_qa_jobs` 含 coordinator + retention。data→apps 私网 `:22` 已放行（仅 `10.2.4.5/32`）。

**node1**：主 jump `180.76.181.161` 与 WG peers 现网不可达时，协调器 archive 失败并记 pending；retention 仍按「满 24h 可丢」独立清理热数据。恢复 node1 后只需再跑受控 `--apply` 核 seal，不必重装 units。

## 配置 owner（现网）

换机/新机按 `tokenkey-host-replacement`；勿把 UCloud 首发流程当升级脚本重跑。

- `compose.override.yml` 打开 QA capture（不配置 QA S3），并设置应用池与在途闸门
  （**现网 2026-11-18**：`GOMEMLIMIT=2GiB`、DB open/idle **80/20**、Redis pool/idle `96/8`、
  `GATEWAY_MAX_IN_FLIGHT_REQUESTS=480`）。应用容器硬内存上限默认 3g（`TOKENKEY_APP_MEMORY_LIMIT`
  可覆盖）。美西 prod 用另一份 compose，勿互相套用。Admin 代理出站探测 preset=
  `domestic`（见 override / `probe-cn-prod-pool-env.sh`）。
- 镜像默认由 CI 上传北京 COS 经 TAT 预下载；契约见
  [design-cn-image-cos.md](../../docs/approved/design-cn-image-cos.md)。
  `pull_policy: never`。主机 `/opt/tokenkey`：`compose.yml` + `compose.override.yml` +
  `compose.local.yml`；`.env` 0600。
- **数据库密码只有一个来源**：`.env` 的 `POSTGRES_PASSWORD` 必须等于数据机实际密码；
  rolling 换镜像前核对 compose 渲染值与线上容器（`rolling.py` credential parity）。

## 远程运维入口

远程运维身份只读 [`prod-target.json`](prod-target.json) 的 `ssm_instance_id` 和
`ssm_region`。agent 由主机主动通过 HTTPS 连接 AWS SSM；billing/usage/error probe 通过
`run-probe.sh --target cn-prod` 默认按 `apps[]` 扇出（每台须有 `ssm_instance_id`）；
单台用 `--node app-a|app-b`。2026-11-17：app-b 已登记 Hybrid
`mi-00a738270d1d7bd48`（snap `amazon-ssm-agent` 3.3.4793.0），裸扇出可用。
不依赖运维机公网 IP，也不需要开放公网 SSH 22。

SSH 只用于一次性 agent bootstrap 或 UHost 控制台不可用时的恢复。需要临时 SSH 时只放行
当前出口 `/32`，完成操作立即移除；不得把动态运维出口固化为长期防火墙规则。日常 probe
不得回退到 SSH。新机 bootstrap 会运行
[`deploy/tencent/lock-down-sshd.sh`](../tencent/lock-down-sshd.sh)：关掉密码登录、root
登录和 TCP 转发，并锁定 root 密码。云防火墙不开放公网 22。

## 发布路径：rolling

cn-prod 的正常发布入口是 `deploy/domestic/rolling.py`，按 `prod-target.json` 的
`apps[]` 顺序逐台执行 drain、换镜像、私网健康检查和回流。入口机继续把请求分给
另一台应用机，因此发布过程不需要节点内蓝绿。

Release 后的 `distribute-cn-image.yml` 分两段执行：`publish` 在 AWS OIDC 身份下上传
并校验 COS 工件、渲染每节点预下载脚本；`publish`/`warm` 均在 `tencent-ops` 下持腾讯凭据，按
`apps[]` 顺序通过 TAT 预下载到每个节点。`warm_fanout` 与 workflow 默认/钉死 `--channel tat`
（手跑也不要传 `auto`：tencent-ops 无 AWS 凭据，SSM 会失败）。
任一节点失败都停止后续节点，且不触碰正在服务的容器。

分发和每个节点预下载回执齐全后，用同一工作流入口切流：

```bash
bash scripts/stage0/dispatch-prod-deploy.sh \
  --operation cn-deploy --tag X.Y.Z --revision FULL_SHA
```

`cn-prod-rolling-deploy.yml` 的审批 job 使用 `prod` Environment，执行 job 使用
`tencent-ops`；输入 tag 是裸 `X.Y.Z`，revision 必须是已审完整 40 位 SHA。

`deploy_via_ssm.sh` 只保留 `pull`（镜像落地）与 `put-file`（应急传文件）。蓝绿 `prepare`/`promote`/`candidate-smoke`/`sync-controls` 已删除；误调用会以 exit 2 拒绝并指向 `rolling.py`。

## 镜像 pull（rolling 前置）

日常发版仍由 `cn-prod-rolling-deploy.yml` 驱动；需要手动预热时：

```bash
bash deploy/domestic/deploy_via_ssm.sh pull --tag 1.8.380 --revision REVIEWED_FULL_SHA
# 显式 GHCR 备用
bash deploy/domestic/deploy_via_ssm.sh pull --transport ghcr --tag 1.8.380 --digest sha256:AMD64_OR_INDEX_DIGEST
```

## 应急 put-file

送文件到主机**只用** `deploy_via_ssm.sh put-file`，不要手写 `base64 -d > ...` 一行流。
2026-10-25 发版时一条手写传输里的 `> bash` 笔误把重定向打到远端 cwd（`/usr/bin`），
`/usr/bin/bash` 被 truncate 成 0 字节：服务没断，但主机上所有 `#!/bin/bash` 单元全挂 ——
`tokenkey-disk-metrics`（磁盘/内存告警）瞎了约 9 小时，`sysstat-summary` 失败，盯盘 probe
取不到数。`put-file` 用 python `repr` 引用的 gzip+base64 分块传输（SSM 整个 document 上限 97KB，
裸 base64 会把 77KB 脚本撑过线），落盘后按原始字节校验 sha256；`--run` 先 `bash -n` 再执行。
`--remote` 只允许写 `/private/*`、`/opt/tokenkey/*`、`/var/lib/tokenkey/*`，裸文件名、`..`
穿越和其它路径一律拒绝 —— 用 allowlist 而非系统路径黑名单，是因为黑名单仍会放过
`/usr/local/bin`（本次事故里被打挂的告警脚本就在那儿）。目标已存在且内容不同时拒绝
覆盖，要覆盖得显式加 `--force`；内容相同的重复 put 直接放过（幂等）。

```bash

bash deploy/domestic/deploy_via_ssm.sh put-file \

  --state /opt/tokenkey/releases/1.8.306-SHORTSHA \
  --receipt PREPARED_RECEIPT_SHA256 \
```

## 初始化与隔离

上游来源为本机受保护导出，首批四个账号后按用户要求追加十个火山计划账号；使用 `ops/accounts/import-accounts.sh`
的 validate → dry-run → apply。目标组 ID 为 2，不能沿用来源 ID 690。其他配置保留原值。
重复执行前先读目标账号/用户；不能重复创建、再次发放初始 $10 或覆盖已消费余额。

当前发布镜像的创建用户链没有持久化 `restrict_public_groups`，因此不能把该字段写入请求
视为已启用。本站通过将默认组设为 inactive/exclusive、只保留目标专属组为可用组完成实际隔离；
已验证客户可用组仅有 `china_deployed`，绑定其他组返回 403。未来开放其他组时须重新评估该边界。

QA capture 与美西 prod 一样打开，只写本地记录和热目录，QA 不上传 S3。cn-prod 直接复用与 prod 相同的共享 Caddy 模板，不增加国内专属路径拦截。
媒体、视频、音频、实时会话及 provider 兼容别名继续交给应用的鉴权、模型目录和供应账号
能力判断，未来新增模型不需要改 Caddyfile。前端不按国内域名隐藏工具菜单或路由，Studio、
Batch Image 等入口与其他站点一致。QA bundle、用户导出和 worker 已退役；旧 QA 路径是否
存在由应用自身的退役状态处理。

本次复审只恢复共享行为：生成器输出共享 Caddy 配置，`test_runtime.py` 验证产品、媒体、
provider、实时和旧 QA 路径都交给应用处理，并已纳入现有 Caddy preflight。合并后按正常应用
镜像发布流程更新前端和配置；不要把媒体或 QA 能力判断写死到 Caddy，也不要重新覆盖生产
`.env` 或重跑初始化。

## 本机凭据

操作员本机（不进仓库）：`~/codes/keys/tokenkey-cn-admin.json`、
`~/codes/keys/tokenkey-cn-rt.json`（0600）；完整初始化状态
`~/.local/share/tokenkey/domestic-onboard`（0700）。禁止粘贴完整导出或 dry-run 日志。

## 监控与通知

应用监控和实时指标开启，复用现有 prod 飞书群。`api-cn.tokenkey.dev` 在共享节点身份
owner 中显式识别为 `cn-prod`，沿用 prod 用户失败告警规则。
通知策略保持现有 prod 的分级规则，并非所有启用规则都会发送飞书：P0 与指定 P1 用户失败
走飞书，普通 CPU/P1 内存等规则仍保留在运维面。邮件 SMTP 未配置，不宣称邮件可达。

`install-host-alerts.sh SHARED_SCRIPT` 安装共享
`deploy/hetzner/tokenkey-disk-metrics-prod.sh`，每五分钟独立于 Docker/Postgres 检查
系统盘和内存；独立数据盘存在时一并检查。磁盘达到 85% 告警、低于 80% 恢复；内存 90% 连续两次或 96%
单次告警，低于 80% 恢复。凭据放在系统盘 `/opt/tokenkey/host-alerts.env`（0600），
通过 `TOKENKEY_HOST_ALERT_ENV_FILE` 指定，数据盘失挂时仍能通知；节点标识为 `cn-prod`。
独立数据盘仍必须是 mountpoint。目录就在根文件系统上时（北京 `/dev/vda2`），安装器接受该布局，单元不设置 `TOKENKEY_DATA_VOLUME_PATH`，只检查根文件系统和内存，避免把根盘误报成数据卷失挂。安装不重启网关、数据库或 Caddy。缺少 0600 的通知配置时拒绝安装。

公网可用性由共享 `ops/observability/host_https_watch.py` 承担：cn-prod 提供境内视角，prod
提供独立站外视角。两处分别检查 cn-prod 公网入口（`public_origin`）、指向美西 prod 的国内域名、美西 prod
矩阵中的客户域名。当前国内主机身份与入口见
[`prod-target.json`](prod-target.json)，美西目标从 `deploy/tencent/candidate-usw-20261022.json` 生成，不能用域名的绿色结果
替代 cn-prod 公网入口。探针每两分钟执行，连续两次失败发 P0 飞书，持续失败每三十分钟提醒，
首次恢复发送恢复消息；健康要求可信 TLS、HTTP 200 且 JSON `status=ok`，拦截页不算健康。
告警标明探测节点和目标 URL；发送失败不推进去重状态，下次重试。

这覆盖公网入口、DNS、TLS、Caddy 和应用就绪状态，不代替模型调用成功率/延迟告警。
境内观察点与 cn-prod 共机，整机失联由美西 prod 观察点兜底，不能代表所有国内运营商。
云厂商告警、备份/恢复不在此安装器范围内。

安装复用 `ops/observability/install-host-https-watch.sh`，`WATCH_ROLE=cn-prod`，将该脚本及
`host_https_watch.py`、`https_health.py`、`feishu_delivery.py`、
本目录的 `prod-target.json`、
`deploy/tencent/candidate-usw-20261022.json`（现网 prod 矩阵，`host_watch` 为 owner）
按 basename 放入主机私有目录。`WATCH_BUNDLE_DIR` 指向该目录；不传 `APPLY` 时只核对身份
并输出配置，`APPLY=yes-host-https-watch` 才安装。cn-prod 读取现有
`/opt/tokenkey/host-alerts.env`；美西 prod 沿用 `/var/lib/tokenkey/.env`，不另复制密钥。安装前备份 unit，使用
内容寻址的代码目录；可恢复备份 unit 后 daemon-reload 回退。不会重启应用、数据库或 Caddy。

（历史，UCloud 期）节点身份修复以 `1.8.292-cn-prod` 本地 amd64 补丁镜像部署，仅替换当时的应用容器。
镜像基于原已校验发布镜像，附源码差异 SHA-256 与二进制 SHA-256；构建记录保存在受保护
初始化状态中。部署前 preflight 和节点身份/通知回归测试通过，使用共享 Stage0
`drain_container` 确认 `draining=true, in_flight=0` 后替换，健康检查通过；原镜像保留回退。

2026-10-14 飞书验收：通过临时、明确标注“非真实故障”的 P0 CPU 规则触发应用实际通知，
事件记录 `feishu_firing_sent=true`、`feishu_firing_status=sent`。测试规则已删除，测试事件已
标记恢复；未修改正式规则阈值。主机定时器 enabled/active、首次执行成功。公网健康检查和
两个复制客户 key 的鉴权在更新后仍通过。邮件与整机失联外部探测不在此次送达证明之内。

备案、备份和恢复按用户决定后续处理；此回执仅记录当前实测可达性。
