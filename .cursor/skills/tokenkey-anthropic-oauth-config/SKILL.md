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
| `account.extra.stability_tier` | 分级基线选择键（`l1/l2/l3/l4/l5`），`check` 会按该字段选择 tier baseline。 |
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

## prod 控制面：anthropic stub R1 + R3-unified 镜像规则

prod 上**自持 edge 主路径**类型的 anthropic stub（`credentials.base_url` 匹配 `https://api-<edge_id>.tokenkey.dev`）必须**镜像**其上游 edge 的容量；**外部兜底类型**的 anthropic stub（例如 `agent.tokensea.ai`、`api.deepseek.com/anthropic`）必须**显式声明** RPM 配额。`prod 是 router、edge 是 worker`——router 公布的容量必须等于它实际能调动的 worker 总容量；过紧会替 edge 提前 429/503（伪 throttle），过松会下推真 429 给客户端；**为零会让组成为外部滥用入口**（2026-05-21 R3-unified 修订点）。

### `declared_rpm` 字段约定（R3-unified 新增）

`accounts.extra.declared_rpm`（int，必填，> 0）是每个 anthropic apikey stub 的 RPM 声明值：

| stub 类型 | `declared_rpm` 来源 | 谁负责对齐 | 漂移检测 |
|---|---|---|---|
| self-edge（`api-<edge>.tokenkey.dev`） | **自动镜像** 上游 edge `default_group.rpm_limit` | edge tier baseline 改动后操作员对齐 prod | guard 检 `declared_rpm == upstream.default.rpm_limit`，不一致 = `r3_self_edge_mirror_drift` |
| external（其他 base_url） | **运营显式声明** 的配额 | 创建 stub 时同步落档 | guard 检 `declared_rpm > 0`，缺失 = `r3_declared_rpm_missing`，零/负 = `r3_declared_rpm_zero_forbidden` |

**runtime 是否消费 `declared_rpm`**：当前 backend Go 代码不直接读取它；runtime 的 RPM 闸门仍由 `group.rpm_limit` 实施（在 group level 限速）。`declared_rpm` 是 guard / audit-trail 的元数据，固化"每个 stub 承诺的产能或配额"，让 group cap 有数学依据。未来若 backend 引入 stub-level 配额，会从同一 key 读。

### 容量约定（runtime 既有事实，2026-05-21 修订）

| 字段 | 所在层 | 0 的含义 | 聚合算子 |
|---|---|---|---|
| `account.concurrency` | 账号 | runtime 无并发限制（[`concurrency_service.go:131`](backend/internal/service/concurrency_service.go:131)） | **absorb-zero SUM**（unlimited 传播） |
| `account.extra.base_rpm` | OAuth 账号 | runtime 未启用（[`account.go:2020`](backend/internal/service/account.go:2020) / `CheckRPMSchedulability`） | **absorb-zero SUM**（OAuth tier baseline 保留旧语义） |
| `account.extra.declared_rpm` | apikey stub | **禁止 0**（unlimited 已退场） | **普通 SUM**（不 absorb-zero） |
| `group.rpm_limit` | 组 | **prod anthropic 组禁止 0**（unlimited 已退场）；其他平台暂未硬化 | **普通 SUM**，等于 Σ `declared_rpm` |

**为什么 declared_rpm 不允许 0**：mixed group（self-edge + external）在旧 R3 absorb-zero 下 → `group.rpm_limit=0` → unlimited group → 外部滥用入口。2026-05-21 R3-unified 把"unknown=unlimited"从合法状态删除，强制每个 stub 显式声明产能（self-edge 自动镜像，external 运营手填），从而 group cap = Σ 是可算的有限上限。

### 镜像规则

**R1 — 账号级 concurrency 镜像**（每个 self-edge stub，保留 absorb-zero）

```
stub.concurrency  ==  absorb_zero_sum(
                        oauth_acc.concurrency
                        for oauth_acc in upstream_edge.default group
                        where active
                      )
```

