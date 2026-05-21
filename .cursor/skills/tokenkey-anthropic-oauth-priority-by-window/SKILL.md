---
name: tokenkey-anthropic-oauth-priority-by-window
description: >-
  TokenKey 跨所有 deployable edge 的 Anthropic OAuth 账号 priority 重排流水线
  （snapshot → plan → apply → verify）。按账号当前 5h/7d 可用用量窗口剩余度
  打分，同 stability tier 内重排 priority（smaller wins），剩余越多 priority
  越小（越优先调度）。**只写** accounts.priority 一个字段，不动 tier baseline、
  不动 group.rpm_limit、不动 credentials。单一脚本
  ops/anthropic/rebalance-anthropic-priority.py 编排，1 个 SQL 模板固化写入。
---

# TokenKey：Anthropic OAuth 按剩余用量窗口重排 priority

适用于本仓库（TokenKey fork of sub2api）。**所有 deployable edge** 上的
`platform=anthropic AND type=oauth` 账号，按当前的 5h / 7d 用量窗口剩余度同
tier 内重排 `accounts.priority`。

与 [`tokenkey-anthropic-oauth-config`](../tokenkey-anthropic-oauth-config/SKILL.md) 关系：

| 流水线 | 写入面 | 何时跑 |
|---|---|---|
| `tokenkey-anthropic-oauth-config` | edge OAuth account 的 tier baseline（concurrency / base_rpm / sticky_buffer / max_sessions / window_cost_limit / `stability_tier` / credentials / extra） | tier 升降级、tier baseline drift |
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

### 打分细节（仅作参考；权威以脚本 `_score_account` 为准）

| 输入字段 | 来源 | 缺失时含义 |
|---|---|---|
| `session_window_utilization`（5h，0..1） | `extra.session_window_utilization`，由 `RateLimitService.UpdateSessionWindow` 从 `anthropic-ratelimit-unified-5h-utilization` 响应头被动采样 | 视为满负载（剩余=0） |
| `passive_usage_7d_utilization`（7d，0..1） | `extra.passive_usage_7d_utilization`，同上从 7d 响应头采样 | 视为剩余 100%（7d 非主导） |
| `passive_usage_sampled_at` | `extra.passive_usage_sampled_at`（RFC3339） | 视为 `never_sampled` → 满负载 |
| `session_window_end` | schema 列 `accounts.session_window_end`（TIMESTAMPTZ） | 视为 `session_window_end_missing` → 满负载 |
| `status` | schema 列 `accounts.status` | 非 `active` 直接 skip，不参与重排 |

打分：

```text
remaining_5h = 0  if (sampled_at stale OR window_end expired OR util_5h missing)
             = clamp(1 - util_5h, 0..1)  otherwise
remaining_7d = 1  if util_7d missing
             = clamp(1 - util_7d, 0..1)  otherwise
remaining_score = min(remaining_5h, remaining_7d)
```

排序键（升序）：`(stale 0/1, -remaining_score, id)` —— stale 永远在 fresh 之后，同 freshness 按 remaining 倒序，平手按 id 升序保证幂等。

`new_priority = tier_base + rank_offset`，其中 `tier_base` 来自 `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json` 的 `tiers.lN.baseline.account.priority`（l1=10, l2=20, l3=30, l4=40, l5=50），`rank_offset = 0..9`。

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
| tier baseline（concurrency / base_rpm / rpm_sticky_buffer / max_sessions / window_cost_limit / `stability_tier` / credentials / extra 其他字段） | [`tokenkey-anthropic-oauth-config`](../tokenkey-anthropic-oauth-config/SKILL.md)（manage-anthropic-config.py） |
| edge / prod `group.rpm_limit` 等 group 字段 | admin UI |
| prod anthropic apikey forward stub 任何字段 | admin UI |
| OAuth 凭据 / status / 轮换 | admin UI / OAuth flow |
| account `extra` 内任何非 priority 的字段（包括 utilization 字段本身——那是被动采样、由用户请求响应头驱动） | 不可写；utilization 由 `RateLimitService.UpdateSessionWindow` 在每个 5h 成功响应时被动更新 |

