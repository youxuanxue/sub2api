---
name: tokenkey-anthropic-oauth-priority-by-window
description: >-
  Rebalance TokenKey Anthropic OAuth account priority by remaining 5h/7d usage windows across deployable edges. Use for snapshot/plan/apply/verify of accounts.priority only; does not change tier, rpm limits, groups, or credentials.
---

# TokenKey：Anthropic OAuth 按剩余用量窗口重排 priority

适用于本仓库（TokenKey fork of sub2api）。**所有 deployable edge** 上的
`platform=anthropic AND type=oauth` 账号，按当前的 5h / 7d 用量窗口剩余度同
tier 内重排 `accounts.priority`。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。本 skill **已达基线**——4 阶段 orchestrator 全机械化，priority 计算公式（freshness 门禁、5h/7d 取最紧、稳定排序键）全在脚本里。

| 步骤 | 类型 | 承载 |
|---|---|---|
| snapshot / plan / apply / verify | 机械 | `python3 ops/anthropic/rebalance-anthropic-priority.py {snapshot\|plan\|apply\|verify}` |
| `remaining_score = min(remaining_5h, remaining_7d)` 打分 | 机械 | `_score_account` |
| 排序键 `(stale 0/1, -remaining_score, id)` | 机械 | orchestrator 内 stable sort |
| `MAX_PER_TIER_PER_EDGE` 安全栏（防 offset 越级） | 机械 | plan 阶段 fail-fast |
| SQL 模板 `accounts.priority` 单字段更新 + DO-block 校验 | 机械 | `deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql` |
| 单元测试覆盖 plan / scoring / SQL name guard | 机械 | `ops/anthropic/test_rebalance_anthropic_priority_plan.py`（preflight 跑） |
| confirm code 字面匹配 | 机械 | `--confirm yes-rebalance-anthropic-priority` |
| 同 tier 内重排原则 / freshness 门禁阈值（120 min）/ stale → 队尾 | 判断 | prompt（架构设计 §1） |
| `status!=active` skip / 跨 tier 不允许 | 判断 | prompt（产品边界） |
| 与 tier baseline 流水线的协作顺序（必须 baseline apply 后跑本 skill） | 判断 | prompt（依赖语义） |

与 [`tokenkey-anthropic-oauth-config`](../tokenkey-anthropic-oauth-config/SKILL.md) 关系：

| 流水线 | 写入面 | 何时跑 |
|---|---|---|
| `tokenkey-anthropic-oauth-config` | edge OAuth account 的 tier baseline（concurrency / base_rpm / sticky_buffer / max_sessions / `stability_tier` / credentials / extra） | tier 升降级、tier baseline drift |
| **本 skill** | edge OAuth account 的 `priority`（仅 1 个 int 列） | 想让"剩余用量多的账号优先调度"时；**每次跑完 tier baseline apply 后必须再跑本流水线**（tier baseline 会把 priority 重置回 tier 基线） |

两条流水线**互不重叠任何字段**，但**有先后依赖**：tier baseline 是先决条件，priority 重排是其后的微调。

权威纪律以仓库根 `CLAUDE.md` 为准。

## 1. 设计原则

- **同 tier 内重排**：不跨 stability tier。l1/l2/l3/l4/l5 的 base priority 是 10/20/30/40/50，间距 10。本流水线在每个 tier band 内只取 offset `0..9`，绝不越级。
- **保守 freshness 门禁**：若账号的 `passive_usage_sampled_at` 早于 `--stale-minutes`（默认 120），或 `session_window_end` 已过期，或 utilization 字段缺失，则视为"满负载（remaining=0）"，排到该 tier 内队尾。**不**当作"剩余 100% 顶到队首"。
- **5h 与 7d 取最紧**：`remaining_score = min(remaining_5h, remaining_7d)`。5h 是即时调度信号，7d 是周配额保险。
- **status≠active 跳过**：被 suspend / error / disabled 的账号 priority 不动；列在 `plan.skipped_accounts` 里供 operator 审阅。
- **失败即停**：apply 任一 step SSM 失败 → 立即 stop，已完成 step 与未完成 step 在 `apply-report.json` 中区分；**verify 必须跑**。
- **先查后说**：每个阶段不凭记忆断言字段值，都来自一次 SSM read。

## 2. 4 阶段流水线

每阶段一个命令，输入/输出明确，失败即停。所有写入通过一个固化 SQL 模板，operator 不写 SQL。