含义：一个 stub 代表整个 edge `default` 组的合计 inflight 容量。多 OAuth 账号 edge 的合并值大于单账号；若任一 upstream OAuth 是 unlimited（`concurrency=0`），stub 也必须 `concurrency=0`。R1 保留 absorb-zero：`account.concurrency=0` 在 runtime 仍然是 unlimited 语义（concurrency_service.go），propagating 它到 stub 是正确语义。

**R3-unified — 分组级 RPM 声明**（每个含 stub 的 prod 组，替代旧 absorb-zero R3）

```
prod_group.rpm_limit  ==  Σ  stub.declared_rpm   for stub in g.stubs    (plain SUM)
stub.declared_rpm     >   0                                              (forbid unlimited)
self-edge stub.declared_rpm  ==  upstream_edge.default_group.rpm_limit   (mirror)
external stub.declared_rpm   ==  operator declared quota                  (visible)
```

含义：每个 stub 都自报一个有限的 RPM 数字；group cap 是它们的普通 SUM。`unlimited`（任何 0）退出合法状态——`unknown ≠ unlimited`，operator 必须显式声明 external 容量。`absorb-zero SUM` 不再用于 R3（仅保留给 R1 concurrency 与 OAuth tier baseline 聚合）。

**`mixed` group 不再特殊**：组合 self-edge + external 的组直接按公式 `SUM(self-edge declared + external declared)` 算 cap；这与"所有 group 都必须有 rpm 闸门"原则一致，不需要拆组。

**注（strict-redline 之后）**：upstream edge `default_group.rpm_limit` 现按 §3.2.1 切换为 `Σ(account.base_rpm + extra.rpm_sticky_buffer)`（含 sticky_buffer 空间）。self-edge stub 的 `declared_rpm` 随之提高，留出黄区流量空间。

### 刻意**不**镜像的字段（设计放弃）

- `accounts.extra.base_rpm` / `extra.max_sessions` / `extra.window_cost_limit` — stub 不读这些（runtime 由 edge OAuth 自己持有，在 edge 侧落档）。
- `accounts.priority` — prod 组内调度顺序，与 edge 内部排序无关。

### 共同 baseline（所有 stub 必须满足）

- `channel_type = 0`
- `rate_multiplier = 1.0`
- `auto_pause_on_expired = true`
- `status = 'active'`
- `accounts.extra.declared_rpm > 0`（R3-unified 新增）

### 强制 pre-apply 门禁

```bash
python3 ops/anthropic/check-prod-stub-mirror.py
python3 ops/anthropic/check-prod-stub-mirror.py --json          # CI-friendly
python3 ops/anthropic/check-prod-stub-mirror.py --legacy-r3     # rollout 过渡窗口 only
```

退出码：
- exit 0 — 所有 stub 通过 R1 + R3-unified + baseline
- exit 1 — 任一违规，**修复后才能 apply**
- exit 2 — SSM / schema / target 解析失败

报告分两段：`--- account-level (R1 concurrency + R3-unified per-stub declared_rpm) ---` 与 `--- group-level (R3-unified Σ declared_rpm) ---`。JSON 模式下 `stub_violation_count` 与 `group_violation_count` 分项汇总；每条 violation 带 `kind` 字段：

