---
status: approved
approved_by: user (conversation)
approved_at: 2026-10-16
scope: UCloud ingress and isolated Node1 production preparation; cutover excluded
---

# Node1 prod 切流前准备

用户批准完成 UCloud 公网入口、WireGuard 隧道和 Node1 prod 的全部切流前准备。
**不得影响当前 AWS prod 网关服务；正式切流须独立审批。** 本文不授权 DNS 切换、
源端停写/排空、修改现网账号/凭据/调度、重启网关或启用候选库的生产账号。
现网身份只读 [舰队入口](../../deploy/hetzner/README.md)。

## 数据完整性

权威源是当前 AWS prod，不能再使用旧 Hcloud 快照作为上线数据。
迁移包括 OLTP、账务幂等、账号/代理/凭据，以及每个用户 **2026-09-01 起至最终切流时刻**
的 `usage_logs`（含后来补录的数据）。为覆盖北京时间零点，导出下界不得晚于
`2026-08-31T16:00:00Z`；保留完整现存使用明细也满足要求。
定时 pgdump 排除了 `usage_logs*`，它只证明 precious 恢复，不能作为历史明细验收。
优先从现有 EBS 快照的独立副本运行 PostgreSQL 并导出，禁止对现网执行重型全库扫描。
快照副本只运行数据库；不启动应用或 OAuth 刷新者。临时资源导出验收后清理。

按同一数据水位逐用户、逐日核对记录数、请求身份/内容、输入输出与缓存 token、
费用、最早/最晚时间；核验历史页面和导出。不以余额/汇总或表存在代替历史记录。
最终冻结、最新凭据与尾部日志同步属于切流审批后的步骤，当前只准备并演练机制。
Redis 不做在线复制；重建缓存和会话影响须在切流包明确。

## 部署与隔离

`dispatch-prod-deploy.sh --target zhupeng-prod` 显式走 SSH；省略 target 保持 AWS workflow。
未知/禁用目标及 GitHub/replay override 必须拒绝，不能回落到现网。
SSH 只运输共享 `deploy_via_ssm_bluegreen.sh` host payload；不复制蓝绿状态机。
渲染 SSH identity 必须 render-only，绝不能发送到 AWS SSM。
`run-probe.sh --target zhupeng-prod` 同样只适配 SSH 运输，沿用共享探针及其 companion
注册。发送前与执行前核对主机/磁盘，独立 flock 和远端 timeout 限制并发/悬挂；拒绝 AWS
instance override。省略该显式目标仍使用原 prod/edge SSM 路径。

主机 machine-id、ext4 数据盘 UUID、bind mount 和 ownership 均在写前核对；不格式化。
旧测试栈、旧候选数据库及原始 sub2api 栈保留，不隐式接管已有数据。
新 AWS 数据恢复到独立候选目录，数据库禁止公网端口与应用连接，保留 DATA_SYNC_HOLD。
候选应用验证只用冻结的脱敏/禁用副本和专用测试资源，不允许复制的活 OAuth 凭据双刷新。
运行时/加密材料通过私有文件传输，禁止输出或入 git。

现有库存 target 仍代表隔离测试栈；正式域名、入口及候选数据的启用不能由普通测试 deploy
隐式完成。共享 Caddy 切换用运行中二进制验证，保留其 PROXY protocol 模块和证书挂载。

正式目标只生成待接管库存，不在准备期替换 `deploy/domestic/targets.json` 或主机
`/opt/tokenkey/target.json`：

```bash
python3 deploy/domestic/target.py --target zhupeng-prod \
  --production-domain api.galaxywaveai.com --admin-email admin@tokenkey.dev
```

批准窗口内安装同一目标到库存与主机 ownership，并安装已暂存的运行时 guard 后，继续
使用同一 SSH dispatcher。正式模式的每次部署/探针都复用 `node1_runtime_guard.py`，
检查批准标记和候选 PG bind；缺任一条件即拒绝。正式模式禁止 bootstrap 或生成空库运行时，
只能接管已经最终对账的候选。准备期渲染不会改库存、安装 ownership、移除 HOLD 或接流量。

## 网络与运维验收

独立 UCloud EIP 按流量计费，经 WireGuard 到 Node1；现有 api-cn 入口不变。
正式域名配置先离线渲染并用指定 IP 验证，DNS 不切换。证书续期、PROXY v2 客户端地址
信任、SSE/长连接、隧道重连均需实测，不以 /health 代替真实模型请求，不做 EIP 压测。
验证实际绑定的代理以及无代理账号的直连上游，不能按代理逻辑名称推断物理位置。

