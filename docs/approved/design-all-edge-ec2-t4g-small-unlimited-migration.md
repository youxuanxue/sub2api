---
title: 全部 Edge 迁移至 EC2 t4g.small Unlimited
status: approved
approved_by: "feng（对话审批 2026-08-04）"
approved_at: 2026-08-04
created: 2026-08-04
owners: [tk-platform]
scope: "把全部活动 Edge 从 Lightsail micro_3_0 迁移到 EC2 t4g.small Unlimited，退役 Lightsail 云资源并清理相关 Agent/代码契约"
---

# 全部 Edge 迁移至 EC2 t4g.small Unlimited 实施计划

> **供执行 Agent 使用：** 实施时必须使用 `subagent-driven-development`（推荐）或 `executing-plans`，按本文复选框逐项推进；任何线上写操作仍受本文人工门禁约束。

**目标：** 将 `us3`、`us4`、`us5`、`us6` 四个活动 Edge 从 Lightsail `micro_3_0` 迁移到启用 CPU Unlimited 的 EC2 `t4g.small`，逐节点保持业务连续和可回滚；全部稳定后退役 Lightsail 云资源，并从仓库中删除 Lightsail 代码、脚本、workflow、skill 和活跃文档入口。

**架构：** 恢复一套通用 EC2 Edge CloudFormation 平台，每个 Edge 使用独立 stack、VPC、EIP 和持久数据卷。迁移期间 Lightsail 继续作为正式 owner，EC2 以 `migration_candidate` 影子目标创建和验证；每次只通过一个 Edge 的矩阵 PR 与 DNS 门禁转移 owner。最后一个 Edge 稳定满 1 天后，先删除 Lightsail 云资源，再把代码库收敛为 EC2-only。

**技术栈：** AWS EC2 Graviton `t4g.small`、AL2023 ARM64、EBS gp3、CloudFormation、EIP、SSM、CloudWatch、GitHub Actions OIDC、PostgreSQL、Redis、Docker Compose、Caddy、Porkbun DNS、Python 与 shell 契约测试。

## 全局约束

- 本任务同时改变基础设施骨架、安全边界和持久状态，属于高风险迁移。合并本文只代表设计获批，不自动授权 AWS、DNS、账号或删除操作。
- 实施范围取执行时 `deployable=true` 的 Edge；当前固定为 `us3`、`us4`、`us5`、`us6`。不复活任何 retired/planned Edge。
- 目标机型固定为 AL2023 ARM64 `t4g.small`，CloudFormation 必须显式写入 `CreditSpecification.CPUCredits: unlimited`，不能依赖 AWS 账户默认值。
- 每台目标机使用 20 GiB 加密 gp3 根卷，以及 20 GiB 加密 gp3 独立数据卷；数据卷挂载到 `/var/lib/tokenkey`，设置 `DeletionPolicy: Retain` 和 `UpdateReplacePolicy: Retain`。swap 固定为 2 GiB。
- 公网只开放 TCP 443 和 8443，关闭 22 和 80。运维走 SSM，Caddy 通过 443 的 TLS-ALPN-01 获取证书。
- Edge ID、域名和业务含义不变，只切换平台 owner 和 A 记录 IP。
- PostgreSQL 的 groups、accounts、account-group bindings 和 credential blobs 做逻辑迁移；Redis 在目标机重建；不复制整机卷或 Caddy 证书目录。
- 导入目标机的账号必须以 `schedulable=false` 落地。目标机本地 OAuth 实测可临时启用一个账号，测试后立即恢复 false，直到正式切流门禁。
- 不允许并行切换两个 Edge。任何一个验收项失败，立即停止后续 fleet rollout。
- 最后一个 Edge 切换成功后，旧 Lightsail fleet 完整保留 1 天（连续 24 小时）。观察期未满，不允许删除 Lightsail 实例或 Static IP。
- Porkbun 凭据不在仓库，DNS 修改保持人工高风险门禁。自动化只能生成计划并核验结果，不能自行推断 DNS 写权限。
- 对 Unlimited 实例，`CPUCreditBalance=0` 不是宕机，也不能触发 P0/P1；借用和收费分别看 `CPUSurplusCreditBalance` 与 `CPUSurplusCreditsCharged`。
- 最终状态必须为 EC2-only：Lightsail 云资源、workflow、矩阵、路由分支、脚本、skill 和活跃文档全部删除。
- 用户已有未跟踪文件 `ssm-params-ghcr-prune.json` 与本任务无关，不得修改或删除。

## 目标 Fleet 与顺序

| 顺序 | Edge | EC2 区域 | Stack | 域名 | 当前 Lightsail IP | 迁移角色 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `us5` | `us-west-2` | `tokenkey-edge-us5-stage0` | `api-us5.tokenkey.dev` | `32.185.163.163` | 当前无账号，基础设施 canary |
| 2 | `us4` | `us-west-2` | `tokenkey-edge-us4-stage0` | `api-us4.tokenkey.dev` | `35.81.204.18` | 首个真实流量 canary，已暴露 credits/可达性问题 |
| 3 | `us6` | `us-east-2` | `tokenkey-edge-us6-stage0` | `api-us6.tokenkey.dev` | `3.148.79.145` | 当前无账号，第二区域 canary |
| 4 | `us3` | `us-east-2` | `tokenkey-edge-us3-stage0` | `api-us3.tokenkey.dev` | `18.220.195.44` | 最后迁移的薄边/SPOF |

