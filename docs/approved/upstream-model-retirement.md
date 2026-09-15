---
title: Upstream model retirement handling
status: draft
approved_by: pending
created: 2026-09-15
related_stories: [US-049]
---

# Upstream model retirement handling

## Proposed contract

PR #2173 removes the withdrawn NVIDIA Build DeepSeek Pro aliases from that
supplier's provisioning scope. Other suppliers and NVIDIA's remaining models
retain their existing eligibility.

`model_retired_error_tk.go` owns retirement evidence: an HTTP 400, 404 or 410
with an explicit model retirement diagnostic. A bare 410, expired file/session,
echoed request content, authentication error or transient availability response
is insufficient. Only diagnostic strings are inspected in structured JSON.

OpenAI-compatible HTTP and NewAPI bridge submit account-fault semantics to the
existing `gateway_failover_policy.go` owner. The existing model availability
owner writes a 30-minute account/model cooldown; bridge uses the executed
protocol Plan and never guesses a model when no Plan exists. Existing account
error-code controls, retry budgets and committed-stream boundaries remain in
force.

After failover exhaustion, an uncommitted OpenAI response preserves a confirmed
retirement status and sanitized diagnostic. Ops classification treats that
confirmed rejection as client-owned unless an account-standing fault is present.
Unrelated 410s retain their existing provider-error treatment. This terminal
response/attribution delta requires approval before merge; it is not covered by
the original failover-owner extraction's no-behavior-change approval.

## Validation

- `TestModelRetirementRequiresModelDiagnostic` and
  `TestModelRetirementDoesNotCoolUnrelatedGone`: reject unrelated errors and
  echoed request content without poisoning model availability.
- `TestBridgeModelRetirementCoolsExecutedModel` and
  `TestBridgeModelRetirementWithoutPlanDoesNotGuessCooldownKey`: switch accounts,
  persist the executed model cooldown, and avoid account-wide or guessed writes.
- `TestModelRetirementFailoverExhaustionRequiresDiagnostic` and
  `TestModelRetirementOpsAttributionRequiresDiagnostic`: terminal response and
  SLA attribution consume the same evidence.
- `TestNVIDIABuildRetiredModelExcludedFromProvisioning`: withdrawal boundary.
- `scripts/sentinels/gateway-tk.json`: implementation, call sites and focused
  regression tests stay anchored through upstream merges.

These are backend unit/handler tests, not browser end-to-end tests. No UI changes
or live account mutations are part of this PR.
