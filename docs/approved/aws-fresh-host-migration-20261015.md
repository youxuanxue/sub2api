---
title: 全新 AWS 主机迁移与 uk2 首台切流
status: approved
approved_by: "feng (2026-10-15 会话明确批准推送、合并并执行 uk2 单节点切流)"
approved_at: 2026-10-15
created: 2026-10-15
owners: [tk-platform]
scope: fresh AWS recovery, precutover preparation, and approved uk2-only cutover
---

# Hcloud → 全新 AWS 节点迁移

迁移准备已获用户授权；用户现已授权先选低流量 edge 正式切流，并明确批准相关代码推送和合并。本轮仅 uk2；prod 与其余 edge 继续暂停切流。候选身份唯一清单为
`deploy/aws/fresh-migration-20261015.json`，现网仍以 `deploy/hetzner/README.md` 为准。

**最新状态（2026-10-15）：uk2 已切换全新 AWS；prod、uk1、us4、us5 仍在 Hcloud。**
下文预同步、hold 与待执行描述为准备阶段记录，仅未切流四台继续适用。

## 授权与停止线

- prod 使用独立新 EC2 c7g.xlarge；四边均为新 Lightsail xlarge_3_0。
  AWS 实查该套餐为 4 vCPU / 16 GiB、84 USD/月/台。
- 可以只读导出源端、隔离恢复候选、预同步、验证基本服务与备份；不压测。
- 用户查看客户流量并明确选择窗口前，不停源端写入，不改 DNS、线上路由、SSM 默认指针，
  不开放候选应用出口或启动候选业务写入者。
- prod 约 11 GiB 的历史 QA raw 文件不搬、不删除；新机未来的 QA capture/archive 不因此退役。
- 旧节点用于历史回查，不作为接回生产写入的回滚目标。历史归档允许使用既有 400 天桶的
  独立前缀，用户决定清理；禁止从归档旧节点整库覆盖当前 Hcloud。

## 已完成的隔离与身份核查

五台新机已创建，网关镜像与源端核验版本为 `1.8.292`，不是 `1.8.192`。
现网域名仍指向 Hcloud。prod 的运行配置包含 `tokenkey.dev`、`api.tokenkey.dev`、
`callmodel.io`、`api.callmodel.io`；四个域名均须随 prod 迁移，不能只改 API 域名。

本轮定位了 prod 候选自动启动的真实原因：`tokenkey-pgdump.service` 的
`Requires=tokenkey.service` 在 06:00 拉起完整栈；原 isolation 启动脚本仍会启动网关。
已对五台候选应用 `ops/migration/hold-fresh-candidate.sh`：

- 持久 `DATA_SYNC_HOLD` 文件与 systemd 条件阻止栈启动；实际 `systemctl start` 被跳过。
- 候选定时备份暂停，栈开机自启禁用，非 PG 容器 restart policy 设为 no；仅 PG 运行。
- 应用 Docker 网络仍为 internal，未对源端服务做对应操作。
- 原 unit 状态和 restart policy 保存在候选 `/var/lib/tokenkey/migration/precutover-hold-*`。
  `migration-hold-stop.conf` 暂时清空 ExecStop，以便停止 oneshot 状态而不停止 PG。
  正式激活时须恢复常规 stop/start 行为和备份定时器，不可直接遗留这些临时覆盖。

新 prod 的 OIDC CFN TargetInstanceId 已是 `i-022f2553503211775`，栈为 UPDATE_COMPLETE。
本轮移除了新 prod 的临时 SSH transfer 安全组附件；SSM 仍 Online。四边的临时 22 入站也已关闭，
保留 Lightsail 基线的 443、8443/TCP 与 34567/UDP。没有增加本地长期 IAM 权限。

## 数据同步实现与实测

`standard_freeze_delta.py` 仍只是计划/SQL 片段生成器，**不是完整停写控制器**。
此前“15 分钟硬超时已实现”“自动整表回退”“count/max 能证明完整内容”的表述不成立。
其输出已明确标注没有运行时 deadline/自动 fallback，错误的 pg_dump `--table-data` 已修正；
整表重载片段不再提前 COMMIT。