切换前必须重新扫描账号与健康状态。如果届时 `us5` 已有账号，则改用仍无账号的 `us6` 做第一个基础设施 canary，之后依次迁移 `us4`、另一个无账号节点和 `us3`。除此之外不允许临时改顺序。

## 成本口径

此前的“约 `$19/月`”是**单台 Edge** 的固定成本，不是四台 fleet 总成本。`$76.46/月` 是四台按同一口径相加后的结果；成本模型没有从单台 19 美元涨到单台 76 美元。

| 组件 | 单台 Edge/月 | 4 台/月 |
| --- | ---: | ---: |
| EC2 `t4g.small` On-Demand | 约 `$12.26` | 约 `$49.06` |
| EBS gp3，共 40 GiB/台 | `$3.20` | `$12.80` |
| 公网 IPv4 | `$3.65` | `$14.60` |
| **固定合计** | **约 `$19.12`** | **约 `$76.46`** |
| 当前 Lightsail `micro_3_0` | `$12.00` | `$48.00` |
| **迁移后固定增量** | **约 `+$7.12`** | **约 `+$28.46`** |

四台 `t4g.small` 的 EC2 计算费本身已经约 `$49.06/月`，因此在“4 台、每台 `t4g.small`”这个目标不变时，fleet 固定成本不可能维持在 `$19/月`。

本次已基于上表完成人工成本评估并决定继续采用四台 `t4g.small`。表内金额只记录决策背景，不是部署契约、运行时预算或自动化门禁；Task 6 的 AWS/IAM/EC2 写操作、后续 DNS 与账号写操作仍须分别取得明确授权。

上表尚未包含：

- EC2 公网出站流量；Lightsail 每台包含 2 TB 流量，而 EC2 没有等价套餐，这是最大的不确定项。
- EBS 快照增量存储。
- CloudWatch 额外用量。
- Unlimited 超出 baseline 后的 surplus charge。
- 迁移观察期内 Lightsail 与 EC2 并行运行的短期重叠成本。

迁移自动化不为成本预测采集或保存 `NetworkOut`/Cost Explorer 数据，不在 evidence、部署 matrix 或 CloudFormation 中保存任何预算字段，也不因成本估算生成 blocker。`NetworkOut` 仍可作为候选机连通性信号使用。未来若要新增预算约束，必须作为独立决策重新设计，不能复用本文的静态估算。

## CPU Credits 监控口径

`t4g.small` 有 2 vCPU、2 GiB 内存，CPU baseline 为 20%，每小时获得 24 credits。Unlimited 的含义是：credits 用完后不因 credits 被限速，而是继续使用 CPU，并可能产生额外费用。

- `CPUUtilization`：容量信号；24 小时平均持续高于 20% 说明长期超过 baseline。
- `CPUCreditBalance`：只做仪表盘展示；Unlimited 下归零不代表性能故障。
- `CPUSurplusCreditBalance`：出现大于零说明开始借用 credits，发成本预警。
- `CPUSurplusCreditsCharged`：大于零说明已产生收费，发账单告警。
- 根卷/数据卷：沿用持续 85% 的磁盘告警门槛。

CloudWatch alarm 是 CPU/credits 状态的唯一探测 owner；实例已有的 host timer 读取 alarm state，并复用 `sync-feishu-config.sh` 注入的 webhook 发送 firing 与 recovery。EC2 Edge 不引入无人消费的可选 SNS 分支。飞书未配置、alarm 缺失或状态查询失败时不得写成功 latch，下一轮 timer 必须继续重试。

## 审查收敛契约（2026-08-05 对话批准）

- 迁移期基础设施容量固定为 `t4g.small`、20 GiB 根卷、20 GiB 数据卷、2 GiB swap 和 daily snapshot；matrix、resolver 与 CloudFormation 任一层偏离都必须失败。后续扩容另走成本评估与审批，不复用本次迁移授权。
- 成本仅为静态决策背景；代码、配置、workflow、预检和 evidence 均不得实现预算输入、实时成本预测或成本 blocker。
- `provision` 在分配 EIP 前必须用刚取得的只读 OIDC 权限运行完整 live migration preflight，并要求 `blockers=[]`；仓库里的历史 evidence 只供 review，不作为线上创建凭证。
- CloudFormation execution role ARN 从已校验的 `AWS_OIDC_ROLE_ARN` 账户号和固定 role name 确定性派生，不依赖未登记的 GitHub variable。
- 账号 extract 必须证明请求 ID 与返回 ID 精确一致，并完整携带 account-group bindings 和 fallback group 闭包；build 生成不含 secret value 的预期 manifest，load 后核对账号、credential key 集、group 与 binding。任一差异不得输出 `LOAD_OK`。
- `set-schedulable` 等单账号写操作必须要求恰好影响一行并回读最终值；零行、多行或最终值不符均失败，不得打印成功标记。

## 迁移状态机

```text
lightsail_active
  -> ec2_candidate_ready
  -> cutover_pending
  -> ec2_active_lightsail_standby
  -> observation_complete
  -> lightsail_retired
  -> ec2_only_codebase
```

迁移期的仓库表达：

- Lightsail owner：`deployable=true`。
- EC2 影子目标：`deployable=false`、`migration_candidate=true`。
- 单 Edge 切换 PR：Lightsail 改为 `deployable=false`；EC2 同时改为 `deployable=true`、`migration_candidate=false`。
- 默认路由只跟随唯一 `deployable=true` 的平台；仅迁移工具允许显式 `--platform ec2|lightsail`。
- 最终 cleanup 删除 `migration_candidate`、显式平台路由和 Lightsail matrix。