备份、离机恢复、磁盘/HTTPS 监控与开机挂载保护均准备并验收；候选生产账号保持禁用。
告警验证不发送测试外部消息。生产 activation 保留审批锁；回滚需保留最新数据与轮换凭据，
旧生产机至少保留七天，不能直接重新启用过期 OAuth 副本。

## 验收记录

测试代码：`deploy/domestic/test_ssh_deploy.py`、`test_runtime.py`、`test_bluegreen.py`，
共享蓝绿回归：`ops/stage0/test_deploy_via_ssm_bluegreen.py`。
Story：US-065。运行证据保存私有运维目录，交付时报告真实完成项与剩余阻塞。
本文的审批仅覆盖切流前准备，不代表代码已合并、生产业务已实测或正式切流已获批准。

## 执行入口与剩余审批边界

`ops/migration/node1_candidate.py` 只提供 prepare / inspect / restore，验证固定主机、
数据盘、候选目录与 Docker 隔离；没有 activation、DNS 或源端停写操作。
恢复必须使用带 usage_logs 的 custom dump 及独立 SHA256，失败留下标记并拒绝直接重试。
`usage_history_manifest.sql` 仅扫描离线快照和隔离恢复库；`usage_history_audit.py`
输出逐用户/日对账结果，不输出用户请求内容。

`node1_history_rehearsal.py` 要求对账通过后才创建一次性数据库副本，禁用全部供应账号、
清除上游凭据、替换用户登录材料，使用只有 loopback 的网络命名空间。
生产基线库不连接演练应用；页面/API 验证结果不能替代最终冻结后的尾部日志和凭据对账。

当前 `standard_freeze_delta.py` 默认排除 usage_logs，且只生成计划、不实现超时回退。
因此不能直接把既有 precious 切流计划用于本次历史数据迁移。最终切流包必须补全历史日志
增量/补录校验、冻结期限和失败恢复；没有通过这些验收时，保持 DATA_SYNC_HOLD。
仅按 max(id) 或 created_at 同步尾部，不足以发现保留旧 ID 的补录或历史行更新。

## 预备工具与审批包

用户已确定迁移后 API 和登录后台统一使用 `api.galaxywaveai.com`。旧四个正式域名
不再是 Node1 的入口验收前置条件；当前 AWS 域名、服务和 DNS 保持不变。新域名已指向
独立测试入口，但开放登录/API、接入候选库仍属于正式切流，必须单独审批。

- `node1_candidate.py delta-manifest|delta-apply --scope precious|usage --artifact ...`
  复用 `fresh_host_delta.py`，额外绑定 Node1 machine-id、数据盘、隔离容器和 HOLD。
  `usage` 范围包括父表下的物理历史分区；按内容校验更新、删除和保留旧 ID 的补录。
  schema/分区集合漂移会拒绝增量包，须在冻结前更新基线；不得绕过校验继续切流。
- `check-node1-usage-delta-postgres.py` 仅创建两个固定命名的合成数据库，验证旧历史改动、
  补录、删除、序列、损坏包拒绝和过期基线拒绝，完成后删除合成数据库。
  此演练不等于真实全量数据的冻结时长已经达标。
- `node1_history_verify.py` 只连接无外网演练命名空间，逐用户/日核对 API 可见条数和跨租户
  明细访问拒绝；真实浏览器页面与 CSV 导出仍需单独验收。
- `node1_full_backup.py` 与 `node1-full-history-backup.service/.timer` 准备包含 usage_logs
  的每日完整备份；原 precious 备份仍承担核心数据较短 RPO。备份对象隔离在现有 AWS 私有
  备份桶的 `node1/` 前缀，沿用该桶七天生命周期。按用户指示复用已有 AWS 账号，凭证仅放
  Node1 root 私有的备份配置文件，不进入应用容器；不修改 AWS prod 的角色、备份任务或对象。
  Node1 定时任务保持未激活，需要正式批准标记、有效备份配置、首轮上传和恢复验收后才能启用。
  备份脚本上报失败，不用“本地成功”代替离机成功。
- `node1_ops_prepare.py` 安装共享磁盘与 HTTPS/job watch 的独立配置。安装后定时器保持
  disabled，service/timer 都受同一个批准标记限制；准备期只运行不发送消息的自测。