实际演练使用 `fresh_host_delta.py` 的显式范围：

| scope | 范围 | 用途 |
|---|---|---|
| `oltp` | 全部非日志、非 QA、非 dedup 的 public 普通表 | 首轮业务表刷新 |
| `precious` | OLTP 加热、归档两张 dedup 表 | 业务状态与幂等账本同快照对账 |
| `logs` | usage、ops_error、ops_system 日志及其普通分区 | 日志尾差，保留历史回查 |
| `all` | 全部 public 普通表 | 仅离线审计；禁止用于 prod 正式补数 |

演练流程为候选基线 → 源端 repeatable-read 只读快照 → 范围内内容对比 → 导出差异 →
既有私有加密 S3 中转 → 候选基线复核 → 单事务替换差异、内容/外键校验与序列恢复。
整表无差异由内容指纹证明，不以相同行数跳过余额或配置变更。dedup 按 ID 区段比较**全部**
既有内容，所以可处理低 ID 晚提交、修改、删除及热表向 archive 的迁移，不依赖 append-only 假设。

结构门使用范围内 pg_dump schema（含相关分区父表），不是仅 information_schema 列 hash。
行内容指纹为逐行 SHA-256 的四组 64-bit 数值和及计数；压缩传输文件单独校验 SHA-256。
候选仅 PG 运行、身份/内部网络/持久 hold 必须通过。主机非阻塞 flock 覆盖基线复核至事务提交，拒绝并发补数；恢复前锁定范围内表，COPY 与校验、
`ALTER SEQUENCE RESTART` 在同一事务中；失败不会留下半套业务数据。
外键校验包含恢复行、继承分区外键以及被保留日志对子表外父键的引用。

单 SQL 设置 120 秒 statement_timeout，候选锁等待 5 秒，快照/恢复事务最多 300 秒；
这些是数据操作保护，不是完整停服时间的承诺。源端查询限制为不启用并行 gather。
OLTP 范围限制 1 GiB，其余显式范围限制 8 GiB；超过需重新评估，不能自动扩大。
正式停写后仍要重新导出、对账；所有当前回执的 `final_freeze_verified` 都是 false。

曾发现大表指纹 SQL 被优化器展开为多次 SHA/JSON 计算，并排序原始行；投影屏障修复后，
候选约 1531 万 dedup 行的实测从触发 120 秒超时降到 63.2 秒。未在源端重复运行失败版本。

### prod 实测回执

| 演练 | 变化范围 | 压缩数据 | 导出 | 恢复及校验 |
|---|---|---:|---:|---:|
| OLTP | 107 表中 23 表变化 | 15.3 MiB | 28.55 s | 42.72 s |
| precious | 109 表，20 个表/区段变化 | 19.7 MiB | 121.61 s | 169.79 s |
| logs | 206 表/分区中 3 个变化 | 8.0 MiB | 49.42 s | 35.05 s |

时间不含 SSM 编排等待。precious 与 logs 的导出+恢复合计约 6.3 分钟，尚不含排空、
五节点协调、入口变更和基本真实请求验证；不能继续承诺完整停服只有 3–5 分钟。
候选基线的提前计算可在停写前完成，但 apply 内的基线检查不会因此跳过。

S3 中转沿用角色已有的 `<prod|edge/id>/pgdump/*` 权限，路径为
`s3://tokenkey-prod-pgdump-682751977094/<prod|edge/id>/pgdump/migration-pre-sync/aws-20261015/`。
prod 批次 `oltp-prod-20261015T0723`、`precious-prod-20261015T074216Z`、
`logs-prod-20261015T075735Z`。先前两轮 dedup-only 预同步的 25199/1159 行仅是历史增量回执，
不再作为完整数据一致性的依据。

### 五节点预同步已完成

