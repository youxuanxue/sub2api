---
status: approved
approved_by: user (conversation)
approved_at: 2026-10-21
risk: high
scope: UCloud cn-prod 106.75.29.139 → node1 preparation only; no cutover
---

# UCloud cn-prod → node1 切流前准备

本文件是 #2698 的高风险准备基线。目标是把当前 UCloud cn-prod 迁移到 node1，并继续使用公网入口 IP `106.75.29.139`。当前只允许只读盘点、离线渲染、隔离恢复和证据整理；正式切流必须另立维护窗口和人工批准。

## 保护边界

- AWS prod 保持零变更：不调用 AWS prod deploy/SSM、蓝绿 promote、DNS、账号/凭据、PostgreSQL、Redis、计费或备份写入路径。
- UCloud cn-prod 保持在线：不重启或 recreate 应用、Caddy、PostgreSQL、Redis；不停止 timer/worker，不冻结写入，不刷新 OAuth，不改账号、额度、供应凭据、代理、防火墙或 EIP。
- 准备阶段不解绑、绑定或探测性移动 `106.75.29.139`，不改 DNS/TLS，不开放 node1 公网业务入口。
- node1 候选必须使用独立目录、独立容器网络和 `DATA_SYNC_HOLD`；数据库无公网端口、无供应商/支付/回调外连、无 OAuth 刷新和真实业务写者。
- 不使用 Tokyo AWS、旧 Hcloud、历史 node1 reader 或 `api.galaxywaveai.com` 测试证据作为本次基线。

## 当前待核实事实

以下事实必须从只读运维入口取得，不能从历史文档推断：

1. UCloud 当前实例 `uhost-1wgyn7gu2f6a`、EIP `eip-1wjj3obkx4mv` 与 `106.75.29.139` 的绑定、北京二 `cn-bj2-02`、私网 `10.9.53.136`、系统盘 `bsi-158d29bf4f`、数据盘 `bsm-1wgz3hm0c0jb` 和基础镜像已只读确认；防火墙、machine-id、运行时版本和备份覆盖仍待确认。
2. node1 machine-id、架构、数据盘 UUID/容量、挂载点、Docker/Compose、WireGuard 对端、SSH/SSM 身份和回滚路径；node1 CPU、内存和数据盘容量必须不低于 UCloud 源端已确认的 8 CPU、8 GiB、20 GiB。
3. `106.75.29.139` 的承载模型：EIP 迁移到 node1，或 EIP 留在 UCloud 入口并经 WireGuard 转发到 node1。未选定模型前不得生成 apply 命令。
4. cn-prod 当前 schema、PostgreSQL/Redis 版本、配置摘要、证书有效期和备份是否覆盖完整 `usage_logs`、历史更新/删除/补录及最新凭据轮换。

当前 inventory 仅保留 `preparation_only=true`、`public_ip=106.75.29.139`、`public_ip_mode=pending`、`inventory_status=pending-readonly-verification` 和 `candidate_identity_status=pending-readonly-verification`，machine-id/数据盘 UUID 使用未核实占位值，不能用于线上部署。

真实 node1 是家庭服务器 `180.76.98.95:2201`，不是 UCloud 的 `node1-ingress-test`。当前 SSH 只读连接超时，CPU、内存、磁盘和宿主机扩容能力均未核实；`candidate_capacity_status=pending-readonly-verification`。在取得真实 node1 读数前，不得判断可扩容性、标记 inventory 为 `verified`，也不得进入部署或切流。

UCloud 资源只读盘点见 [`node1-ucloud-readonly-inventory-20261022.json`](../ops/reports/node1-ucloud-readonly-inventory-20261022.json)；真实 node1 SSH 盘点见 [`node1-external-ssh-readonly-inventory-20261022.json`](../ops/reports/node1-external-ssh-readonly-inventory-20261022.json)。UCloud `node1-ingress-test` 仅为无关观察资源，未被标记为迁移目标。

## 数据基线和隔离恢复

