---
title: Admin UI Performance Rollups
status: pending
approved_by: pending
created: 2026-06-23
owners: [tk-platform]
related_prs: []
related_commits: []
---

# Admin UI Performance Rollups

## Intent

Reduce latency across the production admin UI without changing public response
shapes. Live evidence shows the slow paths are concentrated in dashboard
widgets, usage statistics, and account usage cells. The implementation moves
wide, repeated admin reads onto existing or new rollup-backed paths and leaves
mutable partial ranges on raw `usage_logs`.

## Scope

- `/admin/dashboard`: split stats, trend, model, group, user-trend, and ranking
  loads so the first paint is not blocked by the slowest aggregation.
- `/admin/usage`: split summary and endpoint statistics; lazy-load endpoint
  distribution after the chart enters view.
- `/admin/accounts`: replace per-row passive usage fan-out with one batch call
  for the visible account rows.
- Frontend build output: avoid preloading large admin page chunks and chart
  chunks before the user navigates to those views.
- Observability: add read-only probes for admin API timing, access-log latency
  profiles, and rollup coverage.

## Schema

`backend/migrations/tk_046_usage_dashboard_group_daily_metrics.sql` adds
metrics columns to `usage_dashboard_group_daily` for existing deployments:
request count, token components, total cost, and account cost. The migration is
additive only; it does not drop, rename, or rewrite existing columns.

`backend/migrations/tk_038_usage_dashboard_group_daily.sql` is updated so fresh
databases receive the same shape as already-deployed databases after `tk_046`.

## Read/Write Path

The dashboard aggregation job fills model and group daily rollups. Read paths
use rollups only when their backfill marker proves historical coverage. Until
then they keep the existing raw-scan path, which preserves correctness at the
cost of current latency.

Usage summary reads use `usage_dashboard_hourly` for complete server-timezone
hours and raw `usage_logs` for the head/tail partial spans and data after the
aggregation watermark. Filtered stats continue to use raw logs.

Account passive usage no longer self-fetches once per row during page load; the
page supplies per-row overrides from `/admin/accounts/usage/batch`.

## Risk Controls

- Rollup-backed reads are gated by marker rows or coverage floors, so partial
  rollups do not silently undercount.
- Today/current-hour slices stay raw, preserving live operational numbers.
- The group-daily schema change is additive and is safe to deploy before the
  read path starts using the new metric columns.
- If backfill is deferred by a short request deadline, the next aggregation
  cycle retries and the read path remains on raw data.

## Validation

- `pnpm build`
- `python3 scripts/checks/frontend-release-assets.py --dist backend/internal/web/dist`
- Focused frontend vitest suite for dashboard, usage, and account usage cells.
- Focused backend unit tests for usage stats, dashboard caches, model/group
  rollup gates, and users-trend.
- Integration tests for model and group rollup parity against raw scans.
- `scripts/preflight.sh`
- Post-deploy read-only probes:
  - `ops/observability/probe-admin-aggregation-config.sh`
  - `ops/observability/probe-admin-ui-api-timing.sh`
  - `ops/observability/probe-admin-ui-perf.sh`
