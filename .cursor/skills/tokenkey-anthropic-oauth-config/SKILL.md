---
name: tokenkey-anthropic-oauth-config
description: >-
  TokenKey Anthropic OAuth tier baseline 写入流水线（snapshot → check → plan → apply → verify）。
  覆盖单一写入面：edge anthropic OAuth account 的 tier baseline（concurrency / base_rpm
  / sticky_buffer / max_sessions 等 account 字段）。单一脚本
  ops/anthropic/manage-anthropic-config.py 编排；tier baseline 值只存在于
  baseline JSON 一处，apply SQL 由 orchestrator 运行时从 JSON 派生（无静态 SQL 模板）。
  group.rpm_limit 不由本流水线写——由 admin UI 直接独立设置。
---

# TokenKey：Anthropic OAuth tier baseline 流水线

适用于本仓库（TokenKey fork of sub2api）。**edge anthropic OAuth 账号** 的 tier baseline（concurrency、base_rpm、sticky_buffer、max_sessions、window_cost_limit、`stability_tier` 等）走同一条流水线写入。流水线固化在 `ops/anthropic/manage-anthropic-config.py`。

`group.rpm_limit` **不由本流水线写**——admin UI 独立设置；本流水线只管 OAuth 账号的 tier 字段。

权威纪律以仓库根 `CLAUDE.md` 为准。

## 1. 5 阶段流水线

每阶段一个命令，输入/输出明确，失败即停。所有写入通过一个固化 SQL 模板，操作员不写 SQL。

```bash
JOBDIR="$CLAUDE_JOB_DIR"               # or any scratch dir
MGR=ops/anthropic/manage-anthropic-config.py

# Stage 1 — Snapshot：拉所有 deployable edge 的 anthropic OAuth account 状态到 JSON
python3 $MGR snapshot --out $JOBDIR/snap.json

# Stage 2 — Check：每个 edge 跑一次 edge-oauth-stability guard，union 报告
python3 $MGR check --snapshot $JOBDIR/snap.json

# Stage 3 — Plan：声明 tier 变更意图，机器算 expected_after
python3 $MGR plan-edge-account-tier \
  --edge uk1 --account en-ld-ec2-16-1-b --tier l2 \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json

# Stage 4 — Apply：渲染 SQL → SSM → 写入；失败即停
python3 $MGR apply \
  --plan $JOBDIR/plan.json \
  --confirm yes-apply-anthropic-config-cascade

# Stage 5 — Verify：再 snapshot + 比对每个 action 的 expected_after vs live
python3 $MGR verify --plan $JOBDIR/plan.json
```

### 各阶段语义

| 阶段 | 输入 | 输出 | exit |
|---|---|---|---|
| snapshot | EC2 SSM 权限 | `snap.json`：每个 deployable edge 的 anthropic OAuth account 字段 | 0 / 2 error |
| check | snap.json | 每个 edge 跑 `check-edge-oauth-stability.py`，union violation | 0 ok / 1 violation / 2 error |
| plan-edge-account-tier | snap.json + edge+account+tier | `plan.json`：1 个 action（写 OAuth account 的 tier baseline） | 0 / 2 |
| apply | plan.json + confirm | 渲染 SQL → SSM 执行 → 写 `apply-report.json` | 0 / 1 step failed / 2 |
| verify | plan.json | 再 snapshot + 比对 `actions[*].expected_after` vs live；drift 列表 | 0 / 1 drift / 2 |

### 失败即停 + Pre-apply re-read

- `apply` 任一 step 失败 → STOP，`apply-report.json` 列出已完成 + 未完成 step
- Stage 5 verify 必须跑；drift → operator 决定补 apply 或回滚
- snapshot 出于"先查后说"原则：禁止凭记忆断言字段值，所有断言都来自一次 SSM read

## 2. 不在本流水线范围内（独立操作）

本流水线**只写** edge anthropic OAuth account 的 tier 字段。下列写入面**不由本脚本管**：

| 配置面 | 写入方式 |
|---|---|
| edge / prod `group.rpm_limit` | admin UI 直接编辑；operator 凭运维经验定独立绝对值，与 account 字段解耦 |
| edge / prod 任何 `group` 字段 | admin UI |
| prod anthropic apikey forward stub 字段（base_url / concurrency / ...） | admin UI |
| edge OAuth `account_groups` 绑定（哪个账号进哪个组） | admin UI |
| prod anthropic stub `account_groups` 双绑（default + cc-edges，见 § 3） | admin UI |
| OAuth 凭据轮换 / status | admin UI / OAuth flow |

历史上 2026-05-21 之前曾尝试用本脚本做"account → group 聚合 cascade"（edge group cap = Σ(base_rpm+sticky_buffer)，prod stub.declared_rpm 镜像 edge group cap，prod group cap = Σ declared_rpm），但层层聚合导致 upstream OAuth 实际容量被压在 `Σ(base_rpm+sticky_buffer)` 上界、sticky buffer 黄区 burst 跑不出来。该模型已退役，group 限流回归 operator 手工独立设定。