## 回滚边界

| 阶段 | 回滚方式 |
| --- | --- |
| DNS 切换前 | 只删除或重建 EC2 candidate；Lightsail 仍是正式 owner |
| DNS 已切、Lightsail 仍保留 | 启用源账号、A 记录切回已登记的 Lightsail IP、关闭目标账号、回滚矩阵 PR |
| 1 天观察完成但 Lightsail 尚未删除 | 同上；若凭据已变化，先把目标最新状态逻辑同步回源 |
| Lightsail 已删除 | 不再支持平台回滚；通过 EC2 retained data volume 或 EBS snapshot 恢复 |

## PR 边界

1. 审批 PR：只承载本文。merge 后改为 `status: approved` 并记录人工 approver。
2. 平台 PR：恢复 EC2 CFN/IAM/workflow、临时候选路由、迁移工具、测试和文档；代码合并本身不创建线上资源。
3. 四个状态 PR：每个 Edge 一个最小矩阵 owner 变更，只在该 Edge 的切流窗口合并。
4. 退役 PR：1 天观察完成且 Lightsail 云资源删除后，清理所有 Lightsail/临时迁移代码，重建 Agent 契约，并把本文改为 `shipped`。

### Task 1（任务 1）：建立只读迁移预检

**文件：**
- 新建：`ops/migration/edge-platform-migration-preflight.sh` <!-- script-ref: planned -->
- 新建：`ops/migration/test_edge_platform_migration_preflight.py` <!-- script-ref: planned -->
- 修改：`.gitignore`
- 修改：`scripts/preflight.sh`

**输出契约：**
- `edge-platform-migration-preflight.sh --format json --output docs/evidence/all-edge-ec2-migration-preflight.json`
- JSON 包含 `fleet`、`quotas`、`cpu_24h`、`dns`、`amis` 和 `blockers`，不包含成本或预算字段。
- 仅当两个区域都有 2 个 EIP、2 个 VPC 的余量，`t4g.small` 可用，ARM64 AL2023 AMI 可解析，所有源 SSM 可达，且 DNS 与 matrix 一致时退出 0。

- [ ] **步骤 1：先写 fixture 驱动的失败测试**

正向 fixture 断言四个目标和完整基础设施状态；负向覆盖 EIP 配额不足、DNS 漂移、SSM 不可达和非 ARM AMI，并断言输出不存在成本或预算字段。

- [ ] **步骤 2：确认测试先失败**

```bash
python3 -m unittest ops/migration/test_edge_platform_migration_preflight.py -v # script-ref: planned
```

预期：因预检脚本尚未实现而 FAIL。

- [ ] **步骤 3：实现只读 AWS/DNS 采集**

使用 AWS CLI JSON 和 Python JSON 解析，不解析 table 文本。查询 EC2 offerings/AMI、Service Quotas、EIP/VPC 使用量、SSM 状态、CloudWatch CPU 指标和公共 DNS。不得读取或打印 SecureString 值。

- [ ] **步骤 4：接入确定性测试**

```bash
python3 -m unittest ops/migration/test_edge_platform_migration_preflight.py -v # script-ref: planned
bash -n ops/migration/edge-platform-migration-preflight.sh # script-ref: planned
```

同时在 `.gitignore` 放行 `docs/evidence/`，保证脱敏 receipt 可以进入 review，而不是滞留在单台工作站。

- [ ] **步骤 5：运行真实只读报告并确认基础设施 blocker**

```bash
bash ops/migration/edge-platform-migration-preflight.sh --format json --output docs/evidence/all-edge-ec2-migration-preflight.json # script-ref: planned
```

任何 quota、容量、DNS 或 SSM blocker 都必须在任务 6 前暂停。

### Task 2（任务 2）：恢复加固后的通用 EC2 Edge Stack

**文件：**
- 新建：`deploy/aws/cloudformation/stage0-edge-ec2.yaml`
- 新建：`deploy/aws/stage0/test_stage0_edge_ec2_contract.py`
- 修改：`deploy/aws/stage0/build-cfn.sh`
- 修改：`deploy/aws/stage0/test_build_cfn.py`
- 复用：`deploy/aws/stage0/stage0-ec2-bootstrap.sh`
- 复用：`deploy/aws/stage0/stage0-ec2-userdata-launcher.sub.sh`

**输出契约：** `InstanceId`、`PublicIP`、`EipAllocationId`、`ApiUrl`、`DataVolumeId`、`CpuCreditMode` 和告警名称。

- [ ] **步骤 1：先写 CFN 契约测试**

固定断言 `t4g.small`、`CPUCredits: unlimited`、ARM64 AL2023、20 GiB 加密根卷、20 GiB retained 数据卷、2 GiB swap、SSM instance profile、EIP association，以及仅 443/8443 ingress；22/80 必须不存在。

- [ ] **步骤 2：证明历史模板不能直接复活**

```bash
python3 -m unittest deploy/aws/stage0/test_stage0_edge_ec2_contract.py -v
```

预期历史版本在 `t4g.micro`、缺少 Unlimited、端口过时、无 retained 数据卷等断言上 FAIL。

- [ ] **步骤 3：按当前共享实现重建模板**

