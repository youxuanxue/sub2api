---
title: Edge EC2 零数据丢失迁移
status: approved
approved_by: "feng（对话审批 2026-08-12）"
approved_at: 2026-08-12
owners: [tk-platform]
scope: "将现有 Lightsail Edge 迁移到 EC2 t4g.small Unlimited；IP 允许变化，持久数据不允许丢失"
---

# Edge EC2 零数据丢失迁移

## 目标

为现有 Edge 提供长期 EC2 `t4g.small Unlimited` 运行平台和单 Edge 迁移工具。迁移允许公网 IP 变化，但 PostgreSQL、账号、凭证、Redis、应用文件、密钥、证书及其他持久数据不得丢失。

合并本设计对应的代码不会创建、修改或删除 AWS、DNS、账号或线上资源。每个线上写动作仍在实际执行点取得明确授权。

## 不做什么

- 不自动删除对应的 Lightsail 源机。迁移稳定后的资源退役使用独立 PR。
- 不在长期平台代码中保存跨 Edge 执行顺序；迁移入口一次只处理一个 Edge。
- 不实现应用层双写。
- 不物理复制 PostgreSQL data directory。Lightsail 与 `t4g` 的 CPU 架构不构成 PGDATA 物理可移植性保证，数据库迁移使用 PostgreSQL 逻辑备份和恢复。
- 不保存线上状态快照、迁移 evidence、历史 review 过程、费用或预算门禁。
- 不为未来退役预留无当前消费者的抽象、开关或兼容层。

## 平台能力

每个 EC2 Edge 使用 AL2023 ARM64、`t4g.small` Unlimited、独立 EIP、加密 gp3 数据卷、2 GiB swap、SSM、CloudWatch 和有界日志。基础设施、部署 workflow、OIDC 权限、Edge 路由和生成内容均有一个明确 owner。

迁移期间 Lightsail 与 EC2 可以同时存在，但同一个 Edge 只有一个写入 owner。普通部署、诊断和健康检查必须跟随唯一 owner；双 owner 状态机械失败。

同一 state 文件的本地执行锁覆盖整条跨主机编排，防止两个 controller 交错操作 source/target；迁移控制器、普通 Edge deploy 和 candidate update 另共享每台 host 的远端 action lock。内部 marker `.write-owner-locked`、`.target-write-owner-active`、`.target-proxy-retained` 分别守卫被冻结写端、已接管写入的目标和只保留回滚代理的目标，不扩展四状态模型。账号导入/编辑、Caddy/飞书/host unit 配置同步等其他写入口不虚称已被同一把锁覆盖，必须由每次迁移的执行审批在整个窗口冻结。最终快照前，源端 app、PostgreSQL、Redis 和 Caddy 会物理停止。

## 数据完整性契约

迁移工具先从 live host 机械生成持久化 manifest，不依赖手写目录清单。已知数据面至少包括：

- PostgreSQL 全部 schema、表、序列、业务数据、账号、凭证、groups 和 bindings。
- Redis AOF/RDB 和运行配置。
- `/var/lib/tokenkey/app`。
- Caddy 配置、证书和状态。
- `.env`、`.env.secret` 及其他持久密钥文件。
- pgdump、运行 receipt 和 `/var/lib/tokenkey` 下其他持久文件。

未知且未归类的持久路径不允许静默忽略。manifest 必须记录路径、类型、权限、owner、大小和内容摘要；secret 只记录摘要，不输出明文。

在线阶段可以预同步普通文件和准备逻辑备份。最终收口必须在源应用停止写入后完成：

`prepare` 的在线演练验证 bundle 摘要、成员摘要、完整恢复流程、Compose 配置、镜像拉取和应用容器可创建。它不启动带真实账号数据的目标应用，避免后台任务在 candidate 上刷新账号或产生其他外部副作用。由于演练期间源端仍在持续写入，它不做跨时间点的 PostgreSQL、Redis 或普通文件内容摘要相等比较，也不把演练结果当作最终数据一致性证明。