## 4. 与 tier baseline 流水线的协作顺序

```
   ┌──────────────────────────────────────────────────────────┐
   │ tier 升降级 / tier baseline drift                        │
   │   → tokenkey-anthropic-oauth-config 流水线 apply        │
   │   → 写完后所有目标账号的 priority = tier_base（10/20/…）│
   └──────────────────────────────────────────────────────────┘
                              │
                              ▼
   ┌──────────────────────────────────────────────────────────┐
   │ **本 skill** 流水线 apply                                │
   │   → snapshot → plan → apply → verify                     │
   │   → priority = tier_base + remaining_offset(0..9)        │
   └──────────────────────────────────────────────────────────┘
```

如果 operator 在 tier baseline apply 之后**不**跑本流水线，priority 就停在 tier_base 上（仍可调度，只是失去"剩余多者优先"的微调）。这是安全的退化状态，不会破坏调度正确性。

## 5. 故障速查

| 现象 | 处理 |
|---|---|
| `snapshot` 失败 / SSM 拒绝 | 校验 EC2 instance 在跑 / `edge-targets.json` / OIDC 权限 |
| `plan` 退出码 1（`any_stale=true`） | 检查 `plan.json` 的 `tier_summaries[*].ordering[*].stale_reasons`。常见：`never_sampled`（账号新建未跑过任何请求）、`session_window_expired`（账号长期无流量、窗口已 reset 但还没新请求触发头部采样）。可接受 → apply 即可（stale 账号已排到队尾）；不接受 → 跑几个 warmup 请求让 utilization 被采样后再 plan |
| `plan` 退出码 2 + `account_count > MAX_PER_TIER_PER_EDGE` | 该 edge 该 tier 账号太多，offset 会越级。先用 tier baseline 流水线把部分账号挪到上下 tier，再回来跑本流水线 |
| `apply --confirm` 拒绝 | 必须精确 `yes-rebalance-anthropic-priority` |
| `apply` 中途 SSM 失败 | 看 `apply-report.json` 的 `results[*].ssm_command_id`；用 `aws ssm get-command-invocation` 查具体 stderr；修因后**重新 plan**（不要直接 apply 旧 plan，因为部分账号已写、剩下账号 ranking 可能也变了） |
| `verify` 报 drift | 多为：(a) apply 之后又跑了 tier baseline apply，把 priority 重置了 → 重新 plan + apply；(b) admin UI 手工改了 priority → 看 audit log；(c) apply 时一部分 step 失败但 verify 比对全部 actions |
| 想全局重排（跨 tier） | 不支持，也**不要**这样做：会破坏 stability tier 的调度语义 |

## 6. 附录 A：底层工具（emergency / debug）

正常流程**只走 4 阶段流水线**。下列在流水线 break 或紧急 rollback 时直接用：

**手动写 priority**（自包含 SQL 模板，有 DO-block 校验；orchestrator 内部自动渲染；手动用需自起 `\set ...` 然后 base64 通过 SSM 注入）：

- `deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql`

例如紧急回滚某 edge 某账号到 tier_base：

```bash
SQL=$(mktemp)
cat >"$SQL" <<EOF
\set account_name 'en-ld-ec2-16-1-b'
\set new_priority 20    -- l2 tier base
EOF
cat deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql >>"$SQL"
B64=$(base64 -w0 <"$SQL")
aws ssm send-command --region eu-west-2 --instance-ids i-xxx \
  --document-name AWS-RunShellScript \
  --parameters "commands=[\"set -euo pipefail\necho $B64 | base64 -d | sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1\"]"
```

**底线**：手动绕开 orchestrator 时 op 必须自己做 apply 后复核 —— 同样不允许跳过 § 2 "先查后说"协议。

