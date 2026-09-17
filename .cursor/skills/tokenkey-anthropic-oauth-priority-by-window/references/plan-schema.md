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
