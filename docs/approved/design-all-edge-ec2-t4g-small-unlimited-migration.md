---
title: 全部 Edge 迁移至 EC2 t4g.small Unlimited
status: approved
approved_by: "feng（对话审批 2026-08-10）"
approved_at: 2026-08-10
created: 2026-08-10
owners: [tk-platform]
scope: "将 us3、us4、us5、us6 从 Lightsail 迁移到 EC2，稳定后退役 Lightsail 及其仓库契约"
---

# 全部 Edge 迁移至 EC2 t4g.small Unlimited

## 目标

将 `us3`、`us4`、`us5`、`us6` 顺序迁移到 EC2 `t4g.small` Unlimited，保持 Edge ID、域名和业务语义不变。迁移期间 Lightsail 是可回滚的正式 owner；全部 Edge 稳定后删除 Lightsail 云资源，并通过 cleanup PR 将仓库收敛为 EC2-only。

平台 PR 必须先达到以下状态：

- 基于最新 `origin/main`，主线的部署、归档、诊断和 preflight 契约全部保留。
- 候选机创建、在线预检、账号逻辑迁移、单 Edge 切换验收、回滚和 Lightsail 退役均有可执行且 fail-closed 的工具。
- 合并代码不会创建、修改或删除任何 AWS、DNS、账号或线上资源。
- 运行时不使用预算、成本预测或成本上限作为输入、判断条件或 blocker。

## 不做什么

- 不把基础设施迁移描述为账号扩容。`us3`、`us4` 的单账号 SPOF 和 `us5`、`us6` 的零账号状态必须单独处理。
- 不自动修改 Porkbun DNS。工具只生成计划并核验结果，DNS 写入继续使用单 Edge 人工高风险门禁。
- 不在平台 PR 中删除 Lightsail 云资源。删除必须等待完整 Fleet 观察期结束并再次取得明确授权。
- 不提交在线状态快照、逐轮 review 过程、临时 remediation 计划或无日期 evidence。执行状态必须通过实时探测重新获取。
- 不保留双平台长期抽象。Lightsail 删除后，同时删除迁移期兼容层。

## 目标容量

每个 EC2 Edge 使用独立 stack、VPC、EIP 和持久数据卷：

| 项目 | 配置 |
| --- | --- |
| 实例 | AL2023 ARM64、`t4g.small`、2 vCPU、2 GiB 内存 |
| CPU credits | `CreditSpecification.CPUCredits: unlimited` |
| 根卷 | 20 GiB、加密 gp3 |
| 数据卷 | 20 GiB、加密 gp3，挂载 `/var/lib/tokenkey`，替换与删除时保留 |
| Swap | 2 GiB |
| 快照 | data volume daily snapshot |
| 入站 | TCP 443、8443；不开放 22、80 |
| 运维 | SSM；不依赖 SSH |
| 日志 | Docker 与系统日志必须有有界轮转 |

PostgreSQL 的 groups、accounts、account-group bindings 和 credential blobs 走加密逻辑迁移；Redis 在目标机重建。不复制整机卷、host-local cooldown/error 或 Caddy 证书目录。

## 成本口径

截至 2026-08-10，四台 Edge 的固定月度估算约为：EC2 `$49.06`、EBS gp3 `$12.80`、公网 IPv4 `$14.60`，合计约 `$76.46`。

这是选型参考，不是部署契约，也不是费用上限。估算不包含公网出站、快照增量、额外 CloudWatch 用量、Unlimited surplus charge 和迁移期双平台重叠费用。代码、matrix、workflow、CloudFormation、preflight 和执行 evidence 不得保存预算字段，不得预测账单，也不得因估算值停止迁移。

## 仓库事实源

迁移期间只允许以下 owner：

