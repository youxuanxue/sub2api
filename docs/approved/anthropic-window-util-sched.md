---
title: Anthropic/Codex upstream window-util soft scheduling
status: pending
approved_by: pending
authors: [agent]
created: 2026-07-01
related_prs: []
related_commits: []
---

# Anthropic/Codex upstream window-util soft scheduling

## Decision

Replace Anthropic OAuth/setup-token **local dollar `window_cost` tier caps** with **upstream 5h/7d used-percent soft scheduling**, sharing the Codex kernel (`window_util_sched_tk.go`) at global **0.98 sticky / 0.02 reserve**.

## Irreversible migration

`backend/migrations/tk_050_drop_tier_window_cost_columns.sql` drops `window_cost_limit` and `window_cost_sticky_reserve` from tier rows. Historical tier dollar caps are not preserved in-schema; scheduling relies on passive upstream utilization snapshots only.

## Contract deletion

Removes admin/API fields: `window_cost_limit`, `window_cost_sticky_reserve`, `current_window_cost` (see PR #1110 `contract-deletion-notice`).

## Validation

- Unit: `anthropic_account_scheduler_tk_window_sched_test.go`, `window_util_sched_tk_test.go`, OpenAI window sched tests
- Integration: gateway scheduler paths unchanged structurally; migration applied on deploy
