---
title: Cursor SDK Model Service
status: approved
approved_by: "feng (conversation approval and implementation instruction, 2026-09-07)"
created: 2026-09-07
---

# Cursor SDK Model Service

## Scope and approval

The user approved the reviewed Cursor integration architecture and requested
implementation after a successful official SDK browser login and Composer 2.5
inference on an Ultra subscription. This authorizes implementation and bounded
real-account verification. Production rollout and merge require the completed
implementation and verification evidence to be reviewed.

## Owners and contracts

- Upgrade the pinned new-api dependency before Cursor routing changes. Resolve
  its RelayKit module from the same checkout and retain existing channel behavior.
- Cursor is a supply source and service group. Accounts remain `platform=newapi`,
  `type=apikey`. Persist the source identity in existing account metadata and carry
  it through existing edge mirror synchronization; no new platform or account DB.
- Reuse Sunnyender-org/new-api PR #6869 sidecar at
  `9ab86e3bc0ca368df585edcc096ec172263d222d`, including tests and AGPL attribution.
  The sidecar owns official SDK runs and callback continuation. TokenKey owns
  users, groups, credentials, routing, pricing and billing.
- Browser authorization is the default. An administrator starts a bounded SDK
  login, opens the official authorization URL, and saves the resulting expiring
  user API key through the existing account creation/update service. Credential
  values never appear in browser responses, URLs, logs or public model listings.
  Cancellation, expiration and owner mismatch fail closed.
- The bridge is internal, authenticates TokenKey with a separate shared secret,
  and requires a trusted user/key/account owner for inference. Client-supplied
  owner headers are overwritten. Tool continuation cannot change owner/account.
  Initial deployment uses one bridge and one account on one enabled edge.
- Messages is the bridge protocol. Permit explicitly mapped Cursor model IDs in
  both routing and transport without broadening official Anthropic acceptance.
  Chat and Responses use existing protocol conversion owners, including tools.
- Candidate eligibility follows `candidate-eligibility-ssot.md`: Universal and
  direct selectors share Plan and runtime filters. Cursor has no group-ordering,
  availability or billing bypass. All model families require saved SDK variant
  parameters before Plan can admit them. Public recommendations still follow
  the shared model lifecycle owner; the complete SDK mapping is retained for
  explicit requests and full-catalog validation.
- Preserve model IDs and explicit variant parameters from the authenticated SDK
  catalog. Never silently substitute a model. The full non-Auto model catalog is
  the delivery validation denominator, including all returned model families.
- Normalize incremental usage at the bridge and use existing TokenKey accounting.
  Missing SDK bill-query permission does not invalidate successful inference.
  Do not count a cumulative run snapshot again after a tool turn was billed.
- Bound request size, active/suspended runs, authorization sessions and TTLs.
  Abort disconnected requests; expired or lost continuations return an explicit
  failure and never replay client tools.

## Rollout isolation

The 2026-09-08 instruction to reduce rollout impact authorizes these safeguards:

- The optional Bridge cannot be a gateway startup dependency. Its deployment
  stays internal and has explicit memory, CPU and process limits, protected by
  `scripts/checks/test_cursor_deployment.py` in preflight.
- CLI and Docker share the production entrypoint's bounded session defaults.
  Suspended runs count toward capacity; continuation and draining retain the
  existing harness owner. These limits do not claim production load capacity.
- Cursor credential expiry remains a hard account runtime gate independently
  of optional auto-pause. Invalid SDK credentials cannot become authorized.
  Reauthorization preserves persisted pause state because the existing account
  model does not distinguish an operator pause from expiry auto-pause.
- Shared protocol and cache-billing fixes remain enabled. Regression coverage
  must exercise existing supplies with Cursor unconfigured, real tool wire
  conversion, and disjoint cache buckets through the billing command. Rates,
  historical rows and the approved Cursor estimate policy remain unchanged.
- Validate existing newapi text/media adapters and video terminal/refund paths
  before enabling Cursor. Gateway rollout and Cursor admission are separate;
  an all-model Chat result does not prove sustained load or distributed Edge
  operation. Real-account region checks remain specific to account and egress.

## Validation

Run the original bridge tests before local patches, then exercise authorization
ownership/expiry, internal authentication, model/variant selection, tenant/tool
continuation isolation, incremental usage, cancellation and error behavior.
Run existing new-api compatibility tests and backend/frontend checks after the
upgrade. Verify account authorization and full-model Chat through real TokenKey
UI with Playwright. Verify tools through real agent/client requests. Report
blocked and untested models without removing them from the frozen catalog.

The direct SDK baseline used `@cursor/sdk@1.0.31`, Composer 2.5 with `fast=false`:
response `OK.`, 6112 ms, raw input/output/cache-read tokens 3313/29/416.
`getUsage()` returned `feature_unavailable`; the dashboard showed the matching
request as Included. This baseline does not establish gateway/tool correctness.
