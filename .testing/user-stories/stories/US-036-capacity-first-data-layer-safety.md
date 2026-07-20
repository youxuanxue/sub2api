# US-036-capacity-first-data-layer-safety

- ID: US-036
- Title: Capacity-first 数据层安全原型
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **先用有硬超时的只读容量证据和无替换扩盘计划解除磁盘倒计时**，**以便** 在不影响线上服务、不误删数据的前提下，为后续冷热分离和 RDS 决策争取确定性窗口。

- Trace:
  - 设计锚点：`docs/approved/design-capacity-first-data-layer-safety.md`
  - 容量探针：`ops/observability/probe-data-layer-capacity.sh`
  - 离线投影：`ops/observability/data_layer_capacity_projection.py`
  - 扩盘计划：`ops/stage0/reconcile-cfn-datavolume-no-replace.sh`
- Risk Focus:
  - 逻辑错误：把增长探针超时解释成 green，或容量投影隐式使用乐观回收量。
  - 行为回归：read-only probe 扫全表过久，影响生产 PostgreSQL 延迟。
  - 安全问题：未获批准就创建 prod SSM/change set，或计划夹带实例/卷 replacement。
  - 运行时问题：把 PostgreSQL DELETE 的内部页复用误写成宿主机物理空间已回收。

## Acceptance Criteria

1. **AC-001（有界只读）**：Given 容量探针可能运行在 prod，When 查询 PostgreSQL，Then session 强制只读、锁等待不超过 100ms、近 30 天扫描不超过 2s，总行数不用全表 `COUNT(*)`，分区表大小按叶分区汇总。
2. **AC-002（负向 fail-closed）**：Given PGSTATS 缺失、近 30 天扫描超时或 catalog 行数估算缺失，When 计算容量 verdict，Then 返回 `unknown`，不得返回 green/approaching/trigger。
3. **AC-003（显式投影）**：Given 脱敏 snapshot，When 计算 100 GiB + 90 天热层 scenario，Then ops 回收上下界和残余月增长必须显式输入，输出同时给出两端结果和 DELETE 不等于 `df` 回收的警告。
4. **AC-004（grow-only）**：Given live stack 的 DataVolume size，When 离线生成参数计划，Then 目标小于当前值、超出模板范围、参数缺失或重复时均拒绝。
5. **AC-005（prod plan 审批）**：Given 默认 prod stack，When 未提供与 stack 完全一致的 `--confirm-prod-plan`，Then 在任何 AWS 调用前拒绝。
6. **AC-006（no-replace）**：Given CloudFormation change set，When guard 校验，Then 只接受 `DataVolume` 的 `Modify/Replacement=False/Scope=Properties/Property=Size`；`Instance`、`EIPAssoc`、其它属性或 replacement 全部拒绝。
7. **AC-007（无执行面）**：Given 本阶段扩盘工具，When 审查 shell contract，Then 不存在 `execute-change-set`、部署、容器重启、文件系统 resize 或数据删除路径。

## Assertions

- 本 Story 只验收本地和非生产安全工件，不授权 prod 查询或写入。
- prod plan 也算线上写动作，因为会创建临时 SSM 参数和 no-execute change set。
- 唯一允许的容量估计是显式假设的 offline projection；未知证据不自动补零。
- 归档 worker、S3 写入、恢复 canary 和删除属于下一审批阶段。
- 本变更无 Web surface；运维入口只在 CLI/CI。

## Linked Tests

- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_growth_timeout_is_unknown_not_green`
- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_projection_requires_explicit_reclaim_and_residual_growth`
- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_probe_is_read_only_and_scan_bounded`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_parameter_plan_grows_without_rewriting_unrelated_values`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_parameter_plan_rejects_shrink`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_prod_plan_requires_exact_confirmation_before_aws`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_guard_rejects_instance_replacement`

运行命令：

```bash
python3 ops/observability/test_data_layer_capacity_safety.py
python3 ops/stage0/test_cfn_datavolume_no_replace.py
```

## Status

- [x] InTest — 本地正负向合同已覆盖；prod probe/change set/扩盘/归档均未执行且仍需独立批准。
