# H1 + H2 Pool Consolidation — 2026-05-17

Operational record of two pool consolidation steps applied on 2026-05-17.
Both steps were applied directly via SSM → `tokenkey-postgres` SQL; this
file is the audit + reproducibility artifact.

## Scope

| Step | Target | Surface | What | Why |
| ---- | ---- | ---- | ---- | ---- |
| H1 | Edge stage0 postgres (uk1, fra1) | `accounts` (platform=anthropic, type=oauth) | migrate `stability_tier` from legacy 5-tier (`l1_novice`) to current 3-tier (`l1`) per `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json` | #246 `Adopt human-paced OAuth tiers` shifted the baseline + check + template to 3-tier, but edge account rows still carried the 5-tier label. Drift surface for any future check. |
| H2 | Prod stage0 postgres (us-east-1) | `accounts` (GPT-A*), `groups` (GPT专线 / GPT-pro / free-tier) | `concurrency` 110→30 on 3 active accounts; `is_exclusive` true→false on free-tier; `messages_compaction_enabled / threshold` aligned across all 3 groups | Pool had >6x over-provisioned concurrency vs measured 24h peak; free-tier `is_exclusive=true` contradicted the actual shared membership; compaction differed across same-pool groups. |

us1 was inspected for H1 but **not modified** (`cc-am-or-ec2-5-1-b` was
already `stability_tier=l2`, `diff_count=0`).

## H1 — Anthropic OAuth tier (edge uk1 + fra1)

**Targets and decision (option A, minimal motion)**:

| edge | account name (edge db) | before | after | factor |
| ---- | ---- | ---- | ---- | ---- |
| us1 | `cc-am-or-ec2-5-1-b` | l2 (ok) | l2 (no change) | 0.55 |
| uk1 | `cc-en-ld-ec2-16-1-a` | l1_novice (5-tier, conc=2 / prio=100 / base_rpm=3 / window_cost_limit=90) | **l1** (conc=1 / prio=10 / base_rpm=6 / window_cost_limit=180) | 0.3 |
| fra1 | `cc-fr-fra-ec2-5-1-a` | l1_novice (same) | **l1** | 0.3 |

**Apply path**: per-edge SSM run of a self-contained SQL derived from
`deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql` —
template body unchanged, `\set account_name` / `\set stability_tier`
injected at file head per the `tokenkey-edge-anthropic-oauth-config`
skill §3.2 contract. The reproducible recipe for each edge:

```text
1. python3 -c "<<load check script as module>>; read_live_account(edge, ...)" → snapshot
2. cp deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql to job dir
3. prepend \set account_name / \set stability_tier
4. aws ssm send-command ... <<heredoc <self-contained sql> ...
5. python3 scripts/check-edge-anthropic-oauth-stability.py --edge-id <id> --account-name <name>  → status=ok, diff_count=0
```

The H1 SQL files are intentionally **not committed**: they are pure
\set-injection of the in-tree template; any operator following the skill
can regenerate identical SQL.

**SSM audit IDs**:

- uk1 apply: `a0bb1ae8-a668-4af6-884b-e909dbef8f6a` (eu-west-2)
- fra1 apply: `39ebacbc-572f-403b-93c2-43960eff66d8` (eu-west-3)

**Post-apply verification** — both edges return `status=ok`,
`diff_count=0`, `account_stability_tier=l1`, `baseline_factor=0.3`.

uk1 and fra1 OAuth accounts were `schedulable=false` on the prod-side
forward-stub mirror at apply time; per skill policy and project decision,
edge `schedulable` was not touched. Re-enabling scheduling, if ever
desired, is a separate change.

## H2 — Prod GPT openai-oauth pool

**Evidence (24h)** — measured against `usage_logs` for the active
GPT-A* accounts:

| account | id | current conc | requests_24h | peak in-flight / min | active minutes |
| ---- | ---- | ---- | ---- | ---- | ---- |
| GPT-A0 | 3 | 10 | 0 | 0 | 0 |
| GPT-A1 | 1 | 10 | 0 | 0 | 0 |
| GPT-A2 | 2 | 110 | 1264 | 15 | 311 |
| GPT-A3 | 6 | 110 | 2730 | 26 | 827 |
| GPT-pro1 | 9 | 110 | 1288 | 14 | 326 |

`peak_in_flight_per_minute` is the 1-minute bucket count of usage_logs
rows whose `[created_at, created_at + duration_ms]` window crosses the
bucket; cross-bucket requests are double-counted, so the metric is an
**upper bound** on true concurrency. New `concurrency=30` gives ~15%
headroom over the highest observed minute (26) and ~63% headroom over
the summed active-account peak (55 < 90).