| kind | 含义 | 修法 |
|---|---|---|
| `r1_mirror_drift` | stub.concurrency ≠ absorb_zero_sum(upstream OAuth) | 用 concurrency apply 模板对齐 |
| `r1_upstream_no_active_oauth` | edge default group 没有 active 状态的 anthropic OAuth 账号（可能 status=error / suspended / 软删） | edge OAuth 健康问题，不是镜像数学错误；查 `accounts.status` 与 `error_message`，决定修复 OAuth 还是下线 prod stub。R3-unified 的 `declared_rpm` 镜像仍可继续校验（不依赖 OAuth 状态） |
| `r3_declared_rpm_missing` | stub 缺 `extra.declared_rpm` | 用 R3-unified apply 模板写入 |
| `r3_declared_rpm_zero_forbidden` | declared_rpm ≤ 0 | 同上 + 由 operator 拍非零值 |
| `r3_self_edge_mirror_drift` | self-edge.declared_rpm ≠ upstream.default.rpm_limit | 同上 + 取上游实时值 |
| `r3_group_rpm_zero_forbidden` | group.rpm_limit ≤ 0（含 stubs） | 用 R3-unified apply 模板把组写到 Σ declared_rpm |
| `r3_group_sum_mismatch` | group.rpm_limit ≠ Σ declared_rpm | 同上 |
| `r3_group_sum_blocked_by_stub_violation` | 组内某 stub 还缺 declared_rpm（先修 stub） | 先解决 stub 级违规 |

`--legacy-r3` 开关临时回退到旧 absorb-zero R3，**仅在 R3-unified rollout 过渡窗口使用**，默认开启被 skill 禁止；CI 不接受 `--legacy-r3` 输出作为 PASS。

### R1 / R3-unified 修复路径（guard fail 后 apply）

`check-prod-stub-mirror.py` 只检测、不修改。fail 后按违规类型分两条 apply 路径，**两条都要打**（如果两类违规都存在），否则下次 guard 仍会失败：

- **R1（stub concurrency drift）** — guard `results[i].mirror_violations[kind=r1_mirror_drift]`。op 用 [`anthropic-stub-mirror-concurrency-apply-template.sql`](../../../deploy/aws/stage0/anthropic-stub-mirror-concurrency-apply-template.sql) 逐 stub 把 `account.concurrency` 写到 `expected_concurrency`。模板顶部 DO 块拒绝 OAuth 账号 / 非 self-edge stub。
- **R3-unified（declared_rpm + group cap）** — guard 输出含 `r3_*` kind violations。op 用 [`anthropic-prod-group-r3-unified-apply-template.sql`](../../../deploy/aws/stage0/anthropic-prod-group-r3-unified-apply-template.sql)：单事务内同时写每个 stub 的 `extra.declared_rpm` 和组的 `rpm_limit`，模板内嵌 SUM 校验、成员集校验、declared_rpm > 0 校验。旧 [`anthropic-stub-mirror-rpm-apply-template.sql`](../../../deploy/aws/stage0/anthropic-stub-mirror-rpm-apply-template.sql) 已弃用（保留作为审计 trail，头部有 deprecation 注释指向新模板）。

典型连发顺序：先 R1 stubs 全打完，再 R3-unified group cap。任何顺序都不会触发其他模板的拒绝门禁（模板的 DO 块互斥保护各自的写入面）。

### Pre-apply re-read 协议（2026-05-21 新增，防并发会话覆盖）

**触发场景**：本 skill 的 apply 没有跨会话锁。同一 prod 数据库可能有多个 Claude/Cursor 会话或 admin 前端在同时操作；2026-05-21 调研发现，本会话 R1 apply 后 1 分钟内被另一会话的 admin 操作覆盖。R3-unified 把"unlimited 不再合法"硬化成 guard violation，但写入并发仍要靠协议防御。

**协议**（强制，每次 apply 前执行）：

1. **plan-apply 时**：所有读取过的"实时输入"（edge OAuth tier、edge default.rpm_limit、prod stub `declared_rpm` / `concurrency`、`group.rpm_limit`、`account_groups` 成员集）都打时间戳，记入 plan 文件（`$CLAUDE_JOB_DIR/<env>-<target>-plan.json` 或 SQL 头部注释）。
2. **apply 之前最后一刻**：重新拉一次同样的输入，与 plan 文件比对；任一字段漂移即 stop + re-plan，**不能**直接 apply。
3. **apply 模板内嵌的二次校验**：[`anthropic-prod-group-r3-unified-apply-template.sql`](../../../deploy/aws/stage0/anthropic-prod-group-r3-unified-apply-template.sql) 的 DO 块在事务内再次校验：
   - `stub_inputs` 的 account_id 集合 ≡ 当前 group 的 anthropic apikey 成员集（防 group 成员增删）
   - `Σ declared_rpm == :target_group_rpm`（防 plan/apply 数字不一致）
   - 每个 input 指向有效的 anthropic apikey 账号（防 stub 被删/改）
   - 每个 declared_rpm > 0（防 unlimited 落档）
