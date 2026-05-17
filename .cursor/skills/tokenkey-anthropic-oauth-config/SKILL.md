---
name: tokenkey-anthropic-oauth-config
description: >-
  Query and update Anthropic account/group config across the TokenKey
  control plane (prod stage0) and the edge stage0 hosts: edge OAuth
  stability tiers (Stage0 SQL templates), prod cc-*-oauth forward
  stub bindings, and the cc-edges admin-only routing slot. Default
  read-only check, explicit apply confirmation, mixed-channel risk
  precheck, RPM-alignment guard, post-change verification.
---

# TokenKey：Anthropic 账号与分组配置（prod 控制面 + edge OAuth）

适用于本仓库（TokenKey fork of sub2api）。目标是把“查询漂移 → 计划变更 → 显式确认后更新 → 复核”固化为可复用流程，覆盖两条数据路径：

- **edge stage0**：真正的 anthropic OAuth 账号（`platform=anthropic AND type=oauth`），承载 Anthropic 上游流量；tier baseline + TLS profile 在此落档。
- **prod stage0**：转发 stub（`platform=anthropic AND type=apikey`，credentials 含 `base_url=api-<edge>.tokenkey.dev`），把客户端流量打到对应 edge。

权威纪律以仓库根 `CLAUDE.md` 为准。

## 调用参数

```text
/tokenkey-edge-anthropic-oauth-config edge_id=<id> operation=<check|plan-apply|apply> [account_name=<name>|all] [target_scope=account|group] [target_group_id=<id>] [target_group_name=<name>] [group_ids=1,2] [confirm_apply=yes-apply-edge-anthropic-oauth] [allow_planned=true|false] [update_stable_list=true|false]
```

| 参数 | 语义 |
|---|---|
| `edge_id` | 目标 edge，如 `us1` / `uk1` / `fra1`；支持 `all` 自动枚举所有 edge（默认仅 deployable）。 |
| `target_scope` | 目标范围：`account`（默认）或 `group`。 |
| `account_name` | `target_scope=account` 时必填；目标账号名（`accounts.name`）；`check` 支持 `all` 自动枚举每个 edge 下全部 anthropic oauth 账号。 |
| `target_group_id` | `target_scope=group` 时可选；目标分组 ID（与 `target_group_name` 二选一，优先 ID）。 |
| `target_group_name` | `target_scope=group` 时可选；目标分组名（与 `target_group_id` 二选一）。 |
| `account.extra.stability_tier` | 分级基线选择键（`l1/l2/l3`），`check` 会按该字段选择 tier baseline。 |
| `operation=check` | 默认模式，只读检查当前配置与基准差异；`target_scope=group` 时输出分组聚合结果与成员账号明细。 |
| `operation=plan-apply` | 生成更新计划与请求 payload 预览，不写入。 |
| `operation=apply` | 执行更新（账号字段 + 可选分组绑定）；`target_scope=group` 时对分组内可用账号逐一执行，必须显式确认。 |
| `group_ids` | 可选分组 ID 列表（逗号分隔）。在 `target_scope=account` 时表示该账号分组重绑；在 `target_scope=group` 时默认不改绑定，除非显式提供。 |
| `confirm_apply` | 仅 `apply` 使用，必须精确为 `yes-apply-edge-anthropic-oauth`。 |
| `allow_planned` | 是否允许在 planned edge 上执行检查，默认 `false`。 |
| `update_stable_list` | 可选；仅在人工确认稳定后才更新 baseline stable_accounts。 |

默认行为：
- 用户只说“查配置/看漂移” → `operation=check`
- 用户只说“更新/对齐” → 先执行 `check` + `plan-apply`，拿到确认后再 `apply`
- 未声明 `target_scope` 时默认 `target_scope=account`
- `target_scope=group` 时，`check`/`plan-apply` 可用分组名或分组 ID 定位目标；`apply` 仅允许单 edge + 单分组
- `edge_id=all` 默认只巡检 deployable edge；如需包含 planned，显式加 `allow_planned=true`

## prod 控制面：anthropic stub 双绑规则

prod 上每一个 anthropic 转发 stub（`platform=anthropic AND type=apikey`，credentials 包含 `base_url=api-<edge>.tokenkey.dev`）必须**同时绑两个分组**：

