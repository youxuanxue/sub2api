---
name: tokenkey-anthropic-oauth-config
description: >-
  TokenKey Anthropic 账号/分组配置 5 阶段流水线：snapshot → check → plan → apply
  → verify。覆盖 edge OAuth tier baseline、prod 转发 stub R1 concurrency 镜像、
  prod stub R3-unified declared_rpm 与 group rpm_limit 级联，单一脚本
  ops/anthropic/manage-anthropic-config.py 编排，4 个 SQL 模板固化所有写入。
  默认只读检查、显式 confirm 后才写、apply 之后必复核。
---

# TokenKey：Anthropic 配置 5 阶段流水线

适用于本仓库（TokenKey fork of sub2api）。任何 anthropic 配置变更——edge OAuth tier 升降、prod stub external 配额修改——都走同一条流水线。流水线本身固化在 `ops/anthropic/manage-anthropic-config.py`，本文档只描述协议与背景。

权威纪律以仓库根 `CLAUDE.md` 为准。

## 1. 5 阶段流水线（**唯一推荐路径**）

每阶段一个命令，输入/输出明确，失败即停。所有写入通过 4 个固化 SQL 模板，操作员不写 SQL，不手动编排。

```bash
JOBDIR="$CLAUDE_JOB_DIR"               # or any scratch dir
MGR=ops/anthropic/manage-anthropic-config.py

# Stage 1 — Snapshot：拉 prod + 所有被 prod 引用的 edge 的全量状态到 JSON
python3 $MGR snapshot --out $JOBDIR/snap.json

# Stage 2 — Check：编排 3 个 guard，union 报告
python3 $MGR check --snapshot $JOBDIR/snap.json

# Stage 3 — Plan：声明意图，机器算 cascade
# 3a) edge OAuth 账号 tier 变更（最常见）
python3 $MGR plan-edge-account-tier \
  --edge uk1 --account en-ld-ec2-16-1-b --tier l2 \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json

# 3b) external apikey stub 配额变更
python3 $MGR plan-external-stub \
  --stub tokensea-0.4 --declared-rpm 150 \
  --snapshot $JOBDIR/snap.json --out $JOBDIR/plan.json

# Stage 4 — Apply：按 plan 串行执行；失败即停
python3 $MGR apply \
  --plan $JOBDIR/plan.json \
  --confirm yes-apply-anthropic-config-cascade

# Stage 5 — Verify：再 snapshot + 比对每个 expected_after vs live
python3 $MGR verify --plan $JOBDIR/plan.json
```

### 各阶段语义

| 阶段 | 输入 | 输出 | exit |
|---|---|---|---|
| snapshot | EC2 SSM 权限 | `snap.json`：prod 所有 anthropic apikey stubs + 它们引用的 edge 的 OAuth 账号 + groups | 0 / 2 error |
| check | snap.json | 调 3 个 guard（prod-stub-mirror / edge-oauth-stability / account-group-rpm-alignment），union violation | 0 ok / 1 violation / 2 error |
| plan-edge-account-tier | snap.json + edge+account+tier | `plan.json`：5 个 action（edge account → edge group → prod stub conc → prod groups r3-unified） | 0 / 2 |
| plan-external-stub | snap.json + stub+declared_rpm | `plan.json`：1+ action（仅 prod 侧 groups） | 0 / 2 |
| apply | plan.json + confirm | 渲染每个 action 的 SQL → SSM 执行 → 解析 jsonb 输出 → 校验 `expected_after`；写 `apply-report.json` | 0 / 1 step failed / 2 |
| verify | plan.json | 再 snapshot + 比对 `actions[*].expected_after` vs live；drift 列表 | 0 / 1 drift / 2 |

### Cascade 推导规则（plan-* 内部，op 不需要手算）

**plan-edge-account-tier** — 一个 edge tier 变化会级联到 5 个写入点：

```
edge OAuth account.tier  →  edge default group.rpm_limit
                         →  prod self-edge stub.concurrency
                         →  prod self-edge stub.extra.declared_rpm
                         →  prod each group containing this stub.rpm_limit
```

- step1：写 edge 账号 tier baseline（`anthropic-oauth-stability-tiered-apply-template.sql`）
- step2：写 edge default group rpm_limit = `absorb_zero_sum(base_rpm + sticky_buffer)` over active OAuth（`anthropic-oauth-group-aggregate-apply-template.sql`）
- step3：写 prod 对应 self-edge stub concurrency = `absorb_zero_sum(edge OAuth concurrency)`（`anthropic-stub-mirror-concurrency-apply-template.sql`）
- step4..N：写 prod 含此 stub 的每个 group → 每个成员的 `extra.declared_rpm` + `group.rpm_limit = Σ declared_rpm`（`anthropic-prod-group-r3-unified-apply-template.sql`，单事务）