- UCloud cn-prod 是唯一权威源。生成带 SHA-256 的完整包，包含 precious、账号/分组/密钥/代理/设置、计费幂等和完整 `usage_logs`。
- 普通排除 `usage_logs` 的 pgdump、Tokyo 快照或旧 node1 reader 只能作历史参考，不能作为上线数据。
- 在 node1 独立目录恢复，逐用户/逐 UTC 日核对数量、内容、token、费用、时间范围、序列、约束和租户隔离；恢复失败、账务不符、缺 WAL 或 SHA 不符时保留现场并拒绝继续。
- 以真实 UCloud 基线演练插入、历史更新、删除、保留旧 ID 的补录、schema/分区漂移、损坏包、过期基线和 COMMIT 前超时。所有失败必须安全拒绝/回滚。
- 记录完整包传输、恢复、对账和 delta 耗时，不能用同机 Tokyo 演练耗时替代冻结窗口证据。

## node1 运行时和凭据

- 固定源端与 node1 的镜像 digest、源码 revision、架构和 PostgreSQL 主版本；准备阶段不自动升级。
- 候选仅监听 loopback/私网，管理端口不公网暴露；错误数据盘、空库、缺 `DATA_SYNC_HOLD` 或缺批准标记时拒绝启动。
- 建立数据库加密材料、JWT/TOTP、用户认证、API key、供应账号、OAuth、Caddy/TLS 的来源、传输、轮换和回滚清单；禁止写入日志、git 或双写候选。
- Redis 重新创建并验证登录、限流、幂等和会话影响，不做热复制。
- 用现有 cn-prod 域名和证书模型验证 TLS/SNI、Caddy、PROXY protocol、WireGuard、SSE、登录、模型、usage 和计费；`/health` 只能作为基础门禁。

## 机械门禁

- `deploy/domestic/targets.json` 的 Node1 目标默认只允许 `plan`；`bootstrap`、`deploy`、`smoke-only`、`promote`、公网绑定、批准标记安装和 timer 启用均 fail-closed。
- 目标必须明确来源环境、入口 IP、EIP 承载模型、machine-id、数据盘 UUID、候选根目录和 `DATA_SYNC_HOLD`。
- node1 候选脚本必须先读取 root-private `/etc/tokenkey/node1-migration-inventory.json`；仅当其标记为已核实的 UCloud cn-prod、入口为 `106.75.29.139` 且 machine-id/数据盘/镜像/域名齐全时才可继续，缺失或不匹配时在 Docker/磁盘/探针调用前拒绝。
- 对 UCloud cn-prod、AWS prod、DNS、EIP、凭据、外部通知和生产写者的调用必须带独立批准与维护窗口标记。
- 负向测试必须证明拒绝发生在远端写操作、AWS 调用或 UCloud 在线服务重启之前。

## 正式切流（另立批准）

1. 演练停止新请求、排空长连接、冻结业务写者和 OAuth refresh，记录最终时间水位/LSN；超时则解除候选并恢复源。
2. 源停止写入后生成最终 full/delta，完成 accounts、proxies、密钥、usage、计费和序列对账；目标保持 recovery/HOLD。
3. 源无可写会话后，按已批准的承载模型迁移或转发 `106.75.29.139`，验证回程、TLS、SSE 和真实模型请求。
4. node1 成为唯一写入者后观察真实错误率、延迟、usage、计费和后台任务；旧 UCloud 机和 AWS prod 不参与双写。
5. 目标未写入前可恢复源并回绑定 IP；目标已写入后必须先冻结目标、反向迁移新增账务/usage/凭据，再恢复源。源机、数据和回滚材料至少保留七天。

## 验收门禁

- I0：UCloud 源、EIP、node1、磁盘、网络和版本只读回执。
- I1：UCloud 完整包/恢复 SHA、逐用户/日对账、delta 和故障回滚回执。
- I2：node1 隔离、无外连、无公网业务端口、`DATA_SYNC_HOLD` 和启动拒绝回执。
- I3：现有域名 TLS/PROXY/WireGuard/SSE、登录、模型、usage/计费和后台验收回执。
- I4：最终冻结耗时、单写确认、EIP 操作回执、观察窗口和回滚包；I4 需要独立人工批准，本 PR 不执行。

## 结论

#2698 当前只交付 UCloud → node1 的安全准备和机械保护。AWS prod 与 UCloud cn-prod 在线服务均不得受影响；任何 EIP、源端冻结、最终同步、候选 promote、DNS、证书、凭据或生产 timer 变更都不在本次执行范围。