业务状态/幂等账本和 usage/ops 日志均已恢复并通过内容校验。可复核回执见
[`aws-fresh-presync-20261015.json`](../ops/aws-fresh-presync-20261015.json)。
下表为每节点 precious + logs 两次同步的合计，不含提前基线和 SSM 等待：

| 节点 | 压缩传输量 | 导出 + 恢复/校验 |
|---|---:|---:|
| prod | 27.8 MiB | 375.9 s |
| us4 | 13.9 MiB | 183.4 s |
| us5 | 10.4 MiB | 161.6 s |
| uk1 | 19.6 MiB | 128.8 s |
| uk2 | 21.7 MiB | 156.4 s |

这些是源端在线写入时的预同步实测，不能替代窗口内停写后的最终补数，不能相加当作承诺停服时间。

## 切流入口与控制面准备

`prepare-aws-traffic-transition.py` 从双方实际 Caddyfile 生成带输入 SHA 的四组候选文件：
源端维护响应、源端旧地址转发、新机过渡信任、过渡结束后的新机正式配置。
保持所有正式域名与现有静态/跳转规则。过渡阶段只信任对应旧机的单个 IP；边缘 relay 仅允许
新 prod 与对应旧边代理。所有文件只在准备目录，没有替换现网 Caddyfile。

四组共 20 个配置已在隔离容器内通过 Caddy adapt；候选两组额外通过共享健康策略检查。
候选代理片段从共享 `Caddyfile.edge` 生成，去掉原运行配置中的被动冷却漂移；额外两次隔离 HTTP 请求验证可信代理的真实客户端 IP
可以保留，非可信来源的伪造 X-Forwarded-For 被忽略。未做压力测试。
权威 DNS 实查 prod API/apex TTL=300 秒，四边=600 秒。旧地址转发需要在新机正常后启用，
并观察旧地址访问趋零；仅等待 TTL 不是所有客户端都已更新的证明。

`prepare-aws-control-plane.py` 生成并离线验证待切流 patch，未改现网默认值。
当前 GitHub `PROD_SSM_TARGET=hetzner`、`EDGE_MAIN_GATEWAY_ALLOWED_CIDR=167.233.211.115/32`；
GHCR PAT 参数路径不变。四边 `/tokenkey/lightsail/<id>/ssm_managed_instance_id` 仍指向历史 AWS
身份，正式窗口必须与 instance_name、AWS 矩阵和 GitHub 变量一并切换；不能提前覆盖。
共享代码已补齐混合舰队的 Gemini worker 发布、账号导入、OAuth 检查、健康探针和运维 fan-out；
待切流 patch 只修改选定 edge 的活矩阵和舰队文档。47 项临时副本测试及 workflow 覆盖检查通过；
同 ID 双平台启用会拒绝自动派发。当前工作树活矩阵仍全部 Hcloud，prod 默认值与 GitHub 变量
不变。旧平台的显式历史查询仍可用；不再生成默认 prod 同时迁移的全舰队补丁。

生成命令：`python3 ops/migration/prepare-aws-control-plane.py --edge uk2 --output /tmp/aws-uk2-cutover-pending.patch`。
当前按选定 edge 生成 `.actions.json`，列出该边的 instance_name、static_ip_name、public_ip、
ssm_managed_instance_id 参数与 DNS A 目标；prod 与 GitHub 全局变量保持 Hcloud。没有执行远程写入。
此前全舰队 patch 已被单 edge 方案替代，禁止再直接应用 `/tmp/aws-cutover-final-pending.patch`。
应用 patch 前仍要对当时分支运行门禁，再统一推进默认分支和外部指针，避免混用新旧矩阵。

prod 到四边的固定 IP compose 已在候选本机生成并通过 Docker Compose 实际解析，位于
`/var/lib/tokenkey/migration/prod-edge-pins-prepared/pending/`，权限 0700/0600。
`prepare-prod-edge-pins.py` 检查主机名、持久 hold 与仅 PG 运行；回执绑定源配置和待安装配置 SHA。
蓝绿两个 gateway 都包含新边域名映射。尚未替换 runtime compose、尚未启动容器。
过渡期间暂停发版，待 DNS 收敛后通过正常蓝绿发布重建配置即可移除临时固定地址。