| 分组 | id | 用途 | 谁可见 |
| ---- | ---- | ---- | ---- |
| `default` | 1 | 对外用户流量 | 普通用户 API key |
| `cc-edges` | 15 | admin 调试旁路 | 仅 admin API key（不暴露给普通用户） |

可见性强制点：**`groups.is_exclusive=true` + `user_allowed_groups` 白名单**。`cc-edges` 必须 `is_exclusive=true`，并且只为 admin user 在 `user_allowed_groups` 写入 `(admin_user_id, 15)` 一行；普通用户在前端选择 group 时 `GetAvailableGroups` 会过滤掉 `cc-edges`，新建/更新 API key 时 `CanBindGroup` 会返回 `GROUP_NOT_ALLOWED`。两个分组指向同一个 stub（同一个上游 edge），区别仅在调用方身份。

操作流程：
- **新增 anthropic edge stub**（例如再开一个 edge `xx1`）→ 创建 `cc-xx1-oauth` stub 后，第一时间在 prod 上 `INSERT account_groups` 双行（`default` + `cc-edges`）。两条 binding **不可拆开 apply**：要么一起加，要么一起退。
- **退役 anthropic edge stub**（例如 H3 处理 uk1/fra1） → 软删 stub 前必须先 `DELETE account_groups` 两行；只删一行会留下幽灵 binding，导致下次审计时声音错乱。
- **edge 数据库内部** → 真正的 OAuth 账号绑定 `default` 组即可，**不需要** edge 端复刻 `cc-edges`；admin 调试旁路只在 prod 控制面有意义（admin 直接命中 prod，再透传到 edge）。

任何对 prod stub 的 binding 改动都属于 `target_scope=account` 的 `apply` 范畴，必须走本 skill §3 的 `confirm_apply` 流程。

## prod 控制面：anthropic stub 主路径镜像规则

prod 上**自持 edge 主路径**类型的 anthropic stub（`credentials.base_url` 匹配 `https://api-<edge_id>.tokenkey.dev` 模式）必须**镜像**其上游 edge 的 anthropic OAuth 账号容量。

最小镜像字段：

- `accounts.concurrency` ≡ edge OAuth.concurrency — prod 端 in-flight 与 edge 实际容量对齐，溢出立刻 fallback 而不是堆积成上游 429。

刻意**不**镜像的字段（设计放弃）：

- `accounts.extra.base_rpm` / `extra.max_sessions` / `extra.window_cost_limit` 等 — stub 不读这些，由 edge OAuth 自己持有（在 edge 那一侧落档）。
- 所在组的 `rpm_limit` — 同组通常挂外部兜底 stub（如 `agent.tokensea.ai`），整组限速会卡死兜底容量；prod 端 RPM 不重复 mirror，让 edge `default.rpm_limit` 做最终闸门。
- `accounts.priority` — 是 prod 组内调度顺序，与 edge 内部排序无关。

**外部兜底 stub**（`base_url` 不匹配 self-edge 模式，例如 `https://agent.tokensea.ai`）享有独立容量，**不参与镜像 check**，但仍需满足共同 baseline。

共同 baseline（所有 stub 必须满足）：

- `channel_type = 0`
- `rate_multiplier = 1.0`
- `auto_pause_on_expired = true`
- `status = 'active'`

强制 pre-apply 门禁：

```bash
python3 scripts/check-prod-anthropic-stub-mirror.py
```

- exit 0 — 所有 stub 通过（self-edge mirror 对齐 + external 满足 baseline）
- exit 1 — 至少一个 stub mirror 或 baseline 漂移，**修复后才能 apply**（升降 `prod_stub.concurrency` 与 edge OAuth 对齐）
- exit 2 — SSM/schema/lookup 失败

规则的本质：**prod 是 router、edge 是 worker**。Router 的并发额度不应超过 worker 的处理能力（溢出请求只是把上游 429 推给用户）；不应低于 worker 容量（浪费 quota）。Mirror 把这层语义代码化，让任何 edge tier 变更（如 H1 升降）能自动被 prod stub guard 捕获。

## 一次性跑完（原则）

- 默认只读，先 `check`。
- 任何写入前都先给出 `plan-apply` 预览。
- `apply` 无固定确认口令则拒绝执行。
- 失败先停并报告，不做隐式重试覆盖。
- 输出禁止包含 token/secret 明文。

## 分组口径与聚合规则（target_scope=group）