- `node1_backup_rehearsal.py` 只在已对账的隔离候选上运行完整 dump、Node1 专属 S3 前缀
  上传/下载、SHA256 验证及独立数据库恢复，逐用户/日复核历史和核心表数量。拒绝覆盖旧演练；
  失败保留现场，不把配置/权限 canary 当成整库恢复通过。
- `node1_runtime_guard.py` 在正式启用时用于 Docker 与网关的 ExecStartPre。除了整盘 UUID，
  还检查 `/var/lib/tokenkey/postgres` 确实 bind 到候选 PG18 的 `18/docker` 数据目录；
  防止 Docker 自动重启绕过网关 guard，在缺少数据库挂载时误启动另一份空库。
  相关 drop-in 仅暂存，不在准备期安装到 Docker 或网关服务。

切流审批包必须包含以下具体事实，不以本文件的准备授权替代：

1. 固定版本与镜像摘要；候选最终 schema、正式域名配置、证书与续期方案。
2. 同步前后的逐用户/日历史对账，以及最终 accounts/proxies/密钥和运行时加密材料一致性。
3. 已实测的同步耗时、冻结期限、明确执行人，以及超时后候选保持 HOLD、源端恢复服务的步骤。
4. 源停止全部写入和 OAuth 刷新后才能导出最终凭据；候选通过对账后才允许成为唯一写入者。
5. DNS 未切且候选未写入时，可关闭候选并恢复原 AWS 服务；候选已产生业务写入后，必须先
   冻结候选并把新增账务、历史明细和最新轮换凭据回迁，再恢复 AWS，禁止仅回改 DNS。
6. Redis 重建与用户重新登录影响；旧 AWS 机器至少保留七天，保持数据和凭据回滚能力。

准备阶段禁止创建 `/etc/tokenkey/node1-prod-approved.json`。正式批准后该标记只作为本机
任务的防误启动锁；它不能替代独立审批记录、最终同步回执与单写确认。

## 2026-10-17 Tokyo prod 基线证据

主线已对齐 `origin/main`：`cd7ba80777`（含 edge 退役与代理告警变更）。运行时单独固定为
源端 `1.8.302`；再次只读核查确认应用仍健康、routing_preference 列尚不存在。主线已新增的
SQL 迁移不等于线上已应用；本轮不升级线上。最终窗口前需重新核对源版本与 schema，
若有变化先更新隔离基线和候选镜像，不以旧回执放行。
本轮迁移源是日本 Tokyo
AWS prod（`i-045f062ce33e1a5af`，`ap-northeast-1`，`3.114.187.250`），不是已冻结的
旧美东 standby。Tokyo 源只做只读核对，未停止、重启、冻结、改 DNS、改账号或刷新 OAuth。

源盘在线创建的最新快照为 `snap-05ec45d6be0e08582`（2026-10-17 07:38 UTC）。由该
快照生成的带完整 `usage_logs` dump 已上传并下载到 Node1：

`s3://tokenkey-prod-archive-682751977094/prod/node1-precutover/20261017/tokyo-current/full-with-usage.dump`

dump 大小 `1,917,322,393` 字节，SHA256 为
`9a226d905ec066e0104c1aa2e43a91cfb03cd613038613843c7173c874b79745`。隔离候选已恢复并
完成逐用户/日内容对账：`usage_logs=6,800,467` 且 usage ID 全部唯一，`usage_billing_dedup=15,506,741`，
核心表为 users 65、accounts 328、api_keys 465、groups 93、settings 320，274 个用户日
均为 `missing=0, extra=0, changed=0`。旧快照恢复中发现的 393 个重复 usage ID 已淘汰，
仅保留为审计证据，不能作为上线基线。

Node1 候选目录为 `/data/tokenkey-prod-candidate-tokyo-current`，持久化
`DATA_SYNC_HOLD`、无公网端口、network=none、自动重启关闭；生产备份、磁盘和 HTTPS
监控 timer 均保持 disabled，批准标记不存在。候选运行时尚未接管正式 Compose/Caddy，
不接收业务流量。

Tokyo 源运行时只读核对为 `1.8.302`，源容器 image ID 为
`sha256:84a1514c6fc3e422f7e85484ff43eca826bb1ebc472e02b055d67dcd566af5c4`。
Node1 使用同一发布的 **amd64** 镜像，固定本机 image ID / RepoDigest
`sha256:a973706d7dc5e63cf349c6d3067fc83e106c07c8ddc7d2faa12360cdc396069a`，
revision 与 `v1.8.302` 标签均为 `773afd8d9e713fad6d76a61c84da7770a65f6f9f`。
跨架构镜像不声称 digest 相同；应用镜像以 network=none 做了只读
启动检查，Compose blue/green 渲染到 1.8.302、Caddy 语法和本地依赖镜像检查均通过，
没有启动候选应用或修改正式 Compose。

