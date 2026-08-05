# PR #1551 方案 A 审查收敛实现计划

> **供 Agent 执行：** 必须使用 `subagent-driven-development`（推荐）或 `executing-plans`，按复选框逐项实施。每个步骤都以当前工作树为起点，不得覆盖用户已有修改。

**Goal:** 修复 PR #1551 的 `R-001` 至 `R-007`，同时保留已经准备好的 ARM64 release、SSM prefix、dispatch 参数和凭据文件安全修复。

**Architecture:** 账号迁移在同一个 PostgreSQL 事务中 fail-closed：extract 证明请求账号与完整 group/fallback 闭包，build 生成无 secret value 的预期 manifest，load 只有在目标 manifest 完全一致时才提交。基础设施容量固定为批准的 `t4g.small` 规格，所有成本字段、预测和 blocker 全部删除。provision 从已校验的 OIDC ARN 派生 CloudFormation execution role，并在创建 EIP 前运行实时基础设施预检。CloudWatch 是 CPU/credits 状态唯一 owner；现有 5 分钟 host timer 只读取 alarm state，再通过飞书发送有 delivery latch 的 firing/recovery。

**Tech Stack:** Python 3 stdlib/unittest、Bash、PostgreSQL SQL/JSONB、GitHub Actions、AWS CLI、CloudFormation、SSM、CloudWatch、jq。

## Global Constraints

- 审批基线：`docs/approved/design-all-edge-ec2-t4g-small-unlimited-migration.md`。
- 迁移容量必须精确为 `t4g.small`、20 GiB 根卷、20 GiB 数据卷、2 GiB swap、daily snapshot。
- 成本只保留在审批文档的静态评估表；代码、配置、workflow、preflight 和 evidence 中不得存在预算输入、实时成本预测或成本 blocker。
- 本计划不执行任何 AWS、IAM、EC2、DNS、账号或删除写操作。
- 保留当前未提交修复：禁止单架构 release override；EC2 SSM prefix 为 `/tokenkey/edge/<edge_id>`；dispatch 透传轮换/退役确认；迁移凭据文件与 S3 临时对象 fail-closed 清理。
- PR 风险为 `high`。本地 commit 后必须停在 `loop_state.py gate`；没有新的明确授权不得 push，任何情况下不得自行 merge。

---

### Task 1：账号迁移事务化并用 manifest 严格验收（R-001、R-002）

**Files:**
- Modify: `ops/migration/migrate-edge-accounts.py`
- Modify: `ops/migration/test_migrate_edge_accounts.py`

**Interfaces:**
- 输入：`extract --account-ids <csv>`、源端 `accounts`、`account_groups`、两种 group fallback 关系。
- 输出：权限为 `0600` 的 `payload.json`、`expected-manifest.json`、`migrate.sql`；远端精确标记 `MIGRATION_VERIFIED`；本地仅在验收通过后输出 `LOAD_OK`。
- 新增函数：`parse_account_ids(raw: str) -> list[int]`、`validate_extract_payload(payload: dict, requested_ids: list[int]) -> None`、`build_expected_manifest(payload: dict, account_renames: dict[str, str], group_renames: dict[str, str]) -> dict`、`build_set_schedulable_sql(name: str, value: bool) -> str`。

- [ ] **Step 1：先写 extract 闭包与 manifest 失败测试**

测试直接调用生产校验函数，不在测试里复制判断逻辑：

```python
def test_extract_rejects_a_missing_requested_account(self) -> None:
    payload = self._payload()
    payload["requested_account_ids"] = [11, 12]
    payload["accounts"] = [payload["accounts"][0]]
    with self.assertRaisesRegex(ValueError, "requested account ids"):
        MIGRATE.validate_extract_payload(payload, [11, 12])

def test_extract_rejects_a_dangling_fallback_group(self) -> None:
    payload = self._payload()
    payload["requested_account_ids"] = [11]
    payload["groups"][0]["fallback_group_id"] = 99
    with self.assertRaisesRegex(ValueError, "fallback group"):
        MIGRATE.validate_extract_payload(payload, [11])

def test_expected_manifest_contains_credential_keys_but_no_values(self) -> None:
    payload = self._payload()
    payload["requested_account_ids"] = [11]
    manifest = MIGRATE.build_expected_manifest(payload, {}, {})
    encoded = json.dumps(manifest, sort_keys=True)
    self.assertIn("refresh_token", encoded)
    self.assertNotIn(self.SECRET, encoded)
```