复用 prod 已验证的数据卷/bootstrap 结构、Edge Caddy allowlist、disk/QA/GHCR timers、SSM 和飞书配置。EIP 由 stack 外部分配并绑定，IP 轮换不替换持久状态。

- [ ] **步骤 4：补齐 credits 与磁盘监控**

增加持续 CPU、surplus borrowing、surplus charged、根卷和数据卷告警。禁止为 `CPUCreditBalance=0` 创建宕机告警。host timer 必须读取三个 CPU alarm 的状态并通过现有飞书 webhook 发送配对的 firing/recovery；CloudWatch 状态不重复实现第二套阈值判断。

- [ ] **步骤 5：生成并验证 CFN 工件**

```bash
bash deploy/aws/stage0/build-cfn.sh
bash deploy/aws/stage0/build-cfn.sh --check
python3 -m unittest deploy/aws/stage0/test_build_cfn.py deploy/aws/stage0/test_stage0_edge_ec2_contract.py -v
aws cloudformation validate-template \
  --region us-west-2 \
  --template-body file://deploy/aws/cloudformation/stage0-edge-ec2.yaml
```

### Task 3（任务 3）：增加最小权限 EC2 Edge OIDC 与 Workflow

**文件：**
- 新建：`deploy/aws/cloudformation/cicd-oidc-ec2-edge-addon.yaml`
- 新建：`scripts/checks/ec2-edge-oidc-perm-coverage.py` <!-- script-ref: planned -->
- 新建：`scripts/checks/test_ec2_edge_oidc_perm_coverage.py` <!-- script-ref: planned -->
- 新建：`.github/workflows/deploy-edge-stage0.yml`
- 修改：`deploy/aws/cloudformation/cicd-oidc.yaml`
- 修改：`scripts/checks/workflow-edge-coverage.json`
- 修改：`scripts/checks/test_workflow_edge_coverage.py`
- 修改：`scripts/preflight.sh`

**输出契约：** 独立的 EC2 Edge addon policy 和 CFN execution role，仅允许 `us-east-2`、`us-west-2` 的 `tokenkey-edge-*-stage0`；workflow 支持 `provision|upgrade|rollback|smoke|rotate_egress_ip|decommission`。active Edge 的 `rotate_egress_ip` 必须显式确认人工 DNS 窗口，完成 EIP 绑定后进入 `pending_manual_dns`；旧 EIP 保留到 DNS 与公网健康验证完成。candidate 轮换不触达正式 DNS，成功后自动释放旧 EIP。

- [ ] **步骤 1：先写 IAM/workflow 负向契约测试**

缺少 EC2、CFN、EIP、SSM、`iam:PassRole` 或 credit specification 权限时必须失败。普通 planned target 不得使用候选 provision，stack confirmation 不匹配必须在申请 AWS 凭据前失败。

- [ ] **步骤 2：实现独立 addon**

不要把 EC2 Edge 创建权限重新塞进 prod OIDC 模板。addon 只附加必要 policy，并用独立 CFN execution role。保留现有 `edge-us3` 至 `edge-us6` OIDC trust 和 GitHub Environments。

- [ ] **步骤 3：实现复用共享 primitive 的 workflow**

强制 released multi-arch tag、精确 target confirmation、Environment approval、`aws cloudformation deploy --role-arn`、SSM health、飞书同步和现有 Edge smoke。execution role ARN 必须从已校验的 OIDC role ARN 确定性派生；`provision` 必须在分配 EIP 前重跑 live migration preflight。候选模式不得修改 DNS 或 owner。active EIP 轮换必须要求 `i_understand_active_rotation_requires_manual_dns=true`，并在 summary 中给出旧/新 EIP、待人工修改的 Porkbun A 记录和 DNS 验证后的旧 EIP 释放命令；不得把 `pending_manual_dns` 冒充完整成功。迁移期间 EC2/Lightsail 两条 workflow 必须共享 `edge-stage0-${edge_id}` concurrency group，且 `cancel-in-progress: false`。

- [ ] **步骤 4：验证权限和 workflow 覆盖**

```bash
python3 scripts/checks/ec2-edge-oidc-perm-coverage.py --quiet # script-ref: planned
python3 -m unittest scripts/checks/test_ec2_edge_oidc_perm_coverage.py scripts/checks/test_workflow_edge_coverage.py -v # script-ref: planned
```

### Task 4（任务 4）：引入临时影子平台路由

**文件：**
- 修改：`deploy/aws/stage0/edge-targets.json`
- 修改：`deploy/aws/lightsail/edge-targets-lightsail.json`
- 修改：`deploy/aws/stage0/resolve-edge-target.py`
- 修改：`deploy/aws/stage0/test_resolve_edge_target_list_deployable.py`
- 修改：`deploy/aws/stage0/test_resolve_edge_target_prod_ops_matrix_lightsail.py`
- 修改：`ops/stage0/edge_routing_matrix.py`
- 修改：`ops/stage0/edge_ssm_execution.py`
- 修改：`scripts/stage0/resolve-edge-deploy-route.py`
- 修改：`scripts/stage0/dispatch-edge-deploy.sh`
- 修改：`scripts/test_resolve_edge_deploy_route.py`
- 修改：`scripts/checks/edge-platform-exclusivity.py`
- 修改：`scripts/checks/test_edge_platform_exclusivity.py`