| 职责 | Owner |
| --- | --- |
| Lightsail 当前目标 | `deploy/aws/lightsail/edge-targets-lightsail.json` |
| EC2 candidate | `deploy/aws/stage0/edge-targets.json` |
| EC2 Edge 解析 | `deploy/aws/stage0/resolve-edge-target.py` |
| Lightsail 部署 | `.github/workflows/deploy-edge-lightsail-stage0.yml` |
| EC2 部署 | `.github/workflows/deploy-edge-stage0.yml` |
| EC2 基础设施 | `deploy/aws/cloudformation/stage0-edge-ec2.yaml` |
| EC2 OIDC 权限 | `deploy/aws/cloudformation/cicd-oidc-ec2-edge-addon.yaml` |
| 实时基础设施预检 | `ops/migration/edge-platform-migration-preflight.sh` |
| 账号迁移 | `ops/migration/migrate-edge-accounts.py` |
| 单 Edge 切换验收 | `ops/migration/edge-platform-cutover-check.sh` <!-- script-ref: planned --> |
| Fleet 退役 | `ops/migration/retire-lightsail-fleet.sh` <!-- script-ref: planned --> |

迁移工具可以接受显式 `@lightsail` 或 `@ec2` 目标；普通 deploy、diagnostics、health watch 和 rollout 只能跟随唯一 `deployable=true` 的 owner。两套 matrix 同时把同一 Edge 标为 deployable 时，preflight 必须失败。

`deploy/aws/stage0/build-cfn.sh` 是生成内容的唯一 owner。它必须支持生成 prod 与 EC2 Edge 模板，并保留 `origin/main` 当前的 QA 归档、恢复、GHCR 清理和 bootstrap payload。生成模板禁止人工选择冲突侧。

## 执行状态与 evidence

仓库不保存运行时快照。每次危险动作前重新查询 AWS、SSM、CloudWatch、DNS、账号和 matrix 现状，并要求所有 blocker 为空。

执行工具可以输出脱敏 JSON receipt，默认写入 runner 临时目录或 GitHub Actions artifact。receipt 必须包含执行 commit、资源 ID、观察起止时间、输入摘要和检查结果，不得包含 secret。artifact 只用于审计和定位，不能替代下一步的实时检查。

旧的 `docs/evidence/all-edge-ec2-migration-preflight.json` 必须删除；测试使用 fixture，不依赖仓库中的在线快照。

## 迁移状态机

```text
lightsail_active
  -> ec2_candidate_ready
  -> cutover_pending
  -> ec2_active_lightsail_standby
  -> fleet_observation_complete
  -> lightsail_retired
  -> ec2_only
```

- `lightsail_active`：Lightsail `deployable=true`，EC2 `migration_candidate=true`、`deployable=false`。
- `ec2_candidate_ready`：实例、SSM、volume、swap、Unlimited、日志轮转、本地 smoke 和告警均通过。
- `cutover_pending`：账号迁移和目标真实 OAuth/model smoke 通过，目标账号重新关闭调度，回滚 IP 已登记。
- `ec2_active_lightsail_standby`：单 Edge owner 和 DNS 已切到 EC2，连续观察通过，Lightsail 仍完整保留。
- `fleet_observation_complete`：最后一个 Edge 验收后，四个 Edge 连续健康满 1 天。
- `lightsail_retired`：经独立授权删除 Lightsail 实例、Static IP 和相关注册，EC2 snapshot/备份已验证。
- `ec2_only`：cleanup PR 删除全部 Lightsail、candidate 和双平台兼容契约。

## 顺序执行

Fleet 固定按 `us5 -> us4 -> us6 -> us3` 推进。开始前重新扫描账号；如果 `us5` 已承载账号且 `us6` 仍为空，可以交换两个无账号基础设施 canary 的位置，`us4` 仍是首个真实流量 canary，`us3` 仍最后迁移。

### 1. 实时预检

预检只读查询区域容量、EIP/VPC quota、ARM64 AMI、实例 offering、源 SSM、DNS、CPU 和 matrix。报告不得包含成本字段。任一 blocker 都停止 provision。

### 2. 候选基础设施

经授权部署 EC2 Edge OIDC addon，再通过 workflow 创建 `us5` candidate。workflow 必须在创建 EIP 之前重跑实时预检。candidate 不修改 DNS、不接正式流量。

