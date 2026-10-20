# UCloud domestic prod

审批范围和费用见 [design-domestic-cluster.md](../../docs/approved/design-domestic-cluster.md)。
这是独立 UCloud 目标，不使用 Hetzner dispatcher 或现网数据库；只读观测通过独立注册的 AWS SSM Hybrid agent 下发。

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

## 首次安装与配置 owner

- UCloud 身份使用官方 CLI 的 `tokenkey-domestic` profile；密钥不进入仓库。
- `bootstrap-host.sh DEVICE EXPECTED_BYTES` 只接受新空数据盘。它拒绝带分区、已挂载、
  尺寸不符或含签名的磁盘；已有文件系统必须人工核验后挂载，不会重格式化。
- `prepare-runtime.py STATE_DIR` 从共享 Stage0 Compose/Caddy 模板生成本机受保护 bundle，
  重用已存在的 secrets，不在 stdout 输出配置或密钥。`STATE_DIR/images.json` 必须来自
  已校验的 amd64 镜像，包含各服务的 `source` digest、`config` digest、归档 `sha256` 与本地 `tag`。
- `compose.override.yml` 关闭 QA capture/telemetry，并将应用池对齐 **O 8C/8G**
  （`GOMEMLIMIT=2GiB`、DB open/idle `40/10`、Redis pool/idle `96/8`；内存档未变，
  仅主机从 4C 升到 8C）。蓝绿 `prepare` 会用该文件解析出的池参数覆盖候选容器
  env（密钥仍取自线上容器），因此容量 bump 可先起候选再切 Caddy。现网核对/写入还可用
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

首次 `up -d tokenkey` 后，通过受限 SSH tunnel 初始化管理员、关闭公开注册、关闭
`backend_mode_enabled`（否则普通客户不能登录），再启动 Caddy。后续升级必须走共享 drain
与健康检查契约，不把首次安装步骤作为强制重启生产服务的升级脚本。

## 远程运维入口

`cn-prod` 已注册为 AWS SSM Hybrid managed instance `mi-0efdf781205cff512`，区域为
`eu-west-2`，使用仅附加 `AmazonSSMManagedInstanceCore` 的现有 agent 角色。agent 由主机
主动通过 HTTPS 连接 AWS SSM；billing/usage/error probe 通过 `run-probe.sh --target cn-prod`
下发，不依赖运维机公网 IP，也不需要开放公网 SSH 22。

SSH 只用于一次性 agent bootstrap 或 UHost 控制台不可用时的恢复。需要临时 SSH 时只放行
当前出口 `/32`，完成操作立即移除；不得把动态运维出口固化为长期防火墙规则。日常 probe
不得回退到 SSH。

## SSM 蓝绿升级（默认）

与 probe 相同：经 `prod-target.json` 的 SSM Hybrid 身份下发，**不要**为常规发版开放
SSH 22 或从运维机 scp 离线 tar。入口脚本：

```bash
# 1) Release 成功后 distribute-cn-image.yml 自动上传并预下载；重复调用复用已验证镜像
# 运维机需 boto3 和现有 AWS 短期身份；revision 是已审完整源码 SHA
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
```

`bluegreen.py` 仍是国内主机适配入口，只接受 `api-cn.tokenkey.dev` 的现有运行配置；
从共享 `deploy_via_ssm_bluegreen.sh` 提取健康检查、Caddy 切流、观察与 drain，未复制实现。
候选实测与 `promote` 仍在主机执行（可用 SSM Run Command / Session Manager），需要
`--public-url` 与 `prod-target.json` 的 `public_origin` 一致（静态 IP，不能用已切往 AWS
的域名冒充 UCloud 验证）：

```bash
python3 /opt/tokenkey/releases/artifacts-ssm/bluegreen.py promote \
  --state /opt/tokenkey/releases/RELEASE \
  --receipt PREPARED_RECEIPT_SHA256 \
  --postcheck-plan /private/post-plan.json \
  --smoke-report /private/candidate-smoke.json \
  --public-url https://UCLOUD_STATIC_IP
```

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
先与当前 git tag 核对；不 checkout 或执行该标签的脚本。正式切流审批保持原流程。
S3 对象保留 30 天，非当前版本 7 天；过期版本可显式 GHCR 直拉。
CI 分发失败明确使独立分发 workflow 失败，但不撤销已构建镜像或操作正在服务的容器。
归档下载限速 512 KiB/s；预下载复用主机部署锁。锁忙时任务失败可重试。
预下载成功只表示镜像就绪，不表示候选烟测或线上发布成功。
成功回执在 CI artifact 和主机 `/var/lib/tokenkey/image-distribution/`，均不含签名 URL。
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
两端传实际镜像完整源码 SHA。将候选实测汇总为 JSON：

