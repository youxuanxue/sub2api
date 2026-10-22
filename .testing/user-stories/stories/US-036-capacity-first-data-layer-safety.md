# US-036-capacity-first-data-layer-safety

- ID: US-036
- Title: Capacity-first 数据层安全保护
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **先用有硬超时的只读容量证据和无替换扩盘计划解除磁盘倒计时**，**以便** 在不影响线上服务、不误删数据的前提下，为后续冷热分离和 RDS 决策争取确定性窗口。

- Trace:
  - 设计锚点：`docs/approved/design-capacity-first-data-layer-safety.md`
  - 容量探针：`ops/observability/probe-data-layer-capacity.sh`
  - 容量判定：`ops/observability/data_layer_capacity_verdict.py`
  - 离线投影：`ops/observability/data_layer_capacity_projection.py`
  - 扩盘计划：`ops/stage0/reconcile-cfn-datavolume-no-replace.sh`
- Risk Focus:
  - 逻辑错误：把增长探针超时解释成 green，或容量投影隐式使用乐观回收量。
  - 行为回归：read-only probe 扫全表过久，影响生产 PostgreSQL 延迟。
  - 安全问题：未获批准就创建 prod SSM/change set，或计划夹带实例/卷 replacement。
  - 运行时问题：把 PostgreSQL DELETE 的内部页复用误写成宿主机物理空间已回收。

## Acceptance Criteria

1. **AC-001（有界只读）**：Given 容量探针可能运行在 prod，When 查询 PostgreSQL，Then session 强制只读、锁等待不超过 100ms、近 30 天扫描不超过 2s，总行数不用全表 `COUNT(*)`，分区表大小按叶分区汇总。
2. **AC-002（负向 fail-closed）**：Given PGSTATS 缺失、或分区字节增长信号缺失/不自洽（窗口内无日分区、保留天数超过窗口跨度），When 计算容量 verdict，Then 返回 `unknown`，不得返回 green/approaching/trigger，且**不得回退到基数路径**；此时仍上报已测到的占用与可用空间。
3. **AC-003（显式投影）**：Given 脱敏 snapshot，When 计算 100 GiB + 90 天热层 scenario，Then ops 回收上下界和残余月增长必须显式输入，回收上界不得超过 snapshot 中 ops 关系总大小，输出同时给出两端结果和 DELETE 不等于 `df` 回收的警告。
4. **AC-004（grow-only）**：Given live stack 的 DataVolume size，When 离线生成参数计划，Then 目标小于当前值、超出模板范围、参数缺失或重复时均拒绝。
5. **AC-005（prod plan 审批）**：Given 默认 prod stack，When 未提供与 stack 完全一致的 `--confirm-prod-plan`，Then 在任何 AWS 调用前拒绝。
6. **AC-006（no-replace）**：Given CloudFormation change set，When guard 校验，Then 只接受 `DataVolume` 的 `Modify/Replacement=False/Scope=Properties/Property=Size`；`Instance`、`EIPAssoc`、其它属性或 replacement 全部拒绝。
7. **AC-007（无执行面）**：Given 本阶段扩盘工具，When 审查 shell contract，Then 不存在 `execute-change-set`、部署、容器重启、文件系统 resize 或数据删除路径。
8. **AC-008（唯一正式 owner）**：Given 有界实现已获批准，When prod daily diagnostics 调用容量检查，Then 只使用正式 probe/verdict，仓库不保留并行 prototype 副本。
9. **AC-009（增长不依赖统计信息）**：Given 主机由 pgBackRest 物理恢复（`n_live_tup` 归零、叶分区绝大多数从未 analyze、近 30 天行扫描超时），When 计算容量 verdict，Then 仍给出可用判定——增长只由分区目录字节求得（窗口按 UTC 归集、`MINVALUE` 分区计入总量但排除出窗口、保留期短于窗口按天数折算），基数仅作 `cross_check` 旁证且只报不判。

## Assertions

- 本 Story 只验收本地和非生产安全工件，不授权 prod 查询或写入。
- prod plan 也算线上写动作，因为会创建临时 SSM 参数和 no-execute change set。
- 唯一允许的容量估计是显式假设的 offline projection；未知证据不自动补零。
- 增长度量不得依赖 `pg_stat_user_tables` 累积计数器：它存在 stats 文件而非数据文件，物理恢复后归零。
  `ANALYZE` 也不是解法——日分区滚过即不再写入，autoanalyze 永不再触发，每次恢复/换机都会复发。
- 归档 steady state 见 `ops/archive/README.md` 与 `archive_health`；Exception path CLI 见 approved 归档/canary 设计。
- 本变更无 Web surface；运维入口只在 CLI/CI。

## Linked Tests

- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_losing_every_growth_signal_is_unknown_not_green`
- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_restored_host_shape_still_produces_a_verdict`
- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_growth_is_measured_in_bytes_not_derived_from_cardinality`
- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_missing_byte_accounting_never_falls_back_to_cardinality`
- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_projection_requires_explicit_reclaim_and_residual_growth`
- `ops/observability/test_data_layer_capacity_safety.py`::`DataLayerCapacitySafetyTest.test_probe_is_read_only_and_scan_bounded`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_parameter_plan_grows_without_rewriting_unrelated_values`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_parameter_plan_rejects_shrink`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_prod_plan_requires_exact_confirmation_before_aws`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_guard_rejects_instance_replacement`
- `ops/stage0/test_cfn_datavolume_no_replace.py`::`CfnDataVolumeNoReplaceTest.test_plan_success_uses_fake_aws_and_cleans_preview_artifacts`

运行命令：

```bash
python3 ops/observability/test_data_layer_capacity_safety.py
python3 ops/stage0/test_cfn_datavolume_no_replace.py
```

## Status

- [x] Done — 正式 probe/verdict 已接入 prod daily diagnostics；扩盘 change set 仍须独立批准。
