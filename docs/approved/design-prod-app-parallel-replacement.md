---
title: prod 应用机并行替换，取消原地 LUKS 搬盘
status: approved
approved_by: "feng（2026-11-11 会话：同意取消原地搬盘、改为新机并行替换；不中断请求、验证不过不下线旧机；允许过渡期部分 QA 缺口；后续同意保留双应用节点与摘机演练方向，并明确同意 QA 集中管理方案；入口选型及预算待确认）"
approved_at: 2026-11-11
risk: high
---

# prod 应用机并行替换

## 2026-11-11 后续决定：保留双应用节点

用户在本会话同意以 #3155 为基础继续应用层 HA：保留新旧应用机，共享现有数据机，
先验收动态摘旧机，再验证节点故障恢复。本节取代下文实施步骤 8-10 中的
“最终单应用机、DNS 直接迁到新机、旧归档 owner 交给新机”终态；下文候选准备证据仍有效。
旧机不退役、不迁移 LUKS；cn-prod 与数据服务不重启、不迁移。

因此，#3155 的合并边界仅是候选机和单节点蓝绿运行时的准备，不是双节点 HA 或正式切流实现。
在独立入口、跨节点摘流编排和故障演练完成前，不得把下文历史性的 DNS 迁移步骤当作本 PR
的可执行验收，也不得以合并本 PR 代替入口选型或生产切流审批。

### 已同意的方向与未授权的操作

- 两应用节点前需要独立高可用入口；双 A 记录或其中一台上的单入口代理不作为 HA 验收。
- 优雅摘流不截断已有请求，验证失败不停止旧机。突然崩溃会影响该机在途连接，
  故障恢复验收不声称在途 SSE 可迁移，也不盲目重放已经进入上游的计费请求。
- 范围限于应用节点故障与维护；共享 PostgreSQL/Redis 数据机、同可用区故障不在本阶段覆盖内。
- 已授权继续设计和实现准备，不等同于批准创建任意付费入口、调整网络边界、正式切流、
  硬故障注入、合并或强制推送。入口接入方案、费用和变更清单核实后再申请实际执行。
- 应用成本变为两台长期叠加，不能继续声称退役旧机可节省 260 元/月。
  月费与实例身份沿用舰队入口；负载均衡、连接网络及公网流量须单独核价。

### 入口可行性只读核验

2026-11-11 腾讯云 API 核验结果：

| 查询 | 结果 / RequestId |
| --- | --- |
| 美西 `DescribeLoadBalancers` | `TotalCount=0`；`b5b3a809-bd60-4958-8a30-8507151b140a` |
| 美西 `DescribeResources` | 返回 IPv4 BGP 可用规格；`13b12085-9a22-40de-8105-163e9b77d6a4` |
| 美西 `DescribeCrossTargets` | `TotalCount=0`；`f5c2d0e5-1e0e-4114-aef5-e617049e3745` |
| Lighthouse `DescribeCcnAttachedInstances` | 关联集为空；`17a6a648-5d4b-4f3d-b861-b4aa9a3e98ce` |

Lighthouse `DescribeInstances` 同时确认两应用候选均 RUNNING、均在 na-siliconvalley-2。
仅查询资源与库存，没有创建 CLB、关联网络或改变后端权重。
CLB 的 `RegisterTargets` API 支持 CVM ID 或内网 IP，不代表 Lighthouse ID 可直接作为 CVM ID。
仍须核实 Lighthouse 到目标 VPC 的路由、回程与安全规则、IP 后端接入资格；
不能把“CLB 地域有资源”当成“这两台主机已可绑定”。

同日 `InquiryPriceCreateLoadBalancer` 对公网 IPv4、按量实例、按流量计费、200 Mbps
上限、未指定性能容量型规格的询价返回：实例 0.168 CNY/小时，流量折后 0.3247 CNY/GB，
`LcuPrice=null`；RequestId `fe94280a-a08e-4f5e-b7e8-877201972725`。
按 30 天估算仅实例约 120.96 CNY，另计实际流量；不是完整报价或已批准预算。
该流量费用不能套用 Lighthouse 套餐的无限流量；其它规格、连接网络和计费模式仍需核价。