4. **apply 之后**：立即再跑 `check-prod-stub-mirror.py`，若回到 violation 状态 → 报告漂移源（很可能是另一会话），**不**隐式补打。

**"先查后说"原则**（强制）：任何对 prod/edge 字段值的断言（"X 当前是 N"）必须以一次 SSM read 为前提。不允许"我以为/我记得 X = N"出现在 plan 里。本会话曾因此踩坑——误以为 external stub 已配置 RPM，实际 `accounts.extra` 全空。

### 规则的本质

prod 是 router、edge 是 worker。两条镜像规则的核心约束：
- **R1**：router 公布的并发数 = worker 并发数 SUM（absorb-zero，保留 unlimited 语义）
- **R3-unified**：router 公布的 RPM = Σ worker + external 的 RPM 声明（普通 SUM，禁止 unlimited）

任何 edge tier 变更（H1 升降、增删 OAuth 账号）会被 guard 自动捕获并指向具体修复模板。

历史事故：
- **2026-05-18 01:38-01:42 UTC** — `cc-edges.rpm_limit=8` 与 `edge-us1 default.rpm_limit=8` 形成双层 RPM 限速，prod 抢先 429/503。R3（absorb-zero 版）上线后此模式 fail。
- **2026-05-21** — 调研发现 `default` 组是 mixed（含 external），旧 R3 absorb-zero 让它停留在 `rpm_limit=0` (unlimited)，是唯一无 RPM 闸门的对外入口。R3-unified 把这类状态变为 guard violation 直接禁用；当日 apply 把 `default.rpm_limit` 从 0 升到 364（self-edge 16+48 + external 100×3）。

## 一次性跑完（原则）

- 默认只读，先 `check`。
- 任何写入前都先给出 `plan-apply` 预览。
- `apply` 无固定确认口令则拒绝执行。
- 失败先停并报告，不做隐式重试覆盖。
- 输出禁止包含 token/secret 明文。
- **先查后说**：对 prod/edge 任何字段值的断言（"X 当前是 N"）必须以一次 SSM read 为前提；禁止"我以为/我记得 X = N" 进入 plan。本会话曾因此踩坑——误以为 external stub 已配置 RPM，实际 `accounts.extra` 全空。
- **Pre-apply re-read**：apply 前最后一刻必须重新拉一次"实时输入"与 plan 比对，任一字段漂移立即 stop + re-plan。模板内嵌的 DO 块会做事务内二次校验，但 plan/apply 之间的并发会话窗口需要由 operator 自觉协议保护（见 §"Pre-apply re-read 协议"）。
- **"unlimited" 不是合法状态**（2026-05-21 修订）：`accounts.extra.declared_rpm > 0` 与 prod anthropic `group.rpm_limit > 0` 强制；`0 = unlimited` 仅保留给 `account.concurrency` 与 OAuth `extra.base_rpm`（runtime 既有事实）。

## 分组口径与聚合规则（target_scope=group）

仅统计“分组下可用账号”（建议口径：`deleted_at IS NULL`、平台=`anthropic`、类型=`oauth`、未被临时/永久禁用，且通过当前调度可用性判定）。

聚合算子统一为 **absorb-zero SUM**（见 §"prod 控制面：anthropic stub 主路径镜像规则" 的"容量约定"）：任一可用账号该字段为 0（runtime 即 unlimited）⇒ 聚合结果为 0；否则 = Σ。朴素 SUM 会把 unlimited 当 0 数值算入加法，得出错误的有限上限。