**plan-external-stub** — external 改动不影响 edge，只改 prod：

- step1..N：写 prod 含此 stub 的每个 group → 替换该 stub `declared_rpm` 后整组 SUM

`absorb_zero_sum`（R1 concurrency 用）：任一项 0 ⇒ 总和 0（unlimited 传播）；否则普通 SUM。
R3-unified group rpm_limit 是**普通 SUM**（禁 absorb-zero，禁 unlimited）。详见 § 5。

### 失败即停 + Pre-apply re-read

- `apply` 任一 step 失败 → STOP，`apply-report.json` 列出已完成 + 未完成 step
- 模板 DO-block 在事务内做二次校验（成员集 / Σ / declared_rpm > 0 / 账号 active），plan/apply 之间被并发会话改了 → 拒绝并 RAISE
- Stage 5 verify 必须跑；drift → operator 决定补 apply 或回滚
- snapshot 出于"先查后说"原则：禁止凭记忆断言字段值，所有断言都来自一次 SSM read

## 2. prod 控制面：anthropic stub 双绑规则

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

这些 binding 改动不在 orchestrator 自动 cascade 范围内（涉及 SKU/UX 决策）；用 admin 前端手动操作，再用 Stage 1 snapshot + Stage 2 check 验证。

## 3. R3-unified 镜像规则（背景）

prod 是 router、edge 是 worker。router 公布的容量必须等于它能调动的 worker 总容量；为零会让组成为外部滥用入口。

### `accounts.extra.declared_rpm` 字段约定

| stub 类型 | `declared_rpm` 来源 | 校验 |
|---|---|---|
| self-edge（`api-<edge>.tokenkey.dev`） | 自动镜像 upstream edge `default_group.rpm_limit` | guard 检 `declared_rpm == upstream.rpm_limit`；漂移 = `r3_self_edge_mirror_drift` |
| external（其他 base_url） | 运营显式声明 | guard 检 `declared_rpm > 0`；缺失 = `r3_declared_rpm_missing`，零/负 = `r3_declared_rpm_zero_forbidden` |

`declared_rpm` 当前是 guard / audit-trail 元数据；runtime 仍由 `group.rpm_limit` 实施限速。未来 Go 层若引入 stub-level 配额会从同一 key 读。

### 容量约定（2026-05-21 修订）

| 字段 | 层 | 0 的含义 | 聚合算子 |
|---|---|---|---|
| `account.concurrency` | 账号 | runtime 无并发限制 | absorb-zero SUM（保留 unlimited 语义） |
| `account.extra.base_rpm` | OAuth | runtime 未启用 | absorb-zero SUM（OAuth tier baseline 聚合用） |
| `account.extra.declared_rpm` | apikey stub | **禁止 0**（unlimited 已退场） | 普通 SUM |
| `group.rpm_limit` | 组 | **prod anthropic 组禁止 0** | 普通 SUM = Σ `declared_rpm` |

### R3-unified 算式

```
prod_group.rpm_limit  ==  Σ  stub.declared_rpm   for stub in g.stubs  (plain SUM)
stub.declared_rpm     >   0                                            (unlimited forbidden)
self-edge stub.declared_rpm  ==  upstream_edge.default_group.rpm_limit (mirror)
external stub.declared_rpm   ==  operator declared quota                (visible)
```

历史背景：2026-05-21 之前 R3 用 absorb-zero，mixed group（含 external）→ `rpm_limit=0`（unlimited），是唯一无 RPM 闸门的对外入口。R3-unified 把 unlimited 从合法状态删除，强制每个 stub 显式声明产能。

## 4. 故障速查

