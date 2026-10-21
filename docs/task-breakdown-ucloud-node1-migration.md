---
title: UCloud cn-prod 106.75.29.139 → node1 迁移拆解
status: draft
risk: high
scope: '#2698；仅准备与隔离验收，未授权切流'
---

# 目标

将当前 UCloud cn-prod 迁移到 node1，并继续使用公网入口 IP `106.75.29.139`。迁移完成后保持现有公网域名、客户端地址、账号、用量和计费语义。本文只定义准备、隔离恢复、验收和未来切流所需工作，不授权执行切流。

# 不做什么

- 不调用或修改 AWS prod 的部署、SSM、DNS、账号、凭据、PostgreSQL、Redis、计费和备份写入路径。
- 不重启、recreate、冻结或修改 UCloud cn-prod 的应用、Caddy、PostgreSQL、Redis、timer、worker、账号、额度、供应凭据、代理、防火墙和 EIP。
- 准备阶段不解绑或绑定 `106.75.29.139`，不改 DNS，不开放 node1 公网业务入口，不启用候选生产写者。
- 不把 Tokyo AWS、旧 Hcloud、历史 node1 reader 或现有 `api.galaxywaveai.com` 测试证据当作本次 UCloud 迁移基线。
- 不复制一份会与 UCloud 在线实例同时刷新 OAuth 的生产凭据；Redis 不做热复制。

# 当前事实与冲突

| 项目 | 用户目标 | 当前 PR 仍引用的事实 | 处理 |
|---|---|---|---|
| 源 | UCloud cn-prod | Tokyo AWS `3.114.187.250` | 重新做 UCloud 只读盘点 |
| 公网入口 | `106.75.29.139` | `106.75.17.57`、`106.75.7.50` | 只读核实 EIP ID、绑定实例和可迁移方式 |
| 目标 | node1 | `zhupeng-prod` / `ssh-tunnel` / `ub1.tokenkey.dev` | 重新确认 node1 machine-id、架构、磁盘和网络身份 |
| 域名 | 现有 cn-prod 域名保持不变 | `api.galaxywaveai.com` | 删除新域名交接作为本任务前置 |
| 数据证据 | UCloud cn-prod 完整数据 | Tokyo 快照、Tokyo usage 对账 | 重新生成完整 UCloud 基线 |

在这些事实核对完成前，禁止把目标写成可执行的生产 inventory，也禁止生成 EIP apply 命令。

# 分阶段工作

## A. 只读盘点

1. 读取 UCloud 资产：实例 ID、EIP ID、`106.75.29.139` 当前绑定关系、区域、网络、防火墙、磁盘、machine-id、架构、运行镜像和当前版本。
2. 读取 node1 资产：machine-id、架构、数据盘 UUID/容量、挂载点、Docker/Compose、WireGuard 对端、SSH/SSM 身份、现有工作负载和可回滚路径。
3. 读取 cn-prod 当前 schema、PostgreSQL/Redis 版本、配置摘要、证书有效期、代理/账号/密钥数量和备份覆盖范围。只读采集必须经过现有运维入口，禁止对在线库做重型全库扫描。
4. 确定公网承载模型：
   - 模型一：EIP 从旧 UCloud 主机迁移到 node1；或
   - 模型二：EIP 继续留在 UCloud 入口，通过 WireGuard 转发到 node1。

两种模型的防火墙、回程路由、TLS、PROXY protocol、SSE 和回滚动作不同，必须在实现前选定一种并得到审批。

## B. 数据基线与隔离恢复

1. 以 UCloud cn-prod 为唯一源，生成包含 precious、账号/分组/密钥/代理/设置、计费幂等和完整 `usage_logs` 的备份；覆盖历史更新、删除和补录。
2. 备份文件生成、SHA-256、传输和恢复必须在独立副本完成；禁止用普通排除 `usage_logs` 的 pgdump 证明完整迁移。
3. 在 node1 独立目录和隔离容器恢复，数据库无公网端口、无供应商/支付/回调外连、无 OAuth 刷新、无真实业务写者，持续保留 `DATA_SYNC_HOLD`。
4. 执行逐用户/逐 UTC 日的数量、内容、token、费用、时间范围、租户隔离、序列、约束和计费幂等对账。
5. 用真实 UCloud 基线演练 delta：插入、历史更新、删除、保留旧 ID 的补录、分区/schema 漂移、损坏包、过期基线和 COMMIT 前超时；失败必须拒绝并回滚。
6. 记录完整包传输、恢复、对账和 delta 耗时，作为未来冻结窗口的实测输入。

