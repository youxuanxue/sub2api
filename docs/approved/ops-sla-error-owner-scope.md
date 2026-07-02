---
title: Ops SLA error_owner scope
status: pending
approved_by: pending
created: 2026-07-02
owners: [tk-platform]
scope: "Admin Ops SLA / error distribution public contract + tk_057 migration"
---

# Ops SLA error_owner scope

## Decision

Remove TokenKey-only `is_business_limited` / `business_limited_count`. SLA uses persisted `error_owner`:

| Metric | Definition |
| --- | --- |
| `request_count_total` | `success_count + error_count_total` |
| `error_count_sla` | final errors with `status >= 400` and `error_owner IN ('platform','provider')` |
| `sla` | `(request_count_total - error_count_sla) / request_count_total` |

Client faults (`error_owner = client`) stay in the denominator only.

SSOT: `backend/internal/service/ops_sla_scope.go` (`IsOpsSLAFaultOwner`, `ComputeSLAMetrics`, `OpsSLAFaultOwnerPredicate`).

## Migration

`backend/migrations/tk_057_drop_ops_business_limited.sql` drops:

- `ops_error_logs.is_business_limited`
- `ops_system_metrics.business_limited_count`
- `ops_metrics_hourly.business_limited_count`
- `ops_metrics_daily.business_limited_count`

## Public contract deltas

- Removed API fields: `business_limited_*`, `request_count_sla`, `is_business_limited`.
- Error distribution buckets: `sla_faults` / `client_faults` (was `sla` / `business_limited`).
- Routing empty-pool 429 (`error_phase=routing`, `owner=platform`) counts toward SLA numerator; dedicated `routing_capacity_rejection` alert unchanged.

## Validation

- `go test -tags=unit ./internal/service -run OpsSLA`
- `go test -tags=unit ./internal/handler ./internal/repository` (ops paths)
- `pnpm test:run src/views/admin/ops`
- Post-deploy: `bash ops/observability/probe-sla-breakdown.sh`