入口选型还须证明：逐请求而非仅逐 TCP 连接的摘流语义、已有连接保留、健康检查与恢复抖动控制、
最长流与长上传的超时、真实客户端 IP 的可信来源、TLS/域名续期、日志与带宽计费。
若四层负载均衡按连接分发，权重归零不必然停止已有 keepalive 连接上的新请求；
必须实测并选择能满足排空契约的实现，不能直接用权重归零代替验收。

### 低成本入口候选（未选定、未购买）

2026-11-11 读取 Cloudflare 官方 Network & CDN Markdown 价目页，Load Balancing
附加服务标价为每月 5 USD 起；这只是起价，不是本账户、全部域名、探测与实际用量的总报价。
来源：`https://www.cloudflare.com/plans/network-cdn.md`。
可继续评估代理模式的独立入口；DNS-only 模式仍受 DNS 缓存影响，不能替代逐请求摘流验收。

同日官方 connection limits 文档（页面更新时间 2026-10-22）列出回源 Proxy Read Timeout
为 125 秒，只有 Enterprise zone 可配置；Proxy Write Timeout 为 30 秒。
来源：`https://developers.cloudflare.com/fundamentals/reference/connection-limits/index.md`。
这些不是“所有 SSE 最长只能运行 125 秒”的结论；仍须分别验证首字节、流中静默、持续长流、
非流式长请求和长上传，以及上传大小、API 不缓存、TLS 与真实客户端 IP 信任边界。
当前 Caddy 的客户端 IP 处理也须适配可信代理，不能直接透传并信任任意客户端提交的头。

下一道入口决策依据是账户实际报价与上述兼容性证据；在此之前不购买、不改正式 DNS，
也不把两台应用上的单 Caddy 或双 A 记录作为更便宜的等价 HA 方案。

### 实现与验收顺序

1. 选定入口接入方式与预算，在改正式 DNS 前验证全部正式域名和兼容入口。
   旧 DNS 缓存、直连旧 IP 与旧连接仍可能存在，不停止任何仍有流量的节点。
2. 多节点发布复用现有单机蓝绿 owner，在外层增加跨节点互斥与逐节点编排：
   检查另一节点可独立承载，再摘流、排空、更新、验证、重新加入。
   同一时间只处理一台；失败保留另一台服务，不自动扩大操作范围。
3. 双节点验收共享登录/OAuth session、账号凭据刷新锁、认证缓存失效、并发/RPM 限制、
   调度租约、后台单例任务以及媒体任务/文件可见性。已有 Redis/数据库协调代码不是线上验收。
4. 排空同时检查入口不再分配新请求、全部本机活动颜色的 HTTP inflight 为零，
   并等待异步 usage/计费工作完成与落库。进程内队列不能因 inflight 为零就视为已持久化；
   突发进程丢失下的计量恢复能力另行验证，不将应用 HA 等同于计量零丢失。
5. 新机小比例接流后，摘旧机但不停止旧机。用长流、keepalive 和计量证据验证没有主动截断，
   再验证新机独立承载完整业务峰值及资源余量；随后重新加入旧机并反向演练。
6. 硬故障先用隔离测试流量或已排空节点演练，记录探测窗口、恢复时间、在途断流和租约回收。
   已排空节点故障不证明带载崩溃无影响；对真实用户带载注入仍需独立授权。

### QA 集中管理方案（用户已同意，尚未执行）

用户在本会话明确回复“同意 QA 方案”，批准本节架构与实现方向；线上安装、owner 交接与删除操作
仍须通过实现、测试、review 和执行门禁，不把架构同意记为线上迁移已完成。

数据库备份和 WAL 已在数据机运行，不重复搬迁。将 QA 分区预建、共享数据库分区清理
与归档调度集中为数据机上的唯一协调 owner；各应用只做异步 capture 和本地短期缓冲。
大文件从各应用机直接归档到 node1，不在数据机增加 raw 中转副本，不把应用 QA 目录挂载成
数据机 NFS，也不引入同步远端写文件路径。QA 传输/校验限速、限内存并避让数据库备份。