QA runtime 的独立镜像 pin 为 1.8.288，容器是配置快照，按设计不启动。
现有 Hcloud raw archive 坐标与专用私密凭据已经复制；共享 credential helper 在新 prod
验证文件权限/属主并成功生成 maintenance 子容器挂载。旧 raw 文件不在新机；QA timers 保持暂停。
未把“凭据挂载验证”写成“新机已执行新一轮实际归档”。

旧 us5 的归档 manifest、precious dump 与补充日志已确认位于
`s3://tokenkey-prod-archive-682751977094/prod/legacy-node-archive/20261015-us5-pre-rebuild/us5/`。
该独立前缀未匹配桶内 `prod/ops-archive/` 的 400 天过期规则，当前没有自动过期；由用户后续清理。
这不是所有旧节点均已归档的证明；其它旧节点未删除，Hcloud 最终静态归档需在正式停写后形成。

## 追加停写前预同步（2026-10-15 09:09 UTC 启动）

用户再次授权后，对五台执行 precious + logs，各节点顺序补数，同时最多处理两台。
十组导出/恢复均成功，内容校验通过；每次恢复结束检查仍为仅 PostgreSQL 运行、持久 hold 存在。
源端持续在线写入，未改 DNS/路由/默认 SSM 指针，未启动候选业务服务。同步期间五个正式 API
域名的 `/health` 均返回 200。QA raw 未同步。

| 节点 | 本轮压缩传输 | 导出 + 恢复/校验 |
|---|---:|---:|
| prod | 29.18 MiB | 369.57 s |
| us4 | 14.03 MiB | 184.85 s |
| us5 | 10.53 MiB | 161.11 s |
| uk1 | 19.98 MiB | 125.00 s |
| uk2 | 21.79 MiB | 130.65 s |

耗时不含候选基线扫描和 SSM/S3 编排等待。prod 本轮 369.57 秒，上轮 375.87 秒，
基本持平；本轮结果不能证明正式停服时长会显著下降。预同步减少的是最终待补的数据尾差，
最后的快照内容校验仍需运行。各回执 `final_freeze_verified=false`，不能当作停写一致性证明。
回执与 S3 批次坐标见 [`aws-fresh-presync-20261015T090938Z.json`](../ops/aws-fresh-presync-20261015T090938Z.json)。

## 首台正式切流授权与准备：uk2

用户授权“先挑个流量小的 edge，安全推进正式切流”。2026-10-15 09:28 UTC 同窗统计：

| edge | 近 60 分钟完成请求 | 近 15 分钟完成请求 | 近 5 分钟完成请求 | 采样在途 |
|---|---:|---:|---:|---:|
| us4 | 502 | 110 | 30 | 2 |
| us5 | 233 | 64 | 10 | 1 |
| uk1 | 1287 | 222 | 52 | 3 |
| uk2 | 216 | 42 | 22 | 1 |

选择 uk2（15/60 分钟均最低）；短窗瞬时在途不是排空证明，进入维护前重采样。
源端 `mi-022971cd3f981e9ac`，新机 `mi-022bedb939517c7f3`，均 eu-west-2；
域名 `api-uk2.tokenkey.dev` 从 `2.31.27.73` 切到 `35.178.162.181`，TTL 维持 600。
准备时镜像同为 1.8.292，worker 同为 a700f3e98a35；切流前新机持久 hold、仅 PG 运行。

本轮是混合舰队，不能应用原全舰队补丁：prod 仍在 Hcloud，新 uk2 CIDR 必须允许
`167.233.211.115/32`；过渡期间额外允许旧 uk2 作为可信单地址代理。对应四份 Caddy 配置
位于 `/tmp/aws-uk2-cutover-config/`，新机副本为 `/var/lib/tokenkey/migration/uk2-cutover-prepared/`，
准备与解析不等于安装/重载。