验证真实机型、Unlimited、SSM、数据卷、swap、安全组、Docker health、PostgreSQL migration、Redis、Caddy、上游出站、日志轮转、CloudWatch agent 和飞书配置。`us5` candidate 连续观察 1 小时；失败时停止 Fleet。

### 3. 单 Edge 切换事务

每个 Edge 只允许一个切换事务：

1. 重新读取源账号清单；零账号 Edge 记录实时 zero-account 结果，不做伪迁移。
2. 账号 extract、build、load 和 manifest 验收在 fail-closed 事务中完成；目标账号以 `schedulable=false` 落地。
3. 临时启用一个目标账号执行真实 OAuth/model smoke，结束后立即恢复 false。
4. 准备只修改该 Edge owner 的状态变更；DNS operator 在场时才允许合并。
5. 先启用目标账号，再修改 DNS；源账号在 drain 期间保持启用。
6. 权威 DNS 与公共 resolver 只返回目标 EIP后，验证公网 TLS、`/health`、正式流量和源流量归零。
7. 从目标开始接收正式流量时起连续观察 10 分钟。
8. 验收通过后关闭源账号调度；失败则立即回滚账号、DNS 和 owner。

10 分钟窗口内必须保持 SSM 在线、真实 smoke 成功、无 P0/P1、无持续新增 5xx；served ratio 相比切换前基线下降不超过 5 个百分点，p95 延迟不高于源基线 2 倍，且源 Lightsail 无业务请求。Unlimited surplus 只作为费用信号，不冒充可用性故障。

`us4` 的旧 unreachable 告警必须由真实健康扫描关闭；应发送恢复通知时，必须实际收到飞书恢复消息。未恢复告警或通知链路失败会停止 rollout。

### 4. Fleet 观察与退役

完成 `us3` 的 10 分钟验收后开始 Fleet 观察。完整 Lightsail Fleet 保留 1 天；期间任一 Edge 退化都停止退役并按单 Edge 回滚边界处理。

退役前重新验证：所有 Edge 的 owner 和 DNS 指向 EC2、源账号不可调度、四个 EC2 Edge 连续健康、数据备份可读、数据卷 snapshot 已完成。退役工具默认只输出计划；实际删除必须显式 `--apply`、精确确认全部目标，并再次取得用户授权。

## 回滚

| 阶段 | 回滚方式 |
| --- | --- |
| DNS 前 | 删除或重建 EC2 candidate；Lightsail 不受影响 |
| DNS 后、Lightsail 保留 | 启用源账号、DNS 切回登记 IP、关闭目标账号、恢复 Lightsail owner |
| Fleet 观察期 | 同上；必要时先把目标端最新账号状态逻辑同步回源 |
| Lightsail 删除后 | 不再承诺平台回滚；使用 retained EBS data volume、snapshot 和逻辑备份恢复 |

任何失败都必须停止后续 Edge。禁止一边保留用户路径故障，一边继续下一个节点。

## 合并与执行门禁

平台 PR 合并前必须完成：

- 最新 `origin/main` 已合入且无冲突。
- prod 与 EC2 Edge CloudFormation 由同一生成 owner 重建并通过 drift check。
- 迁移、路由、workflow、OIDC、告警和退役的正向/负向测试通过。
- 活跃仓库 surface 不存在成本/预算运行时契约。
- 历史 remediation 计划和在线 evidence 已删除。
- `scripts/preflight.sh` 与代码审查均通过。

合并平台 PR 不授权线上写操作。IAM、candidate provision、账号、DNS、Lightsail 删除分别在实际执行点取得明确授权。

## 最终 cleanup

Lightsail 云资源删除后，cleanup PR 必须删除：

- Lightsail CloudFormation、workflow、matrix、resolver、deploy/diagnostics/rotation 脚本和测试。
- `@lightsail`、`migration_candidate`、平台 exclusivity 与双平台路由分支。
- Lightsail Agent skills、AGENTS 索引、活跃 runbook 和相关 preflight/sentinel。
- 本迁移临时工具中只服务于 Lightsail 回滚或退役的部分。

cleanup 后重新生成 Agent 契约并运行完整 preflight。仓库只保留 EC2 owner；本设计状态更新为 `shipped`。