聚合字段建议口径：
- `group_agg.available_account_count` = 可用账号数量
- `group_agg.total_base_rpm` = absorb-zero SUM(每个可用账号 `extra.base_rpm`)
- `group_agg.total_redline` = absorb-zero SUM(每个可用账号 `extra.base_rpm + extra.rpm_sticky_buffer`)；这是 strict-redline 口径下 group cap apply 的目标值，运行时对齐账号 NotSchedulable 阈值
- `group_agg.total_max_sessions` = absorb-zero SUM(每个可用账号 `extra.max_sessions`)
- `group_agg.total_window_cost_limit` = absorb-zero SUM(每个可用账号 `extra.window_cost_limit`)
- `group_agg.effective_concurrency` = absorb-zero SUM(每个可用账号 `account.concurrency`)
- `group_agg.min_priority` / `max_priority` = 分组内优先级范围（数值越小优先级越高）
- `group_agg.tier_distribution` = 各 tier 账号计数（L1~L5）

`check` 输出应同时包含：
1) 分组聚合结果（group_agg）；
2) 成员账号明细（每个账号的 tier、diff_count、关键字段）；
3) 分组是否存在“混合 tier / 混合 channel”风险标记。

## 1) Read-only 检查（operation=check）

复用脚本：`ops/anthropic/check-edge-oauth-stability.py`

### 1.1 账号模式（target_scope=account）

标准命令形态：

```bash
python3 ops/anthropic/check-edge-oauth-stability.py \
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
python3 ops/anthropic/check-edge-oauth-stability.py \
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
  - `total_redline_before/after`（strict-redline 模式下 group cap apply 的目标值）
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
python3 ops/anthropic/check-account-group-rpm-alignment.py --target <edge_id|prod>
python3 ops/anthropic/check-account-group-rpm-alignment.py --target <edge_id|prod> --strict-redline
```

对每个 anthropic group（`rpm_limit > 0`）按所选模式校验账号 redline ↔ `group.rpm_limit`：

| 模式 | redline 定义 | 含义 |
|---|---|---|
| 默认（legacy） | `account.extra.base_rpm` | 历史 H1 兼容口径，仅校验绿区上限。**已知漏防护**：group.rpm_limit 可被夹到 Σ base_rpm，sticky_buffer 空间无法生效，黄区流量被组提前 429。 |
| `--strict-redline`（推荐） | `account.extra.base_rpm + extra.rpm_sticky_buffer` | 对齐 runtime `(*Account).CheckRPMSchedulability` 的 NotSchedulable 阈值（[`account.go:2092`](../../../backend/internal/service/account.go:2092)）。group cap 必须为 in-flight StickyOnly 流量留位。 |

两个模式共享两层规则：

- **Layer A**（账号不被组卡）：`max(redline) ≤ group.rpm_limit`
  违反 = 组成为单账号的隐性 bottleneck。H1 (2026-05-17) 在 edge uk1/fra1 上踩中（`default.rpm_limit=3` 卡住 `base_rpm=6`）。
- **Layer B**（组 cap 不超出组内产能总和）：`Σ(redline) ≥ group.rpm_limit`
  违反 = 组的 RPM 上限超过组内 OAuth 账号实际能合并撑起的速率，多出的 cap 是虚的（永远跑不到）。

`--strict-redline` 额外增加：

- **Layer C**（baseline drift）：每个 `base_rpm > 0` 的账号必须同时具备 `extra.rpm_sticky_buffer > 0`。
  违反 = baseline 尚未落档。修法：按 [`anthropic-oauth-stability-baselines-tiered.json`](../../../deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json) 中该账号 tier 的 `rpm_sticky_buffer` 字段补齐，再重跑 guard。

跳过条件（不计入 violation）：