再增加 captured-command 测试，验证 extract SQL 包含 `WITH RECURSIVE`，种子来自全部所选账号 binding，同时沿 `fallback_group_id` 和 `fallback_group_id_on_invalid_request` 递归，并把 `requested_account_ids` 写入 payload。

- [ ] **Step 2：运行测试并确认 RED**

```bash
python3 -m unittest \
  ops.migration.test_migrate_edge_accounts.MigrationSafetyTest.test_extract_rejects_a_missing_requested_account \
  ops.migration.test_migrate_edge_accounts.MigrationSafetyTest.test_extract_rejects_a_dangling_fallback_group \
  ops.migration.test_migrate_edge_accounts.MigrationSafetyTest.test_expected_manifest_contains_credential_keys_but_no_values -v
```

预期：三个生产函数尚不存在，测试以 `ERROR` 失败。

- [ ] **Step 3：实现请求 ID 校验和递归 fallback 闭包**

账号 ID 只解析一次；空列表、重复 ID、非正整数直接失败。生成 SQL 时使用下面的完整查询结构，数组内容由解析结果生成：

```sql
WITH RECURSIVE selected_accounts AS (
  SELECT *
  FROM accounts
  WHERE id = ANY(ARRAY[11,12]::bigint[]) AND deleted_at IS NULL
), seed_group_ids AS (
  SELECT DISTINCT group_id
  FROM account_groups
  WHERE account_id IN (SELECT id FROM selected_accounts)
), group_closure AS (
  SELECT g.*
  FROM groups g
  JOIN seed_group_ids seed ON seed.group_id = g.id
  WHERE g.deleted_at IS NULL
  UNION
  SELECT fallback.*
  FROM group_closure parent
  JOIN groups fallback
    ON fallback.id = parent.fallback_group_id
    OR fallback.id = parent.fallback_group_id_on_invalid_request
  WHERE fallback.deleted_at IS NULL
)
SELECT json_build_object(
  'requested_account_ids', to_json(ARRAY[11,12]::bigint[]),
  'schema', json_build_object(
    'accounts', (SELECT json_agg(json_build_object('column_name', column_name, 'data_type', data_type) ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 'accounts'),
    'groups', (SELECT json_agg(json_build_object('column_name', column_name, 'data_type', data_type) ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 'groups')
  ),
  'accounts', (SELECT json_agg(row_to_json(a) ORDER BY a.id) FROM selected_accounts a),
  'bindings', (SELECT json_agg(row_to_json(b) ORDER BY b.account_id, b.group_id, b.priority) FROM account_groups b WHERE b.account_id IN (SELECT id FROM selected_accounts)),
  'groups', (SELECT json_agg(row_to_json(g) ORDER BY g.id) FROM group_closure g)
);
```

`validate_extract_payload` 必须拒绝：请求与返回账号集合不相等、重复账号、binding 指向请求外账号、binding 指向缺失 group、fallback 指向缺失 group、返回 group 超出从 binding 种子可达的 fallback 闭包。下载并解析 JSON 后先调用该函数，再打印脱敏摘要。

- [ ] **Step 4：在同一插入事务内生成并校验目标 manifest**

manifest 数组按名称和 priority 稳定排序，结构固定为：

```json
{
  "accounts": [
    {"name": "kiro-us4-real", "credential_keys": ["access_token", "refresh_token"]}
  ],
  "groups": [
    {"name": "kiro-us4", "fallback_group": null, "invalid_request_fallback_group": null}
  ],
  "bindings": [
    {"account": "kiro-us4-real", "group": "kiro-us4", "priority": 0}
  ]
}
```

