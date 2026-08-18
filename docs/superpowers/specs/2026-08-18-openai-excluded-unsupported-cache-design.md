# OpenAI Failover Exclusion Unsupported Cache Fix

## Background

OpenAI-compatible failover records accounts that already failed the current request in `ExcludedIDs`. When terminal reselection finds no candidate, request-aware diagnostics count those accounts as `Excluded` before evaluating model support.

The unsupported-model predicate currently ignores `Excluded`. A pool containing model-supporting excluded accounts plus a remaining model-unsupported account is therefore misclassified as a deterministic unsupported-model client error. That error is stored in the group/model negative cache and can short-circuit unrelated requests for the cache TTL.

## Decision

Treat any excluded account as evidence that the terminal selection failure is not a pure unsupported-model failure. `tkSelectionFailedDueToUnsupportedModel` must require `Excluded == 0`, in addition to its rate-limit, unschedulable, runtime-blocked, profit-veto, and eligible guards. Any model-supporting account rejected for a non-model reason prevents deterministic unsupported-model classification.

Keep the fix at the shared predicate so all OpenAI-compatible selection exits use the same classification. Do not add retry-specific cache exceptions or duplicate the rule at call sites.

The predicate is also used by the generic gateway selection wrapper. The conservative rule is the same on both paths: once a viable account is excluded for the current request, the terminal selection result is not proof that the model is unsupported.

## Behavior

- A pool rejected only because every candidate lacks the requested model remains `ErrUnsupportedModel` and may populate the group/model negative cache.
- A terminal failover pool with any excluded account remains in the no-available/failover error family, even if another account is model-unsupported.
- That terminal failover result must not populate the group/model negative cache.
- The handler preserves the last upstream failover response when reselection exhausts the pool; an upstream 502 sequence therefore ends as 502 rather than being replaced by an unsupported-model 400.
- A later request without those per-request exclusions must still be able to select a supporting account.

## Observability

Retain both existing events:

- `openai_account_selection_failed` is the service-level capacity diagnosis carrying `excluded`, `model_unsupported`, and related counters.
- `openai.account_select_failed` is the handler-level selection failure event.

The service-level event was skipped during the incident because the incorrect unsupported predicate returned early. It is not an obsolete marker, so no sentinel or log-event rename is part of this fix.

## Tests

Use TDD with these regression layers:

1. Predicate: `ModelUnsupported=1` and `Excluded=3` returns false.
2. Predicate: mixed model-unsupported plus runtime-blocked or profit-vetoed candidates returns false.
3. Predicate compatibility: a pool rejected purely because every candidate lacks the model still returns true, preserving the existing unsupported-model 400 path.
4. Request-aware classification and cache: supporting accounts excluded or runtime-blocked alongside an unsupported account return the no-available family and leave the negative cache empty.
5. Handler/router failover loop: supporting accounts return upstream 502 responses and are added to the request exclusion set; terminal reselection must preserve the last upstream 502 and leave the negative cache empty. After the upstream fixture recovers, a subsequent same-group request without exclusions must select a supporting account successfully.

Run focused unit tests, the affected service package test suite, agent-contract checks, and `./scripts/preflight.sh` before commit and push.

## Scope

This fix does not introduce a new public API contract or status-code mapping. It restores the existing failover contract: after all attempted accounts return upstream 502, terminal reselection must not replace that failure with an unsupported-model 400. There are no model-mapping, database-state, frontend, or deployment-configuration changes. Web impact: none.
