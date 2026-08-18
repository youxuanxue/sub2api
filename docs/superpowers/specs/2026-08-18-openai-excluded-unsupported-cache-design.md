# OpenAI Failover Exclusion Unsupported Cache Fix

## Background

OpenAI-compatible failover records accounts that already failed the current request in `ExcludedIDs`. When terminal reselection finds no candidate, request-aware diagnostics count those accounts as `Excluded` before evaluating model support.

The unsupported-model predicate currently ignores `Excluded`. A pool containing model-supporting excluded accounts plus a remaining model-unsupported account is therefore misclassified as a deterministic unsupported-model client error. That error is stored in the group/model negative cache and can short-circuit unrelated requests for the cache TTL.

## Decision

Treat any excluded account as evidence that the terminal selection failure is not a pure unsupported-model failure. `tkSelectionFailedDueToUnsupportedModel` must require `Excluded == 0`, in addition to its existing rate-limit, unschedulable, and eligible guards.

Keep the fix at the shared predicate so all OpenAI-compatible selection exits use the same classification. Do not add retry-specific cache exceptions or duplicate the rule at call sites.

## Behavior

- A pool rejected only because every candidate lacks the requested model remains `ErrUnsupportedModel` and may populate the group/model negative cache.
- A terminal failover pool with any excluded account remains in the no-available/failover error family, even if another account is model-unsupported.
- That terminal failover result must not populate the group/model negative cache.
- A later request without those per-request exclusions must still be able to select a supporting account.

## Observability

Retain both existing events:

- `openai_account_selection_failed` is the service-level capacity diagnosis carrying `excluded`, `model_unsupported`, and related counters.
- `openai.account_select_failed` is the handler-level selection failure event.

The service-level event was skipped during the incident because the incorrect unsupported predicate returned early. It is not an obsolete marker, so no sentinel or log-event rename is part of this fix.

## Tests

Use TDD with these regression layers:

1. Predicate: `ModelUnsupported=1` and `Excluded=3` returns false.
2. Request-aware classification and cache: three supporting excluded accounts plus one unsupported account returns the no-available family and leaves the negative cache empty.
3. Scheduler flow: terminal reselection with the same excluded set does not return `ErrUnsupportedModel`; a subsequent selection without exclusions succeeds and proves the shared cache was not polluted.

Run focused unit tests, the affected service package test suite, agent-contract checks, and `./scripts/preflight.sh` before commit and push.

## Scope

No public API contract, status-code mapping, model mapping, database state, frontend behavior, or deployment configuration changes. Web impact: none.
