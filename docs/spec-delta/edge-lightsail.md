# Edge Lightsail 部署路径（spec delta）

> **2026-06-07 更新：Lightsail 是 edge 的唯一路径。** EC2/CFN 的 edge 路径已移除
> （`deploy-edge-stage0.yml`、`stage0-edge-ec2.yaml`、EIP 轮换工具已删除，
> `edge-targets.json` 清空为 stub）。本 delta 此前把 Lightsail 描述为「与 EC2 并行的
> 实验路径」——该定位已废止；现在它是 edge 的唯一实现。**prod 主网关仍是 EC2/CFN，不受影响。**

## Background

最初 Edge Stage0 基于 EC2 + CloudFormation + SSM Run-Command，与 prod 共享
`ops/stage0/deploy_via_ssm.sh`、烟测与 diagnostics 入口；本 delta 当初新增 Lightsail
作为并行降本/区域覆盖路径。此后多 Edge 全部收敛到 Lightsail，EC2 edge 路径整体退役，
Lightsail 成为唯一 edge 实现（与 prod 仍共享上述 SSM/smoke/diagnostics primitive）。

## Delta

### ADDED

- `deploy/aws/lightsail/*`：Lightsail 矩阵、bootstrap 渲染、provision 脚本、README
- `deploy/aws/lightsail/generated-launch-script.sh`：render-bootstrap 产物，**进版本控制**，
  preflight 通过 `bash deploy/aws/lightsail/render-bootstrap.sh --check` 强制与 Stage0 源一致
- `deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml`：在现有 GHA OIDC role 上
  附加 Lightsail API + SSM Hybrid Activation + managed-instance SendCommand 权限
- `.github/workflows/deploy-edge-lightsail-stage0.yml`：`provision` / `upgrade` /
  `rollback` / `smoke`（镜像校验与 SSM 升级 primitive 与 EC2 Edge 共用）
- SSM 状态参数前缀：`/tokenkey/lightsail/<edge_id>/`（managed instance id、instance name 等）
- `ops/lightsail/rotate-static-ip.sh`：Lightsail Static IP 三步轮换（allocate → swap → release），
  受 `tokenkey-stage0-edge-lightsail-ip-rotation` skill 驱动
- `.cursor/skills/tokenkey-stage0-edge-lightsail-expansion/SKILL.md`：新增 Lightsail edge 全流程
- `.cursor/skills/tokenkey-stage0-edge-lightsail-ip-rotation/SKILL.md`：Lightsail IP 污染轮换

### MODIFIED

- `deploy/aws/stage0/resolve-edge-target.py --prod-ops-matrix`：把 Lightsail deployable edges
  也吐进 target matrix，每条 target 带 `platform` 字段（`ec2` / `lightsail`）
- `.github/workflows/ops-daily-diagnostics.yml`：按 `platform` 分支——`lightsail` 边沿从 SSM
  `${ssm_prefix}/ssm_managed_instance_id` 解析 `mi-*`，跳过 CloudFormation describe；
  error_clustering 在 Lightsail 上标 `skip`（installer 仍 EC2/CFN 耦合），健康 + 日志信号计数与 EC2 同等
- `ops/stage0/external_health.sh`：支持 `SSM_PREFIX + AWS_REGION + DOMAIN` 形态作为 Lightsail
  调用入口；原 lightsail-only health 脚本（旧 `ops/lightsail/` 目录下的同名 shim）收敛删除
- `scripts/preflight.sh`：新增 `lightsail edge launch-script drift` 段；把
  `deploy/aws/lightsail` 纳入 determinism-baseline 单测发现

### REMOVED

- 旧的 lightsail-only `external_health` shim（收敛进 `ops/stage0/external_health.sh`）
- 工作流默认值 `EDGE_MAIN_GATEWAY_ALLOWED_CIDR=34.194.234.88/32`（必须由 GitHub Environment 显式提供）

## Scenarios

### 正向

1. **provision**：workflow 创建 SSM Hybrid Activation → Lightsail 实例 launch script
   注册 SSM → **`DescribeInstanceInformation` 以本次 `ActivationId`（`ActivationIds` 过滤器）
   主路径解析 `mi-*`（不可用「标签过滤器 + ResourceType」混用）；bootstrap `hostnamectl` 对齐 Lightsail `instance_name` 作为兜底** → 写入 managed instance id → 分配 Static IP → DNS 后 smoke 通过
2. **upgrade**：从 SSM 读 `mi-*` → `deploy_via_ssm.sh` 换 tag → external health +
   `edge_post_deploy_smoke.sh` 通过
