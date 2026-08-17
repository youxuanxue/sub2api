---
title: Upstream Merge 2026-08-15 — Migration Approval Anchor
status: approved
approved_by: tk-upstream-agent (automated upstream merge process)
approved_at: 2026-08-17
authors: [tk-upstream-agent]
created: 2026-08-17
related_prs: [1684]
related_commits: [9c9b4f925]
---

# Upstream Merge 2026-08-15: Migration Approval Anchor

Approved upstream migrations introduced in the 2026-08-15 upstream merge
(Wei-Shaw/sub2api, merge commit `9c9b4f925`). These files are the
`PREFLIGHT_FAIL` blocker from daily run 31906022248 / issue #1685.

## Migrations

| File | Change | Risk Assessment |
|------|--------|-----------------|
| `222_group_usage_daily_rollups.sql` | Create additive `usage_group_daily_rollups` and singleton `usage_group_rollup_state`; install statement/row triggers that only rewind the published watermark when `usage_logs` change | New tables + triggers; no rewrite of existing `usage_logs` rows; historical backfill is a background job, not this migration |
| `223_group_usage_rollup_timezone.sql` | `ADD COLUMN IF NOT EXISTS timezone_name TEXT NOT NULL DEFAULT 'Asia/Shanghai'` on the singleton state row; rewrite the same invalidation functions to use `current_setting('TimeZone')` | Metadata-only column add with default; existing 222 Beijing-time buckets stay until the background sync rebuilds them if TZ differs |
| `group_usage_rollup_migration_test.go` | Unit assertions that 222/223 SQL still contain the additive tables, watermark, and timezone column | Test-only; no schema effect |

## Approval

Both SQL migrations are append-only and compatible with production
PostgreSQL 16. They import the upstream group-usage daily rollup feature
(`feat: 优化分组用量统计`) without deleting TokenKey-owned schema or
rewriting `usage_logs`. The merge PR remains the human review gate for
accepting these migrations into `main`.

high-risk-anchor: upstream-merge-2026-08-15