集中执行本身不会修复现有 `site/hour` 目标覆盖：源数据仍分布在两应用机。
归档与 receipt 必须绑定站点、稳定的 capture source 身份、UTC 小时及内容摘要；
同小时各源隔离发布，重试不得覆盖已校验的其它源。稳定 source 身份不复用蓝/绿颜色或镜像版本。
旧布局只读兼容，不把旧单源 receipt 冒认为所有节点均已归档。

清理分为两个作用域：共享数据库分区只由协调 owner 统一处理；每台应用机的热文件按各自源回执
独立清理。保留既有数据库锁与 receipt 硬门禁，小时参与源集合须覆盖该小时已接流、摘流及故障节点，
不能仅看当前在线列表，也不能因一台节点 archive 成功就 DROP 全站该小时。
节点故障、回执缺失或迟到文件须保留明确未完成状态；源完成须确认 capture 队列封口，不能只看墙钟跨小时。
每节点清理完成单独记录，不能复用一个全站 `hot_files_cleaned_at` 声称所有磁盘均已回收。

用户接受部分 QA 缺失可用于显式 gap，但缺失不得伪造 archive 成功或放宽计费保护。
已有 receipt 硬门禁不因迁移位置被静默取消；故障节点缺口的过期回收策略仍须在 apply 前明确，
本次架构同意不授权绕过门禁强制清理。
数据机协调任务失败只暂停 QA 维护并告警，不阻塞网关请求，不自动停业务或删除唯一副本。
本节尚未安装新 timer、复制 raw、停用旧 owner 或删除分区/文件。

## 范围与授权

本方案取代 PR #3148 的 prod 原地 LUKS 搬盘维护窗口。北京时间 2026-11-11
21:45 预复制与 22:00 停机迁移 timer 已停止；不得恢复或执行原搬盘 apply。

本站应用替换，新机普通 ext4 根盘；数据机 PostgreSQL/Redis 不搬迁、不重启。
新机与旧机并行，验证失败保持旧机服务。不得为了完成窗口截断流、停止旧 Caddy 或重启旧 Docker。
cn-prod 不在本次变更内。旧主机、LUKS 文件、密钥和本地历史 QA 保留；退役销毁不在本次 apply 内。

用户已接受过渡期部分 QA 数据缺失。该例外不涵盖计量、计费、会话、账户配置或已提交请求。
不伪造归档 receipt，不把遗漏标成已恢复，不删除未知旧数据。

## 目标与预算

腾讯美西 na-siliconvalley-2，Ubuntu 24.04，2 vCPU / 8 GB / 80 GB SSD，
锐驰套餐 `bundle_rs_nmc_lin_med8_01`，云 API 2026-11-11 报价 95 CNY/月，
200 Mbps 峰值、无限流量。月付一台，按现有生产策略自动续费。
旧应用机继续付费直至另行批准退役。除此之外不创建数据库、负载均衡或其它付费资源。

新机 Docker 使用普通根盘 `/var/lib/docker`，业务 `/var/lib/tokenkey/.env`、
QA `/var/lib/tokenkey/app`、Caddy `/var/lib/tokenkey/caddy`。
固定根盘 UUID 与既有私有 `.env` 标记作为启动 guard；不接受任意空目录。
网关、Caddy、数据面客户端命名与既有 Stage0 探针相同。

## 实施与回退

1. 在源主机明确记录维护窗取消；核对迁移 service 未运行、timer 无下次触发、生产健康正常。
2. 修改共享发版 owner 支持显式 `tencent-root` 与固定根盘 UUID，默认旧 LUKS guard 不变。
   同步 resolver 输出与 systemd guard；完成正负向测试、preflight 后创建新机。
3. 使用幂等 ClientToken 创建一台；记录实例与控制面身份，先关闭公网 SSH，只开放生产 HTTPS。
   以 TAT bootstrap、SSM Hybrid 日常管理，不复用旧 SSM 身份。