把 manifest 以 `0600` 写入 `expected-manifest.json`，并把相同 JSON 嵌入现有 `DO $mig$` block；JSON 中只允许 credential key，不允许 value。在 block 结束前，通过 `_amap`/`_gmap` join 生成实际 manifest。若 `actual IS DISTINCT FROM expected`，执行 `RAISE EXCEPTION 'migration manifest mismatch'`；只有相等时才插入 `scheduler_outbox` 的 `full_rebuild`。

删除“最近 5 分钟创建”的宽泛验证查询。远端脚本只能在 `psql -v ON_ERROR_STOP=1` 成功后执行 `echo MIGRATION_VERIFIED`；`cmd_load` 必须看到一行精确的 `MIGRATION_VERIFIED` 才能输出 `LOAD_OK`。由于 manifest 比较位于同一个 `DO` block，比较失败会回滚 group、account、binding 和 outbox 写入。

- [ ] **Step 5：先写 set-schedulable 单行写失败测试**

```python
def test_set_schedulable_requires_exactly_one_row(self) -> None:
    sql = MIGRATE.build_set_schedulable_sql("kiro-us4-real", True)
    self.assertIn("GET DIAGNOSTICS affected = ROW_COUNT", sql)
    self.assertIn("affected <> 1", sql)
    self.assertIn("schedulable IS DISTINCT FROM true", sql)
    self.assertLess(sql.index("affected <> 1"), sql.index("INSERT INTO scheduler_outbox"))

def test_set_schedulable_rejects_missing_success_marker(self) -> None:
    args = argparse.Namespace(
        to_target="edge:us4@ec2",
        account_name="kiro-us4-real",
        value="true",
        execute=True,
    )
    with mock.patch.object(MIGRATE, "resolve_edge", return_value=("us-west-2", "i-test")), \
         mock.patch.object(MIGRATE, "ssm_run", return_value="UPDATE 0\n"), \
         self.assertRaises(SystemExit):
        MIGRATE.cmd_set_schedulable(args)
```

- [ ] **Step 6：运行单行写测试并确认 RED**

```bash
python3 -m unittest \
  ops.migration.test_migrate_edge_accounts.MigrationSafetyTest.test_set_schedulable_requires_exactly_one_row \
  ops.migration.test_migrate_edge_accounts.MigrationSafetyTest.test_set_schedulable_rejects_missing_success_marker -v
```

预期：缺少 SQL builder 的测试报 `ERROR`；当前实现接受零行结果的测试报 `FAIL`。

- [ ] **Step 7：实现单事务单行更新**

`build_set_schedulable_sql` 生成一个 PostgreSQL `DO` block，顺序固定为：

```sql
UPDATE accounts
SET schedulable = true, updated_at = now()
WHERE name = 'kiro-us4-real' AND deleted_at IS NULL;
GET DIAGNOSTICS affected = ROW_COUNT;
IF affected <> 1 THEN
  RAISE EXCEPTION 'expected 1 account, updated %', affected;
END IF;
IF EXISTS (
  SELECT 1
  FROM accounts
  WHERE name = 'kiro-us4-real'
    AND deleted_at IS NULL
    AND schedulable IS DISTINCT FROM true
) THEN
  RAISE EXCEPTION 'schedulable verification failed';
END IF;
INSERT INTO scheduler_outbox (event_type, payload, created_at)
VALUES ('full_rebuild', NULL, now());
```

远端仅在 `psql` 成功后输出 `SET_OK`，本地必须检查一行精确的 `SET_OK`。零行、多行或回读值不符时，DO block 失败并回滚，不插入 outbox，也不打印成功标记。

- [ ] **Step 8：运行账号迁移完整测试**

```bash
python3 -m unittest ops/migration/test_migrate_edge_accounts.py -v
```

预期：全部通过；stdout/stderr 不含 credential value；本地目录/文件仍为 `0700/0600`；S3 和远端临时文件清理测试继续通过。

- [ ] **Step 9：提交账号迁移单元**

```bash
git add ops/migration/migrate-edge-accounts.py ops/migration/test_migrate_edge_accounts.py
git commit -m "fix(migration): close account transfer verification gaps"
```

---

