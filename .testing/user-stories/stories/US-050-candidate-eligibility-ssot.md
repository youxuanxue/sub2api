# US-050-candidate-eligibility-ssot

- ID: US-050
- Title: Universal and direct requests share candidate eligibility
- Priority: P0
- As a / I want / So that: As a Universal key user, I want all authorized backends with a legal native or converted route to participate, so temporary account failures do not hide healthy capacity or change my billing tier.
- Trace: `docs/approved/candidate-eligibility-ssot.md`
- Risk Focus:
  - 逻辑错误: keep model support separate from current availability and scoped saturation.
  - 行为回归: preserve mixed pools, native/media capability owners and subscription priority.
  - 安全问题: enforce authorized groups, forced platform and one billing binding per request.
  - 运行时: evaluation is read-only; slot acquisition, session registration and retry remain selector responsibilities.

## Acceptance Criteria

1. AC-001 (positive): Given authorized Vertex and Antigravity accounts When one is unavailable Then the resolver selects the eligible peer using the real request Plan.
2. AC-002 (negative): Given configured capability but no currently available account When resolving Then return capacity 429; unknown evidence must not become entitlement 403.
3. AC-003 (regression): Given native and valid converted routes When comparing groups Then native support adds no priority advantage.
4. AC-004 (regression): Given a mixed Gemini/Antigravity pool When evaluating Then existing scheduler membership controls admission.
5. AC-005 (regression): Given sustained empty-pool feedback When choosing a group or account Then the same scoped temporary penalty applies, all-saturated pools remain last resorts, and subscription priority survives.
6. AC-006 (integration): Given a compressed Responses compact request When authentication evaluates candidates Then the exact profile/path reaches Plan before billing binding and raw body/headers remain unchanged.
7. AC-007 (mechanical): Given removal of a shared filter, saturation reader or consuming call site When running sentinels Then preflight fails.

## Assertions

- Native and converted routes have identical group-selection rank.
- Cooling and disabled accounts cannot turn supported capacity into a 403.
- Antigravity saturation affects only its resolved model; expiry restores base priority.
- Healthy balance capacity cannot displace a usable subscription due to saturation alone.
- Request parsing precedes billing-group mutation and restores compressed bytes exactly.

## Linked Tests

- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityGoogleBackends`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityCapacityIsNotEntitlement`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityUnknownCapabilityIsNotEntitlement`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityNativeAndConverterEqual`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityMixedPoolUsesSchedulerMembership`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilitySaturationPreservesBillingTier`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityAntigravitySaturationScopeAndParity`
- `backend/internal/repository/antigravity_saturation_counter_cache_test.go`::`TestAntigravitySaturationCounterCache_FixedWindow`
- `backend/internal/server/middleware/universal_routing_tk_test.go`::`TestMaybeResolveUniversal_CandidatePlanPrecedesBillingAndPreservesBody`
- `backend/internal/server/middleware/universal_routing_tk_test.go`::`TestMaybeResolveUniversal_CapacityDoesNotDenyEntitlement`
- Run:

```bash
cd backend && go test -tags unit ./internal/service ./internal/server/middleware ./internal/repository ./internal/handler ./internal/engine/protocolrouter
python3 scripts/sentinels/check-gateway-tk.py --quiet
python3 .testing/user-stories/verify_quality.py
```

## Status

- [x] Done

## Evidence

- Full service, middleware, repository, handler, protocolrouter and server unit packages: PASS.
- Production DI regression and Wire regeneration: PASS.
- Protocol SSOT checker self-tests: 81 passed.
- golangci-lint: 0 issues.