| 现象 / violation kind | 处理 |
|---|---|
| snapshot 失败 / SSM 拒绝 | 校验 EC2 instance 在跑 / `edge-targets.json` / OIDC 权限 |
| `apply --confirm` 拒绝 | 必须精确 `yes-apply-anthropic-config-cascade` |
| `r1_mirror_drift` | edge OAuth concurrency 改了但 prod stub 未跟上 → `plan-edge-account-tier` 重新算 |
| `r3_declared_rpm_missing` | stub 缺 `extra.declared_rpm` → orchestrator 不应漏写；查 apply-report 看哪 step 失败 |
| `r3_declared_rpm_zero_forbidden` | declared_rpm ≤ 0 写入 → 检查 plan.json 的 stub_inputs 值是否被外部改成 0 |
| `r3_self_edge_mirror_drift` | edge default.rpm_limit 改了但 prod stub declared_rpm 未跟上 → `plan-edge-account-tier` |
| `r3_group_rpm_zero_forbidden` | prod group rpm_limit 被外部回退到 0 → 重跑 plan + apply |
| `r3_group_sum_mismatch` | group.rpm_limit ≠ Σ declared_rpm → 重跑 plan + apply |
| `upstream_no_active_oauth` | edge default group 没有 active OAuth（status=error/suspended/软删）→ edge OAuth 健康问题，不是镜像数学错误 |
| apply 模板 DO-block RAISE "stub_inputs SUM=...mismatch...:target_group_rpm=..." | plan 与 apply 间被并发会话改了；重新 snapshot + plan |
| apply 模板 DO-block RAISE "group has X members but stub_inputs has Y" | 组成员被增删；重新 snapshot + plan |
| verify drift | operator 决定再 apply 或回滚（用 admin 前端按 plan.live_inputs 的 *_before 反向写回） |

## 5. 附录 A：底层工具（emergency / debug）

正常流程**只走 5 阶段流水线**。下列工具在流水线 break 或紧急 rollback 时直接用：

**Guards**（只读检查）：
- `ops/anthropic/check-prod-stub-mirror.py [--json] [--legacy-r3]` — R1 + R3-unified
- `ops/anthropic/check-edge-oauth-stability.py --edge-id E --account-name A [--json] [--emit-sql FILE]` — edge OAuth tier baseline drift
- `ops/anthropic/check-account-group-rpm-alignment.py --target T [--strict-redline] [--json]` — Layer A/B/C 校验

**Apply 模板**（每个都自包含、有 DO-block 校验；orchestrator 内部自动渲染，手动写需要自包含 SQL 起手 `\set ...` 然后 base64 通过 SSM 注入）：
- `deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql` — edge OAuth tier
- `deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql` — edge OAuth group cap（strict-redline）
- `deploy/aws/stage0/anthropic-stub-mirror-concurrency-apply-template.sql` — prod stub R1 concurrency
- `deploy/aws/stage0/anthropic-prod-group-r3-unified-apply-template.sql` — prod stub declared_rpm + group cap（单事务）
- ⚠ deprecated：`deploy/aws/stage0/anthropic-stub-mirror-rpm-apply-template.sql`（顶部 DO-block hard gate，需 `SET app.ack_deprecated='yes-r3-unified-replaces-this'` 才能跑；仅用于 emergency rollback）

**底线**：手动绕开 orchestrator 时 op 必须自己完成 cascade 计算 + DO-block 校验 + apply 后复核——同样不允许跳过 § 1 "先查后说"协议。

## 6. 附录 B：tier baseline 与 stable accounts

- baseline JSON：`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`（`tier_order: l1..l5`，每个 tier 含 `baseline.account.*` 与 `baseline.extra.*`，orchestrator 在 plan-edge-account-tier 中读它算 cascade）
- 更新 stable_accounts 列表（仅人工确认后）：
  ```bash
  python3 ops/anthropic/check-edge-oauth-stability.py \
    --edge-id $EDGE --account-name $ACCT \
    --update-stable-list --confirm yes-update-anthropic-stable-list
  ```
  禁止在未确认稳定前更新 stable list。

## 7. 扩展阅读

- `ops/anthropic/manage-anthropic-config.py`（5 阶段 orchestrator，本 skill 唯一推荐入口）
- `ops/anthropic/check-prod-stub-mirror.py`（R1 + R3-unified guard）
- `ops/anthropic/check-edge-oauth-stability.py`
- `ops/anthropic/check-account-group-rpm-alignment.py`
- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`
- `deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql`
- `deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql`
- `deploy/aws/stage0/anthropic-stub-mirror-concurrency-apply-template.sql`
- `deploy/aws/stage0/anthropic-prod-group-r3-unified-apply-template.sql`
- ⚠ deprecated：`deploy/aws/stage0/anthropic-stub-mirror-rpm-apply-template.sql`
- `backend/internal/handler/admin/account_handler.go`
- `backend/internal/service/admin_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/server/routes/admin.go`
