# US-036-prod-data-archive-rds

- ID: US-036
- Title: Prod 数据归档与 RDS 惰性演练平台
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产运维者**，我希望 **在不改变当前 prod 数据层的前提下，先建立经审批和演练约束的归档/RDS 平台**，**以便** 容量进入预警后能正式推进，又不会用未经验证的一键切换或旧库回滚制造数据丢失。

- Trace:
  - 设计锚点：`docs/approved/design-prod-data-archive-rds.md`
  - 执行 owner：`docs/deploy/aws-data-layer-migration.md`
  - RDS 模板：`deploy/aws/cloudformation/stage0-data.yaml`
  - 切换入口：`ops/stage0/cutover_data_layer_via_ssm.sh`
- Risk Focus:
  - 逻辑错误：本机/RDS compose profile、SSM overlay 或 alarm threshold 组合错误。
  - 行为回归：PR 合并后默认不再启动本机 PostgreSQL/Redis，或 edge 被意外切到外部数据层。
  - 安全问题：RDS 暴露公网、密码进入普通参数，或 pending 设计仍可触发生产切换。
  - 运行时问题：RDS 开始接收写入后自动回旧本地库，导致用户、计费和用量增量丢失。

## Acceptance Criteria

1. **AC-001（默认不变）**：Given 没有 data-layer SSM overlay，When compose 解析本机 profile，Then PostgreSQL/Redis 均启动，app 仍连接本机服务。
2. **AC-002（外部模式）**：Given 非生产 overlay 指向 RDS，When compose 叠加 external override，Then 本机 PostgreSQL 不启动、Redis 保留、app 只依赖 Redis。
3. **AC-003（审批负向）**：Given 生产设计仍为 pending，When 调用 cutover apply，Then 在任何 AWS 读写前拒绝。
4. **AC-004（回退负向）**：Given operator 调用旧的 rollback action，When 脚本解析参数，Then 明确拒绝 stale-local rollback。
5. **AC-005（容量基线）**：Given 当前数据库约 23.7 GiB，When 解析 RDS 模板，Then 候选为 PostgreSQL 18.1、`db.t4g.medium`、100 GiB 初始盘、500 GiB ceiling、14 天 PITR。
6. **AC-006（安全边界）**：Given 数据栈创建，When 检查 RDS properties，Then private、encrypted、Retain、DeletionProtection、Performance Insights 均启用，连接 alarm 在演练给出阈值前禁用。
7. **AC-007（生成物）**：Given compose/bootstrap/wrapper 变化，When 运行生成门禁，Then CFN 与 Lightsail 生成物按解码内容校验且 UserData 不超平台上限。

## Assertions

- 生产批准由 `docs/approved/design-prod-data-archive-rds.md` frontmatter 机械派生，不靠 operator 记忆。
- SSM overlay 是 endpoint/mode 的唯一运行时真相；默认缺失时保持本机模式。
- 脚本一旦尝试启动 RDS-backed app，只保留 RDS desired state 并要求前向修复。
- 现有珍贵类 pgdump 继续每小时进 S3，但不被描述为 usage/ops/QA 历史归档。
- 本 Story 只验收惰性平台和安全门；生产归档写入、RDS 创建与切流仍需设计批准和独立窗口。

## Linked Tests

- `deploy/aws/stage0/test_compose_data_layer_modes.py`::`ComposeDataLayerModesTest.test_local_mode_is_full_stack`
- `deploy/aws/stage0/test_compose_data_layer_modes.py`::`ComposeDataLayerModesTest.test_external_mode_drops_postgres_keeps_redis`
- `ops/stage0/test_cutover_data_layer_safety.py`::`CutoverDataLayerSafetyTest.test_prod_apply_is_blocked_while_design_is_pending`
- `ops/stage0/test_cutover_data_layer_safety.py`::`CutoverDataLayerSafetyTest.test_stale_local_rollback_action_is_rejected`
- `deploy/aws/stage0/test_data_layer_template.py`::`DataLayerTemplateTest.test_initial_capacity_matches_current_prod_baseline`
- `deploy/aws/stage0/test_data_layer_template.py`::`DataLayerTemplateTest.test_database_is_private_retained_and_observable`

运行命令：

```bash
python3 deploy/aws/stage0/test_compose_data_layer_modes.py
python3 ops/stage0/test_cutover_data_layer_safety.py
python3 deploy/aws/stage0/test_data_layer_template.py
bash deploy/aws/stage0/build-cfn.sh --check
bash deploy/aws/lightsail/render-bootstrap.sh --check
```

## Status

- [x] InTest — 惰性平台、审批负向、回退负向和 RDS 配置契约已有离线门禁；真实归档恢复、同量级 restore、failover/PITR 与生产切换仍受 pending 设计和独立审批阻断。