## 3. prod 控制面：anthropic stub 双绑规则

prod 上每一个 anthropic 转发 stub（`platform=anthropic AND type=apikey`，credentials 含 `base_url=api-<edge>.tokenkey.dev`）必须**同时绑两个分组**：

| 分组 | id | 用途 | 谁可见 |
|---|---|---|---|
| `default` | 1 | 对外用户流量 | 普通用户 API key |
| `cc-edges` | 15 | admin 调试旁路 | 仅 admin API key |

可见性强制点：`groups.is_exclusive=true` + `user_allowed_groups` 白名单。`cc-edges` 必须 `is_exclusive=true`，admin user 在 `user_allowed_groups` 写入 `(admin_user_id, 15)`。

operate 流程：
- **新增 anthropic edge stub** → 同步 `INSERT account_groups` 双行（default + cc-edges）。两行不可拆开 apply。
- **退役 anthropic edge stub** → 软删 stub 前 `DELETE account_groups` 两行。
- **edge 内部** 真正的 OAuth 账号绑定 `default` 组即可，不复刻 `cc-edges`（admin 调试旁路只在 prod 控制面有意义）。

这些 binding 改动通过 admin 前端手动操作；本流水线**不**涉及。

## 4. 故障速查

| 现象 | 处理 |
|---|---|
| snapshot 失败 / SSM 拒绝 | 校验 EC2 instance 在跑 / `edge-targets.json` / OIDC 权限 |
| `apply --confirm` 拒绝 | 必须精确 `yes-apply-anthropic-config-cascade` |
| tier baseline drift（check-edge-oauth-stability `extra_baseline_drift` / `account_field_drift`） | 用本流水线 plan-edge-account-tier 重写到对应 tier |
| check guard 报 `status: drift` 且 `diffs[].path` 含 `/credentials/temp_unschedulable_rules`，但数值字段全等 | 加 `--force-template-rewrite` 让 plan 不再走 noop 短路，强制重写 credentials 端字段（snapshot/verify 不读 rules，所以默认 noop；这条 flag 是 escape hatch）。Apply 完跑一次 `check` 当真值 |
| OAuth account `status=error/suspended` | OAuth 凭据问题（token 过期 / 403 / 上游禁用），见 OAuth 故障文档；不在本流水线范围 |
| verify drift | operator 决定再 apply 或回滚（用 admin 前端按 plan.live_inputs 的 `edge_account_before` 反向写回） |
| 想调 group.rpm_limit | admin UI 直接编辑 group，不要再用本流水线，也**不要**按 account 聚合手算 |

## 5. 附录 A：底层工具（emergency / debug）

正常流程**只走 5 阶段流水线**。下列工具在流水线 break 或紧急 rollback 时直接用：

**Guards**（只读检查）：
- `ops/anthropic/check-edge-oauth-stability.py --edge-id E --account-name A [--json] [--emit-sql FILE]` — edge OAuth tier baseline drift

**Apply SQL（JSON 派生，无静态模板）**：tier baseline 值只存在于 baseline JSON 一处；apply SQL 由 orchestrator 运行时从 JSON 渲染——内部复用 guard 的 `effective_baseline_for_tier`（合并 `shared_baseline` + tier 覆盖）+ `generate_sql`。手动 / 紧急生成同一份 SQL：
- `python3 ops/anthropic/check-edge-oauth-stability.py --edge-id E --account-name A --emit-sql out.sql`（按账号 live tier 渲染；改 tier 时先在 baseline JSON 调整对应 tier 值再渲染），再 base64 通过 SSM 注入。

**底线**：手动绕开 orchestrator 时 op 必须自己做 apply 后复核——同样不允许跳过 § 1 "先查后说"协议。

## 6. 附录 B：tier baseline 与 stable accounts

- baseline JSON：`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`（`tier_order: l1..l5`，每个 tier 含 `baseline.account.*` 与 `baseline.extra.*`，orchestrator 在 plan-edge-account-tier 中读它算 expected_after）
- 更新 stable_accounts 列表（仅人工确认后）：
  ```bash
  python3 ops/anthropic/check-edge-oauth-stability.py \
    --edge-id $EDGE --account-name $ACCT \
    --update-stable-list --confirm yes-update-anthropic-stable-list
  ```
  禁止在未确认稳定前更新 stable list。

## 7. 扩展阅读

- `ops/anthropic/manage-anthropic-config.py`（5 阶段 orchestrator，本 skill 唯一推荐入口）
- `ops/anthropic/check-edge-oauth-stability.py`
- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`（tier baseline 唯一真值源；apply SQL 运行时从它派生，无静态 SQL 模板）
- `backend/internal/handler/admin/account_handler.go`
- `backend/internal/service/admin_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/server/routes/admin.go`