### Task 2：删除成本自动化并锁定批准容量（R-003）

**Files:**
- Modify: `deploy/aws/stage0/edge-targets.json`
- Modify: `deploy/aws/stage0/resolve-edge-target.py`
- Modify: `deploy/aws/stage0/test_resolve_edge_target_list_deployable.py`
- Modify: `deploy/aws/cloudformation/stage0-edge-ec2.yaml`
- Modify: `deploy/aws/stage0/test_stage0_edge_ec2_contract.py`
- Modify: `.github/workflows/deploy-edge-stage0.yml`
- Modify: `ops/migration/edge-platform-migration-preflight.sh`
- Modify: `ops/migration/test_edge_platform_migration_preflight.py`
- Modify: `docs/evidence/all-edge-ec2-migration-preflight.json`

**Interfaces:**
- resolver 只输出区域、stack、域名、固定容量、SSM prefix 和迁移状态。
- live preflight 只输出 `fleet`、`quotas`、`cpu_24h`、`dns`、`amis`、`instance_type_offerings`、`ssm`、`blockers`。
- 任一 EC2 Edge target 偏离批准容量时 resolver 直接失败。

- [ ] **Step 1：先把测试改成无成本契约并确认 RED**

删除成本正向测试，增加以下行为断言：

```python
def test_real_candidates_have_fixed_capacity_and_no_budget_fields(self) -> None:
    matrix = json.loads(_REAL_MATRIX.read_text(encoding="utf-8"))
    self.assertNotIn("max_monthly_budget_usd", matrix)
    self.assertNotIn("max_fleet_monthly_budget_usd", matrix)
    for target in matrix["targets"].values():
        self.assertEqual(
            ("t4g.small", 20, 20, 2, "daily"),
            (
                target["instance_type"],
                target["root_volume_gib"],
                target["data_volume_gib"],
                target["swap_gib"],
                target["snapshot_schedule"],
            ),
        )
        self.assertNotIn("monthly_budget_usd", target)
```

为五个容量字段各加一个 drift case，逐个篡改 matrix 后断言 resolver 非零退出且错误包含字段名。preflight 测试断言输出不含 `network_out_30d`、`fixed_monthly_usd`、`forecast_monthly_usd`、`approved_fleet_ceiling_usd`，并断言 fake AWS 没有收到 `NetworkOut` metric 调用。CFN 测试断言没有 `MonthlyBudgetUsd`，且五个容量参数只能接受批准值。

```bash
python3 -m unittest \
  deploy/aws/stage0/test_resolve_edge_target_list_deployable.py \
  deploy/aws/stage0/test_stage0_edge_ec2_contract.py \
  ops/migration/test_edge_platform_migration_preflight.py -v
```

预期：现有预算字段、成本输出、NetworkOut 成本采集和宽松 CFN 容量范围导致测试失败。

- [ ] **Step 2：删除 EC2 迁移面的全部成本输入和输出**

从 EC2 matrix 删除 `max_monthly_budget_usd`、`max_fleet_monthly_budget_usd` 和逐 target 的 `monthly_budget_usd`。删除 resolver 的预算解析/校验/输出、workflow 的 `MONTHLY_BUDGET_USD`、CFN 的 `MonthlyBudgetUsd`、preflight 的成本常量/公式、30 日 NetworkOut 采集、成本字段和成本 blocker。

用结构化命令清理已有 evidence，写入前验证 JSON：

```bash
evidence_tmp="$(mktemp)"
jq 'del(.network_out_30d, .fixed_monthly_usd, .forecast_monthly_usd, .approved_fleet_ceiling_usd)' \
  docs/evidence/all-edge-ec2-migration-preflight.json > "$evidence_tmp"
jq empty "$evidence_tmp"
mv "$evidence_tmp" docs/evidence/all-edge-ec2-migration-preflight.json
```

- [ ] **Step 3：建立固定容量的唯一 resolver owner**

在 resolver 定义并校验：

```python
APPROVED_CAPACITY = {
    "instance_type": "t4g.small",
    "root_volume_gib": 20,
    "data_volume_gib": 20,
    "swap_gib": 2,
    "snapshot_schedule": "daily",
}
```