仅统计“分组下可用账号”（建议口径：`deleted_at IS NULL`、平台=`anthropic`、类型=`oauth`、未被临时/永久禁用，且通过当前调度可用性判定）。

聚合字段建议口径：
- `group_agg.available_account_count` = 可用账号数量
- `group_agg.total_base_rpm` = Σ(每个可用账号 `extra.base_rpm`)
- `group_agg.total_max_sessions` = Σ(每个可用账号 `extra.max_sessions`)
- `group_agg.total_window_cost_limit` = Σ(每个可用账号 `extra.window_cost_limit`)
- `group_agg.effective_concurrency` = Σ(每个可用账号 `account.concurrency`)
- `group_agg.min_priority` / `max_priority` = 分组内优先级范围（数值越小优先级越高）
- `group_agg.tier_distribution` = 各 tier 账号计数（L1~L3）

`check` 输出应同时包含：
1) 分组聚合结果（group_agg）；
2) 成员账号明细（每个账号的 tier、diff_count、关键字段）；
3) 分组是否存在“混合 tier / 混合 channel”风险标记。

## 1) Read-only 检查（operation=check）

复用脚本：`scripts/check-edge-anthropic-oauth-stability.py`

### 1.1 账号模式（target_scope=account）

标准命令形态：

```bash
python3 scripts/check-edge-anthropic-oauth-stability.py \
  --edge-id "$EDGE_ID" \
  --account-name "$ACCOUNT_NAME" \
  --json
```

如果需要 planned edge：追加 `--allow-planned`。

重点读取输出字段：
- `status`（`ok` / `drift` / `error`）
- `account_stability_tier`（账号当前标记）
- `baseline_tier`（实际对比使用的等级）
- `baseline_factor`
- `diff_count`
- `diffs`
- `ssm_command_id`

若 `diff_count>0`，可选生成 SQL（仅供审阅，不建议直接落库改账号）：

> 注意：`--emit-sql` 只支持单 edge + 单账号；任一参数为 `all` 时会被拒绝。

```bash
python3 scripts/check-edge-anthropic-oauth-stability.py \
  --edge-id "$EDGE_ID" \
  --account-name "$ACCOUNT_NAME" \
  --emit-sql /tmp/${EDGE_ID}-${ACCOUNT_NAME}.sql \
  --json || true
```

### 1.2 分组模式（target_scope=group）

先解析目标分组（`target_group_id` 或 `target_group_name`），枚举该分组下全部可用 anthropic oauth 账号，然后对每个账号执行账号模式 `check`，最终汇总为分组结果。

重点读取输出字段：
- `group_status`（`ok` / `drift` / `error`）
- `group_agg.available_account_count`
- `group_agg.total_base_rpm`（分组可用账号 rpm 之和）
- `group_agg.total_max_sessions`
- `group_agg.total_window_cost_limit`
- `group_agg.effective_concurrency`
- `group_agg.tier_distribution`
- `member_results[]`（每个账号的 `status` / `diff_count` / `baseline_tier`）

## 2) 变更预览（operation=plan-apply）

`plan-apply` 只做两件事：
1. 基于 `check` 结果列出待更新字段；
2. 生成 admin API 请求 payload 预览。

### 2.1 账号模式（target_scope=account）

安全限制：
- `plan-apply` 不允许 `account_name=all`（避免批量账号预览被误当可执行清单）。

`group_ids` 语义必须与后端一致：
- **不传 `group_ids`**：不改分组绑定；
- **传空数组 `[]`**：清空分组绑定；
- **传非空数组 `[1,2]`**：重绑分组。

### 2.2 分组模式（target_scope=group）

- 基于分组内 `member_results[]` 生成“逐账号变更清单”（账号 ID、字段 diff、tier、预估影响）。
- 生成“分组聚合前后对比预览”：
  - `total_base_rpm_before/after`
  - `total_max_sessions_before/after`
  - `total_window_cost_limit_before/after`
  - `effective_concurrency_before/after`
- 默认不允许 `target_group_id=all` 或 `target_group_name=all` 的 apply 级预览；如需批量分组操作，应拆分为多个单分组执行。

## 3) 执行更新（operation=apply）

### 3.1 强制确认

必须提供：

```text
confirm_apply=yes-apply-edge-anthropic-oauth
```

缺失或不匹配则拒绝执行。