已补齐共享 live-target 枚举、同 ID 双平台拒绝、worker 平台选择、OAuth 检查、账号导入、
健康探针与运维 fan-out 的混合舰队支持，已随 PR #2643 合并。选定 uk2 的矩阵/DNS/SSM 随本次切流更新，其余节点保持 Hcloud。47 项临时混合副本测试通过。

prod 上计划临时暂停并在验收后恢复的原启用镜像账号仅为：
`216 antigravity-uk2`、`199 china-uk2`、`66 kiro-uk2`、`63 openai-uk2`。
`214 cc-uk2` 与 `213 gemini-uk2` 原本停用，必须保持原状态。
按同平台与共享 group 检查，其他 edge 均有启用镜像候选；这不是每个模型可用性的承诺。
暂停前重读账号状态并保存私有回执，只有本次确实暂停的账号才恢复。

执行顺序：先发布已验证的混合控制面支持 → 暂停上述镜像 → uk2 维护入口与存量排空 →
停止旧 gateway/worker 与可触发写入的 timer，并固定旧库只读 → precious/logs 最终补数和
冻结回执 → 新机恢复常规网络/systemd，基础服务与最小真实请求验收 → 旧地址只转发、
单域名 DNS 与单 edge SSM/矩阵切换 → 恢复原启用镜像 → 验证真实新流量/旧库无新增，恢复新机备份。
旧主机继续保留数据，不恢复为生产写库。prod 与其余三边不停止、不改域名、不切控制面。

本轮推送、合并和 uk2 正式切流已获用户明确批准。实际执行与回执见下文；prod、uk1、us4、us5 仍禁止自行切流。

## 后续节点正式窗口的执行顺序

1. 用户确定窗口后，重新核对 DNS、镜像、schema、主机身份、输入配置 SHA、活跃任务与预算。
2. 入口进入维护；使用现有 `/health/inflight` 与 drain 机制等待存量请求完成，再停止所有
   gateway、worker、QA/保留策略等逻辑写入者。排空超时保持停止线，不强杀客户长请求。
3. precious 与 logs 各执行最终导出/应用/内容、外键、序列对账。源端全程保持停写。
   任一步失败保持候选离线，不自动运行未经演练的大库全量恢复，不自动切回旧机写入。
4. 清理候选 hold/临时 systemd 覆盖，恢复部署网络和启动定义；先启动边缘再启动 prod，
   用新 IP 的域名映射检查健康与最小真实请求。prod 到四边应固定新 IP，避免 DNS 缓存调用旧边。
5. 应用新边 CIDR、待切流控制面和正式 DNS；旧地址只代理到新机，旧应用继续停止，防止双写。
6. 恢复新机备份/QA/监控计划，验证新备份与归档回执、错误率及旧机无新业务写入；观察旧地址
   流量后移除临时信任。旧数据按独立前缀归档并核对清单，资源清理另按用户决定执行。

## 验证与回执位置

- 单元：`python3 -m unittest ops.migration.test_standard_freeze_delta ops.migration.test_fresh_host_delta ops.stage0.test_resolve_prod_ssm_target`。
- 真实 PostgreSQL：`check-oltp-refresh-postgres.py` 与 `check-fresh-host-delta-postgres.py`，仅独立测试库。
  已验证余额更新、损坏文件拒绝、过期基线拒绝、低 ID 晚提交、热转冷归档、外键失败回滚、
  分区父序列及无关业务表保留。测试库已清理。
- 本机受保护工作目录：`/tmp/tokenkey-aws-migration-20261015/`；本轮逐阶段回执为
  `/tmp/{precious,logs}-<node>-<UTC批次>-receipt.json`，数据对象留在上述私有 S3 前缀。
