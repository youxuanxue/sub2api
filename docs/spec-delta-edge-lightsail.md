# Edge Lightsail 并行部署路径（spec delta）

## Background

现有 Edge Stage0 基于 EC2 + CloudFormation + SSM Run-Command，与 prod 共享
`ops/stage0/deploy_via_ssm.sh`、烟测与 diagnostics 入口。项目规划文档
`docs/deploy/tokenkey-multiregion-egress-gateway-plan.md` §6.1 已判定
**EC2/CFN 为默认 Edge 路径**（OPC：多 Edge 时避免 N 套 runbook 漂移）。

本 delta 新增 **与 EC2 并行的 Lightsail Edge 路径**，用于成本/区域覆盖实验；
**不替换** `deploy-edge-stage0.yml` 与 `stage0-edge-ec2.yaml`。

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
4. **IP rotation**：`ops/lightsail/rotate-static-ip.sh <edge_id> --apply` 三步换 IP
   并把 `${ssm_prefix}/public_ip` 同步更新

### 负向

1. `edge-targets-lightsail.json` 中 `deployable=false` → resolve 脚本 fail-before-AWS
2. 未部署 `cicd-oidc-lightsail-addon` → workflow 在 Lightsail/SSM API 处 fail
3. Hybrid Activation 过期或未注册 → user-data 中 `amazon-ssm-agent -register` 直接
   fail-fast（不再被 `|| true` 吞掉），provision step 报 `BOOTSTRAP_FAIL:` 终止
4. `recreate=false`（默认）下实例已存在 → `provision-edge.sh` 立刻 `::error::` 退出，
   不会静默销毁；要销毁重建必须显式 `recreate=true`
5. `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` Environment 未配置 → provision step 立即报错
   （workflow 没有兜底默认；硬编码 prod IP 与 IP rotation 实践冲突，已彻底移除）

### 回归

- 现有 `deploy-edge-stage0.yml` + EC2 Edge 栈不受影响
- prod / EC2 edge 在 ops-daily-diagnostics 的处理路径未变（platform=ec2 走 CFN 分支）

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
