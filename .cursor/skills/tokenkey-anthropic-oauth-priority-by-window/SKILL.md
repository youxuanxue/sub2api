---
name: tokenkey-anthropic-oauth-priority-by-window
description: >-
  Rebalance TokenKey Anthropic OAuth account priority by remaining 5h/7d usage windows across deployable edges. Use for snapshot/plan/apply/verify of accounts.priority only; does not change tier, rpm limits, groups, or credentials.
---

# TokenKey：按剩余用量重排 Anthropic OAuth priority

仅重排 deployable edge 上 `platform=anthropic AND type=oauth` 的 `accounts.priority`，不跨 stability tier，不写 caps/groups/credentials/status/utilization 或 prod stub。

计算、排序、SQL 与验证由 `ops/anthropic/rebalance-anthropic-priority.py` 承载；模型只审目标、stale 证据与失败后的处置，不手算排名。tier base 来自 `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`。

## 1. 设计原则

- status 非 active 的账号跳过。5h/7d 取最紧；缺 5h/采样时间/窗口结束或样本过期按满负载，stale 排队尾；缺 7d 不让 7d 主导。
- plan 的 tier-band 容量/range/账号定位护栏失败必须停，不能 clip 或跨 tier 消除错误。
- 如同时调整 tier，先经 [OAuth 配置 owner](../tokenkey-anthropic-oauth-config/SKILL.md) 完成，再重新 snapshot/重排；tier apply 可能将 priority 重置回 base。
- apply 必须在用户授权范围内；任何 step 失败停止，查看已完成/未完成项，修因后重新 snapshot/plan，不能重放旧 plan。verify 必须跑，漂移交 operator 判断补写或回滚。

## 2. 4 阶段流水线

```bash
JOBDIR="$CLAUDE_JOB_DIR"
MGR=ops/anthropic/rebalance-anthropic-priority.py
python3 "$MGR" snapshot --out "$JOBDIR/snap.json"
python3 "$MGR" plan --edge all --snapshot "$JOBDIR/snap.json" \
  --out "$JOBDIR/plan.json" --stale-minutes 120
```

`--edge` 可收窄为指定 edge。snapshot/plan 环境或用法错误退出 2；plan 退出 1 表示 `summary.any_stale`，仍有 plan：查看 `tier_summaries[*].ordering[*].stale_reasons` 与 action 的 `ranking.stale`，不能因 actions 为空就判全绿。可接受 stale 队尾策略才应用；否则先处理采样缺口后重做 plan。

```bash
python3 "$MGR" apply --plan "$JOBDIR/plan.json" \
  --confirm yes-rebalance-anthropic-priority
python3 "$MGR" verify --plan "$JOBDIR/plan.json"
```

apply/verify 退出 1 是步骤失败/漂移，2 是错误；0 才通过。输出用带字段名的 JSON，不凭记忆断言 live 值。

## 按需诊断

- SSM、stale、容量或 verify 失败：[故障处理](references/troubleshooting.md)。
- 解析工件：snapshot 的 `edges` 按 edge_id 索引，账号在 `oauth_accounts`；跳过 error/skipped 节点。plan 看 `summary`、`tier_summaries`、`skipped_accounts`、`actions[*].target/current/expected_after/ranking`。实际 versioned JSON 是字段真相，不维护静态样例。
- 流水线无法覆盖的授权紧急回滚：[底层工具](references/emergency.md)。SQL 模板为 `deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql`，绕开 orchestrator 仍须写后复核。
