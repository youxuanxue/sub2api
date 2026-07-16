---
title: Usage Wallet Balance Failure Fallback
status: approved
approved_by: xuejiao (conversation approval, 2026-07-16)
approved_at: 2026-07-16
authors: [agent]
created: 2026-07-16
related_prs: ["#1352"]
related_stories: []
---

# Usage Wallet Balance Failure Fallback

## Intent

Keep `GET /v1/usage` available when the balance cache and database are both
temporarily unavailable. The endpoint is a display surface for the authenticated
API key; it is not a billing authorization or deduction path.

## Approved Contract

- Wallet mode reads `balance` and `remaining` through `BillingCacheService`, so
  the current billing balance cache remains the source of truth.
- If the cache lookup falls back to the database and that lookup also fails, the
  endpoint returns HTTP 200 with the balance snapshot attached to the already
  authenticated API key.
- The snapshot is accepted only when its user ID matches the authenticated
  subject. If no matching snapshot exists, the fallback value is zero.
- A fallback response emits
  `gateway.usage_balance_load_failed_using_auth_snapshot` with the user ID, API
  key ID, and lookup error.
- The fallback value is display-only. Billing eligibility, balance deduction,
  quota enforcement, and account authorization continue to use their existing
  service paths unchanged.

## Compatibility

The route and response schema do not change. The intentional behavior change is
that a transient balance dependency failure returns a successful usage response
instead of HTTP 500. Successful balance lookups retain the existing response
semantics.

## Data And Security

No schema, stored state, credential path, or authorization rule changes. The
authenticated snapshot is never used for a different user and is not persisted
by this fallback.

## Validation

- Cache success returns the cached balance without querying the database.
- Cache and database failure returns the matching authenticated snapshot.
- Handler unit tests and the project preflight must pass.