- 待切流配置：`/tmp/aws-transition-pending-reviewed/`；禁止把生成目录当成已上线证明。
- 本轮控制面离线补丁、预同步回执、Caddy 解析和临时地址配置验证均已完成；源码全量 preflight 作为最终交付门禁。
- 当前新增的混合控制面代码需先完成发布；之后的工作位于正式窗口：维护排空、停写、最后补数、激活、基本真实请求、DNS/控制面切换，及接流量后的备份/归档验收。
  不把候选离线配置验证描述成已承接客户流量。

## uk2 正式切流结果（2026-10-15 UTC）

机器可读回执：[`aws-uk2-cutover-20261015.json`](../ops/aws-uk2-cutover-20261015.json)。
用户明确批准后，PR #2643 全部检查通过并于 10:06:43 合并；主分支随后发现两份审批
文档仍为 pending，本次按真实会话授权补全审批字段，不改变批准范围。

- 10:09:49 起暂停四个原启用的 uk2 镜像；10:10:12 旧入口进入维护，10:10:14 排空至
  `draining=true, in_flight=0`，停止旧 gateway/worker 并设置永久 archival hold、禁自动重启、
  禁旧 pgdump timer、旧数据库默认只读。旧数据库不是回滚写库。
- 冻结后 precious：20 个变化单元，3,693,669 压缩字节，导出 19.43 秒、应用 34.00 秒；
  logs：3 个变化单元，19,176,254 压缩字节，导出 25.46 秒、应用 61.89 秒。
  两次应用均在提交前校验内容、外键和序列；独立冻结证据绑定各 SSM 命令，
  保留底层通用同步回执的 `final_freeze_verified=false`，不把预同步工具伪装为停写控制器。
- 新机保留原 PG 卷，重建正常网络；PG 数据目录、账号数与日志数在启动业务前一致。
  首次启动遇到业务容器引用已删除隔离网络 ID，使用正式 Compose 强制重建网关/Caddy 后解决；
  再启动时 Gemini control alias 要求 worker 已运行，因此先启动 worker 再启动 systemd unit。
  后续节点必须把这两步纳入解除隔离顺序，不能只连接新网络或删除 hold。
- 10:19:33 旧入口改为只转发至新机，单 edge 维护响应窗口 **9 分 21 秒**；四镜像均于
  10:20:59 恢复，暂停操作窗口 **11 分 10 秒**。其他 edge 持续服务，不能把这两个窗口
  描述为全站停服，也不能据此断言所有请求均无影响。
- DNS A 已更新，1.1.1.1 / 8.8.8.8 均解析到 35.178.162.181；四个 uk2 SSM 参数逐项写入并回读。
  prod 地址、GitHub 全局变量及其余三边身份不变。
- 新旧地址 `/health` 均 200；prod 经两地址访问未鉴权 relay 均 401；公网 relay 为预期 403。
  gateway 1.8.292 健康，worker `/readyz` 为 ready、digest a700f3e98a35、协议 1。
  prod 镜像 199 的 deepseek-v4-flash 小请求成功并计费归属正确（servable）；采样新库已新增
  正常流量，旧库冻结后新增 usage 为 0。旧 Caddy 采样无 relay 503，另有 8 个非 relay 维护响应。
- 新机 pgdump 已实际上传、下载哈希一致，AES256；两小时备份 timer、磁盘指标、GHCR 清理
  timer 均 active。旧库整库归档为
  `s3://tokenkey-prod-pgdump-682751977094/edge/uk2/pgdump/archive-hcloud-20261015/frozen-full.dump`，
  27,856,900 字节，上传后下载 SHA256 一致；保留在现有桶独立前缀，按用户决定以后清理，
  不承诺该新前缀具有 400 天自动过期规则。

过渡期旧地址继续转发，旧主机保留只读历史数据；新机仅信任旧 uk2 的单一代理地址。
待旧地址流量归零再收紧到 candidate-final Caddy 配置，不强制断开客户长连接。
其余四台保持切流前状态，后续切流需要另选窗口。