逐 key 比较 matrix 值；漂移时调用 `fail(f"edge_id {edge_id} {key} must be {expected!r}")`。保留已修正的 SSM 输出 `/tokenkey/edge/<edge_id>`。

CFN 参数机械约束为：`InstanceType.AllowedValues: [t4g.small]`；根卷和数据卷 `MinValue`/`MaxValue` 都为 `20`；`SwapSizeGiB.AllowedValues: [2]`；`SnapshotSchedule.AllowedValues: [daily]`。

- [ ] **Step 4：运行聚焦测试和成本残留扫描**

```bash
python3 -m unittest \
  deploy/aws/stage0/test_resolve_edge_target_list_deployable.py \
  deploy/aws/stage0/test_stage0_edge_ec2_contract.py \
  ops/migration/test_edge_platform_migration_preflight.py -v
grep -RInE 'MonthlyBudgetUsd|monthly_budget_usd|max_fleet_monthly_budget_usd|APPROVED_FLEET_CEILING_USD|forecast_monthly_usd|fixed_monthly_usd|network_out_30d' \
  deploy/aws/stage0 \
  deploy/aws/cloudformation/stage0-edge-ec2.yaml \
  .github/workflows/deploy-edge-stage0.yml \
  ops/migration \
  docs/evidence/all-edge-ec2-migration-preflight.json
```

预期：测试全绿；`grep` 在所列 EC2 迁移 surface 返回 1 且没有输出。

- [ ] **Step 5：提交容量与成本单元**

```bash
git add deploy/aws/stage0/edge-targets.json \
  deploy/aws/stage0/resolve-edge-target.py \
  deploy/aws/stage0/test_resolve_edge_target_list_deployable.py \
  deploy/aws/cloudformation/stage0-edge-ec2.yaml \
  deploy/aws/stage0/test_stage0_edge_ec2_contract.py \
  .github/workflows/deploy-edge-stage0.yml \
  ops/migration/edge-platform-migration-preflight.sh \
  ops/migration/test_edge_platform_migration_preflight.py \
  docs/evidence/all-edge-ec2-migration-preflight.json
git commit -m "fix(migration): remove cost gates and pin edge capacity"
```

---

### Task 3：provision 前跑实时预检并派生 CFN role（R-004、R-005）

**Files:**
- Modify: `.github/workflows/deploy-edge-stage0.yml`
- Modify: `scripts/checks/test_workflow_edge_coverage.py`
- Preserve/verify: `scripts/stage0/dispatch-edge-deploy.sh`
- Preserve/verify: `scripts/test_resolve_edge_deploy_route.py`

**Interfaces:**
- 输入：当前 GitHub Environment 的 `AWS_OIDC_ROLE_ARN`。
- 输出：step output `cfn_execution_role_arn=arn:<partition>:iam::<account>:role/tokenkey-cfn-ec2-edge-stage0`。
- 时序：配置 OIDC 凭据后执行 live preflight；preflight 成功后才允许第一次 `allocate-address`。

- [ ] **Step 1：先写 workflow 契约测试并确认 RED**

```python
def test_ec2_provision_derives_cfn_role_and_runs_preflight_before_eip(self) -> None:
    text = EC2_WORKFLOW.read_text(encoding="utf-8")
    self.assertNotIn("AWS_EC2_EDGE_CFN_ROLE_ARN", text)
    self.assertIn("role/tokenkey-cfn-ec2-edge-stage0", text)
    self.assertLess(
        text.index("edge-platform-migration-preflight.sh"),
        text.index("aws ec2 allocate-address"),
    )
```

另断言 live preflight step 带 `if: inputs.operation == 'provision'`，输出位于 `${{ runner.temp }}`，并用 `jq -e '.blockers == []'` 校验结果。

```bash
python3 -m unittest scripts/checks/test_workflow_edge_coverage.py -v
```

预期：workflow 仍读取不存在的 Environment variable，且没有创建 EIP 前的实时预检，测试失败。

- [ ] **Step 2：从已校验 OIDC ARN 派生 execution role**