3. **daily diagnostics**：`ops-daily-diagnostics.yml` 自动把 deployable Lightsail
   edge 接入矩阵，复用 SSM SendCommand 跑 docker ps / 健康 / 日志信号计数；同
   一份 ops-report 同时覆盖 prod + EC2 edge + Lightsail edge
4. **IP rotation**：`ops/lightsail/rotate-static-ip.sh <edge_id> --apply` 三步换 IP、
   拒绝落在 exclusion registry 的候选 IP、自动 append 旧 IP 到
   `deploy/aws/stage0/edge-polluted-ips.json`，并把 `${ssm_prefix}/public_ip` 同步更新

### 负向

1. `edge-targets-lightsail.json` 中 `deployable=false` → resolve 脚本 fail-before-AWS
2. 未部署 `cicd-oidc-lightsail-addon` → workflow 在 Lightsail/SSM API 处 fail
3. Hybrid Activation 过期或未注册 → user-data 中 `amazon-ssm-agent -register` 直接
   fail-fast（不再被 `|| true` 吞掉），provision step 报 `BOOTSTRAP_FAIL:` 终止
4. `recreate=false`（默认）下实例已存在 → `provision-edge.sh` 立刻 `::error::` 退出，
   不会静默销毁；要销毁重建必须显式 `recreate=true`
5. `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` Environment 未配置 → provision step 立即报错
   （workflow 没有兜底默认；硬编码 prod IP 与 IP rotation 实践冲突，已彻底移除）
6. 运维/CI 用错 **SSM 区域**（误用 `ec2_equivalent_region` 调 `aws ssm`，或与
   `edge_ssm_execution.py` 解析出的 `REGION` 不一致）→ Parameter / managed instance
   查空或 SendCommand 错目标；**Lightsail 路径下一律以矩阵 `lightsail_region` 为准**

### 回归

- prod 主网关（EC2/CFN）不受影响——本 delta 只动 edge。
- （历史）此前与 EC2 edge 并存时，EC2 edge 在 ops-daily-diagnostics 走 CFN 分支；2026-06-07 EC2 edge 路径移除后，edge 全部走 Lightsail（platform=lightsail）分支。

## Validation

```bash
# 解析器单测（Lightsail 矩阵 + EC2 矩阵的 platform 字段联合用例）
python3 -m unittest discover -s deploy/aws/lightsail -p 'test_*.py' -t deploy/aws/lightsail -v
python3 -m unittest deploy.aws.stage0.test_resolve_edge_target_prod_ops_matrix_lightsail -v

# render-bootstrap 漂移门禁（已接入 preflight）
bash deploy/aws/lightsail/render-bootstrap.sh --check

# 完整 preflight
PREFLIGHT_BASE=origin/main bash scripts/preflight.sh
```

Lightsail 实机 provision/smoke：**未验证**（需账户内 Lightsail + addon IAM 一次性 setup）。
首次实机 provision 接 `tokenkey-stage0-edge-lightsail-expansion` skill 走 `operation=full`。

## Delivered / Deferred 矩阵

| 维度 | 状态 | 入口 |
|---|---|---|
| Matrix + resolver（多 region / bundle / SSM 前缀） | Delivered | `deploy/aws/lightsail/edge-targets-lightsail.json`、`resolve-edge-lightsail-target.py` |
| Provision / Upgrade / Rollback / Smoke workflow | Delivered | `.github/workflows/deploy-edge-lightsail-stage0.yml` |
| IAM addon（一次性） | Delivered | `deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml` |
| Bootstrap 漂移检测 | Delivered | preflight § `lightsail edge launch-script drift` |
| External health 调用形态收敛 | Delivered | `ops/stage0/external_health.sh`（SSM_PREFIX 分支） |
| Static IP rotation（污染恢复） | Delivered | `ops/lightsail/rotate-static-ip.sh` + skill |
| Edge expansion runbook（人 + 机械） | Delivered | `tokenkey-stage0-edge-lightsail-expansion` skill |
| 日常诊断接入（health + log signal counts via SSM） | Delivered | `ops-daily-diagnostics.yml`（platform 分支） |
| Error clustering 自动 install + run | Deferred | installer EC2/CFN 耦合；先标 `skip`，待 Lightsail 边沿活跑后再决定是否做平台兼容版本 |
| 实机 provision/smoke 验证 | Deferred | 需账户内 Lightsail quota + 一次性 addon 部署；首跑由 skill 驱动 |
| 多区跨 Lightsail Static IP 池规划 | Deferred | 等首批 fra1 / sg1 真实上线之后再评估 |