### 3.2 S2 alignment guard（强制 pre-apply 门禁）

任何 `apply` 之前**必须**先跑：

```bash
python3 scripts/check-account-group-rpm-alignment.py --target <edge_id|prod>
```

对每个 anthropic group（`rpm_limit > 0`）同时校验两层规则：

- **Layer A**（账号不被组卡）：`max(account.base_rpm) ≤ group.rpm_limit`
  违反 = 组成为单账号的隐性 bottleneck。H1 (2026-05-17) 在 edge uk1/fra1 上踩中（`default.rpm_limit=3` 卡住 `base_rpm=6`）。
- **Layer B**（组 cap 不超出组内产能总和）：`Σ(account.base_rpm) ≥ group.rpm_limit`
  违反 = 组的 RPM 上限超过组内 OAuth 账号实际能合并撑起的速率，多出的 cap 是虚的（永远跑不到）。

跳过条件（不计入 violation）：

- `rpm_limit = 0` 或 NULL — 视为 unlimited，整组跳过。
- `rpm_limit > 0` 但组内**无** anthropic OAuth 账号 / 无任何账号声明 `extra.base_rpm` — 视为 stub-only 组（如 prod `cc-edges`），本 guard 不适用；如需对 stub 容量护栏，见 §"prod stub 主路径镜像规则"。

退出码：

- exit 0 — 所有 in-scope 组通过两层
- exit 1 — 至少一组违反 Layer A 或 Layer B，**必须先修复**再 apply：
  - Layer A 修法：升 `group.rpm_limit` ≥ 该组 max base_rpm；或调低 offender 账号 tier
  - Layer B 修法：降 `group.rpm_limit` ≤ 该组 sum base_rpm；或往组里加 OAuth 账号补容量
- exit 2 — 目标/SSM/schema 故障，按错误排查

### 3.3 模板 SQL 是 apply 源头

更新配置时禁止临时手写 SQL 或直接拼一份新的字段清单。必须从仓库模板复制出本次执行 SQL，再只替换本次目标变量和经 `plan-apply` 确认的差异：

- 账号 tier/stability/TLS baseline 更新：复制 `deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql`
- 分组聚合 rpm 更新：复制 `deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql`

推荐落点：`$CLAUDE_JOB_DIR/<edge>-<target>-apply.sql`，不要改原模板来执行单次任务。复制后必须生成**自包含 SQL**：把模板正文复制进该文件，并在文件顶部写入本次 `\set account_name`、`\set stability_tier`、`\set group_name` 等变量；远端 edge 不保证存在仓库 checkout，所以禁止让 SSM/psql 执行依赖远端文件路径的 `\i deploy/aws/stage0/...`。复制后必须保留模板里的 profile、tier、聚合口径和事务结构；只允许改执行变量或经 `plan-apply` 确认过的字面值。若需求确实要求模板未覆盖的新字段，先更新模板和本 skill，再执行 apply，避免 checker、模板和人工 SQL 分叉。

### 3.4 账号模式（target_scope=account）

安全限制：
- `apply` 仅允许单 edge + 单账号；
- 出现 `edge_id=all` 或 `account_name=all` 一律拒绝执行。

预检 mixed-channel 风险（涉及 `group_ids` 时）：
- 先调用 `POST /api/v1/admin/accounts/check-mixed-channel`
- 若预检返回风险且未明确确认，停止执行。

执行策略：
1. 从 `anthropic-oauth-stability-tiered-apply-template.sql` 复制模板正文，生成自包含本次 SQL；
2. 在本次 SQL 顶部写入 `account_name` 与 `stability_tier`；
3. 如需改分组绑定，先完成 mixed-channel 预检，再在同一份自包含 SQL 中加入经确认的绑定变更；
4. 通过 SSM 把该自包含 SQL 作为 heredoc 传给目标 edge 的 `tokenkey-postgres` 容器执行，不依赖远端目录。

### 3.5 分组模式（target_scope=group）

安全限制：
- `apply` 仅允许单 edge + 单分组；
- 出现 `edge_id=all`、`target_group_id=all`、`target_group_name=all` 一律拒绝执行。

