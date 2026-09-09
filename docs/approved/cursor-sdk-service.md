---
title: Cursor Stateless OAuth Model Service
status: approved
approved_by: "feng (conversation approval and implementation instruction, 2026-09-07; stateless architecture and estimated billing revision, 2026-09-08; system-to-user compatibility and review-fix push approval, 2026-09-09)"
created: 2026-09-07
---

# Cursor Stateless OAuth Model Service

## Approved architecture

The user's 2026-09-08 instruction supersedes the SDK sidecar architecture.
TokenKey remains a standalone Go gateway. Cursor inference uses the authenticated
CLI AgentService protocol directly, with connections, protobuf blobs and output
buffers limited to one request. There is no inference session store, SDK worker,
local tool execution, Bridge secret or cross-request connection affinity.

Cursor is the supply source and service group; accounts remain
`platform=newapi`, `type=apikey`, channel type 14. Model family, supply and public
protocol remain separate dimensions. The new-api upgrade in this PR is retained.
The native adapter follows the MIT protocol subset from can1357/oh-my-pi
`a8b0d6cc18a69e90f2bfa0da496bb537fce9319d`, checked against Cursor CLI
`2026.09.02-c22c1a3`; attribution is in
`backend/internal/integration/cursor/agentpb/VENDORED_FROM.md`.

## Owners and contracts

- Browser authorization uses the official CLI PKCE login and poll endpoints.
  Short-lived, administrator-bound sessions use the existing Redis deployment.
  Compare-and-set claims preserve expiry and prevent poll/cancel/import races.
  No credential appears in public authorization responses.
- Imported access tokens have a hard expiry gate. A returned refresh JWT is not
  treated as an API key or as proof of a working refresh endpoint. Reauthorize
  in the browser when needed; replacing credentials preserves operator pauses.
- Account creation/update, group binding and probes use existing owners.
  A dedicated Cursor group may contain multiple Cursor accounts.
- The authenticated catalog supplies exact base IDs, default regular-speed
  parameters and legacy wire slugs. Imported catalog entries do not bypass
  pricing, activation, candidate eligibility or protocol capability gates.
  A missing wire slug prevents direct native candidate admission.
- The integration package adapts native Cursor frames to Messages once.
  Chat and Responses continue through the existing protocol registry and
  converters. Cursor has no separate selection or conversion fallback.
- Every request supplies its complete history. Tools are returned to the client
  for execution, and the native call is canceled at handoff. A later request
  reconstructs history on a fresh connection. Native filesystem/shell callbacks
  are rejected.
- Inference uses account-aware HTTP transport and proxy settings, requires
  HTTP/2, rejects redirects, and never falls back to HTTP/1 for Cursor.
  Request cancellation and body closure release the producer and connection.

## Billing

The user approved estimated charging for successful tool handoffs on 2026-09-08.

Complete upstream input/output/cache usage wins and is tagged
`cursor-oauth-reported`. When a successful tool handoff has no terminal usage,
estimate this request's input/output with the same pure tokenizer as Kiro and
tag it `cursor-oauth-estimated`. Settle once through the existing accounting
owner. Never add estimated tokens to reported tokens or reconcile separate runs.
The optional terminal Messages usage field `tk_billing_tier` carries provenance
through Edge relays. The shared usage parser preserves it; accounting accepts
only the two Cursor labels and only for Cursor accounts.

Kiro's prefix-based cache estimate is a separate policy, not evidence of Cursor
cache hits. Cursor's missing-usage estimate starts with zero cache buckets, which
can cost more than an actual cache-discounted request. Antigravity normally reads
upstream usageMetadata and is not evidence that all OAuth supplies estimate usage.

## Acceptance and open issue

Current authenticated CLI catalog contains six fixed models: Composer 2.5,
Grok 4.6, Grok 4.5, Kimi K3, Kimi K2.7 Code and GLM 5.2. Direct native text probes
passed for all six before production wiring. This does not prove that the older
SDK catalog's Claude/GPT/Gemini models are callable from the same account/egress.

The prototype passed a real tool call and continuation over a new connection.
Focused regression tests cover shared candidate ordering, credential expiry,
atomic imports, cache billing, streaming truncation and consumer cancellation.
These tests do not replace TokenKey UI or coding-client acceptance.

**System-to-user compatibility was approved on 2026-09-09.** Earlier Composer probes did
not follow a marker supplied through request-context rules, non-file rules,
system_prompt_spec append or root system history. Diagnostic probes observed a
context callback but no marker in the returned prompt blobs. They are diagnostic
observations, not passing system-instruction acceptance tests. The accepted
compatibility path prepends system text to the first user message on every full
history reconstruction, including tool continuation. It preserves subsequent user
messages and does not modify the caller's history. This is user-message content,
not a guarantee of native system priority or resistance to conflicting user
instructions. Existing native rule/system fields remain supplemental.
Tests decode the actual outgoing protobuf and cover first requests, multiple
turns, tool continuation and rejection of malformed system/tool content. They do
not establish model obedience. Real coding-client acceptance is still required
before presenting this as a complete programming-client service.

Current boundary checks also reject image/thinking content blocks and forced
tool choice. Native `max_tokens` enforcement and reasoning-history replay remain
unverified; bounded response memory is not a substitute for a generation limit.

The backend unit suite passed before the final guard changes, followed by focused
Cursor, relay billing and transport regressions. Frontend lint, type checking and
207 critical tests passed. The corrected Playwright login check observes the
real accounts page and Cursor entry, not merely its URL. This is not evidence of
completed official OAuth authorization, all-model Chat or programming-client
acceptance; the local database still contains the historical SDK test account.

Before release, verify browser authorization/import and all returned fixed models
through the real TokenKey Chat UI with Playwright; verify tool round trips through
real coding clients and the supported protocol routes. Publish current evidence,
not the removed SDK Bridge's historical report. Complete full tests and preflight,
review and push PR #2036. This approval does not authorize merging or deployment.