上述数据恢复和历史对账通过，不等于最终冻结同步通过。最终冻结前仍须演练 usage 增量
覆盖新增、历史补录/旧 ID 更新、删除、尾部日志和 OAuth 凭据一致性，并测量冻结时长；
超时必须保持 HOLD 并按回滚步骤恢复 AWS 源。正式切流、DNS、源端停写、账号或凭据
变更均未执行，也未获得单独切流审批。

随后对 Tokyo 源做的只读健康检查仍为 healthy，应用容器和 PostgreSQL 均未重启；截至本轮
只读核对，源端 usage 计数已从快照水位 `6,800,467` 增长到 `6,822,897`，行数净差
`22,430`。该差值不代表全部变化行数；历史更新和删除仍须按内容对账。最终切流前必须
重新生成完整尾部 delta，不能把本次快照计数当作
当前实时水位；这部分尾部同步仍属于切流审批窗口内的操作。

### 本轮补充验收

证据目录：本机 `~/.local/share/tokenkey/node1-prod-precutover/tokyo-closeout-20261017/`，
Node1 `/root/tokenkey-node1-precutover-tokyo-20261017/`。只保留无秘密的结论到仓库。

- 历史 API：隔离应用核对 10 个用户、277 个 UTC 用户日、6,800,467 行，10 次跨租户
  请求均被拒绝。274 个 CST 用户日是完整保留范围对账，与 UTC 页面日界不同。
- 真实 UI/CSV：Playwright 登录隔离用户 38，实际日期控件选 2026-09-01，页面显示
  154 条；点击导出获取全部 154 条，与原始基线的时间、模型、token 与费用逐项一致。
  截图、CSV 和 `ui-csv-receipt.json` 已保存。此项是一个真实页面样本；全用户覆盖来自 API。
- 候选运行时：单域名 Caddy 与前端 URL 已暂存为 `api.galaxywaveai.com`，blue/green
  均固定源端同版本的 amd64 镜像摘要，数据库 mount 由 guard 的同一 ROOT 派生，指向 Tokyo 当前候选。
  渲染、语法校验和缺批准标记拒绝启动通过；尚未安装或接管正式运行时。
- 单域名 HTTPS：使用现有新域名证书，在无外网的历史副本命名空间运行 Caddy，
  `/health`、`/login` 和 `/api/v1/settings/public` 均通过证书校验并返回 200。
  此测试只证明站点路由，不等于生产登录、验证码或 GitHub 登录回调通过。
- 新域名真实流式：独立测试栈经共享 SSH dispatcher 升级到 `1.8.302` 后，使用临时
  API-key 账号，通过新 EIP 请求 `kimi-k3`；直连及当前 Tokyo 快照的 `jp-uk1`、
  `jp-uk2`、`jp-us4`、`jp-us5` 绑定逐条 HTTP 200/SSE，usage 精确归属指定测试账号。
  本轮发现旧 US 测试代理已经过时，已用当前快照的 endpoint 重测，不能沿用旧 IP 的结论。
  临时探针仅开放随机路径下的 chat 请求，普通登录/API 始终 503；测试后移除路由、
  停用测试账号和四条代理、清空认证及测试余额，保留回执，不删除回滚数据。
- 运维：HTTPS watch 已改为新域名，缺批准标记拒绝运行的验证通过。四类备份/监控
  timer 均保持 disabled，未发送外部通知；历史 UI 演练应用、Redis 和私有桥接已停止。
- Tokyo 完整离机恢复：1,917,328,417 字节完整备份已上传 S3 的 Node1 独立前缀、
  下载并验证 SHA256 `e77c307fe1357bb92b07397edb8f62110c8556826c21f365524738acea0bb48b`，
  再恢复到独立数据库。6,800,467 行、10 个用户、274 个 CST 用户日内容零差异，七张
  核心表计数一致；往返含 dump、传输、恢复和对账为 2,257.14 秒，不是最终冻结耗时。
