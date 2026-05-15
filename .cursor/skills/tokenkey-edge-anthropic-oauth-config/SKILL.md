---
name: tokenkey-edge-anthropic-oauth-config
description: >-
  Query and update edge Anthropic OAuth account stability config (including
  account fields and group bindings) with default read-only check, explicit
  apply confirmation, mixed-channel risk precheck, and post-change verification.
---

# TokenKey：Edge Anthropic OAuth 配置查询与更新（含账号与分组）

适用于本仓库（TokenKey fork of sub2api）。目标是把“查询漂移 → 计划变更 → 显式确认后更新 → 复核”固化为可复用流程。

权威纪律以仓库根 `CLAUDE.md` 为准。

## 调用参数

```text
/tokenkey-edge-anthropic-oauth-config edge_id=<id> account_name=<name> operation=<check|plan-apply|apply> [group_ids=1,2] [confirm_apply=yes-apply-edge-anthropic-oauth] [allow_planned=true|false] [update_stable_list=true|false]
```

| 参数 | 语义 |
|---|---|
| `edge_id` | 目标 edge，如 `us1` / `uk1` / `fra1`；支持 `all` 自动枚举所有 edge（默认仅 deployable）。 |
| `account_name` | 目标账号名（`accounts.name`）；支持 `all` 自动枚举每个 edge 下全部 anthropic oauth 账号。 |
| `account.extra.stability_tier` | 分级基线选择键（`l1_novice/l2_junior/l3_mid/l4_senior/l5_ultra`），`check` 会按该字段选择 tier baseline。 |
| `operation=check` | 默认模式，只读检查当前配置与基准差异。 |
| `operation=plan-apply` | 生成更新计划与请求 payload 预览，不写入。 |
| `operation=apply` | 执行更新（账号字段 + 可选分组绑定），必须显式确认。 |
| `group_ids` | 可选分组 ID 列表（逗号分隔）。 |
| `confirm_apply` | 仅 `apply` 使用，必须精确为 `yes-apply-edge-anthropic-oauth`。 |
| `allow_planned` | 是否允许在 planned edge 上执行检查，默认 `false`。 |
| `update_stable_list` | 可选；仅在人工确认稳定后才更新 baseline stable_accounts。 |

默认行为：
- 用户只说“查配置/看漂移” → `operation=check`
- 用户只说“更新/对齐” → 先执行 `check` + `plan-apply`，拿到确认后再 `apply`
- `edge_id=all` 默认只巡检 deployable edge；如需包含 planned，显式加 `allow_planned=true`

## 一次性跑完（原则）

- 默认只读，先 `check`。
- 任何写入前都先给出 `plan-apply` 预览。
- `apply` 无固定确认口令则拒绝执行。
- 失败先停并报告，不做隐式重试覆盖。
- 输出禁止包含 token/secret 明文。

## 1) Read-only 检查（operation=check）

复用脚本：`scripts/check-edge-anthropic-oauth-stability.py`

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

## 2) 变更预览（operation=plan-apply）

`plan-apply` 只做两件事：
1. 基于 `check` 结果列出待更新字段；
2. 生成 admin API 请求 payload 预览。

安全限制：
- `plan-apply` 不允许 `account_name=all`（避免批量账号预览被误当可执行清单）。

`group_ids` 语义必须与后端一致：
- **不传 `group_ids`**：不改分组绑定；
- **传空数组 `[]`**：清空分组绑定；
- **传非空数组 `[1,2]`**：重绑分组。

## 3) 执行更新（operation=apply）

### 3.1 强制确认

必须提供：

```text
confirm_apply=yes-apply-edge-anthropic-oauth
```

缺失或不匹配则拒绝执行。

安全限制：
- `apply` 仅允许单 edge + 单账号；
- 出现 `edge_id=all` 或 `account_name=all` 一律拒绝执行。

### 3.2 预检 mixed-channel 风险（涉及 group_ids 时）

先调用：
- `POST /api/v1/admin/accounts/check-mixed-channel`

若预检返回风险且未明确确认，停止执行。

### 3.3 调用更新接口

单账号更新统一走：
- `PUT /api/v1/admin/accounts/:id`

请求体包含：
- 需要对齐的账号字段（如 `concurrency`、`priority`、`rate_multiplier`、`auto_pause_on_expired` 等）
- 可选 `group_ids`

备注：
- 本 skill 当前聚焦单账号执行链。
- 批量更新可后续扩展到 `POST /api/v1/admin/accounts/bulk-update`。

## 4) 变更后复核

`apply` 完成后必须自动复核：

1. 再跑一次 `operation=check`；
2. 对比前后 `diff_count` 与 `diffs`；
3. 输出结构化结果：
   - `edge_id`
   - `account_name`
   - 更新字段列表
   - 分组变更（若有）
   - 复核状态（是否收敛到 `diff_count=0`）

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

单目标模式：

```text
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
- `backend/internal/handler/admin/account_handler.go`
- `backend/internal/service/admin_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/server/routes/admin.go`