1. 源应用 drain，确认在途请求归零并停止写入。
2. 生成 PostgreSQL 一致性逻辑备份。
3. Redis 强制持久化并停止。
4. 对普通文件做最终增量同步。
5. 在 EC2 恢复数据库、Redis、文件、权限和密钥。
6. 逐表验证行数与内容摘要，逐账号验证非明文凭证摘要，逐文件验证 manifest。

任一缺失、摘要不一致、数据库对象不完整或未知路径未归类，都必须停止切换。

## 四状态模型

```text
prepared -> cutting_over -> observing -> stable
```

- `prepared`：EC2 基础设施、在线预同步、bundle/恢复无副作用演练和完整数据 manifest 全部通过；目标应用保持停止，最终数据一致性、应用健康和旧 IP 代理链路仍由正式 cutover 校验。
- `cutting_over`：单个受 checkpoint 保护的命令完成源端冻结、最终同步、恢复、数据验证、EC2 启用和旧 IP 代理，随后输出精确 DNS 变更并等待人工执行与确认。
- `observing`：EC2 是唯一写入端，Lightsail 只把旧 IP 请求代理到 EC2，连续观察 10 分钟。
- `stable`：观察通过；Lightsail 保留用于受控回滚，但不运行独立应用、PostgreSQL 或 Redis。

`cutting_over` 内部 checkpoint 只用于完成态重复调用和显式失败恢复，不暴露为额外生命周期状态。若控制进程在远端动作完成与本地 checkpoint 落盘之间中断，禁止猜测并继续 cutover，必须先执行显式 rollback，再重新 prepare/cutover。checkpoint 状态文件必须原子写入，并绑定 Edge ID、源/目标资源、执行 commit 和数据 manifest 摘要。

## 停写与旧 IP 排水

每个 Edge 正常成功切换的源端停写上限为 120 秒。正式切换前必须至少完成一次不影响线上的迁移演练；若实测或保守预测无法在上限内完成，工具必须拒绝进入 `cutting_over`。一旦切换失败，控制器停止向前推进并优先完成直接恢复或完整反向同步与校验；异常回滚维护窗可能超过 120 秒，不以牺牲数据完整性满足该正常切换上限。

EC2 数据验证通过后，Lightsail 只保留 Caddy，并反向代理到 EC2 EIP。代理路径必须在正式切换前通过临时入口和真实 Host header 验证。随后才允许修改 DNS。缓存旧 IP 的请求继续进入 EC2，不能写回 Lightsail 的旧数据层。

## 失败与回滚

- EC2 接受写入前失败：DNS 不变，恢复 Lightsail 应用、PostgreSQL 和 Redis。
- EC2 接受写入后失败：先冻结 EC2，反向同步 EC2 新增数据并完成同等级校验，才允许恢复 Lightsail。禁止直接切回旧数据库。
- 回滚等待 DNS 回切时，EC2 只保留旧 EIP 代理；普通 deploy 和 candidate update 都必须机械拒绝重新启动该目标应用。
- DNS 回切后继续保留旧 EC2 EIP 排水；排水结束后显式复用 `rollback` 释放目标为 candidate，释放前拒绝重新 `prepare`。
- 120 秒只约束正常向前切换，不是异常回滚完成 SLA；回滚以数据完整性优先。
- `stable` 不授权删除 Lightsail。资源删除和仓库 Lightsail cleanup 另行审批、实现和执行。

## 验证门禁

- 基础设施、OIDC、部署、唯一 owner 路由有正向和负向测试。
- 迁移工具覆盖完整 manifest、secret redaction、未知路径拒绝、数据库/Redis/文件校验、120 秒超时、完成态重复调用、中断态拒绝继续和两类回滚边界。
- 测试覆盖控制器状态与失败恢复、远端命令计划、临时文件树 manifest，以及 PostgreSQL/Redis 命令和数据校验契约；不以文件存在性冒充验收。
- CloudFormation 生成内容通过 drift check，完整 preflight 和严格 review 通过。