- `rpm_limit = 0` 或 NULL — 视为 unlimited，整组跳过。
- `rpm_limit > 0` 但组内**无** anthropic OAuth 账号 / 无任何账号声明 `extra.base_rpm` — 视为 stub-only 组（如 prod `cc-edges`），本 guard 不适用；如需对 stub 容量护栏，见 §"prod stub 主路径镜像规则"。
- `--strict-redline` 模式下，组内任一账号 `extra.rpm_strategy = 'sticky_exempt'` —— 该策略没有有限的 redline，整组 skip 并提示 op 手动核对（TokenKey 当前不启用此策略，仅作前向防御）。

退出码：

- exit 0 — 所有 in-scope 组通过两层（strict 模式额外含 Layer C）
- exit 1 — 至少一组违反 Layer A / B / C，**必须先修复**再 apply：
  - Layer A 修法：升 `group.rpm_limit` ≥ 该组 max(redline)；或调低 offender 账号 tier
  - Layer B 修法：降 `group.rpm_limit` ≤ 该组 sum(redline)；或往组里加 OAuth 账号补容量
  - Layer C 修法：按 baseline tier 补齐账号的 `extra.rpm_sticky_buffer`
- exit 2 — 目标/SSM/schema 故障，按错误排查

#### 3.2.1 rollout 顺序（默认 → strict）

新口径上线需要观察窗口，避免 guard 自锁现有 group cap：

1. **PR 合入** — `--strict-redline` 默认关闭，线上 apply 流仍走旧口径，行为零回归。
2. **离线巡检** — 逐 edge / prod 用 `--strict-redline --json` 跑一遍，确认 Layer C 全过（baseline 已逐 tier 落档），列出 Layer A/B violation 清单。
3. **升 edge group cap** — 对每个 strict-mode 下违反 Layer A/B 的 **OAuth-bearing** group（典型是 edge `default`），按 [`anthropic-oauth-group-aggregate-apply-template.sql`](../../../deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql) 生成自包含 SQL，把 `group.rpm_limit` 升到 `Σ redline`。
4. **同步 prod 镜像（R1 + R3-unified）** — `check-prod-stub-mirror.py` 是 guard 不是 applier；edge tier 改完后它会在下一次巡检里检出 prod 镜像漂移。**两类违规都要修**：
   - **R1（stub concurrency drift）**：edge OAuth 账号 concurrency 升档后，prod 对应 self-edge stub 的 `account.concurrency` 必须同步抬升。op 用 [`anthropic-stub-mirror-concurrency-apply-template.sql`](../../../deploy/aws/stage0/anthropic-stub-mirror-concurrency-apply-template.sql) 逐 stub apply（取值 `results[i].expected_concurrency`）。
   - **R3-unified（declared_rpm + group cap）**：edge `default_group.rpm_limit` 升档后，prod 含该 stub 的所有组（不论 stub-only 还是 mixed）都需要同步更新——stub 的 `extra.declared_rpm` mirror 上游值，group.rpm_limit 重算为 `Σ declared_rpm`。op 用 [`anthropic-prod-group-r3-unified-apply-template.sql`](../../../deploy/aws/stage0/anthropic-prod-group-r3-unified-apply-template.sql) 单事务 apply（取值 `results[i].expected_declared_rpm` + `group_results[i].expected_rpm_limit`）。
   注意：strict-redline aggregate 模板不适合 prod stub-only / mixed group——它针对 OAuth-bearing edge group，会按 `Σ(base_rpm+sticky_buffer)` 算 prod 没有的字段。R3-unified 模板专门处理 prod 侧。旧 [`anthropic-stub-mirror-rpm-apply-template.sql`](../../../deploy/aws/stage0/anthropic-stub-mirror-rpm-apply-template.sql) 已弃用（保留审计 trail）。
5. **切默认** — 全部 strict-mode 巡检通过后，另起一个小 PR 把 `--strict-redline` 翻成默认（或删除旧口径），完成切换。

### 3.3 模板 SQL 是 apply 源头

更新配置时禁止临时手写 SQL 或直接拼一份新的字段清单。必须从仓库模板复制出本次执行 SQL，再只替换本次目标变量和经 `plan-apply` 确认的差异：