**Decisions**:

- H2.a — `accounts.concurrency = 30` on GPT-A2 (id=2), GPT-A3 (id=6),
  GPT-pro1 (id=9). GPT-A0 (id=3) and GPT-A1 (id=1) kept at
  `concurrency=10` as cold spare (24h zero traffic; not downscheduled
  to avoid OAuth-token decay surprises and to preserve a fast spare
  if GPT-A2/A3/pro1 hit an unexpected upstream block).
- H2.b — `groups.is_exclusive = false` on `free-tier` (id=4): the
  group already shared all 5 GPT-A* accounts with GPT专线 / GPT-pro;
  the prior `is_exclusive=true` did not reflect reality.
- H2.c — `groups.messages_compaction_enabled=true` +
  `messages_compaction_input_tokens_threshold=180000` on `GPT-pro`
  (id=6, was NULL) and `free-tier` (id=4, was `false/NULL`); `GPT专线`
  (id=2) was already aligned. Same-pool groups now have identical
  cache behavior.

**Apply path** — single SSM run, single transaction, hand-written SQL
(no template):

```sql
BEGIN;

-- H2.a
WITH a_updated AS (
  UPDATE accounts
  SET concurrency = 30, updated_at = NOW()
  WHERE id IN (2, 6, 9)
    AND name IN ('GPT-A2', 'GPT-A3', 'GPT-pro1')
    AND platform = 'openai' AND type = 'oauth'
    AND deleted_at IS NULL
    AND concurrency = 110
  RETURNING id, name, concurrency AS new_concurrency
)
SELECT 'H2.a' AS step, * FROM a_updated;

-- H2.b
WITH b_updated AS (
  UPDATE groups
  SET is_exclusive = false, updated_at = NOW()
  WHERE id = 4 AND name = 'free-tier' AND is_exclusive = true
  RETURNING id, name, is_exclusive AS new_is_exclusive
)
SELECT 'H2.b' AS step, * FROM b_updated;

-- H2.c
WITH c_updated AS (
  UPDATE groups
  SET messages_compaction_enabled = true,
      messages_compaction_input_tokens_threshold = 180000,
      updated_at = NOW()
  WHERE id IN (6, 4)
    AND name IN ('GPT-pro', 'free-tier')
    AND (
      messages_compaction_enabled IS DISTINCT FROM true
      OR messages_compaction_input_tokens_threshold IS DISTINCT FROM 180000
    )
  RETURNING id, name,
    messages_compaction_enabled AS new_enabled,
    messages_compaction_input_tokens_threshold AS new_threshold
)
SELECT 'H2.c' AS step, * FROM c_updated;

COMMIT;
```

**SSM audit ID**: `1bdef9ab-835b-4fb3-ac92-7d3c776d5799` (us-east-1).

**Post-apply verification** — all 6 expected rows updated, all 8 verified
fields match plan (3 account.concurrency, 3 group.is_exclusive,
3 group.messages_compaction_enabled, 3 group.messages_compaction_input_tokens_threshold).

### Rollback (H2)

If H2 needs to be reversed, run as a single transaction on
`tokenkey-prod-stage0`:

```sql
BEGIN;
UPDATE accounts
  SET concurrency = 110, updated_at = NOW()
  WHERE id IN (2, 6, 9) AND concurrency = 30;
UPDATE groups
  SET is_exclusive = true, updated_at = NOW()
  WHERE id = 4 AND name = 'free-tier' AND is_exclusive = false;
UPDATE groups
  SET messages_compaction_enabled = NULL,
      messages_compaction_input_tokens_threshold = NULL,
      updated_at = NOW()
  WHERE id = 6 AND name = 'GPT-pro';
UPDATE groups
  SET messages_compaction_enabled = false,
      messages_compaction_input_tokens_threshold = NULL,
      updated_at = NOW()
  WHERE id = 4 AND name = 'free-tier';
COMMIT;
```

For H1, rollback is a re-apply of the legacy 5-tier values via a similar
template-derived SQL; given baseline + check are now 3-tier, **the right
fix is to advance, not roll back**.

## Follow-ups not in this change

- 30–60 min runtime observation of GPT-A2/A3/pro1 under `concurrency=30`;
  watch `ops_error_logs` for any new "concurrency exceeded" pattern. Use
  `tokenkey-online-log-troubleshooting` skill.
- A0/A1 long-term: 24h zero traffic — either keep as warm cold spare
  with periodic heartbeat, or formally retire. Out of scope here.
- Remaining low/medium items from the prior consolidation review
  (N1: airouter / edges / default groups; N2: per-group `rpm_limit`
  defaults; L1: dispatch.config_keys cleanup) — separate PR.
