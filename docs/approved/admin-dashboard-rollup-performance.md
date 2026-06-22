---
title: Admin Dashboard Rollup Performance
status: pending
approved_by: pending
created: 2026-06-22
owners: [tk-platform]
related_prs: []
related_commits: []
---

# Admin Dashboard Rollup Performance

## Intent

Admin dashboard cold starts should avoid wide raw scans over `usage_logs` for the
30-day user trend and model stats widgets. The change keeps the public dashboard
response shape unchanged while serving completed server-timezone days from daily
rollup tables and reading partial/today slices from raw logs.

## Schema

`backend/migrations/tk_042_usage_dashboard_model_daily.sql` adds
`usage_dashboard_model_daily`, keyed by `(bucket_date, model)`.

The table stores the same aggregate fields returned by model stats:
requests, token components, total cost, actual cost, and account cost. It is
additive and has no destructive migration step.

## Read/Write Path

`dashboardAggregationRepository.AggregateRange` and `RecomputeRange` populate and
rebuild model daily rows with the same day window used by existing dashboard
rollups.

`GetModelStatsWithFilters` may use the model rollup only for requested-model
stats without dimension filters. Filtered requests continue to use the raw query.

`GetUserUsageTrend` may use `usage_dashboard_user_platform_daily` for day
granularity. Top-user selection must stay compatible with the legacy raw query:
rank by token volume, then user id.

## Risk Controls

- Completed days only come from rollup rows; today and partial edge slices stay
  raw, so mutable data remains exact.
- Empty or partially populated rollups fall back to raw spans through the shared
  coverage-floor planner.
- The new high-risk anchor preflight config includes `backend/migrations/`, so
  future backend migrations cannot bypass approval-anchor detection.

## Validation

- `go test -tags=unit ./internal/repository/... -run 'Rollup|ModelStats|UserUsageTrend'`
- `pnpm --dir frontend run build`
- `PREFLIGHT_BASE=origin/main bash scripts/preflight.sh`