- 合成故障演练：损坏包、内容不符、基线过期和分区漂移均拒绝；在 DELETE/COPY/序列
  修改后、COMMIT 前注入真实数据库超时，行与序列完整回滚。源端冻结/解冻没有执行，
  不能用此项声称整个跨云切流失败恢复已通过。
- OAuth：用户确认暂无专用测试账号，记录为**业务验收缺口**。不在 Node1 启用或刷新
  AWS prod 正在使用的同一份 OAuth 凭据；API-key 供应测试不能替代该缺口。
- `node1_delta_rehearsal.py` 只可写入完整离机恢复后的独立数据库，演练历史修订、删除、
  多余行及账号/密钥/代理差异修复；要求先通过完整恢复回执。其同机耗时不能作为跨云
  传输或最终冻结时限的通过证据。

### 新域名登录与客户端交接

基线 `settings.frontend_url` 仍为 `https://callmodel.io`，`api_base_url` 仍为
`https://api.callmodel.io`；`GetFrontendURL` 数据库优先，单改 `SERVER_FRONTEND_URL`
不能修正邀请/邮件链接。已准备 `final-runtime-pending/domain-settings.pending.json`，
仅在最终源数据对账后、独立切流批准下应用下列有意差异并复核：

| 设置 | 候选目标 |
|---|---|
| `frontend_url` | `https://api.galaxywaveai.com` |
| `api_base_url` | `https://api.galaxywaveai.com` |
| `github_oauth_redirect_url` | `https://api.galaxywaveai.com/api/v1/auth/oauth/github/callback` |

GitHub 登录在基线已启用，回调仍是旧 API 域名；需在第三方应用注册新回调。准备期不能
修改现有 GitHub 应用而破坏 AWS 登录，可单独准备新应用，或把旧应用改回调留到审批窗口。
前端回调 `/auth/oauth/callback` 是相对路径，保留。Turnstile 已启用，需核实 widget
允许 `api.galaxywaveai.com`，新增时保留既有域名；未获得第三方配置证据前，登录验收有缺口。
Google 登录与 Passkey 在该基线未启用；不擅自开启、关闭或绕过现有验证机制。
用户确认由运营配置第三方服务，具体填写值与验收清单见
[新域名登录配置交接](../ops/node1-domain-login-handoff.md)。

新域名当前已指向测试入口，正式切流的关键动作是开放新入口、接入唯一写入者及客户端
Base URL/后台书签迁移，而非单纯修改 DNS。切流窗口须停止旧入口的新请求、排空在途请求
和 OAuth 刷新，再做最终同步；未改地址的旧客户端不能继续向 AWS 写入。准备期保留旧域名
现状，不添加重定向或代理。Redis 不迁移，用户在新域名重新登录；已签发旧域名邮件链接和
第三方回调纳入交接。新端发生写入后，回滚需要回迁数据与凭据，不能只撤回新域名入口。

## 2026-10-16 已取得的历史证据

运行证据在本机私有目录 `~/.local/share/tokenkey/node1-prod-precutover/aws-preparation-20261016/`
及 Node1 `/root/tokenkey-node1-precutover-aws/`；含秘密文件不进入仓库。