4. 保留旧来源规则，追加新机的 DB/Redis 私网和出口代理来源；验证旧、新两条路径。
   生产 secret 加密传输，私有落盘，不打印环境、密码、token 或 TLS 私钥。
5. 启动与源相同 digest 的应用，沿用共享 DB/Redis、加密密钥和认证 secret。
   新机每色内存硬上限 3GB、GOMEMLIMIT 2GiB，DB pool 25，Redis pool 96；两机两色上限加运维余量须小于 200。
   旧机未调整前，按两色 100 + 新机两色 50 = 150 预留空间。
   缓存/插件是本机缓存；检查所有配置 bind 与异步任务，不克隆数据服务。
6. 新机暂设 `QA_CAPTURE_ENABLED=false`，不装归档 timer；旧机继续采集/归档。
   该阶段新机承接的请求有明确 QA 缺口，不影响 usage 写入与计费。
7. 用 `curl --resolve` 检查全部正式域名 TLS、网页、机器端点与真实授权请求；
   验证计量入账、共享 session、出口可用、错误归属、连接池及并发调度。
   测量新旧入口流式请求完整性；不得仅凭 `/health` 通过接流。
8. DNS 分阶段迁移全部仍指向旧机的正式域名及兼容别名，TTL 300；
   旧 DNS 缓存、直连旧 IP 和已有流继续走旧机。新旧应用同时工作，不向旧机发 drain 信号。
   出现新机问题先恢复 DNS 并保持两机在线；不得停止可能仍有缓存流量的新机。
9. 验证通过后同窗更新舰队身份、resolver、watcher、CI/SSM 权限、发版布局、告警、恢复入口。
   旧机作为兼容入口保留；退役至少满足七天保留、无旧入口业务流量、无在途请求及独立授权。
10. QA 只允许一个归档 owner：旧 timer 结束后停用，再在新机下一次蓝绿中恢复 capture 与本地
    maintenance/归档。交接小时可缺失；不并行写同一 node1 目标，不覆盖旧历史。
    恢复 owner、时间范围和仍未归档的旧文件都要记录。QA 例外是过渡许可，不是长期关闭。

## 验收

- 迁移期间没有因主动停止旧机或排空上限而截断用户请求；失败不下线旧机。
- 新入口相同 TLS 域名、认证与分组，真实流正常结束且 usage/扣费一致；不重复执行请求作“重试验证”。
- 两个入口都可达；备份与 WAL 在原数据机持续推进；旧数据库容器未被启动。
- 新机重启恢复在接流前验证；切流结束可以正常使用 deploy-stage0，不留另一次停机布局整改。
- QA 缺口明确记录，正式归档 owner 恢复；旧机与旧卷仍在，销毁未执行。

## 实际证据

当前为 **prepare，尚未切流**。默认 resolver、DNS 和归档 owner 仍指向旧应用机；
本 PR 只准备兼容部署代码与候选，不把合并 PR 等同于完成迁移。