**接口：**
- `resolve_target(data: dict, edge_id: str, *, confirm_stack: str = "", profile: str = "", allow_planned: bool = False, allow_migration_candidate: bool = False) -> dict`
- `edge_ssm_execution.py --edge-id us4 --platform auto|ec2|lightsail --format json`

- [ ] **步骤 1：登记四个 EC2 candidate**

使用目标表中的区域、stack 和域名；统一写入 `instance_type=t4g.small`、`root_volume_gib=20`、`data_volume_gib=20`、`swap_gib=2`、`snapshot_schedule=daily`、四个 `/tokenkey/edge/us*` SSM prefix、`deployable=false` 和 `migration_candidate=true`。不得加入成本或预算字段。

- [ ] **步骤 2：先写路由测试**

断言 auto 仍走 Lightsail，显式 EC2 可到 candidate；候选不进入定时 diagnostics/rollout/health-watch；双平台同时 `deployable=true` 仍被拒；普通 planned target 不能冒充 candidate。

- [ ] **步骤 3：实现最小候选状态，不新增第三套 registry**

- [ ] **步骤 4：运行聚焦测试**

```bash
python3 -m unittest \
  deploy/aws/stage0/test_resolve_edge_target_list_deployable.py \
  deploy/aws/stage0/test_resolve_edge_target_prod_ops_matrix_lightsail.py \
  scripts/test_resolve_edge_deploy_route.py \
  scripts/checks/test_edge_platform_exclusivity.py -v
```

### Task 5（任务 5）：让账号迁移显式区分平台

**文件：**
- 修改：`ops/migration/migrate-edge-accounts.py`
- 新建：`ops/migration/test_migrate_edge_accounts.py` <!-- script-ref: planned -->

**接口：**
- `parse_target("edge:us4@lightsail") -> ("edge", "us4", "lightsail")`
- `parse_target("edge:us4@ec2") -> ("edge", "us4", "ec2")`
- 保留 `prod` 与旧 `edge:<id>` 行为；所有写操作继续默认 dry-run，只有 `--execute` 才执行。

- [ ] **步骤 1：先写 parser/resolver 测试**

覆盖两个显式平台、`prod`、未知平台、缺失 Edge，以及同一 Edge ID 的两个平台解析为不同 SSM instance ID。

- [ ] **步骤 2：写迁移安全测试**

断言目标账号强制 `status=active`、`schedulable=false`，清理 host-local cooldown/error/proxy/tier，保留 credential JSON，重建 group bindings 与 fallback group 闭包，并触发一次 scheduler full rebuild；请求账号必须全部存在，load 后必须核对账号、credential key 集、group 与 binding，单账号状态写入必须恰好影响一行。日志不得出现 secret value。本地 credential 缓存目录必须为 `0700`、文件必须为 `0600`；远端临时文件使用 `umask 077` 和退出清理；S3 临时对象在下载、加载或验证失败时也必须删除。

- [ ] **步骤 3：实现平台参数透传和隔离缓存标签**

- [ ] **步骤 4：运行测试与无账号 fail-closed 验证**

```bash
python3 -m unittest ops/migration/test_migrate_edge_accounts.py -v # script-ref: planned
python3 ops/migration/migrate-edge-accounts.py extract \
  --from edge:us5@lightsail \
  --account-ids ""
```

预期第二条因缺少账号 ID 失败且不产生写入；真实 ID 只能来自任务 1 的新鲜报告。

### Task 6（任务 6）：创建并验证 `us5` EC2 基础设施 Canary

**证据文件：** `docs/evidence/all-edge-ec2-migration-us5.json`。本任务不修改 DNS 或 owner。

- [ ] **步骤 1：经审批部署 IAM addon**

```bash
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-cicd-ec2-edge-addon \
  --template-file deploy/aws/cloudformation/cicd-oidc-ec2-edge-addon.yaml \
  --capabilities CAPABILITY_NAMED_IAM
```

- [ ] **步骤 2：解析当前 release 并创建 candidate**

首次创建每个区域的 candidate 前，先通过受控 secret 通道把 GHCR pull token 写入该 Edge 的 SecureString：`/tokenkey/edge/<edge_id>/ghcr/pat`。不得在命令行、日志或 evidence 中展开 token 值；workflow 会在创建 EIP/stack 前校验参数存在且类型为 `SecureString`。当前 GHCR 即使为 public，也继续使用该认证路径，保持与 prod bootstrap 的镜像拉取契约一致。

workflow 取得 OIDC 后、创建 EIP 前自动重跑 live migration preflight；任一 blocker 都终止本次 provision。CloudFormation execution role ARN 由 workflow 从 OIDC role ARN 的账户号确定性派生，无需另设 `AWS_EC2_EDGE_CFN_ROLE_ARN`。

```bash
EDGE_MIGRATION_TAG="$(gh release list -L 1 --json tagName --jq '.[0].tagName' | sed 's/^v//')"
gh workflow run deploy-edge-stage0.yml \
  -f edge_id=us5 \
  -f operation=provision \
  -f tag="${EDGE_MIGRATION_TAG}" \
  -f confirm_stack=tokenkey-edge-us5-stage0 \
  -f allow_migration_candidate=true
```

- [ ] **步骤 3：实测 credits、磁盘、端口和 SSM**

```bash
aws ec2 describe-instance-credit-specifications \
  --region us-west-2 \
  --instance-ids "${EDGE_MIGRATION_INSTANCE_ID}" \
  --query 'InstanceCreditSpecifications[0].CpuCredits' \
  --output text
```