| 检查 | 实测结果与边界 |
|---|---|
| main 与空测试栈 | `b439285ac` / 1.8.300；共享 SSH prod dispatcher 升级和健康检查通过，仅空测试栈蓝绿，不是生产切流 |
| AWS 快照完整恢复 | 数据快照 `snap-07ab3efff07097efb`，2026-10-16 03:16:07 UTC（11:16 CST）；完整 dump 1,894,545,165 字节，SHA256 校验通过 |
| 历史 usage logs | 6,705,566 行、10 个有历史记录的用户、267 个用户日；逐用户/日数量、token、费用及行内容指纹全部一致，差异为零 |
| 历史查询 API | v1.8.300 隔离应用的 10 个用户、270 个 UTC 用户日逐项通过，共 6,705,566 行；10 次跨租户明细请求均被拒绝。现网公开设置确认业务时区 UTC，不能把兼容保留但忽略的 `timezone` 查询参数当作日界 owner；CST 保留范围对账与 UTC 页面日界分开核验 |
| 核心表 | users 65、accounts 325、api_keys 465、groups 93、settings 320、usage_billing_dedup 15,411,840，与同一快照源一致 |
| 网络 | 独立流量 EIP 106.75.7.50、300 Mbps；私有隧道 HTTPS 200；隧道重启后恢复健康；没有 EIP 压测 |
| HTTPS 与流式 | 四个正式域名使用现有有效证书，私有路径 TLS 校验、健康、站点、法律页面通过；受控 SSE 四段按秒抵达，PROXY v2 正确传递地址并覆盖伪造 HTTP 地址头；未替代真实模型验收 |
| 真实模型 | 在 Node1 空测试栈使用临时 newapi API-key 账号探测 `kimi-k3`：直连及 uk1/uk2/us4/us5 四条代理均 200，后四次为流式，usage 精确归属指定测试账号。没有启用/刷新复制的生产 OAuth；不据此声称各 OAuth 平台都通过业务请求验收 |
| 运维任务 | 沿用已有 AWS 账号；加密对象上传/下载 canary 通过；precious、完整历史备份、磁盘和 HTTPS/job timer 均保持 disabled；批准标记缺失时启动被拦截，没有发送外部通知 |
| 完整离机恢复 | 从 Node1 未修改的候选基线生成 1,894,605,800 字节完整备份，上传现有 S3 桶的独立 rehearsal 前缀、下载并核对 SHA256 后，恢复到独立数据库；6,705,566 行用量、267 个 CST 用户日的内容指纹及七张核心表计数全部一致。往返含恢复与对账约 2,184 秒；不代表最终冻结同步时长，定时任务仍未启用 |
| PG18 接管 | 独立合成集群从 `18/docker` 接到共享 Compose 的 PGDATA 路径，32 行及校验和一致，重启保留；正式候选接管仍未执行 |
| 同步扫描耗时 | 独立 AWS 快照 reader 上 precious 109 表约 78.57 秒、usage 76 表约 124.08 秒；仅指纹扫描，不含导出、跨云传输、Node1 应用与最后对账，不能承诺完整停服时长 |
| 真实历史增量 | 只在独立快照 reader 修改 9 月 5 日的一行旧用量；导出包含全量历史指纹和一个变化分区，约 3.58 MB / 124.30 秒。Node1 在一次性历史库核验全部基线、应用和内容/外键校验约 175.53 秒；恢复该测试改动并再次验证原始内容后共 285.53 秒，正式候选基线未写入 |
| 测试收尾 | 临时供应账号 inactive、schedulable=false，API key 已清空；四条测试代理停用并清空认证；共享探针 key 全部停用，测试余额归零，管理隧道关闭。独立 AWS reader、两块克隆卷与专用安全组已清理；当前 AWS prod、源卷与原始快照保留 |

2026-10-16 10:01 的例行 precious 备份没有 usage_logs 数据；本次上述恢复使用另行导出的
完整快照备份，不能混用两者。此后新增记录、历史回填/修改与 OAuth 轮换必须在最终同步包含。
本表为 10 月 16 日历史记录；当前域名与 UI/CSV 状态以本轮补充验收为准。
新域名真实模型公网流式请求已由本轮补充验收覆盖；证书首次自动签发已通过，实际续期
周期尚未发生。全链路最终同步时限与失败恢复仍需补齐；
各 OAuth 平台业务请求保留为用户已知的验收缺口。
**以上不是“所有切流前工作完成”或切流授权。**

## 新域名公网健康入口验收

运营配置的 `api.galaxywaveai.com` 已由权威 DNS 与公共解析器确认指向新 EIP
`106.75.7.50`。独立 UCloud HAProxy 的 SNI/Host 白名单增加该域名，Node1 Caddy
通过 TLS-ALPN-01 自动取得 Let's Encrypt 证书（有效至 2027-01-15 UTC）。
现有配置原件与回滚材料保存在各主机及本机私有运维目录 `galaxywave-ingress/`。

公网实测 `/health` 连续五次 HTTPS 200，证书验证开启，完整请求约 58–118 ms；
HTTP 返回 308 跳转 HTTPS。该路径穿过 UCloud、WireGuard 和 Node1，连接的是独立测试栈。
根路径、模型列表和登录接口均验证为 503，未开放业务流量、历史演练页面或候选库。
批准标记仍不存在，候选 HOLD 与隔离校验通过，生产备份及监控 timer 仍 disabled。

此证据仅证明新域名公网健康路径与首次自动签证成功，不代表旧正式域名可用、真实模型
流式请求已通过该公网路径、实际续期周期通过或已获切流授权。现有 AWS prod 和正式 DNS
未修改。新域名配置目前是测试入口的宿主配置；后续重新生成 Caddy 配置前须保留该入口，
正式运行时已按用户决定暂存为该单域名，开放业务仍需纳入独立切流审批包。
