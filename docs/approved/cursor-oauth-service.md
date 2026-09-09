---
title: Cursor Stateless OAuth Model Service
status: approved
approved_by: "feng (conversation approval and implementation instruction, 2026-09-07; stateless architecture and estimated billing revision, 2026-09-08; system-to-user compatibility, review-fix push, shared unsupported-output-limit compatibility and automatic credential renewal instruction, 2026-09-09)"
created: 2026-09-07
---

# Cursor Stateless OAuth Model Service

## Approved architecture

TokenKey is a standalone Go gateway. Cursor inference uses the authenticated
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
- Imported access tokens have a hard expiry gate. The shared background OAuth
  refresh service renews native Cursor accounts before expiry using the verified
  Desktop `POST /oauth/token` contract. The current access token is the renewal
  grant; the returned access token is also the next grant, matching Desktop.
  The initial login refresh JWT is not treated as a user API key. Renewal uses
  the account proxy, rejects redirects, and shares locking, retries and cache
  publication with other providers. Credential/expiry writes and failure state
  changes match the attempted credential version and proxy. Operator pauses
  survive renewal; revoked credentials still require browser reauthorization.
- Account creation/update, group binding and probes use existing owners.
  A dedicated Cursor group may contain multiple Cursor accounts.
- The authenticated catalog supplies exact base IDs, default regular-speed
  parameters and legacy wire slugs. Imported catalog entries do not bypass
  pricing, activation, candidate eligibility or protocol capability gates.
  A missing wire slug prevents direct native candidate admission.
- The integration package adapts native Cursor frames to Messages once.
  Chat and Responses continue through the existing protocol registry and
  converters. All converted and native Messages sends use
  `doNativeMessagesRequest`; native completion errors and billing provenance
  also reach the Chat/Responses settlement result. Cursor has no separate
  selection or conversion fallback.
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
The native optional fields retain presence: missing input, output or cache
buckets are not reported zeros. A normal completion with incomplete usage fails;
a successful tool handoff uses the approved estimate. Negative usage is rejected.

Kiro's prefix-based cache estimate is a separate policy, not evidence of Cursor
cache hits. Cursor's missing-usage estimate starts with zero cache buckets, which
can cost more than an actual cache-discounted request. Antigravity normally reads
upstream usageMetadata and is not evidence that all OAuth supplies estimate usage.

## Output limit compatibility

The user approved this compatibility contract on 2026-09-09. Native Cursor and
ChatGPT/Codex OAuth transports omit unsupported top-level `max_tokens`,
`max_output_tokens` and `max_completion_tokens`. They do not promise a generation
or spending cap. TokenKey does not truncate output locally or expand estimated
billing to simulate enforcement; the existing usage and settlement owners remain
authoritative.

`backend/internal/service/upstream_output_limits_tk.go` owns the field list and
map/raw JSON omission. Cursor uses it at the native Messages boundary, including
converted Chat/Responses and probes. Codex uses it in the shared OAuth transform
and HTTP passthrough/WebSocket compatibility normalization, including setup
tokens using the same transport. Subsequent alias normalization must not restore
a removed limit. Only top-level generation fields are omitted; tool schemas and
input content are preserved.

Other supplies retain their native policies: Kiro sends `inferenceConfig.maxTokens`;
Antigravity sends `generationConfig.maxOutputTokens` with its existing thinking
budget and model bounds; OpenAI API keys retain their existing Responses limits.
Sending these fields is not proof that every upstream model enforces them.
The shared omission owner is a transport compatibility rule, not a candidate
eligibility or billing policy. Its call sites and behavioral tests are protected
by `scripts/sentinels/gateway-tk.json`.

## System and feature boundaries

The approved compatibility path prepends system text to the first user message
on every full-history reconstruction, including tool continuation. Subsequent
user messages and the caller's history are preserved. This does not guarantee
native system priority or resistance to conflicting user instructions. Native
rule/system fields are supplemental; the user-message path is authoritative.

Image/thinking content blocks and forced tool choice are rejected.
Reasoning-history replay is unverified. The authenticated account catalog is
the model source; availability must still pass the shared serving gates above.

## Validation and maintenance

`backend/internal/integration/cursor/` owns native protocol and authorization
tests. Service tests cover actual protocol dispatch, settlement, expiry and
background renewal; repository integration tests cover atomic imports and
credential updates. `scripts/checks/test_cursor_deployment.py` rejects obsolete
runtime dependencies and retired integration contracts through preflight.

| Local entry | Purpose |
| --- | --- |
| `node scripts/cursor/local-dev.mjs prepare\|start\|stop` | Isolated PostgreSQL/Redis and Go/frontend processes; requires Docker, Go and installed frontend dependencies |
| `node frontend/e2e/cursor-live.tk.mjs authorize\|chat\|chat-all` | Real Playwright UI authorization/import and model calls; run against that local stack |
| `node scripts/cursor/client-probe.mjs tools\|parallel\|text` | Client protocol/tool checks; requires the local gateway key from UI Chat and OpenAI/Anthropic SDKs installed under `.cache/cursor-dev/clients` |
| `node scripts/cursor/billing-audit.mjs tools\|parallel\|text` | Match the corresponding local client probe to persisted usage and prices |
| `node scripts/cursor/claude-code-probe.mjs` | Local Claude Code Read tool acceptance; requires the local gateway key and installed CLI |

Direct live Go probes consume an explicitly supplied JSON file containing
`access_token` via `TOKENKEY_CURSOR_CREDENTIALS_FILE`; keep it outside version
control. Select `-run '^TestAgentLive$'` for text/tool continuation,
`-run '^TestAgentLiveCatalog$'` for the authenticated catalog, or
`-run '^TestOAuthRefreshLiveChainAndInference$'` for chained renewal and inference.
These probes contact the real provider and are not UI e2e tests. Local artifacts
under `.cache/cursor-dev/` are evidence from a particular run, not runtime config
or a source of model availability.

At PR #2036 commit `c22f5c8d0`, isolated live acceptance passed official browser
authorization, UI model calls, tool continuation, billing reconciliation and
chained renewal. The real background service updated PostgreSQL expiry and Redis
credentials, followed by a successful Playwright Composer call. This proves
renewal before expiry, not recovery of already-expired or revoked credentials.
Production deployment status belongs to release records, not this design.

Complete full tests and preflight, review and push PR #2036. This approval does not
authorize merging or deployment.
