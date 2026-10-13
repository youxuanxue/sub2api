---
title: hcloud 全栈蓝绿与 QA raw 归档
status: approved
approved_by: "user（本会话：全栈 hcloud、QA 去 AWS、uk1 限额及普通内容不额外加密）"
created: 2026-10-11
owners: [tk-platform]
risk: high
scope: "prod/Edge 蓝绿发布；QA 归档契约指向 qa-raw-only.md"
---

# hcloud 全栈蓝绿

## Gateway / Edge

prod 与四边复用 `ops/stage0/deploy_via_ssm_bluegreen.sh`。平台适配只解析目标与
SSM region/profile；不复制切流、排空或回滚状态机。前端随 gateway 镜像发布。

PostgreSQL/Redis 保持单份数据，Caddy 只热切 app upstream。Gemini worker 保持
独立容器与既有会话目录，镜像更新使用它自己的发布入口；不随每次 gateway 切色重建。
QA maintenance 保留独立 pin 与 lifecycle 锁。

首次升级继续 legacy → blue：旧进程服务到新进程健康、Caddy 切流并观察成功后才排空。
这次保留 prepare 对 legacy 的拒绝，避免把“准备候选”偷偷变成第一次正式切流。
首次迁移验收后即可使用既有 prepare/replay/promote。

部署前验证 arm64 manifest、SQL 向后兼容、内存/磁盘、真实目标身份；prod 与 Edge
明确传 profile。新增 hcloud prod Volume 挂载验证及 systemd `RequiresMountsFor`，
避免卷未挂载时在根盘创建一套空数据。蓝绿配置保留需要的凭证/对象存储配置及共享目录，
不输出 secret。重启后只启动已确认活动色；不确定状态遵守现有保留两色、人工恢复契约。

发布顺序仍为 canary full → prod full → 其余 Edge infra；所有节点使用同一发布镜像。
切流前失败清理候选并保留旧色；切流后失败保留证据和旧色，不自动反复切换。
回滚通过原入口部署 previous tag，并执行相同兼容性与健康门禁。


## 身份、数据卷与运行时

hcloud prod 在任何主机写入与 Docker 调用前验证 `/var/lib/tokenkey` 为真实 Hetzner ext4
Volume 挂载。Docker 和 tokenkey systemd unit 使用 RequiresMountsFor 与 ExecStartPre，
防止启动时在根盘创建空数据层。`bluegreen_host_runtime.py` 校验文件权限、owner 与挂载。
Hybrid 临时 AWS 身份通过 `/var/lib/tokenkey/aws` 目录只读挂载，支持原子文件轮换；
这些控制面、媒体/telemetry 依赖与 QA raw 存储迁移分别管理。

raw 文件身份只挂载给 maintenance 子容器，网关两种颜色不挂载 raw 凭据。
显式文件缺失、格式错误或与 inline credentials 混用时拒绝，不回退到进程 AWS 身份。
候选准备失败恢复原宿主配置；发布不静默改 raw bucket、endpoint 或凭据文件坐标。

## QA 当前决策与进展

用户后续决定退役 QA 主机、uk1 worker/队列、Bundle 与用户导出。本文件中的旧共用资源、
隔离实验与迁移步骤已移出当前部署指南；历史证据保留在 Git。
唯一现行 QA 契约为 [QA raw-only](qa-raw-only.md)：prod 定时归档到私有 raw S3，
存量按 AWS 原有生命周期自然到期；新增归档经 restore 与 maintenance 接受验证切换存储。
代码 PR 本身不部署。后续新增 raw 归档已完成 Hetzner 切换与验收，AWS 旧数据仍保留自然到期和恢复访问；当前状态及恢复分界见 [QA 运维入口](../../ops/qa/README.md#生产存储与恢复分界)。

## 发布验收证据

2026-10-11 的 1.8.284 已完成 prod 与活 Edge 蓝绿升级、排空和烟测（#2571）。
这是该次发布证据；当前线上版本、节点与 AWS 保留依赖以
[Hetzner 运维入口](../../deploy/hetzner/README.md) 和实时只读探针核实。

蓝绿状态机沿用 [Stage0 部署基线](deploy-stage0-workflow.md) 与
[Edge 安全决策](edge-bluegreen-release-safety.md)。raw 存储验收见上述 QA 运维入口；
AWS 资源退役仍须单独核查与审批，不从代码就绪推导上线完成。
