---
title: Ops SLA error_owner scope
status: approved
approved_by: "xuejiao (PR #1156 approval, 2026-07-02)"
approved_at: 2026-07-02
created: 2026-07-02
owners: [tk-platform]
related_prs: [1156]
scope: "Admin Ops SLA / error distribution public contract + tk_057/tk_058 migrations"
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

`backend/migrations/tk_058_update_routing_capacity_alert_description.sql` updates the
seeded `routing_capacity_rejection_count` alert description to match the new
`error_owner` SLA semantics.

## Attribution requires a captured upstream verdict

`error_owner` is derived from `error_phase`, and `classifyOpsErrorLog` can only
reach `phase=upstream` when the request left an upstream verdict on the gin
context (an `OpsUpstreamErrorEvent`, or `OpsUpstreamStatusCodeKey`). An
`api_error` with no captured verdict falls through to `phase=internal`, which
maps to `owner=platform`.

**Therefore a forward path that gives up on an account MUST record its upstream
verdict, even when the status never reaches the client.** A gateway that
converts an upstream failure into an internal signal (account switch, failover,
in-place backoff exhaustion) discards the status on the way out, so the event is
the only surviving attribution.

Booking a provider failure as `platform` is not merely a mislabel: in
`ops/observability/daily_error_report.py`,
`code_owned = owner == "platform" and phase == "internal" and status >= 500`,
which at high confidence sets `repair_eligible` and queues an automated
code-repair Draft PR against code that is not at fault — while the real provider
signal stays out of `upstream_error_rate`. This happened on prod and
`edge-us4-ls` (2026-09-10) for Antigravity `gemini-2.5-flash-image` 503 (4→22)
and `claude-opus-4-6` 502 (2→7): `handleSmartRetry` was the one exit from the
Antigravity retry loop that emitted no event.

Owner: `backend/internal/service/antigravity_gateway_tk_smart_retry_attribution.go`
(consumed at every terminal `handleSmartRetry` exit). The existing TK narrowings
still take precedence over the restored provider attribution, since all three
read the same captured verdict:

- `tkUpstreamDownstreamCapacity` — a relayed TokenKey edge's own pool-empty
  envelope stays `routing` / `platform`, not provider health.
- `tkUpstreamClientCanceled` — a client cancel stays `request` / `client`.

## Public contract deltas

- Removed API fields: `business_limited_*`, `request_count_sla`, `is_business_limited`.
- Error distribution buckets: `sla_faults` / `client_faults` (was `sla` / `business_limited`).
- Routing empty-pool 429 (`error_phase=routing`, `owner=platform`) counts toward SLA numerator; dedicated `routing_capacity_rejection` alert unchanged.

## Validation

- `go test -tags=unit ./internal/service -run OpsSLA`
- `go test -tags=unit ./internal/service -run TestSmartRetry` (attribution owner)
- `go test -tags=unit ./internal/handler -run TestAntigravitySmartRetry` (resulting phase/owner + TK narrowings)
- `go test -tags=unit ./internal/handler ./internal/repository` (ops paths)
- `pnpm test:run src/views/admin/ops`
- Post-deploy: `bash ops/observability/probe-sla-breakdown.sh`