必须返回 `unlimited`。同时验证真实机型为 `t4g.small`、SSM `Online`、数据卷挂载到 `/var/lib/tokenkey`、2 GiB swap，以及安全组只开放 443/8443。

- [ ] **步骤 4：DNS 前跑 SSM-local smoke**

验证 Docker health、PostgreSQL migrations、Redis 重建、Caddy config、上游出站、disk metrics、飞书配置和 local/infra smoke。外部证书与 HTTPS 留到 DNS 切换后验证。

- [ ] **步骤 5：观察 1 小时**

要求实例/SSM 不掉线、无磁盘/内存 P0、`NetworkOut` 非零、credit mode 仍为 Unlimited、无意外 `CPUSurplusCreditsCharged`。证据只记录资源 ID、IP、时间和结果，不记录 secret。

### Task 7（任务 7）：执行单个 Edge 切换事务

**文件：**
- 新建：`ops/migration/edge-platform-cutover-check.sh` <!-- script-ref: planned -->
- 新建：`ops/migration/test_edge_platform_cutover_check.py` <!-- script-ref: planned -->
- 新建：`docs/evidence/all-edge-ec2-migration-<edge>.json`
- 每次修改两个 matrix 中同一个 Edge 的 owner 状态

**工具契约：** 只提供 `plan`、`post-dns`、`rollback-ready` 三种只读检查；必须传 Edge、源 IP、目标 EIP 和 DNS 核验值；不得写 DNS、账号或 AWS 资源。

- [ ] **步骤 1：先写 fail-closed 测试**

拒绝过期账号清单、源/目标 SSM 失败、非 Unlimited、目标账号提前启用、DNS 指向未知 IP、缺少回滚 IP 和双平台同时 deployable。

- [ ] **步骤 2：做最终账号逻辑迁移**

真实流量 canary `us4` 从 `edge:us4@lightsail` 提取新鲜账号，原名/原组构建并加载到 `edge:us4@ec2`，核对 rows/groups/bindings/credential keys。其它有账号 Edge 使用同一参数化事务；无账号 Edge 记录已核验的 zero-account posture，不做伪迁移。

- [ ] **步骤 3：在目标机跑真实 OAuth/model smoke**

临时启用一个目标账号，跑真实模型请求，随后立即恢复 `schedulable=false`。OAuth、refresh 或模型探测失败均阻断 DNS。

- [ ] **步骤 4：准备并审批单 Edge 状态 PR**

只切一个 Edge：Lightsail `deployable=false`，EC2 `deployable=true`、`migration_candidate=false`。运行路由、exclusivity、workflow coverage 和完整 preflight；DNS operator 在场时才 merge。

- [ ] **步骤 5：启用目标账号并切 DNS**

先启用目标账号，源账号在 DNS drain 期间保持启用。Porkbun A 记录从源 Lightsail IP 改为 candidate EIP，TTL 设为 600；权威 DNS 与 `@1.1.1.1` 必须只返回新 EIP。

- [ ] **步骤 6：验证外部 TLS 与正式流量**

等待 Caddy 通过 TLS-ALPN-01 获取证书，要求公网 `/health` 200、full Edge smoke 通过、prod 主机解析到新 EIP、EC2 access log 收到正式请求，且源 Lightsail 业务流量开始归零。

- [ ] **步骤 7：进入 10 分钟 drain 与切流观察窗口**

以权威 DNS 和 `@1.1.1.1` 只返回目标 EIP、目标 EC2 收到正式业务流量的时间为观察起点。观察期间源账号保持启用以便快速回滚，但源端不得再收到业务请求。

- [ ] **步骤 8：完成 10 分钟验收并关闭源账号**

连续 10 分钟要求：SSM 在线；`/health` 与真实 OAuth smoke 通过；无 P0/P1；无新增持续 5xx；served ratio 相比切换前 2 小时下降不超过 5 个百分点；p95 延迟低于源基线 2 倍；credit mode 仍为 Unlimited；源 Lightsail 无业务请求。通过后把源端对应账号设为 `schedulable=false`。源数据库、实例、Static IP 和 SSM 注册继续保留供 1 天内回滚。surplus charge 只作为成本异常，不冒充可用性故障。

- [ ] **步骤 9：验证飞书恢复通知**

运行真实 edge-health watcher。若该 Edge 存在未恢复的 unreachable/degraded 告警，必须收到飞书恢复通知并推进 state key；未恢复通知是 rollout blocker。

- [ ] **步骤 10：任一验收失败立即回滚**

重新启用源账号、DNS 切回证据中的 Lightsail IP、关闭目标账号、验证源 `/health` 和 OAuth smoke、回滚状态 PR。用户路径失败时不继续向前修补。

### Task 8（任务 8）：顺序迁移全部 Fleet

- [ ] **步骤 1：完成 `us5` 切换并观察 10 分钟**
- [ ] **步骤 2：完成 `us4` 真实流量切换并观察 10 分钟**

`us4` 的旧 unreachable/credits 事件必须由成功健康扫描关闭；应发送恢复通知时，必须实际收到。

- [ ] **步骤 3：创建并切换 `us6`，观察 10 分钟**
- [ ] **步骤 4：创建并切换 `us3`，观察 10 分钟**

单账号/SPOF 状态单独记录，平台迁移不宣称解决账号冗余。

- [ ] **步骤 5：启动 fleet 退役观察期**

