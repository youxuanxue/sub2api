---
title: prod 应用机并行替换，取消原地 LUKS 搬盘
status: approved
approved_by: "feng（2026-11-11 会话：同意取消原地搬盘、改为新机并行替换；不中断请求、验证不过不下线旧机；允许过渡期部分 QA 缺口）"
approved_at: 2026-11-11
risk: high
---

# prod 应用机并行替换

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
