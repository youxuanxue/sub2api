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
- `deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml`：在现有 GHA OIDC role 上
  附加 Lightsail API + SSM Hybrid Activation + managed-instance SendCommand 权限
- `.github/workflows/deploy-edge-lightsail-stage0.yml`：`provision` / `upgrade` /
  `rollback` / `smoke`（镜像校验与 SSM 升级 primitive 与 EC2 Edge 共用）
- SSM 状态参数前缀：`/tokenkey/lightsail/<edge_id>/`（managed instance id、instance name 等）

### MODIFIED

- 无 EC2/CFN 模板或现有 `deploy-edge-stage0.yml` 行为变更

### REMOVED

- 无

## Scenarios

### 正向

1. **provision**：workflow 创建 SSM Hybrid Activation → Lightsail 实例 launch script
   注册 SSM → 写入 managed instance id → 分配 Static IP → DNS 后 smoke 通过
2. **upgrade**：从 SSM 读 `mi-*` → `deploy_via_ssm.sh` 换 tag → external health +
   `edge_post_deploy_smoke.sh` 通过

### 负向

1. `edge-targets-lightsail.json` 中 `deployable=false` → resolve 脚本 fail-before-AWS
2. 未部署 `cicd-oidc-lightsail-addon` → workflow 在 Lightsail/SSM API 处 fail
3. Hybrid Activation 过期或未注册 → upgrade/smoke 无法 SendCommand

### 回归

- 现有 `deploy-edge-stage0.yml` + EC2 Edge 栈不受影响

## Validation

```bash
python3 deploy/aws/lightsail/test_resolve_edge_lightsail_target.py
bash deploy/aws/lightsail/render-bootstrap.sh --check
python3 scripts/export_agent_contract.py --check
```

Lightsail 实机 provision/smoke：**未验证**（需账户内 Lightsail + addon IAM 一次性 setup）。