以 `us3` 最后一个 10 分钟验收完成时间作为 `fleet_observation_started_at`，四个 Edge 连续健康满 24 小时（1 天）后才进入任务 9。

### Task 9（任务 9）：退役线上 Lightsail Fleet

**临时文件：**
- `ops/migration/retire-lightsail-fleet.sh` <!-- script-ref: planned -->
- `ops/migration/test_retire_lightsail_fleet.py` <!-- script-ref: planned -->
- `docs/evidence/all-edge-ec2-migration-retirement.json`

脚本默认只输出计划；破坏性执行必须显式传：

```text
--apply --confirm retire-lightsail-us3-us4-us5-us6-after-one-day
```

若 EC2 未全绿、任一 Lightsail 仍 deployable、DNS 不匹配 EC2 EIP、源账号仍可调度、连续 24 小时证据不足或 EC2 数据卷快照缺失，脚本必须拒绝执行。

- [ ] **步骤 1：用 fixture 测试全部破坏性门禁**

覆盖错误 DNS、观察期不足、源账号仍开启、缺快照、出现计划外 Lightsail 资源和部分删除后重试；脚本必须幂等并先打印精确目标。

- [ ] **步骤 2：做最终加密逻辑备份与 EC2 快照**

通过现有 SSM/S3 安全链路导出每个源数据库，生命周期 30 天，不把 credentials 写入日志；为四个 EC2 数据卷创建并验证快照。证据只保存 S3 key、snapshot ID 和 checksum。

- [ ] **步骤 3：运行 plan mode 并取得删除审批**

```bash
bash ops/migration/retire-lightsail-fleet.sh # script-ref: planned
```

- [ ] **步骤 4：逐个删除 `us5`、`us4`、`us6`、`us3` 的 Lightsail 资源**

每个节点按顺序删除实例、注销记录的 `mi-*`、删除对应 `/tokenkey/lightsail/us*/*` 参数、释放 Static IP，并把旧 IP 记录为 `platform-retirement` 历史。每删一个都重新验证其 EC2 域名健康，再继续下一个。

- [ ] **步骤 5：删除账户级 Lightsail addon**

确认无 TokenKey Lightsail 实例或 managed instance 后，删除 `tokenkey-cicd-lightsail-addon` stack。保留 `edge-us3` 至 `edge-us6` GitHub Environments，EC2 workflow 继续使用它们。

- [ ] **步骤 6：证明云端零残留**

要求不存在 TokenKey Lightsail instance、Static IP、`Platform=lightsail` 的 SSM Hybrid managed instance、`/tokenkey/lightsail/` 参数、残留 activation 或 Lightsail addon IAM policy/role；重新实测四个 EC2 Edge。

### Task 10（任务 10）：把仓库收敛为 EC2-only

**删除：**
- `.cursor/skills/tokenkey-stage0-edge-lightsail-expansion/`
- `.cursor/skills/tokenkey-stage0-edge-lightsail-ip-rotation/`
- `.cursor/skills/tokenkey-stage0-edge-platform-migration/`
- `.github/workflows/deploy-edge-lightsail-stage0.yml`
- `deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml`
- `deploy/aws/lightsail/`
- `ops/lightsail/`
- `ops/stage0/verify-edge-lightsail-network.sh`
- `ops/stage0/test_verify_edge_lightsail_network.py`
- `scripts/checks/lightsail-oidc-perm-coverage.py`
- `scripts/checks/test_lightsail_oidc_perm_coverage.py`
- `scripts/checks/edge-platform-exclusivity.py`
- `scripts/checks/test_edge_platform_exclusivity.py`
- `deploy/aws/stage0/test_resolve_edge_target_prod_ops_matrix_lightsail.py`
- `scripts/stage0/resolve-edge-deploy-route.py`
- `scripts/test_resolve_edge_deploy_route.py`
- 任务 1、7、9 的临时迁移脚本及测试

**修改：**
- `.cursor/skills/tokenkey-stage0-edge-expansion/SKILL.md`
- `.cursor/skills/tokenkey-stage0-edge-ip-rotation/SKILL.md`
- `.cursor/skills/tokenkey-anthropic-oauth-config/SKILL.md`
- `.cursor/skills/tokenkey-anthropic-oauth-priority-by-window/SKILL.md`
- `.cursor/skills/tokenkey-online-log-troubleshooting/SKILL.md`
- `.cursor/skills/tokenkey-online-traffic-profile/SKILL.md`
- `.cursor/skills/tokenkey-servable-model-refresh/SKILL.md`
- `.cursor/skills/tokenkey-stage0-release-rollout/SKILL.md`
- `deploy/aws/stage0/edge-targets.json`
- `deploy/aws/stage0/resolve-edge-target.py`
- `ops/stage0/edge_routing_matrix.py`
- `ops/stage0/edge_ssm_execution.py`
- `ops/migration/migrate-edge-accounts.py`
- `scripts/stage0/dispatch-edge-deploy.sh`
- `scripts/stage0/rollout-edges.sh`
- `scripts/checks/workflow-edge-coverage.py`
- `scripts/checks/workflow-edge-coverage.json`
- `scripts/checks/test_workflow_edge_coverage.py`
- `scripts/preflight.sh`
- `.github/workflows/ops-daily-diagnostics.yml`
- `.github/workflows/fleet-feishu-config-sync.yml`
- `.github/workflows/ops-stage0-ghcr-prune-timer.yml`
- `.github/workflows/ops-stage0-host-mem-guard.yml`
- `.github/workflows/ops-stage0-pg-dump-refresh.yml`
- `deploy/aws/cloudformation/cicd-oidc.yaml`
- `deploy/aws/README.md`
- `docs/global/agent-reference.md`
- `docs/deploy/tokenkey-edge-ip-history.md`
- `docs/spec-delta/README.md`
- 由生成器更新 `docs/agent_integration.md`
- 由 `dev-rules/sync.sh --local` 更新 `AGENTS.md`

