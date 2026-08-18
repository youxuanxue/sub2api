# Universal API Key Capability Discovery Implementation Plan

> **For Codex:** Execute with `executing-plans`, keep each behavior in a RED → GREEN cycle, and do not claim completion without `verification-before-completion`.

**Goal:** Make every API-key discovery surface and the website project one callable, protocol-aware capability truth while preserving native client schemas and existing runtime routing.

**Architecture:** Add a service-layer `UniversalCapabilityService` that owns entitlement loading, model candidate enumeration, shape-aware scheduler checks, and deterministic selected-group resolution. Gateway and authenticated UI handlers receive this owner through existing Wire late binding and only project typed results. Standard endpoints expose no internal routing metadata; the authenticated UI endpoint exposes the selected service needed for price/menu context.

**Tech Stack:** Go/Gin/Wire, Vue 3/TypeScript, Vitest, Playwright, generated agent-contract Markdown.

---

## Task 1: Capability owner

**Files:**
- Create: `backend/internal/service/universal_capability_tk.go`
- Test: `backend/internal/service/us046_universal_capability_test.go`
- Modify: `backend/internal/service/wire_tk.go`

1. Add a failing service test where two authorized groups advertise chat and video models but only shape-supported candidates are returned in stable order with the resolver-selected group.
2. Add a failing test proving `GetAvailableGroups` and provider errors propagate.
3. Implement protocol/shape and modality types, candidate enumeration, `UniversalGroupSupportsRequest` checks, and deterministic group selection by reusing the existing resolver rules.
4. Run `go test ./internal/service -run TestUS046 -count=1`.

## Task 2: Authenticated capability API

**Files:**
- Create: `backend/internal/handler/api_key_capability_handler_tk.go`
- Test: `backend/internal/handler/us046_universal_discovery_test.go`
- Modify: `backend/internal/handler/handler.go`
- Modify: `backend/internal/handler/wire.go`
- Modify: `backend/internal/server/routes/user_tk_routes.go`

1. Add failing tests for owner success, direct-key scope, foreign-key 404, and internal-error 500.
2. Implement `GET /api/v1/me/api-keys/:id/capabilities` with JWT ownership enforcement.
3. Wire the handler and run focused handler tests.

## Task 3: Native discovery projections

**Files:**
- Modify: `backend/internal/handler/gateway_handler_tk_model_list.go`
- Modify: `backend/internal/handler/gateway_handler.go`
- Modify: `backend/internal/handler/gemini_v1beta_handler.go`
- Modify: `backend/internal/handler/openai_codex_models_handler.go`
- Modify: relevant Antigravity model handler
- Modify: `backend/internal/server/routes/gateway.go`
- Test: `backend/internal/handler/us046_universal_discovery_test.go`

1. Add failing tests for OpenAI/Anthropic native fields, Gemini `supportedGenerationMethods`, both Codex paths, and authorized Antigravity filtering.
2. Replace Universal union helpers with capability-service projections and propagate internal errors.
3. For Codex, select a callable OpenAI backing group and reuse the existing manifest handler rather than inventing a manifest.
4. Run all affected handler and middleware tests.

## Task 4: Frontend SSOT and product language

**Files:**
- Create/modify: `frontend/src/api/api-key-capabilities.ts`
- Modify: `frontend/src/composables/useTkUseKey.ts`
- Modify: `frontend/src/views/user/studio/MediaStudioView.vue`
- Modify: key creation/edit views and i18n owner files
- Test: focused Vitest files

1. Add failing unit tests that automatic-routing menus call only the capability endpoint, filter by protocol/modality, and preserve load errors.
2. Implement the API client and replace user-entitlement/public-catalog discovery workarounds in Quickstart and Studio.
3. Change visible language from “Universal/全能” to default automatic routing; describe direct binding as an advanced restriction.
4. Run frontend typecheck and focused unit tests.

## Task 5: Real UI acceptance

**Files:**
- Create: `frontend/e2e/us046-universal-capability-discovery.e2e.ts`

1. Add Playwright fixtures/routes for keys and the capability endpoint.
2. Drive the real Keys/Quickstart or Studio UI, select an automatic-routing key, verify protocol/model choices, and verify the visible load-error state.
3. Run the e2e test in Chromium and inspect screenshots for overlap or clipping.

## Task 6: Contracts, verification, and delivery

1. Run Go formatting, focused tests, broader backend tests, frontend lint/typecheck/unit tests, and Playwright.
2. Run `python scripts/export_agent_contract.py` and `python scripts/export_agent_contract.py --check`.
3. Update US-046 linked tests/status and ensure `python .testing/user-stories/verify_quality.py` passes.
4. Run `./scripts/preflight.sh`; fix and rerun until green.
5. Use `requesting-code-review`, then `verification-before-completion` and `finishing-a-development-branch`.
6. Commit with the approval anchor and web-impact evidence, push to `origin`, create a Chinese PR body containing every commit SHA/subject and the current HEAD freshness anchor.

## Task 7: R-010 authenticated pricing selector parity

**Files:**
- Modify: `backend/internal/service/me_pricing_catalog_tk_test.go`
- Modify: `backend/internal/service/me_pricing_catalog_tk.go`
- Modify: `backend/internal/handler/me_pricing_catalog_handler_tk_test.go`
- Modify: `backend/internal/handler/me_pricing_catalog_handler_tk.go`
- Modify: `frontend/src/api/me-pricing.ts`
- Modify: `frontend/src/views/PricingView.vue`
- Modify: `frontend/src/views/__tests__/PricingView.spec.ts`
- Modify: `frontend/src/i18n/tk/legacyMissing.tk.ts`
- Modify: `docs/approved/universal-key-capability-discovery.md`
- Modify: `.testing/user-stories/stories/US-046-universal-key-capability-discovery.md`

**Interfaces:**
- Consumes: an owned active `service.APIKey` with `RoutingModeUniversal` and no `GroupID`, plus the caller's accessible groups.
- Produces: `GET /api/v1/me/pricing-catalog?api_key_id=<id>` with `target_group: null`, a stable de-duplicated union in `models`, and the existing `authorized_groups_by_model` group provenance. Direct-key and explicit-group selectors keep their current group-scoped contract.

1. Add a service test proving an owned automatic-routing key returns the union of its owner's accessible group menus, keeps duplicate model IDs once, marks no group as current, and leaves `TargetGroup` nil.
2. Run the focused service test and verify it fails with `ErrMePricingAPIKeyNotFound`.
3. Add a handler serialization test proving the successful universal response emits JSON `target_group: null`; keep the existing foreign/missing-key 404 test.
4. Change target selection to distinguish a user-wide key scope from a group scope, aggregate group rows deterministically for the former, and preserve the existing default/direct behavior.
5. Update the Go and TypeScript DTOs so `target_group` and picker `group_id` are nullable, keep universal keys in `my_keys`, and render a distinct automatic-routing scope in PricingView.
6. Run focused service, handler, and PricingView tests until green.
7. Add the omitted pricing projection and acceptance criterion to the approved design and US-046, run the linked Story verification, then run full preflight.
