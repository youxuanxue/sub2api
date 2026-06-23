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

## Pre-deploy Prod Baseline

Read-only probes were run against prod via
`ops/observability/run-probe.sh --target prod` on 2026-06-23
12:57-12:59 UTC. This is the comparison baseline for post-deploy validation;
it does not prove the change has shipped.

- Rollup coverage: aggregation watermark was fresh at
  2026-06-23T12:58:35Z, but `model_daily_backfilled=false` and
  `group_daily_metrics_backfilled=false`.
- Group rollup schema: prod `usage_dashboard_group_daily` did not yet have the
  `tk_046` metric columns, so group rollup consistency diff was skipped until
  the migration is deployed.
- Dashboard users trend: `dashboard.snapshot.7d.users-trend-only` and
  `dashboard.snapshot.7d.full` returned HTTP 500.
- Slow direct timings:
  - `usage.stats.summary-only.ui-default-range`: 3.93s.
  - `usage.stats.summary-only`: 2.31s.
  - `dashboard.snapshot.7d.models-only`: 2.31s.
  - `usage.chart.groups-only.7d`: 1.89s.
  - `usage.stats.endpoints-only`: 1.33s.
  - `usage.stats.default`: 1.24s.
- Access-log profile over the prior 24h:
  - `/api/v1/admin/usage/stats`: p50 962ms, p90 2993ms, p95 3668ms,
    max 4206ms.
  - `/api/v1/admin/dashboard/snapshot-v2`: p50 119ms, p90 1439ms,
    p95 3189ms, max 4324ms, including 22 HTTP 500 responses.
  - `/api/v1/admin/accounts/:id/usage`: 496 requests, p50 352ms,
    p95 719ms, max 1546ms.
  - `/api/v1/admin/accounts/usage/batch`: 11 requests, p50 5ms,
    p95 30ms, max 30ms.

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

Post-deploy acceptance requires stronger evidence than green CI: the prod
deployment must show `tk_046` applied, model/group backfill markers ready or a
safe raw fallback while markers converge, dashboard users-trend no longer
returning 500, and materially lower latency for `usage.stats`, dashboard
model/group chart reads, and account passive usage loading.