在 `Resolve EC2 edge target` 中执行：

```bash
if [[ ! "$OIDC_ROLE_ARN" =~ ^arn:(aws(-[a-z]+)?):iam::([0-9]{12}):role/.+$ ]]; then
  echo "::error::AWS_OIDC_ROLE_ARN is not a valid IAM role ARN"
  exit 1
fi
CFN_ROLE_ARN="arn:${BASH_REMATCH[1]}:iam::${BASH_REMATCH[3]}:role/tokenkey-cfn-ec2-edge-stage0"
echo "cfn_execution_role_arn=$CFN_ROLE_ARN" >> "$GITHUB_OUTPUT"
```

删除 job 级 `CFN_EXECUTION_ROLE_ARN`。每个 `aws cloudformation deploy --role-arn` 都通过 step-local env 使用 `${{ steps.edge.outputs.cfn_execution_role_arn }}`。

- [ ] **Step 3：在 provision 资源创建前插入实时预检**

紧跟 `Configure AWS credentials via OIDC` 添加：

```yaml
- name: Run live migration preflight
  if: inputs.operation == 'provision'
  env:
    REPORT: ${{ runner.temp }}/all-edge-ec2-migration-preflight.json
  run: |
    set -euo pipefail
    bash ops/migration/edge-platform-migration-preflight.sh --format json --output "$REPORT"
    jq -e '.blockers == []' "$REPORT" >/dev/null
```

下一步中的 `aws ec2 allocate-address` 必须继续是整个 provision 的第一个资源创建调用。不上传 report，不读取或打印 SecureString value。

- [ ] **Step 4：运行 workflow 与 dispatch 测试**

```bash
python3 -m unittest \
  scripts/checks/test_workflow_edge_coverage.py \
  scripts/test_resolve_edge_deploy_route.py -v
```

预期：全部通过；已准备的轮换/退役参数透传和禁止单架构 release override 继续通过。

- [ ] **Step 5：提交 workflow 单元**

```bash
git add .github/workflows/deploy-edge-stage0.yml \
  scripts/checks/test_workflow_edge_coverage.py \
  scripts/stage0/dispatch-edge-deploy.sh \
  scripts/test_resolve_edge_deploy_route.py
git commit -m "fix(migration): gate EC2 provision on live preflight"
```

---

### Task 4：把 CloudWatch CPU 状态转发到飞书并删除无消费方 SNS（R-006、R-007）

**Files:**
- Modify: `deploy/aws/stage0/stage0-ec2-bootstrap.sh`
- Modify: `deploy/aws/stage0/build-cfn.sh`
- Modify/regenerate: `deploy/aws/cloudformation/stage0-edge-ec2.yaml`
- Modify/regenerate: `deploy/aws/cloudformation/stage0-single-ec2.yaml`
- Modify: `deploy/aws/stage0/test_stage0_edge_ec2_contract.py`
- Modify: `deploy/aws/stage0/test_build_cfn.py`
- Create: `ops/stage0/test_ec2_cloudwatch_alarm_delivery.sh` <!-- script-ref: planned -->
- Modify: `scripts/preflight.sh`

**Interfaces:**
- 输入：三个 EC2 Edge CloudWatch alarm 的 `StateValue`，以及 `/var/lib/tokenkey/.env` 中已有的飞书 webhook/signing secret。
- 输出：每个 alarm 独立的 firing active latch 和 cooldown stamp；只有飞书 body 返回 `code:0` 才写 latch；只有 recovery 返回 `code:0` 才清 latch。
- 失败：webhook 缺失、alarm 缺失、AWS 查询失败、飞书拒绝时返回非零并保留重试状态。

- [ ] **Step 1：先写可执行状态迁移测试并确认 RED**

新 shell test 从真实 bootstrap 提取 `tokenkey-disk-metrics.sh` heredoc，用 fake `aws`、`curl` 和固定 clock 执行生产函数，覆盖：