执行策略：
1. 固定成员快照：先锁定分组内可用账号清单（执行期不允许隐式扩容）；
2. 逐账号预检：若涉及分组重绑，逐账号做 mixed-channel 预检；
3. 对每个需要收敛的成员账号，基于 `anthropic-oauth-stability-tiered-apply-template.sql` 生成自包含 SQL 并执行；
4. 成员账号全部收敛后，基于 `anthropic-oauth-group-aggregate-apply-template.sql` 生成自包含 SQL 更新分组聚合 rpm；
5. 失败即停：任一账号或分组聚合更新失败立即停止，并输出已成功列表与待处理列表。

幂等要求：
- 对已收敛账号重复 apply 不应产生额外副作用；
- 结果输出必须包含 `applied_count` / `skipped_count` / `failed_count`。

## 4) 变更后复核

`apply` 完成后必须自动复核：

1. 再跑一次 `operation=check`；
2. 对比前后 `diff_count` 与 `diffs`；
3. 输出结构化结果。

账号模式输出：
- `edge_id`
- `account_name`
- 更新字段列表
- 分组变更（若有）
- 复核状态（是否收敛到 `diff_count=0`）

分组模式输出：
- `edge_id`
- `target_group_id` / `target_group_name`
- `member_total` / `applied_count` / `failed_count`
- `group_agg_before` / `group_agg_after`（含 total_base_rpm 等）
- `remaining_drift_accounts[]`
- 复核状态（是否全成员收敛）

## 5) 可选：更新 stable_accounts（仅人工确认后）

仅在人工确认“该账号已稳定且应纳入基线名单”时执行：

```bash
python3 scripts/check-edge-anthropic-oauth-stability.py \
  --edge-id "$EDGE_ID" \
  --account-name "$ACCOUNT_NAME" \
  --update-stable-list \
  --confirm yes-update-anthropic-stable-list
```

禁止在未确认稳定前更新 stable list。

## 6) 失败处理与回滚

- mixed-channel 预检失败：停止，不写入。
- 更新 API 非 2xx：停止并报告响应摘要，不继续后续动作。
- 复核仍有漂移：输出残余差异，进入人工判断（再次 apply 或回滚）。
- 回滚策略：使用同一更新 API 按变更前快照反向写回（账号字段 + group_ids）。

## 7) 输出模板（建议）

单目标模式（账号）：

```text
target_scope=account
edge_id=<id>
account_name=<name>
operation=<check|plan-apply|apply>
status=<ok|drift|applied|failed>
diff_count_before=<n>
diff_count_after=<n>
updated_fields=<...>
group_ids_change=<unchanged|cleared|rebinding:...>
notes=<risk/precheck/rollback info>
```

单目标模式（分组）：

```text
target_scope=group
edge_id=<id>
target_group_id=<id>
target_group_name=<name>
operation=<check|plan-apply|apply>
status=<ok|drift|applied|failed>
member_total=<n>
applied_count=<n>
failed_count=<n>
group_agg.total_base_rpm=<sum>
group_agg.total_max_sessions=<sum>
group_agg.total_window_cost_limit=<sum>
notes=<risk/precheck/rollback info>
```

批量模式（任一参数为 `all`）：

```text
mode=batch
selector.edge_id=<id|all>
selector.account_name=<name|all>
summary.edge_total=<n>
summary.account_result_total=<n>
summary.ok_count=<n>
summary.drift_count=<n>
summary.error_count=<n>
```

## 8) 故障速查

| 现象 | 根因 | 处理 |
|---|---|---|
| check 失败且无结果 | edge 目标元数据缺失或 SSM 查询失败 | 先校验 `edge-targets.json`、区域与栈、OIDC/SSM 权限 |
| apply 被拒绝 | 未提供固定确认口令 | 传入 `confirm_apply=yes-apply-edge-anthropic-oauth` |
| 分组更新失败 | `group_ids` 含不存在分组或 mixed-channel 风险未通过 | 先调用 mixed-channel 预检并修正分组 |
| apply 成功但仍有漂移 | 仅更新了部分字段或存在额外策略字段 | 对照 `diffs` 补齐字段后重试 |
| 稳定名单更新失败 | 缺确认口令 | 使用 `--confirm yes-update-anthropic-stable-list` |

## 扩展阅读

- `scripts/check-edge-anthropic-oauth-stability.py`
- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`
- `deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql`
- `deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql`
- `backend/internal/handler/admin/account_handler.go`
- `backend/internal/service/admin_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/server/routes/admin.go`