- 账号 tier/stability/TLS baseline 更新：复制 `deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql`
- 分组聚合 rpm 更新（OAuth-bearing edge group）：复制 `deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql`
- R1 镜像 concurrency 更新（prod 单 stub）：复制 `deploy/aws/stage0/anthropic-stub-mirror-concurrency-apply-template.sql`
- R3-unified `declared_rpm` + group cap 更新（prod 任意 anthropic group）：复制 `deploy/aws/stage0/anthropic-prod-group-r3-unified-apply-template.sql`
- ⚠ 已弃用：`deploy/aws/stage0/anthropic-stub-mirror-rpm-apply-template.sql`（保留审计 trail，新写入禁用，违反 R3-unified 的 absorb-zero 算式）

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
python3 ops/anthropic/check-edge-oauth-stability.py \
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
group_agg.total_redline=<sum>
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
| apply 成功但仍有漂移 | 仅更新了部分字段或存在额外策略字段；或并发会话覆盖了写入 | 对照 `diffs` 补齐字段后重试；如同步发现 `updated_at` 在 apply 之后被改 → 触发"Pre-apply re-read 协议"调查另一会话 |
| 稳定名单更新失败 | 缺确认口令 | 使用 `--confirm yes-update-anthropic-stable-list` |
| guard 报 `r3_declared_rpm_missing` | apikey stub 未在 `extra` 中落档 declared_rpm | 用 R3-unified apply 模板补齐（self-edge 取上游 default.rpm_limit，external 由 operator 拍配额） |
| guard 报 `r3_declared_rpm_zero_forbidden` | declared_rpm ≤ 0 | 同上；R3-unified 禁止 unlimited |
| guard 报 `r3_self_edge_mirror_drift` | edge default.rpm_limit 涨过 / 缩过，prod stub declared_rpm 未跟上 | 取上游实时 rpm_limit，用 R3-unified apply 模板对齐 |
| guard 报 `r3_group_rpm_zero_forbidden` | 含 stub 的组 `rpm_limit=0`（unlimited） | 用 R3-unified apply 模板把组写到 Σ declared_rpm |
| guard 报 `r3_group_sum_mismatch` | group.rpm_limit ≠ Σ stub.declared_rpm | 同上 |
| guard 报 `r3_group_sum_blocked_by_stub_violation` | 组级 SUM 校验被某 stub 级 missing/zero 阻塞 | 先解决 stub 级违规 |
| apply 模板 DO 块 RAISE "stub_inputs SUM(declared_rpm)=A does not match :target_group_rpm=B" | plan 与 apply 数字不一致 | 重做 plan-apply，写入一致的 SUM 与 group cap |
| apply 模板 DO 块 RAISE "group id=X has Y member(s) but stub_inputs has Z row(s)" | plan/apply 期间组成员被增删 | 重新拉成员快照 → re-plan → re-apply |
| guard 报 `--legacy-r3` PASS 但默认模式 FAIL | rollout 过渡窗口未完成 | 不接受 legacy 模式为 PASS；按 R3-unified 修复路径 apply |

## 扩展阅读

- `ops/anthropic/check-edge-oauth-stability.py`
- `ops/anthropic/check-account-group-rpm-alignment.py`
- `ops/anthropic/check-prod-stub-mirror.py`（R1 + R3-unified guard）
- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`
- `deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql`（OAuth 账号 tier）
- `deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql`（OAuth-bearing edge group）
- `deploy/aws/stage0/anthropic-stub-mirror-concurrency-apply-template.sql`（R1 stub concurrency）
- `deploy/aws/stage0/anthropic-prod-group-r3-unified-apply-template.sql`（R3-unified stub declared_rpm + group cap）
- ⚠ deprecated：`deploy/aws/stage0/anthropic-stub-mirror-rpm-apply-template.sql`（仅保留审计 trail）
- `backend/internal/handler/admin/account_handler.go`
- `backend/internal/service/admin_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/server/routes/admin.go`