```text
ALARM + 飞书接受       -> active latch 存在
OK + active + 飞书接受 -> 发 recovery，删除 active/cooldown
ALARM + 飞书拒绝       -> 不写 active latch
OK + recovery 被拒绝   -> 保留 active/cooldown
alarm 缺失或查询失败   -> 非零退出，不写成功 latch
INSUFFICIENT_DATA      -> 不发 recovery，保留 active latch
```

CFN 测试必须要求 Edge `InstanceRole` 含 `cloudwatch:DescribeAlarms`，拒绝 Edge template 中的 `AlarmSnsTopicArn`、`AlarmTopicProvided`、`AlarmActions`，并证明三个 alarm 名称都进入 Edge host env。

```bash
bash ops/stage0/test_ec2_cloudwatch_alarm_delivery.sh # script-ref: planned
python3 -m unittest deploy/aws/stage0/test_stage0_edge_ec2_contract.py -v
```

预期：当前没有 CloudWatch state handler，shell test 失败；缺 IAM 权限且仍有 SNS branch，CFN test 失败。

- [ ] **Step 2：把三个 alarm 名称确定性写入 Edge UserData**

在 `build-cfn.sh` 的 Edge launcher 生成分支增加：

```bash
export TK_CLOUDWATCH_CPU_ALARM_NAMES='${ProjectName}-${EdgeId}-cpu-24h-above-baseline,${ProjectName}-${EdgeId}-cpu-surplus-borrowing,${ProjectName}-${EdgeId}-cpu-surplus-charged'
```

bootstrap 把它写入 `/var/lib/tokenkey/.env`：

```bash
TOKENKEY_CLOUDWATCH_CPU_ALARM_NAMES=${TK_CLOUDWATCH_CPU_ALARM_NAMES:-}
```

prod 不设置该变量，因此现有 prod 行为不变。

- [ ] **Step 3：扩展现有 5 分钟 host timer**

每轮只调用一次：

```bash
aws cloudwatch describe-alarms \
  --alarm-names "${alarm_names[@]}" \
  --region "$REGION" \
  --output json
```

使用 `jq` 解析，并要求每个配置名称精确返回一条 alarm。只把 `StateValue` 传给 `handle_cloudwatch_alarm_state`，不在 host 重新计算阈值。

状态机实现必须等价于：

```bash
case "$state" in
  ALARM)
    if tk_feishu_alert "$cooldown" \
      "🔴 Edge CPU 告警 ${NODE} — CloudWatch alarm=${alarm} state=ALARM"; then
      printf '1\n' >"$active"
    else
      return 1
    fi
    ;;
  OK)
    if [ -r "$active" ]; then
      tk_feishu_post_now \
        "✅ Edge CPU 告警已恢复 ${NODE} — CloudWatch alarm=${alarm} state=OK" || return 1
      rm -f "$active" "$cooldown"
    fi
    ;;
  INSUFFICIENT_DATA) ;;
  *) return 1 ;;
esac
```

EC2 `tk_feishu_alert` 在 webhook 缺失、curl 失败或 body 非 `code:0` 时必须返回非零。新增无 cooldown 的 `tk_feishu_post_now`。alarm 名称先限制为 `[A-Za-z0-9._-]+`，再用于 `/run/tokenkey-cloudwatch-<name>.active` 和 `.cooldown`，拒绝任何含路径字符的名称。

- [ ] **Step 4：授予只读 alarm state 并删除 Edge SNS branch**

在 Edge instance role 的 `cloudwatch:PutMetricData` 同级增加 `cloudwatch:DescribeAlarms`，`Resource: '*'`；该 Describe API 不支持 alarm resource scope。

只从 `stage0-edge-ec2.yaml` 删除 `AlarmSnsTopicArn` 参数、`AlarmTopicProvided` condition 和全部 `AlarmActions`。本 PR 不修改 prod template 的既有可选 SNS 契约。

- [ ] **Step 5：重新生成 CFN 并把行为测试接入 preflight**

将新 shell test 加入 `scripts/preflight.sh`，然后执行：