**新建：**
- `scripts/checks/edge-platform-contract.py` <!-- script-ref: planned -->
- `scripts/checks/test_edge_platform_contract.py` <!-- script-ref: planned -->
- `docs/archive/deploy/edge-lightsail-retired.md`

- [ ] **步骤 1：先建立最终 EC2-only 契约测试**

断言四个活动 target 都是合法 EC2 target、EC2 workflow 存在、禁止路径不存在、活跃 skill/docs 不再把 Agent 引向 Lightsail；archive/history 明确排除在文字残留检查之外。

- [ ] **步骤 2：为公共契约删除加入显式说明**

commit subject/body 必须带 `contract-deletion-notice`，列出被删 workflow、CLI 参数、skill、matrix 和替代 EC2 入口。

- [ ] **步骤 3：删除 Lightsail 与临时迁移文件**

只有任务 9 云端删除证据齐全后才能执行。`edge-lightsail.md` 移到 archive，并从活跃 spec 索引删除。

- [ ] **步骤 4：简化所有混合路由**

删除 Lightsail JSON load、`mi-*`/Hybrid resolution、显式 platform flag、candidate 字段、exclusivity 逻辑、Lightsail diagnostics skip 和相关 opt-out/comment，只保留一个 EC2 owner 路径。

- [ ] **步骤 5：重写两个保留的 EC2 skill**

`tokenkey-stage0-edge-expansion` 改为当前 EC2/CFN 新增 Edge 工作流；`tokenkey-stage0-edge-ip-rotation` 改为当前 EIP 轮换工作流。不得继续保留迁移历史或 Lightsail redirect prose。

- [ ] **步骤 6：重新生成 Agent 契约与导航**

```bash
python3 scripts/export_agent_contract.py
python3 scripts/export_agent_contract.py --check
dev-rules/sync.sh --local
dev-rules/sync.sh --check
```

生成后的 Agent 文档与 `AGENTS.md` 只能暴露 EC2 Edge skill/入口，不得出现已删除 Lightsail skill。

- [ ] **步骤 7：运行零残留与聚焦测试**

```bash
python3 scripts/checks/edge-platform-contract.py # script-ref: planned
python3 -m unittest scripts/checks/test_edge_platform_contract.py -v # script-ref: planned
python3 -m unittest discover -s ops/stage0 -p 'test_*.py' -t ops/stage0 -v
python3 -m unittest discover -s scripts -p 'test_*.py' -t scripts -v
bash deploy/aws/stage0/build-cfn.sh --check
```

### Task 11（任务 11）：最终实测与收口

- [ ] **步骤 1：运行真实 fleet health scan**

```bash
bash ops/observability/scan-edge-health.sh --with-prod
```

预期 `us3`、`us4`、`us5`、`us6`、`prod` 均可达，不再经过 Lightsail/Hybrid。`us3` 在补号前可以是 `thin`，但不能是 `unreachable` 或 `down`。

- [ ] **步骤 2：核对四台 credits 信号**

逐台确认 `CpuCredits=unlimited`，采集 24 小时 `CPUUtilization`、`CPUCreditBalance`、`CPUSurplusCreditBalance` 和 `CPUSurplusCreditsCharged`。仅 credit balance 归零不导致验收失败。

- [ ] **步骤 3：运行仓库出口门禁**

```bash
./scripts/preflight.sh
```

- [ ] **步骤 4：把审批基线标记为 shipped**

将本文改为 `status: shipped`，填写真实 `approved_by`、`approved_at`、`shipped_at` 和全部相关 PR；同步更新 `docs/approved/README.md`。

## 最终验收标准

- 四个活动 Edge 域名均指向独立 EC2 EIP，实例实测为 AL2023 ARM64 `t4g.small` 且 CPU Unlimited。
- PostgreSQL 状态和 credential blobs 完整迁移，目标账号通过真实模型请求，Redis 已重建，源账号全部不可调度。
- 根卷/数据卷、SSM、CloudWatch 和飞书控制符合目标契约。
- 每个 Edge 完成 10 分钟切流验收，之后完整 fleet 连续稳定 24 小时（1 天）才退役旧平台。
- 所有旧 Lightsail instance、Static IP、SSM Hybrid registration/parameter/activation 和 addon IAM 资源均已删除。
- 活跃代码、workflow、matrix、脚本、skill、Agent 导航和文档只保留 EC2 Edge 平台。
- Lightsail 历史只存在于 git history 或明确的 archive/history 文档，Agent 无法把它选成活动工作流。

## 明确不做

- 不在本迁移中给 `us3`、`us5`、`us6` 补账号；账号容量/SPOF 是另一项运营决策。
- 不迁移 `prod`；prod 保持现有 EC2/CFN 平台与机型。
- 不引入 ALB、Auto Scaling、多 AZ PostgreSQL 或托管 Redis。
- 本迁移不购买 Savings Plans 或 Reserved Instances。
- 不把 Porkbun 凭据写入仓库，也不自动执行 DNS 写操作。
