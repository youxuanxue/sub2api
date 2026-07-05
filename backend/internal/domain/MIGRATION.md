# domain/ Migration Plan (A-01: service/ decomposition)

## Goal

Break the structural layer violation where `repository/` imports `service/`
(upward dependency). Shared types/constants/errors move to `domain/` so
`repository/` can import `domain/` instead.

## Batch 1 (this commit)

**Moved to domain/:**

| File | Symbols | Count |
|------|---------|-------|
| `errors.go` | 31 error sentinel vars (ErrUserNotFound, ErrAPIKeyNotFound, etc.) | 31 |
| `scheduler_events.go` | SchedulerOutboxEvent* constants (6) | 6 |
| `usage_cleanup.go` | UsageCleanupStatus* constants (5) | 5 |
| `constants.go` (pre-existing) | Status/Role/Platform/AccountType/RedeemType/etc. constants (34) | 34 |

**Impact:**
- 1169 `service.*` references in repository/ replaced with `domain.*`
- 6 repository files completely dropped the `service/` import
- 175 repository files still import `service/` (down from 181)
- Backward-compatible: service/ re-exports all moved symbols as aliases

## Top 20 remaining candidates (by usage count in repository/)

| # | Symbol | Type | Uses | Methods | Est. effort |
|---|--------|------|------|---------|-------------|
| 1 | Account | struct | 299 | 177 | XL - needs method extraction first |
| 2 | User | struct | 208 | ~20 | L - many methods reference service internals |
| 3 | APIKey | struct | 127 | ~15 | L |
| 4 | Group | struct | 109 | ~10 | M |
| 5 | UsageLog | struct | 84 | ~5 | M |
| 6 | Proxy | struct | 56 | 0 | S - good candidate |
| 7 | RedeemCode | struct | 39 | ~2 | S |
| 8 | UserSubscription | struct | 33 | ~3 | S |
| 9 | OpsDashboardFilter | struct | 28 | 4 | S |
| 10 | UsageCleanupTask | struct | 25 | 1 | S - good candidate |
| 11 | UsageCleanupFilters | struct | 22 | 0 | S - good candidate |
| 12 | OpsPercentiles | struct | 21 | 0 | S - good candidate |
| 13 | UserPlatformQuotaRecord | struct | 20 | 0 | S - good candidate |
| 14 | BillingCache | interface | 19 | N/A | M - interface, deferred |
| 15 | UsageBillingCommand | struct | 18 | 0 | S - good candidate |
| 16 | UserListFilters | struct | 17 | 0 | S - good candidate |
| 17 | SchedulerBucket | struct | 16 | 2 | S |
| 18 | OpsErrorLogFilter | struct | 16 | 3 | S |
| 19 | ChannelModelPricing | struct | 16 | 6 | M |
| 20 | AccountGroup | struct | 11 | 0 | S - but references Account/Group |

## Recommended batch 2

Focus on zero-method structs that don't reference other service types:
- UsageCleanupFilters (22 uses, 0 methods, standalone fields)
- UsageCleanupTask (25 uses, 1 method)
- OpsPercentiles (21 uses, 0 methods)
- UserPlatformQuotaRecord (20 uses, 0 methods)
- UsageBillingCommand (18 uses, 0 methods)
- UserListFilters (17 uses, 0 methods)
- Proxy (56 uses, 0 methods, standalone)

Estimated: ~200 more service.* references migrated.

## Recommended batch 3

Move the remaining small structs with few methods, plus begin extracting
method-free subsets of the big structs (Account, User, APIKey, Group).

## Pattern

For each migrated type:
1. Define in `domain/` (canonical owner)
2. Add `type Foo = domain.Foo` alias in `service/` (backward compat)
3. Update `repository/` imports to use `domain.Foo`
4. handler/ and other service/ callers keep working via the alias

Interfaces are deferred until their method signatures no longer reference
un-migrated service types.