## C. 运行时与凭据兼容

1. 固定源端与 node1 的镜像 digest、源码 revision、架构和 PostgreSQL 主版本；准备阶段不自动跟随主线升级。
2. 隔离运行时仅监听 node1 loopback/私网，管理端口不公网暴露；错误数据盘、空库、缺少 `DATA_SYNC_HOLD` 或缺少批准标记时必须拒绝启动。
3. 将正式迁移所需的运行时密钥列成清单：数据库加密材料、JWT/TOTP、用户认证、API key、供应账号凭据、OAuth、Caddy/TLS。每项定义来源、传输、轮换和回滚，不把正式密钥写入日志、git 或候选双写环境。
4. Redis 重新创建并验证登录、限流、幂等和会话影响；不把 Redis 热复制当作数据库迁移完成。
5. 用当前 cn-prod 域名和证书模型做隔离验证，覆盖 TLS/SNI、Caddy、PROXY protocol、WireGuard、SSE、登录、API、模型请求、usage 和计费归属；`/health` 只能作为基础门禁。

## D. 机械安全门禁

1. Node1 inventory 默认只允许 `plan`、身份核对和隔离恢复；`bootstrap`、`deploy`、`promote`、公网绑定、批准标记安装和 timer 启用默认拒绝。
2. 目标配置必须包含源环境、入口 IP、EIP 承载模型、目标 machine-id、数据盘 UUID、候选根目录和 `DATA_SYNC_HOLD` 约束；未知或未核实值拒绝执行。
3. node1 候选脚本在远端必须先读取 root-private 的 `/etc/tokenkey/node1-migration-inventory.json`；只有 `inventory_status=verified`、来源为 UCloud cn-prod、入口为 `106.75.29.139` 且 machine-id/数据盘/镜像/域名字段齐全时才允许继续。缺失或不匹配时在任何 Docker、磁盘或探针调用前拒绝。
4. 任何对 UCloud cn-prod、AWS prod、DNS、EIP、凭据、外部通知或生产写者的调用必须有独立的切流批准和维护窗口标记。
5. 为上述拒绝路径补充负向测试，证明拒绝前没有远端写操作、没有 AWS 调用、没有 UCloud 在线服务重启。

## E. 未来切流（独立审批）

1. 先完成冻结演练：停止新请求、排空长连接、冻结业务写者和 OAuth refresh，记录最终时间水位/LSN；超时则解除候选并恢复源。
2. 源停止写入后生成最终 full/delta，完成 accounts、proxies、密钥、usage、计费和序列对账；目标保持 recovery/HOLD。
3. 确认源无可写会话后，再按已批准的承载模型迁移或转发 `106.75.29.139`，验证回程、TLS、SSE 和真实模型请求。
4. node1 成为唯一写入者后观察真实错误率、延迟、usage 和后台任务；AWS prod 和旧 UCloud 资源保持隔离。
5. 目标未写入前可恢复源并回绑定 IP；目标已写入后必须先冻结目标，执行反向 delta、账务和凭据回迁，再恢复源。源机、数据和回滚材料至少保留七天。

# 验收证据

- I0：UCloud 源、EIP、node1、磁盘、网络和版本只读回执。
- I1：完整 UCloud 备份/恢复 SHA、逐用户/日对账、delta 和故障回滚回执。
- I2：node1 隔离、无外连、无公网业务端口、`DATA_SYNC_HOLD` 和启动拒绝回执。
- I3：当前域名 TLS/PROXY/WireGuard/SSE、登录、模型、usage/计费和后台验收回执。
- I4：最终冻结耗时、单写确认、EIP 操作回执、观察窗口和回滚包；I4 需要独立人工批准，本任务不执行。

# 实现顺序

1. 先补 UCloud/node1 只读 inventory 和网络承载设计；不写未知事实。
2. 通过人工审批后，替换 PR 中 Tokyo 专属常量、旧 EIP、新域名和旧 evidence。
3. 实现源数据导出、隔离恢复、真实 UCloud delta 和密钥兼容验证。
4. 加入 plan-only/approval guard 及负向测试，确保默认路径不会 deploy 或切流。
5. 完成隔离业务验收和灾备回执后，另立切流审批包；本拆解不包含 apply、部署或线上变更。