```bash
JOBDIR="$CLAUDE_JOB_DIR"               # or any scratch dir
MGR=ops/anthropic/rebalance-anthropic-priority.py

# Stage 1 — Snapshot：拉所有 deployable edge 的 anthropic OAuth account
#   + 关键 utilization 字段（5h / 7d util、sampled_at、session_window_end）
python3 $MGR snapshot --out $JOBDIR/snap.json

# Stage 2 — Plan：跨所有 edge 按 (edge, stability_tier) 分桶打分排序，
#   生成每 account 的 new_priority = tier_base + rank_offset
#   --edge 可以是 'all'（默认推荐）或单个 edge id
python3 $MGR plan \
  --edge all \
  --snapshot $JOBDIR/snap.json \
  --out $JOBDIR/plan.json \
  --stale-minutes 120

# Stage 3 — Apply：每个 action 渲染 SQL → SSM → 写入；失败即停
python3 $MGR apply \
  --plan $JOBDIR/plan.json \
  --confirm yes-rebalance-anthropic-priority

# Stage 4 — Verify：再 snapshot + 比对每个 action 的 expected_after vs live
python3 $MGR verify --plan $JOBDIR/plan.json
```

### 各阶段语义

| 阶段 | 输入 | 输出 | exit |
|---|---|---|---|
| snapshot | EC2 SSM 权限 | `snap.json`：每个 deployable edge 的 anthropic OAuth account 字段 + 5h/7d utilization + sampled_at + session_window_end | 0 / 2 error |
| plan | snap.json + `--edge {all\|<id>}` | `plan.json`：每 (edge, tier) 桶内排名 + 每 account 的 expected_after.priority | 0 ok / 1 any_stale（仍生成 plan，标 `ranking.stale=true`）/ 2 |
| apply | plan.json + `--confirm` | 逐 step 渲染 SQL → SSM → `apply-report.json` | 0 / 1 step failed / 2 |
| verify | plan.json | 再 snapshot + 比对 `actions[*].expected_after.priority` vs live | 0 / 1 drift / 2 |

`plan` 的 **exit 1 / `summary.any_stale`**：以 `tier_summaries[*].stale_count` 与各 action 的 `ranking.stale` 为准；**不会**仅因「没有生成 apply action（priority 已是算出的顺序）」就把 stale 误判为全绿。

### 打分细节（仅作参考；权威以脚本 `_score_account` 为准）

打分、freshness、字段缺失处理、稳定排序和 tier band 均由 `rebalance-anthropic-priority.py` 计算；消费 plan，不手工重算。缺 5h/采样时间/窗口结束视为满负载，缺 7d 则不让 7d 成为主导。stale 排在 fresh 后，同分按 id；具体字段看 snapshot 参考。

### 安全护栏

- **每 tier 每 edge 上限 10 个账号**（`MAX_PER_TIER_PER_EDGE`）。超过 → `plan` 直接 fail，避免 offset 越级到下一 tier band。如果某 edge 真的需要更多，**先**拆 tier 或调整 baseline 间距，不要靠 clip。
- **SQL 模板内 range_check**：拒绝 `new_priority < 1` 或 `> 999`，作为脚本算错时的最后兜底。
- **`COMMIT` 之前 DO-block 校验**：`target` 找不到（账号被中途删除）→ `RAISE EXCEPTION`，整事务回滚。

### 失败即停 + Pre-apply re-read

- `apply` 任一 step SSM 失败 → STOP；`apply-report.json` 列出已完成 + 未完成 step
- Stage 4 verify 必须跑；drift → operator 决定补 apply 或回滚
- snapshot 出于"先查后说"原则：禁止凭记忆断言字段值，所有断言都来自一次 SSM read

## 3. 不在本流水线范围内

| 配置面 | 谁负责 |
|---|---|
| tier baseline（concurrency / base_rpm / rpm_sticky_buffer / max_sessions / `stability_tier` / credentials / extra 其他字段） | [`tokenkey-anthropic-oauth-config`](../tokenkey-anthropic-oauth-config/SKILL.md)（manage-anthropic-config.py） |
| edge / prod `group.rpm_limit` 等 group 字段 | admin UI |
| prod anthropic apikey forward stub 任何字段 | admin UI |
| OAuth 凭据 / status / 轮换 | admin UI / OAuth flow |
| account `extra` 内任何非 priority 的字段（包括 utilization 字段本身——那是被动采样、由用户请求响应头驱动） | 不可写；utilization 由 `RateLimitService.UpdateSessionWindow` 在每个 5h 成功响应时被动更新 |

## 4. 与 tier baseline 流水线的协作顺序

同时调整 tier baseline 时先读协作顺序；本流程仅写 priority，不能顺带改 tier/caps/groups/credentials。 见 [操作细则](references/tier-order.md)。

## 5. 故障速查

plan stale、SSM/apply/verify 失败时读取；任一步 apply 失败立即停止，verify 不省略。 见 [操作细则](references/troubleshooting.md)。

## 6. 附录 A：底层工具（emergency / debug）

仅在流水线无法覆盖的授权 emergency/debug 操作中读取。 见 [操作细则](references/emergency.md)。

## 7. 附录 B：snapshot JSON 形状

仅解析 snapshot 工件时读取；字段以实际版本化输出为准。 见 [操作细则](references/snapshot-schema.md)。

## 8. 附录 C：plan JSON 形状（节选）

仅解析 plan 工件时读取。 见 [操作细则](references/plan-schema.md)。

## 9. 扩展阅读

仅定位实现或契约 owner 时查询。 见 [操作细则](references/owners.md)。