| 项 | 回执 / 结果 |
|---|---|
| 取消原地搬盘 | SSM `294c9981-f8de-4005-8c9f-cd02795e535f`；旧 schedule 标记 cancelled，21:45/22:00 timer inactive，LUKS 未改 |
| 创建候选 | Tencent request `0e75135e-eb4a-4408-bd45-9b8cd6d79efe`，ClientToken `tokenkey-usw-app-parallel-20261111`；身份见候选矩阵与舰队入口 |
| 数据 ACL | SSM `0f6994f3-a2e1-4538-98a9-d443f2ae689f`；原来源保留，追加候选来源，无数据服务重启 |
| 新应用初始化 | SSM `06b89f62-577d-4c19-b4c5-b3a36d04d62f`；普通根盘、共享 PG/Redis、BBR、五个域名定向 TLS/health 成功 |
| 镜像一致 | `1.8.361`，repo digest `sha256:d9097d8384c42f16ac315565778e6e4b66d2dd2e7a4bb006f2f98ae0136716c6`；amd64，revision `896d6221c9d26f914339a3701a17cf06853069f5`。Docker image-store 不同，不跨主机比较 `.Id` |
| 出口代理 | SSM `f023d544-0979-4c5b-8e86-39beefff53fb`；16 条活配置均认证连接 HTTP 200。国内八台代理 PID 不变，旧来源规则保留 |
| 真实流 | SSM `a70ce3a7-8df4-4727-8be4-5f85ccc678fa`；新入口 HTTP 200、`response.completed`，耗时 1888ms。脚本随后因遗漏 usage ID 的 `local:` 前缀而失败，不能记为整段脚本成功 |
| 入账复核 | SSM `bbfe3ec2-d42d-4571-b469-dff0647ed429`；同一请求 `local:ed815656-43bb-4727-9d53-383ba9bf186a` 恰一条 usage，account 238/key 339，23 input / 6 output tokens，total/actual cost 均 0.00053；测试 key 已 disabled。未重放付费请求 |
| 系统补丁 | SSM `825216f2-c992-4513-acf7-a76ab4bc67aa`；仅候选升级。自动升级 timer 保持 masked |
| 首次重启 | SSM `b59faccc-8f43-471e-b01e-53e85111b1a8`：TLS/health/数据连接恢复，但 tokenkey.service failed，不能判通过。`22415077-ce6c-437e-a268-bee8408fc4bc` 定位到控制别名检查先于应用 ready |
| 修复后重启 | SSM `4fe9fddc-74f7-440b-8446-df73607ee43a` 安装共享 owner 的启动等待；`667f1651-fdca-419e-ac59-a9ce77593348` 确认等待成功、systemd 完成；`6dd658dc-f73c-46e8-b2a4-d7853e404000` 确认两个 unit active/Result=success，五域 TLS、控制别名和 PG/Redis 认证全部通过 |
| QA 既有缺口 | SSM `670db7aa-73d2-4e03-b08f-6e693e7565fe`：旧机仍是唯一归档 owner，但北京时间 21:00 任务在 21:30 timeout；timer 下次 2026-11-12 01:00。不能把 timer active 当作归档成功；新候选未启动归档，不存在本次双写 |

部署 owner 为 `ops/stage0/deploy_via_ssm_bluegreen.sh`：根盘契约只对精确候选 SSM 身份生效，
旧 LUKS 默认不变；根盘实例的控制别名启动等待也由该 owner 安装，不依赖临时脚本长期存在。
数据客户端复用 `ops/stage0/install-dataplane-client.sh` 的仅客户端契约，不启动数据服务。
启动目录和明文 `.env` 必须 root 私有；一次性传输采用候选端独有 RSA recipient + CMS AES256，
只经控制面转运密文。一次性包、recipient key/cert 和候选容器环境快照已按精确文件清理；
原 `.env`、数据客户端秘密、TLS 和 QA 历史未动。后续 QA SSH 材料交接需重新生成 recipient，不能复用已清理材料。

当前仅验证了一个有界真实流，不是吞吐量或延迟基准。2C8G 的性能/成本收益仍须经切流后
实际 CPU、RSS、队列/连接池饱和度、平台错误和延迟观察；保留旧机承担回退，不因单次 1888ms 就宣称性能等同。

### 切流门禁（未完成）

- 修复后的候选重启已通过；继续切流仍需以下门禁。
- 当前实现 preflight/review、PR 与独立人工合并确认；主线具备根盘部署契约后才允许切流。
- 精确新 SSM ARN 追加到 CI OIDC inline policy，保留旧应用、数据与 cn-prod 权限并验证。
- 宿主 watcher、disk metrics、prune 安装与验收；不得用暂停声明掩盖未安装任务。
- QA binary/SSH 材料安全交接；旧归档任务空闲后停用 timer，再启动新 singleton owner。
  新 capture 恢复、交接小时缺口及旧机留存文件明确记账；不能长期关闭 capture。
- 分阶段 DNS、默认 resolver/舰队 SSOT 同窗更新，观察真实流量、usage 和平台错误。
  DNS 回滚不停止任一可能仍有缓存连接的主机。旧机至少保留七天，销毁另行审批。