```bash
bash deploy/aws/stage0/build-cfn.sh
bash deploy/aws/stage0/build-cfn.sh --check
bash ops/stage0/test_ec2_cloudwatch_alarm_delivery.sh # script-ref: planned
python3 -m unittest \
  deploy/aws/stage0/test_build_cfn.py \
  deploy/aws/stage0/test_stage0_edge_ec2_contract.py -v
bash scripts/checks/check-stage0-ssm-host-parse.sh
```

预期：prod/Edge 生成模板与共享 bootstrap 一致；状态迁移行为测试通过；UserData 和三段 SSM payload 大小门禁继续通过。

- [ ] **Step 6：提交告警单元**

```bash
git add deploy/aws/stage0/stage0-ec2-bootstrap.sh \
  deploy/aws/stage0/build-cfn.sh \
  deploy/aws/cloudformation/stage0-edge-ec2.yaml \
  deploy/aws/cloudformation/stage0-single-ec2.yaml \
  deploy/aws/stage0/test_stage0_edge_ec2_contract.py \
  deploy/aws/stage0/test_build_cfn.py \
  scripts/preflight.sh \
  ops/stage0/test_ec2_cloudwatch_alarm_delivery.sh # script-ref: planned
git commit -m "fix(alerting): deliver EC2 CPU alarm recovery to Feishu"
```

---

### Task 5：完整验证并重审 PR #1551，停在高风险 push 门禁

**Files:**
- Verify: `origin/main...HEAD` 的全部 PR 文件。
- 只有机械门禁发现具体缺陷时才修改对应 owner 文件。

**Interfaces:**
- 输出：clean worktree、聚焦测试全绿、full preflight 全绿、`xj-review` 零条 medium+ finding、本地 commit stack 等待 push 授权。

- [ ] **Step 1：运行完整聚焦测试**

```bash
python3 -m unittest \
  ops/migration/test_migrate_edge_accounts.py \
  ops/migration/test_edge_platform_migration_preflight.py \
  deploy/aws/stage0/test_resolve_edge_target_list_deployable.py \
  deploy/aws/stage0/test_stage0_edge_ec2_contract.py \
  deploy/aws/stage0/test_build_cfn.py \
  scripts/checks/test_workflow_edge_coverage.py \
  scripts/test_resolve_edge_deploy_route.py -v
bash ops/stage0/test_ec2_cloudwatch_alarm_delivery.sh # script-ref: planned
bash deploy/aws/stage0/build-cfn.sh --check
```

- [ ] **Step 2：运行 full preflight 并登记结果**

```bash
bash -o pipefail -c 'PREFLIGHT_BASE=origin/main bash scripts/preflight.sh 2>&1 | tee /tmp/xj-review-preflight-1551.txt'
python3 dev-rules/scripts/review/loop_state.py record \
  --key youxuanxue/sub2api#1551 --kind script --id preflight --outcome pass
```

预期：输出 `preflight (with sub2api checks): PASS`。

- [ ] **Step 3：运行确定性 review pipeline 和 full-conformance 自审**

```bash
python3 dev-rules/scripts/review/pipeline.py find \
  --scope origin/main...HEAD \
  --preflight /tmp/xj-review-preflight-1551.txt \
  --json
python3 dev-rules/scripts/review/pipeline.py dimensions \
  --risk high --mode full_conformance --json
```

按 `Intent -> Code -> Validation` 对照审批文档。只有聚焦测试证明相应行为后，才把 `R-001` 至 `R-007` 逐条登记为 pass。若出现新的 medium+ finding，回到其 owner task，重新执行 RED/GREEN/full preflight。

- [ ] **Step 4：确认所有修改已提交并检查最终 diff**

```bash
git status --short
git diff --check origin/main...HEAD
git log --oneline origin/feature/all-edge-ec2-migration..HEAD
```

预期：无未提交文件；diff check 通过；所有 commit 只在本地，尚未 push。

- [ ] **Step 5：停在高风险 outward-action gate**

```bash
python3 dev-rules/scripts/review/loop_state.py gate --key youxuanxue/sub2api#1551
```

预期：`verdict=halt`，reason 为高风险 push 需要人工批准。向用户汇报本地 commit、测试、剩余风险和精确 PR diff；收到明确 push 授权前不得 push，收到独立 merge 指令前不得 merge。
