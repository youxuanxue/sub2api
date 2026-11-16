# 腾讯北京 cn-prod（domestic）

当前站点是腾讯北京 `tokenkey-cn-prod`，已采用入口机 + 多应用机 + 数据机的生产布局。
`lhins-6qmb3fd6`（`154.8.214.179`）只是历史 app-a 身份，当前节点、私网和数据面
以 [`prod-target.json`](prod-target.json) 的 `entry`、`apps[]` 和数据机配置为准。
原始实例与 `cn-ip1..4` 到期 **2026-11-22**，续费标志
`NOTIFY_AND_AUTO_RENEW`（2026-10-23 改）；到期自动扣费，账户余额不足仍会停机。
UCloud 转发 `106.75.29.139` 已于 2026-10-27 退役。国内长期入口是
`https://api.galaxywaveai.com`，站点定义在 [`entry/Caddyfile.tmpl`](entry/Caddyfile.tmpl)。
`api-cn.tokenkey.dev` 仍暂时指向美西，日后退役；退役前不要把它解析到北京，也不要写成
`public_origin` 或 `SERVER_FRONTEND_URL`。下面的表是 2026-10-14 首次上线记录。

逻辑 dump 调度已改为 `ops-cn-backup-watch.yml`，本变更不恢复它。2026-11-09 停写窗口之后，`tokenkey-cn-postgres-backup` 和 `tokenkey-cn-postgres-archive-check` 在数据机 `VM-4-5-ubuntu`（`10.2.4.5`）上跑，应用机上的对应 timer 已停用。数据机上的 `tokenkey-cn-backup-identity.timer`（15min）运行 `cn_backup_identity.py`，读数据机 root-only `/etc/tokenkey/cos-backup.env`，重写 `/etc/tokenkey/pgbackrest-cos/pgbackrest.conf`；由 `ops/stage0/install-data-machine-identity.sh` 安装。轮换 COS 密钥只改数据机上的这一个文件。2026-11-08 起唯一仓库是北京 COS `repo2`（`tokenkey-cnprod-backup-bj-1320330536` `/cn-prod/postgres`），东京 AWS `repo1` 停写、对象保留待清理；详见[设计](../../docs/approved/design-tencent-cos-backup-node1-qa.md#2026-11-08-决定停写-aws-repo1cos-为唯一-repository)。备份验收到此为止，不再另开恢复机，也不从东京仓库再导一份库。

审批范围和费用见 [design-domestic-cluster.md](../../docs/approved/design-domestic-cluster.md)。
这是独立于美西 prod 的国内站点，不使用 Hetzner dispatcher 或美西数据库；只读观测通过独立注册的 AWS SSM Hybrid agent 下发。

## 首次部署目标（2026-10-14）

| 项目 | 值 |
|---|---|
| 公共入口 | https://api-cn.tokenkey.dev |
| 项目 / 地域 / 可用区 | `org-bpx31r` / `cn-bj2` / `cn-bj2-02` |
| 主机 | `uhost-1wgyn7gu2f6a`，2 CPU / 4 GiB |
| 系统盘 / 数据盘 | 40 GiB / 20 GiB SSD |
| 数据盘 | `bsm-1wgz3hm0c0jb`，挂载 `/var/lib/tokenkey` |
| 公网 | `eip-1wgywcyzw8bs`，`106.75.17.57`，5 Mbps BGP |
| 防火墙 | `firewall-pvx1loip`，80/443 公网，SSH 22 关闭；运维走 AWS SSM Hybrid / UHost 控制台 |
| 管理员 | `admin@cn.tokenkey.dev` |
| 客户 | `rt@tk.com`，初始 $10，仅 `china_deployed`，专属倍率 0.3 |

此表及初始化回执记录首次上线状态。用户随后已授权 10 Mbps 带宽、更多客户和供应账号；
部署前须读取现网，不按此历史表重建账号、充值、分组或资源。

## 迁机收尾：2026-10-23 腾讯北京（已修）

2026-10-22 迁机只搬了运行态（容器、数据、Caddyfile），没铺发布布局，发 1.8.318 时暴露并修完：

| 漂移 | 后果 | 处理 |
| --- | --- | --- |
| `/opt/tokenkey/compose{,.override,.local}.yml` 三件套全缺 | `bluegreen.py prepare` 读不到服务定义，**阻断发版** | 从仓库铺回（**不要**跑 `prepare-runtime.py`，它是首装脚本、会重新生成数据库密码与 JWT secret） |
| `prod-target.json` `public_origin` 还是 UCloud `106.75.29.139` | `promote` 切流前校验必然失败 | 改为腾讯北京 `154.8.214.179` |
| `.env` `TOKENKEY_IMAGE` pin 在临时 `cn-prod-monitor-*`，且该镜像已被 GHCR prune 删除 | 蓝绿容器按 image id 跑，现网无感；但任何从 `.env` 重建（恢复、`compose up`）会因 `pull_policy: never` 直接失败 | 重新 pin 到在服版本；`promote` 与 `rollback` 现在都会 repin 到当前路由到的颜色（`repin_env_image`，对齐美西的 `env_set TOKENKEY_IMAGE` / `restore_env_if_safe`） |
| `SECURITY_PROXY_PROBE_PRESET` 只写进 override，没列入 `*_ENV_KEYS` | 候选 env 继承自现役容器、只覆盖列出的键，所以该键**永远**传不到新颜色，Admin 代理探测一直走境外 `ip-api.com` | 新增 `CONFIG_ENV_KEYS`；`test_pool_env_probe.py` 现在会在 override 声明了未传播的键时失败 |

日志轮转和 compose 项目名随 1.8.318 蓝绿自动归位（override 的 20m×3、`cn-prod-bluegreen`）。
蓝绿 loopback 端口 blue=18081 / green=18082 是约定，不是漂移；`compose.local.yml` 的 18080 只用于无蓝绿的单容器。

数据面切流之后，`compose.override.yml` 把 `postgres` 和 `redis` 放进 `local-data` profile，并清掉 `tokenkey` 对它们的 `depends_on`。只加 profile 时 `docker compose config` 会因依赖未定义而失败，蓝绿 `prepare` 读的就是这份 config。清掉依赖后，默认项目只剩网关和 Caddy，不会启动旧库。加上 `--profile local-data` 会把旧主库拉起来，和数据机写同一套 COS 仓库。这份 override 由 `prepare-runtime.py` 随发版铺到 `/opt/tokenkey/compose.override.yml`。2026-11-10 主机上的副本已经是这一版，`compose.yml + compose.override.yml + compose.local.yml` 只解析出 `tokenkey` 和 `caddy`，地址都是 `10.2.4.5`。

## 入口机与多应用机（2026-11-15 起）

| 角色 | 实例 | 私网 | 公网 | 说明 |
|---|---|---|---|---|
| 入口机 | `lhins-j89o28j0` | `10.2.4.7` | `154.8.185.224` | Caddy 终止 `api.galaxywaveai.com` 的 TLS，按 3:1 分发到两台应用机；公网只开 80/443 |
| app-a | `lhins-6qmb3fd6` | `10.2.4.11` | `154.8.214.179` | 4C16G，单容器 `tokenkey`，私网 `10.2.4.11:8080`；云防火墙只放行入口 `10.2.4.7` |
| app-b | `lhins-0kxx7cco` | `10.2.4.13` | `82.156.89.64` | 2C4G，单容器 `tokenkey`，无公网入站；监听 `10.2.4.13:8080`，同样只放行 `10.2.4.7` |

节点身份只看 [`prod-target.json`](prod-target.json) 的 `entry` 与 `apps[]`。`apps[]` 的顺序就是滚动顺序，小机器在前当金丝雀。设计见 [cn-prod 多应用机滚动发版](../../docs/approved/design-cn-prod-rolling-deploy.md)。

- 入口机健康检查每 3 秒访问各节点 `/health`，连续 2 次失败即摘除。drain 让节点 `/health` 返回 503，入口机先停派新请求，节点再等在途请求清零。
- 日常发版由 `rolling.py` 逐台 drain、换镜像、健康检查和回流，入口机持续把新流量分给另一台。节点内蓝绿与 `tokenkey-entry-origin` socat 仅保留为旧布局迁移和故障恢复路径，不再作为正常发布路径。
- 入口机 Caddyfile 在 `/var/lib/tokenkey/caddy/etc/Caddyfile`，挂载的是目录而不是单个文件：单文件挂载在 `sed -i` 换 inode 后会继续读旧内容。入库模板为 [`entry/Caddyfile.tmpl`](entry/Caddyfile.tmpl)，`python3 entry_caddy.py --diff <线上文件>` 只读比对（忽略注释和空白），`--out PATH` 渲染。`to` 列表按私网 IP 排序，权重跟随各自节点。
- 应用机的节点覆盖由 `python3 node_compose.py --node app-b --out /opt/tokenkey/compose.node.yml` 渲染：`<private_ip>:8080` 与 `127.0.0.1:18080`、`memory_limit`、`TOKENKEY_NODE_NAME=cn-prod-<name>`、`GATEWAY_MAX_IN_FLIGHT_REQUESTS`（取自 `compose.override.yml`）和节点 `env`。`--diff EXISTING` 只读，有差异时退出码 1。
- 证书是 2026-11-15 从 app-a 复制的同一张（有效期至 2027-01-21），入口机之后用 TLS-ALPN-01 自行续期。DNS 切到入口机后，app-a 上 `api.galaxywaveai.com` 站点续期会失败，属预期，切流稳定后删掉该站点。

### 看门狗

| 主机 | 安装参数 | 监控内容 |
|---|---|---|
| 入口机 | `WATCH_ROLE=cn-prod-entry` | `https://<entry.domain>/health`；证书剩余不足 20 天；每个 `apps[]` 节点直接探测 `http://<private_ip>:8080/health`，503 持续超过 15 分钟告警，全部不健康立即告警 |
| app-a | `WATCH_ROLE=cn-prod WATCH_NODE=app-a` | 本机 `127.0.0.1:18080/health`、QA timer、磁盘，并保留对 prod 与 `public_origin` 的观测 |
| app-b | `WATCH_ROLE=cn-prod WATCH_NODE=app-b` | 本机 `127.0.0.1:18080/health`、磁盘；QA timer 待数据机协调器 |

带 `WATCH_NODE` 或入口机角色时，installer 用 `hostname -I` 核对 `prod-target.json` 的私网 IP。不带 `WATCH_NODE` 的 `cn-prod` 配置保持不变。三台都读 `/opt/tokenkey/host-alerts.env`。

### 迁移步骤

| # | 动作 | 入口 |
|---|---|---|
| M1 | 带新镜像正常发一次版（仍是蓝绿，app-b 手工换同一镜像） | 下文蓝绿流程 |
| M2 | app-b 已改用渲染的 `compose.node.yml` 并装看门狗；QA timer 待数据机协调器上线。入口机 `entry_caddy.py --diff` 差异审核后入库并装看门狗 | 本节 |
| M3 | 直连 app-a 的业务请求为 0 后，把 `public_origin` 改成入口机域名（2026-11-15 完成；IP 站点上只剩健康探测） | `prod-target.json` |
| M4 | app-a 改为单容器 `tokenkey`，绑定 `10.2.4.11:8080`，停 socat 和 blue/green（2026-11-15 完成）。app-a 的 Caddy 改指 `tokenkey:8080`，M3 之后再关 80/443。Caddyfile 是单文件挂载，`sed -i` 后 `caddy reload` 仍读旧 inode，要从宿主文件 `caddy adapt` 后经 admin API `/load` 加载 | 设计文档 M4 |
| M5 | 至少完成两次真实 rolling 发版并保留完整 receipt；同版本原样滚动会被 `mode=skip` 跳过，起不到演练作用 | `rolling.py` |
| M6 | 连续两次真实滚动成功后删除 `bluegreen.py` 的 cn-prod 分支和 socat 代码 | PR |

### 加减应用机清单

加一台（减一台反向执行，先从入口机摘除再停机）：

1. `prod-target.json` 的 `apps[]` 追加节点（`name`、`lighthouse_instance_id`、`private_ip`、`public_ip`、`weight`、`memory_limit`，可选 `env`）。
2. 数据机 `dataplane_source_acl.py --phase add-source`；云防火墙放行 5432/6379。
3. 8 台出口机 `proxy_source_add.py`，阿里云 1080 规则。
4. 新机 `/opt/tokenkey/.env` 的凭据与线上一致（`POSTGRES_PASSWORD` 见下文），用 `node_compose.py --out` 写 `compose.node.yml`，以 drain 状态起容器，跑 smoke 后再放行。
5. 入口机 `entry_caddy.py --diff` 确认只多出新节点，`--out` 写入后 `caddy reload`。
6. 装 QA timer（见「QA 归档」）与看门狗（`WATCH_NODE=<name>`），并重装入口机看门狗，让它探测新节点。

### QA 归档

app-a 的 `tokenkey-qa-provision/cleanup/archive` 由 `ops/qa/local_maintenance_host.py install --include-node1-archive --node1-environment cn-prod` 安装，归档走 v1 站点路径 `qa-raw/cn-prod/<小时>`，cleanup 用该路径的回执做删除门禁。app-b 不能照抄同一条命令：两台写同一个 v1 小时会互相冲突。`node1_archive.py --source-id cn-prod-app-b` 已能写隔离的 v2 路径，但现有 cleanup 拒绝把单源 v2 回执当删除凭据，所以 app-b 目前只能靠 `ops/qa/COORDINATOR.md` 的数据机协调器加 `force_retention.py` 方案。2026-11-15 用户已同意采用该方案，延后实施；在此之前 app-b 只抓取不清理（约 22MB/小时，根盘余 41G）。

## 首次安装与配置 owner

> 本节记录 UCloud 首发时的安装流程；腾讯北京迁机按 host-replacement 技能执行，不重跑本节。

- UCloud 身份使用官方 CLI 的 `tokenkey-domestic` profile；密钥不进入仓库。
- `bootstrap-host.sh DEVICE EXPECTED_BYTES` 只接受新空数据盘。它拒绝带分区、已挂载、
  尺寸不符或含签名的磁盘；已有文件系统必须人工核验后挂载，不会重格式化。
- `prepare-runtime.py STATE_DIR` 从共享 Stage0 Compose/Caddy 模板生成本机受保护 bundle，
  重用已存在的 secrets，不在 stdout 输出配置或密钥。`STATE_DIR/images.json` 必须来自
  已校验的 amd64 镜像，包含各服务的 `source` digest、`config` digest、归档 `sha256` 与本地 `tag`。
- `compose.override.yml` 打开 QA capture，不配置 QA S3 上传，并将应用池对齐当前宿主规格（**4C16G**，2026-11-14 升级）。同一份 override 还设置应用容器的硬内存上限（`mem_limit`/`memswap_limit`，默认 3g，`TOKENKEY_APP_MEMORY_LIMIT` 可覆盖）与全局在途闸门 `GATEWAY_MAX_IN_FLIGHT_REQUESTS`（默认 512，0 为关闭）。美西 prod 的容器上限走另一份 compose（`docker-compose.bluegreen.yml`，默认 8g），两站机制不同，不要互相套用
  （`GOMEMLIMIT=2GiB`、DB open/idle `40/10`、Redis pool/idle `96/8`；内存档未变，
  仅主机从 4C 升到 8C）。蓝绿 `prepare` 会用该文件解析出的池参数，以及
  `QA_CAPTURE_ENABLED=true`，覆盖候选容器
  env（密钥仍取自线上容器）。候选若带上 QA archive 或 S3 driver 会被拒绝。容量 bump 可先起候选再切 Caddy。现网核对/写入还可用
  `ops/observability/probe-cn-prod-pool-env.sh`（经 `run-probe.sh --target cn-prod`，
  写入需 `APPLY=yes-tune-cn-prod-pools`，会 recreate 当前色）。单号 Inflight/RPM/TPM
  阶梯用 `ops/observability/probe-cn-prod-single-account-ladder.sh`（`--env ACCOUNT_ID=…`，
  `--with ops/pricing/probe_reserved_resources.sh`，会 pin 探针组，须授权压测）；
  同时注入 `SECURITY_PROXY_PROBE_PRESET=domestic`，让 Admin 代理「出站探测」走国内
  优先链（ipip → ip9 → qq-ip2city → ipify），避免默认境外 `ip-api.com` 在国内 SOCKS
  上出现数秒假延迟。显式 `security.proxy_probe.urls` 仍优先于 preset。
  `compose.local.yml` 只将管理端口绑定到主机 loopback。
- 默认由 CI 上传同一 GHCR 构建的 amd64 归档到私有 S3，经 SSM 预下载并校验导入。
  分发契约见 [cn-prod-image-distribution.md](../../docs/approved/cn-prod-image-distribution.md)。
  `pull_policy: never` 防止启动时漂移；GHCR 直拉是显式备用路径。
- 主机运行目录 `/opt/tokenkey`，Compose 文件依次为 `compose.yml`、`compose.override.yml`、
  `compose.local.yml`；环境文件 `.env` 权限 0600。Caddyfile 位于
  `/var/lib/tokenkey/caddy/Caddyfile`。

**数据库密码只有一个来源。** 共享 `compose.yml` 用 `DATABASE_PASSWORD=${POSTGRES_PASSWORD}`，所以 `.env` 的 `POSTGRES_PASSWORD` 必须等于数据机实际接受的密码。蓝绿候选从线上容器继承密钥，`.env` 漂移不会在蓝绿里暴露，只会在新节点或从 compose 重建时以 `password authentication failed` 出现（2026-11-15 app-b 首次启动即如此；当日已把 cn-prod `.env` 的 `POSTGRES_PASSWORD` 收敛到线上值，备份为 `.env.before-pgpw-converge-*`）。`bluegreen.py prepare` 现在比较 compose 渲染出的 `DATABASE_PASSWORD`/`REDIS_PASSWORD` 与线上容器，不一致就拒绝，报错只给键名不给值。轮换数据库密码时必须同时改数据机、`.env` 的 `POSTGRES_PASSWORD` 和 `DATABASE_PASSWORD`。

首次 `up -d tokenkey` 后，通过受限 SSH tunnel 初始化管理员、关闭公开注册、关闭
`backend_mode_enabled`（否则普通客户不能登录），再启动 Caddy。后续升级必须走共享 drain
与健康检查契约，不把首次安装步骤作为强制重启生产服务的升级脚本。

## 远程运维入口

远程运维身份只读 [`prod-target.json`](prod-target.json) 的 `ssm_instance_id` 和
`ssm_region`。agent 由主机主动通过 HTTPS 连接 AWS SSM；billing/usage/error probe 通过
`run-probe.sh --target cn-prod` 下发，不依赖运维机公网 IP，也不需要开放公网 SSH 22。

SSH 只用于一次性 agent bootstrap 或 UHost 控制台不可用时的恢复。需要临时 SSH 时只放行
当前出口 `/32`，完成操作立即移除；不得把动态运维出口固化为长期防火墙规则。日常 probe
不得回退到 SSH。新机 bootstrap 会运行
[`deploy/tencent/lock-down-sshd.sh`](../tencent/lock-down-sshd.sh)：关掉密码登录、root
登录和 TCP 转发，并锁定 root 密码。云防火墙不开放公网 22。

## 发布路径：rolling 默认，蓝绿仅兼容/恢复

cn-prod 的正常发布入口是 `deploy/domestic/rolling.py`，按 `prod-target.json` 的
`apps[]` 顺序逐台执行 drain、换镜像、私网健康检查和回流。入口机继续把请求分给
另一台应用机，因此发布过程不需要节点内蓝绿。

Release 后的 `distribute-cn-image.yml` 分两段执行：`publish` 在 AWS OIDC 身份下上传
并校验私有 S3 工件、渲染每节点预下载脚本；`tencent-ops` job 只持腾讯凭据，按
`apps[]` 顺序通过 TAT 预下载到每个节点。warm 固定 `--channel tat`，不使用 `auto`：
app-a 仍带 SSM Hybrid 身份，`auto` 会选中 AWS SSM，而腾讯 job 没有 AWS 凭据。
任一节点失败都停止后续节点，且不触碰正在服务的容器。

分发和每个节点预下载回执齐全后，用同一工作流入口切流：

```bash
bash scripts/stage0/dispatch-prod-deploy.sh \
  --operation cn-deploy --tag X.Y.Z --revision FULL_SHA
```

`cn-prod-rolling-deploy.yml` 的审批 job 使用 `prod` Environment，执行 job 使用
`tencent-ops`；输入 tag 是裸 `X.Y.Z`，revision 必须是已审完整 40 位 SHA。

`deploy_via_ssm.sh` 与 `bluegreen.py` 仍保留为迁移过渡和恢复路径。它们只能用于
尚未完成单容器迁移的节点、恢复旧布局，或经单独批准的故障处理；不能作为已收敛
节点的日常发布入口。连续两次真实 rolling 发布成功并有完整 receipt 前，不能删除
这些兼容代码。

## SSM 蓝绿升级（仅兼容/恢复）

与 probe 相同：经 `prod-target.json` 的 SSM Hybrid 身份下发，**不要**为常规发版开放
SSH 22 或从运维机 scp 离线 tar。入口脚本：

```bash
# 1) Release 成功后 distribute-cn-image.yml 自动上传并预下载；重复调用复用已验证镜像
# 运维机需现有 AWS 短期身份。主机上已有同一 tokenkey-domestic/tokenkey:TAG-SHORT
# 且预下载回执的 image id 一致时，pull 不再渲染 S3，本机不需要 boto3。镜像不在或回执对不上时，
# S3 render 需要 boto3（入口自动挑选能 import 的 python3，也可设 CN_PROD_PYTHON3）。
# revision 是已审完整源码 SHA
bash deploy/domestic/deploy_via_ssm.sh pull --tag 1.8.306 --revision REVIEWED_FULL_SHA
# 可选：--digest sha256:… 再校验 amd64 manifest digest（不是 multiarch index）
# 显式备用路径；旧版本没有 S3 工件时也走此入口
bash deploy/domestic/deploy_via_ssm.sh pull --transport ghcr --tag 1.8.306 --digest sha256:AMD64_OR_INDEX_DIGEST
# GHCR 可用 CN_PROD_SSM_DRY_RUN=1 打印脚本；S3 不打印含签名 URL 的脚本

# 2) 同步 bluegreen 控制文件到 /opt/tokenkey/releases/artifacts-ssm/
bash deploy/domestic/deploy_via_ssm.sh sync-controls

# 3) prepare（不切公网流量）；revision 为已审完整源码 SHA
bash deploy/domestic/deploy_via_ssm.sh prepare \
  --tag 1.8.306 \
  --revision REVIEWED_FULL_SHA \
  --image tokenkey-domestic/tokenkey:1.8.306-SHORTSHA

# 4) 候选实测。--state 用 prepare 回执里的 state，不要另写烟测脚本。
bash deploy/domestic/deploy_via_ssm.sh candidate-smoke \
  --state /opt/tokenkey/releases/1.8.306-SHORTSHA
```

`bluegreen.py` 仍是国内主机适配入口。Caddy 站点可以是 `api-cn.tokenkey.dev`，也可以是北京静态 IP `154.8.214.179`（`https://` 与 `:443` 写法同样接受）。`/var/lib/tokenkey` 可以是独立挂载，也可以直接在根文件系统上；根盘用该目录与 `/` 的设备是否相同来识别，不会把普通目录伪装成 mountpoint。
从共享 `deploy_via_ssm_bluegreen.sh` 提取健康检查、Caddy 切流、观察与 drain，未复制实现。切流后的本机健康观察使用 Caddy 里的那个站点名，因此静态 IP 站点不会再拿 `api-cn.tokenkey.dev` 去对本地证书。
候选实测是 `deploy_via_ssm.sh candidate-smoke`。它在主机上跑已同步的
`candidate_smoke.py`，对候选 loopback（blue 18081、green 18082）请求 `/health` 和
`/api/v1/settings/public`，把结果写到 `/private/candidate-smoke.json`。任一用例失败时
命令非 0，失败用例仍留在文件里。`promote` 使用清单里的 `public_origin`，传入其他 URL
会在下发前拒绝。

`--state` 是 `prepare` 实际写入的目录，形如 `/opt/tokenkey/releases/<version>-<shortsha>`；
`prepare` 的回执里有 `state` 字段，直接取那个值，不要照抄本文的示例版本号。

`--postcheck-plan` 必须由生成器产出，**不要手写**。它同时被两个阶段消费：`configure()`
校验 `range`，切流后 `finish()` 还要读 `changes` 拼发版说明。手写一份只带 `range` 的
plan 能通过切流前校验，却会让通知阶段抛 `KeyError`（2026-10-23 cn-prod 1.8.318 踩过）。
`configure()` 现在会在切流前就拒绝缺 `changes` 的 plan。

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
# 1) 在仓库侧生成 plan（revision 用完整 SHA，与 prepare 的 --revision 一致）
python3 scripts/release_post_check.py plan \
  --live LIVE_FULL_SHA --new NEW_FULL_SHA > /tmp/post-plan.json

# 2) plan 是数据，用 put-file 送到主机。上一版文件还在且内容不同时加 --force。
bash deploy/domestic/deploy_via_ssm.sh put-file \
  --local /tmp/post-plan.json --remote /private/post-plan.json

# 3) 切流。receipt 用 prepare 回执的 receipt_sha256。public-url 取自 prod-target.json。
bash deploy/domestic/deploy_via_ssm.sh promote \
  --state /opt/tokenkey/releases/1.8.306-SHORTSHA \
  --receipt PREPARED_RECEIPT_SHA256 \
  --postcheck-plan /private/post-plan.json
```

`candidate-smoke` 写出的报告形态是 `{"revision","container_id","cases":[{"case","passed"}]}`。
`container_id` 是 `docker inspect` 的 `Id`。上游 429/限流不在这四条控制面检查里；
那种失败按故障速查对候选定点复测，复测结论留在证据里，不删分母。

### 私有 S3 分发初始化

发布包含此变更的版本前执行一次。现有桶和地域以 `image-distribution.json` 为准；
不得替换整个桶策略或删除其它生命周期规则。以下操作只管理分发目录权限/保留期限，
不更新主机、数据库、网关或 Docker daemon。先跑 preflight。

```bash
bucket=$(python3 -c 'import json; print(json.load(open("deploy/domestic/image-distribution.json"))["bucket"])')
region=$(python3 -c 'import json; print(json.load(open("deploy/domestic/image-distribution.json"))["region"])')
role_arn=$(gh variable get AWS_OIDC_ROLE_ARN -R youxuanxue/sub2api)
instance=$(python3 -c 'import json; print(json.load(open("deploy/domestic/prod-target.json"))["ssm_instance_id"])')
ssm_region=$(python3 -c 'import json; print(json.load(open("deploy/domestic/prod-target.json"))["ssm_region"])')
aws cloudformation deploy --region "$region" --stack-name tokenkey-cn-image-distribution \
  --template-file deploy/domestic/image-distribution-policy.yaml --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides RunnerRoleName="${role_arn##*/}" BucketName="$bucket" \
    ManagedInstanceId="$instance" SsmRegion="$ssm_region"
python3 deploy/domestic/image_distribution_lifecycle.py
python3 deploy/domestic/image_distribution_lifecycle.py --apply
```

独立 `distribute-cn-image.yml` 由成功 Release 的 workflow_run 触发，使用默认分支代码
与已有 main OIDC 信任，不开放其它 tag/branch 信任。下载的版本/revision 仅作数据，
先与当前 git tag 核对；不 checkout 或执行该标签的脚本。

`publish` 只使用 AWS OIDC/S3 权限，把校验后的归档写入私有前缀，并由
`warm_fanout.py render-all` 按 `apps[]` 生成每节点的短期签名脚本。render artifact
含短期签名 URL，只保留 1 天且从不打印；publish receipt 保留 7 天。`warm` job 使用
`tencent-ops` Environment，只使用腾讯 TAT 凭据，校验 render artifact 后逐节点执行
`warm_fanout.py dispatch-rendered --channel tat`，任一节点失败即 fail-stop 并写失败
receipt。正式 rolling 发布仍由 `cn-prod-rolling-deploy.yml` 的 `prod` 审批 job
放行，分发成功不等于发布成功，也不表示已切流。

S3 对象保留 30 天，非当前版本 7 天；过期版本可显式 GHCR 直拉。
CI 分发失败明确使独立分发 workflow 失败，但不撤销已构建镜像或操作正在服务的容器。
归档下载限速 512 KiB/s；预下载复用共享 host deploy lock。锁忙时任务失败可重试。
预下载成功只表示镜像就绪，不表示候选烟测或线上发布成功。
成功回执在 CI artifact 和主机 `/var/lib/tokenkey/image-distribution/`；节点回执不含
签名 URL，`rolling.py` 还要求其 `image_id` 与本地不可变 tag 完全一致。
导入开销发生在 Docker daemon；下载进程 nice/ionice 不等于 daemon 资源限制。

### 恢复：离线 tar + 受限 SSH（非默认）

仅当主机无法 `docker pull` GHCR 或 SSM Hybrid 不可用时使用。临时放行运维出口 `/32`，
scp 已校验的 amd64 archive → `docker load` → 绑定本地 tag，完成立即移除 SSH 规则。
不得把动态运维出口固化为长期防火墙规则。

prepare 只启动候选；复用当前应用环境、数据挂载和实际 Docker 网络，数据库/Redis 不重建。
解析后的 Compose 挂载必须与现网实际源目录、目标和读写模式一致，且 `/app/data` 仍绑定
国内数据盘；配置漂移时在创建候选前拒绝执行。
候选管理端口仅监听 loopback（blue 18081、green 18082），正式入口仍由 Caddy 决定。
切流前实测候选，promote 对容器身份、配置和回执重新核验后热切；健康观察和旧容器
`draining=true, in_flight=0` 验证通过才 stop 旧容器。失败保留新旧容器供诊断。
旧容器和镜像保留，`rollback --state ... --receipt ...` 重启旧进程、确认健康、恢复原路由，
再排空候选。旧进程若因发布中断仍运行在 drain 状态，先验证在途归零再停止、启动，
避免 `docker start` 对运行中容器无效。所有操作均使用共享 host lock；运行文件和含环境变量的 Compose 为 0600。
后续轮次若候选颜色名称已存在，先核对其是否仍承担回退义务；入口不会自动删除旧候选。
Docker 的 `unless-stopped` 重启策略负责活动容器重启；原单容器 Compose 不再作为应用升级入口。
切流前用 `scripts/release_post_check.py plan --live OLD_SHA --new NEW_SHA` 生成计划，
两端传实际镜像完整源码 SHA。候选实测由 `candidate-smoke` 写入
`/private/candidate-smoke.json`，失败用例保留。计划经 `put-file` 落盘；仅恢复路径才用受限 SSH。`promote`
在切流前核对源码/容器身份及全部用例通过，并固化控制脚本与证据哈希。
`--public-url` 必须与 `prod-target.json` 中的 `public_origin` 一致，该清单也供公网监控使用，
并随发布证据固化哈希；填入其他公网 IP 会在切流前拒绝。原国内域名已指向美西 prod，不能用
域名的健康响应代替 cn-prod 验证。

切流和 drain 完成后，`promote` 自动调用共享 probe/evaluator 做立即检查，并等待至真实
切流后至少五分钟再检查累计流量、HTTP/带内最终 5xx 与 panic；同时核对当前活动容器及
公网健康。两阶段 gate 通过后，自动复用 `notify-feishu-release.sh` 向现有 prod 飞书群发
**cn-prod** 上线卡片。卡片中的环境、旧/新版本、静态 IP 入口和切流时间来自本次证据，
补发不会把当前发送时间写成上线时间。通知密钥仅从本机 root-only
`/opt/tokenkey/host-alerts.env` 读取，不经过远程投递参数、仓库或输出。

验证结果和通知回执保存在 `STATE/post-release/`。通知失败只警告并记录失败状态，不回滚
健康应用，也不伪装成发送成功。`notification.json` 的 `status=sent` 表示飞书 HTTP/API
确认接收；不等同所有群成员已读。失败/缺配置可用同一状态重试：

```bash
python3 bluegreen.py notify --state /opt/tokenkey/releases/1.8.306-2d4b4cdbf \
  --receipt PREPARED_RECEIPT_SHA256 \
  --postcheck-plan /private/post-plan.json \
  --smoke-report /private/candidate-smoke.json \
  --public-url https://CN_PROD_STATIC_IP
```

`notify` 不切流或重启，拒绝对已非当前健康活动版本发成功通知；已确认成功的同次发布不再
重复发送。网络超时可能存在“飞书已接收但本机没收到确认”的不确定性，人工补发前核对群。
若 postcheck 本身失败，保留已切流现场并非零退出，不发成功通知；先排查再决定恢复或重试。

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

## 本机凭据与交付

- `~/codes/keys/tokenkey-cn-admin.json`：管理员登录地址、邮箱、密码。
- `~/codes/keys/tokenkey-cn-rt.json`：客户登录地址、密码来源说明与 API key。
- 两文件权限均为 0600；完整初始化状态位于
  `~/.local/share/tokenkey/domestic-onboard`（0700）。禁止粘贴完整导出或 dry-run 日志。
- 没有自动发送邮件或客户消息。

客户的 `tokensea_online`、`国产LLM测试` 两个有效 key 已按用户要求从 hcloud 原值复制，
归属本站 `rt@tk.com` 并绑定本站 `china_deployed`。原 key 可直接改用
`https://api-cn.tokenkey.dev/v1`；新旧站点余额/用量独立记账。
已停用的源站临时压测 key 未复制。原始 key 和账号完整导出不进入仓库。
用户随后要求保持原登录密码：已通过加密传输将源账号密码校验值定点同步至本站同名用户，
核对校验值一致且余额不变，原新机密码和旧登录 token 均返回 401。原密码明文不可恢复，
未用明文模拟登录；本机客户凭据文件已移除失效密码，并注明沿用 hcloud 密码。

## 验收回执

2026-10-14：北京本机直连 DNS、可信 HTTPS 和 `/health` 通过。四个账号配置与受保护导出
逐项一致。Chat Completions 普通/流式请求及 Responses 请求成功，usage 记录有账户归属和
0.3 倍率；实际覆盖 VolcEngine 两个账号和 Ali 的 `ali-token-plan-7`，另一个 Ali 账号完成配置
核验但未单独做请求归属验证。首次验收使用管理员临时 key，结束后已删除，首客初始余额 $10。

追加的十个火山账号（原名、`-2`、`-4` 至 `-10`、`-40`）凭据与所有可导出配置逐项一致，
绑定专属组且 active/schedulable；本站共十四个供应账号。两个复制的客户 key 均通过公网
模型列表和 kimi-k3 请求验证（HTTP 200、有正文），这两次客户验证实际记账 $0.00062686。

[Kimi K3 性能报告](../../docs/ops/reports/ucloud-kimi-k3-20261014/report.md)：4K–512K 八档，
正式请求 76/76 成功；含校准/预热 85/85 成功，管理员实际记账 $1.157457。
独立压测 key 已停用并验证 401。测试为 prod 主机经公网域名发起的小样本混合缓存测量，
不代表终端用户网络、冷缓存性能或持续容量承诺。

Playwright 真实 UI 完成管理员登录、账号/用户页、客户登录、API key/用量页，未捕获页面异常。
客户访问管理 API、绑定默认组和公开注册均返回 403。共享 preflight 通过；磁盘初始化实测拒绝
系统分区盘并正确挂载新数据盘。

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
