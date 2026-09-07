---
title: Candidate Eligibility SSOT
status: approved
approved_by: "feng (conversation approval, 2026-09-07)"
created: 2026-09-07
---

# Candidate Eligibility SSOT

## Approval and scope

The user approved converging on four existing owners in the 2026-09-07
conversation, with the instruction to implement this SSOT principle.
This authorizes implementation; merge and production deployment remain separate.
The Chinese collaboration name is recorded in project AGENTS.md. Use
`candidate-eligibility-ssot` as the stable search term.

This contract supersedes the model-hint preference and support/readiness
conflation in universal-key-routing.md for evaluated requests. It does not
replace the protocol-routing SSOT, endpoint authorization, or billing owners.

## Owners

| Fact or decision | Unique owner | Consumer contract |
| --- | --- | --- |
| Model mapping, native/converter legality | `protocolrouter.Router.Plan` via `protocol_routing_context.go` | Parse the actual request using `protocolrouter.ParseCanonicalRequest`; a valid converter is equally eligible. |
| Account eligibility and current availability | Existing schedulers: `gateway_candidate_eligibility.go`, `openai_candidate_eligibility.go` and their existing quota/auth/capability helpers | Universal reads `evaluateGroupCandidates`; selectors use the same filters. |
| Empty-pool scope and expiring preference | `candidate_saturation.go` over existing Redis saturation counters | Group choice, account scoring and sticky eviction consume the same scoped state. |
| Authorized backing group and billing binding | `UniversalRoutingResolver` and `MaybeResolveUniversal` inside authentication | Select from the authenticated user's span, bind exactly one group before subscription/balance checks. |

## Required behavior

1. Disabled, error, expired, credential-unready, account/model cooldown, quota,
   RPM and temporary-unschedulable accounts cannot win current selection.
   Existing window-guard recovery remains subordinate to hard eligibility.
2. The actual protocol, Responses path and feature profile reach Plan before
   group choice. Prefixes/catalog overlap cannot exclude a proven legal text
   route. Native and media paths retain their existing capability owners.
3. Mixed pools retain the existing scheduler membership rules, including
   opt-in Antigravity and explicit Claude/newapi mappings. Platform equality
   alone is not an account admission rule.
4. Missing capability evidence and repository errors are unknown/error states.
   They cannot prove lack of entitlement. Configured but temporarily
   unavailable capacity returns 429, not a model-not-in-plan 403.
5. Classified downstream empty-pool feedback uses the existing threshold and
   fixed window constants in `edge_mirror_stub_saturation_tk.go`. Redis expires
   the window from its first hit. Saturation never changes persisted priority,
   writes a cooldown, or excludes all remaining candidates.
6. Antigravity feedback is scoped to account plus resolved upstream model.
   Other existing counters retain their account scope and Redis keys.
   OpenAI-compatible edge mirrors use the shared platform registry.
   Genuine upstream quota exhaustion retains its authoritative cooldown.
7. Universal ordering is usable subscription first, then healthy versus
   saturated capacity within that billing tier, then existing sort_order/id.
   A saturated subscription must not lose to a healthy balance group merely
   because of its soft penalty. Saturated last resorts and read failures
   preserve base ordering. Healthy peers prevent an entire group being penalized.
8. Account selection retains configured priority, routing, sticky and load
   policy, with the shared bounded saturation preference. Universal evaluation
   does not reserve concurrency slots, register sessions, or mutate bindings.
   The actual selector rechecks live state and owns acquisition/waiting races.
9. Group authorization, forced platform, endpoint opt-ins, reserved probe
   isolation and subscription validity remain enforced. Forwarding never adds
   cross-group failover or changes the bound billing group mid-request.

No equal-probability distribution is introduced. With both providers healthy,
existing ordering may still select Vertex. Antigravity participates when it
has a legal route and eligible capacity, including when earlier groups are
unavailable or saturated.

## Validation

Acceptance criteria and test mapping: US-050-candidate-eligibility-ssot.
Focused tests cover real resolver and scheduler filters, native/converter
equality, mixed pools, saturation scope/TTL, unknown capability, billing-tier
priority, and pre-billing request parsing with raw compressed-body restoration.
Gateway sentinels protect owners, consumer call sites and regression tests;
the protocol-routing checker protects the delegation to the existing Plan gate.

This is a backend/API change with no new UI surface, schema or live configuration.
Local unit and middleware integration tests are required; production verification
requires deploying the reviewed change before a Universal-key probe can prove
the new behavior.