```json
{"revision":"候选完整SHA","container_id":"候选完整Docker ID","cases":[{"case":"实际用例名","passed":true}]}
```

`cases` 必须来自真实实测，失败或未执行用例不得删掉。计划和烟测报告默认经 SSM
（Run Command / Session Manager）写入主机私有目录；仅恢复路径才用受限 SSH。`promote`
在切流前核对源码/容器身份及全部用例通过，并固化控制脚本与证据哈希。
`--public-url` 必须与 `prod-target.json` 中的 `public_origin` 一致，该清单也供公网监控使用，
并随发布证据固化哈希；填入其他公网 IP 会在切流前拒绝。原国内域名已切往 AWS，不能用
域名的健康响应代替 UCloud 验证。

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
python3 bluegreen.py notify --state /opt/tokenkey/releases/RELEASE \
  --receipt PREPARED_RECEIPT_SHA256 \
  --postcheck-plan /private/post-plan.json \
  --smoke-report /private/candidate-smoke.json \
  --public-url https://UCLOUD_STATIC_IP
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

QA capture 已关闭，cn-prod 直接复用与 prod 相同的共享 Caddy 模板，不增加国内专属路径拦截。
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
系统盘、数据盘和内存。磁盘达到 85% 告警、低于 80% 恢复；内存 90% 连续两次或 96%
单次告警，低于 80% 恢复。凭据放在系统盘 `/opt/tokenkey/host-alerts.env`（0600），
通过 `TOKENKEY_HOST_ALERT_ENV_FILE` 指定，数据盘失挂时仍能通知；节点标识为 `cn-prod`。

公网可用性由共享 `ops/observability/host_https_watch.py` 承担：cn-prod 提供境内视角，prod
提供独立站外视角。两处分别检查 UCloud 静态 IP、当前转到 AWS 的国内域名、AWS prod
清单中的客户域名。当前国内主机身份与入口见
[`prod-target.json`](prod-target.json)，AWS 目标仍从现有 fleet 清单生成，不能用域名的绿色结果
替代 UCloud 静态 IP。探针每两分钟执行，连续两次失败发 P0 飞书，持续失败每三十分钟提醒，
首次恢复发送恢复消息；健康要求可信 TLS、HTTP 200 且 JSON `status=ok`，拦截页不算健康。
告警标明探测节点和目标 URL；发送失败不推进去重状态，下次重试。

这覆盖公网入口、DNS、TLS、Caddy 和应用就绪状态，不代替模型调用成功率/延迟告警。
境内观察点与 cn-prod 共机，整机失联由 AWS 观察点兜底，不能代表所有国内运营商。
UCloud 云告警、备份/恢复不在此安装器范围内。

安装复用 `ops/observability/install-host-https-watch.sh`，`WATCH_ROLE=cn-prod`，将该脚本及
`host_https_watch.py`、`https_health.py`、`feishu_delivery.py`、
本目录的 `prod-target.json`、
`deploy/aws/fresh-migration-20261015.json`
按 basename 放入主机私有目录。`WATCH_BUNDLE_DIR` 指向该目录；不传 `APPLY` 时只核对身份
并输出配置，`APPLY=yes-host-https-watch` 才安装。cn-prod 读取现有
`/opt/tokenkey/host-alerts.env`；AWS 沿用现有 `.env`，不另复制密钥。安装前备份 unit，使用
内容寻址的代码目录；可恢复备份 unit 后 daemon-reload 回退。不会重启应用、数据库或 Caddy。

节点身份修复以 `1.8.292-cn-prod` 本地 amd64 补丁镜像部署，仅替换 UCloud 应用容器。
镜像基于原已校验发布镜像，附源码差异 SHA-256 与二进制 SHA-256；构建记录保存在受保护
初始化状态中。部署前 preflight 和节点身份/通知回归测试通过，使用共享 Stage0
`drain_container` 确认 `draining=true, in_flight=0` 后替换，健康检查通过；原镜像保留回退。

2026-10-14 飞书验收：通过临时、明确标注“非真实故障”的 P0 CPU 规则触发应用实际通知，
事件记录 `feishu_firing_sent=true`、`feishu_firing_status=sent`。测试规则已删除，测试事件已
标记恢复；未修改正式规则阈值。主机定时器 enabled/active、首次执行成功。公网健康检查和
两个复制客户 key 的鉴权在更新后仍通过。邮件与整机失联外部探测不在此次送达证明之内。

备案、备份和恢复按用户决定后续处理；此回执仅记录当前实测可达性。