## 7. 附录 B：snapshot JSON 形状

```json
{
  "version": 1,
  "captured_at": "2026-05-21T08:00:00Z",
  "edges": {
    "uk1": {
      "deployable": true,
      "instance_id": "i-0abc...",
      "region": "eu-west-2",
      "stack": "tokenkey-edge-uk1-stage0",
      "domain": "api-uk1.tokenkey.dev",
      "oauth_accounts": [
        {
          "id": 123,
          "name": "en-ld-ec2-16-1-b",
          "platform": "anthropic",
          "type": "oauth",
          "status": "active",
          "priority": 20,
          "concurrency": 2,
          "stability_tier": "l2",
          "session_window_end": "2026-05-21T10:00:00Z",
          "session_window_utilization": 0.42,
          "passive_usage_7d_utilization": 0.18,
          "passive_usage_7d_reset": 1769404154,
          "passive_usage_sampled_at": "2026-05-21T07:55:13Z"
        }
      ]
    },
    "fra1": { "deployable": false, "skipped_reason": "edge fra1 is planned; pass --allow-planned to include" }
  }
}
```

## 8. 附录 C：plan JSON 形状（节选）

```json
{
  "version": 1,
  "kind": "anthropic_priority_rebalance",
  "confirm_code": "yes-rebalance-anthropic-priority",
  "intent": {"edges": ["uk1"], "stale_minutes": 120, "max_per_tier_per_edge": 10},
  "snapshot_captured_at": "2026-05-21T08:00:00Z",
  "plan_built_at": "2026-05-21T08:00:42Z",
  "summary": {"total_actions": 3, "skipped_accounts": 1, "tier_buckets": 2, "any_stale": false},
  "tier_summaries": [
    {
      "edge_id": "uk1",
      "stability_tier": "l2",
      "tier_base_priority": 20,
      "account_count": 3,
      "stale_count": 0,
      "ordering": [
        {"rank": 0, "account_name": "...", "remaining_score": 0.82, "old_priority": 20, "new_priority": 20, "stale": false, "stale_reasons": []},
        {"rank": 1, "account_name": "...", "remaining_score": 0.58, "old_priority": 20, "new_priority": 21, "stale": false, "stale_reasons": []},
        {"rank": 2, "account_name": "...", "remaining_score": 0.15, "old_priority": 21, "new_priority": 22, "stale": false, "stale_reasons": []}
      ]
    }
  ],
  "skipped_accounts": [
    {"edge_id": "uk1", "account_name": "suspended-acct", "reason": "status=suspended", "current_priority": 25}
  ],
  "actions": [
    {
      "step": 1,
      "kind": "account_priority",
      "target": {"env": "edge", "edge_id": "uk1", "account_id": 124, "account_name": "..."},
      "ranking": {"stability_tier": "l2", "tier_base_priority": 20, "tier_rank": 1, "remaining_score": 0.58, "remaining_5h": 0.58, "remaining_7d": 0.82, "stale": false, "stale_reasons": []},
      "current": {"priority": 20},
      "expected_after": {"priority": 21}
    }
  ]
}
```

## 9. 扩展阅读

- `ops/anthropic/rebalance-anthropic-priority.py`（4 阶段 orchestrator，本 skill 唯一推荐入口）
- `deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql`（apply 模板，单账号单字段 update + DO-block 校验）
- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`（`tiers.lN.baseline.account.priority` 是 tier_base 的源头）
- `backend/internal/service/ratelimit_service.go` — `UpdateSessionWindow`、`calculateAnthropic429ResetTime`（utilization 被动采样的实际写入路径）
- `backend/ent/schema/account.go` — `priority`、`session_window_end` 字段声明 + `(platform, priority)` 复合索引
- [`tokenkey-anthropic-oauth-config`](../tokenkey-anthropic-oauth-config/SKILL.md) — tier baseline 写入流水线（本 skill 的上游协作方）
