---
title: Protocol Routing Single Source of Truth
status: approved
approved_by: "feng (conversation approval, 2026-08-27)"
authors: [codex]
created: 2026-08-24
revised: 2026-09-01
revision_note: >
  Slimmed §§7–14 to contract-only after ship; §§1–6 unchanged.
  Plans and Execute no longer compare account or capability revision tokens.
  Send-time freshness is authoritative reload plus route-fact equivalence.
  /health is drain-only. Protocol remediations never 503 the process.
  TrafficReady and Ready() are gone; CutoverReady is the only report gate.
  NewAPI exact-endpoint resolution is per declared protocol: undeclared
  protocols are not resolved, and a missing Responses URL omits that route
  from both exact endpoints and the Plan-facing supported set.
  On a non-OfficialEndpointAnthropic identity, native messages is legal only
  for a Claude-family resolved model; dual-stack OpenAI relays convert
  inbound /v1/messages for GPT and other non-Claude models. Conversation
  approval on 2026-09-01 also requires conversion adapters to fail over an
  empty zero-usage terminal attempt instead of synthesizing protocol success.
related_stories: []
---

# Protocol Routing Single Source of Truth

## 1. Ownership

This design owns:

1. one persisted endpoint-capability fact shared by all accounts with the same
   canonical endpoint identity;
2. one runtime protocol-planning and execution owner;
3. account authorization and health as independent scheduler hard gates.

Protocol support is a property of an upstream endpoint identity. Credentials,
balance, limits, and health are properties of an account. Probe evidence is
audit metadata; request normalization is an immutable input; account health
remains account state. None of them independently choose a protocol.

There is no fourth protocol fact. Handler fallbacks, platform-derived
endpoints, compatibility helpers, account extra fields, engine bridge checks,
and `ForwardAs*` methods cannot independently pick a wire protocol or host.

This is a high-risk design because it governs the core inference path, database
schema, credential destination selection, and deployment readiness boundary.

## 2. Scope and non-goals

The governed customer-facing and upstream text wire protocols are:

- `messages`
- `chat_completions`
- `responses`
- `gemini_generate_content`

`gemini_generate_content` covers Gemini `generateContent` and
`streamGenerateContent`. It is a wire protocol, not a Google product, host,
credential type, or account class.

This design includes native forwarding and explicitly registered, lossless
conversions among governed protocols. It also includes protocol-specific
Responses path variants such as compact and input-token operations when an
individual route explicitly supports them.

Only text generation is governed. Embeddings, image/video/audio generation,
OCR, rerank, and Gemini multimodal identity remain outside this router. One
ingress predicate owns this boundary; handlers cannot independently choose
between SSOT routing and legacy text routing.

This design deliberately does not add:

- administrator protocol ordering or manual protocol overrides;
- `preferred_protocols`, `forced_protocol`, adaptive/fixed modes, scores, or
  graph search;
- a universal protocol graph or universal intermediate representation;
- credential-dependent endpoint capability;
- periodic per-account capability probing;
- inference from platform name, a similar base URL, or another account;
- media-protocol governance.

## 3. Ownership model

The persisted protocol-capability SSOT is:

```text
protocol_endpoint_capabilities
```

An account references exactly one shared capability row when it is governed:

```text
accounts.protocol_endpoint_capability_id
```

The reference is nullable only for account classes outside this design. An
active and schedulable governed account without a valid reference is not ready
and has no legal text route.

The runtime decision owner for governed generation remains focused at:

```text
backend/internal/engine/protocolrouter
```

It owns protocol identifiers, the fixed route registry, request-feature and
model constraints, endpoint resolution, route planning, and the only execution
entry that may reach governed generation transports.

For the generation family governed here, `protocolrouter.Plan` is the request
planning implementation. This document does not own catalog policy, runtime
account readiness, asynchronous task continuation, or non-generation execution
families. Delivery outcome composition stays outside this protocol design.

The separation is strict:

| Fact or decision | Owner | Protocol effect |
| --- | --- | --- |
| Endpoint native protocols | `protocol_endpoint_capabilities.supported_protocols` | Shared persisted SSOT |
| Generation route and conversion legality | `protocolrouter` | Only generation runtime decision owner |
| Credential, balance, limit, cooldown, health | account/scheduler owners | Independent hard gates |
| Probe observations | capability evidence | Audit and update input only |
| Customer request semantics | immutable `CanonicalRequest` | Planner input only |

No production consumer may reconstruct endpoint capability from account type,
platform, URL shape, legacy fields, probe evidence, or a transport helper.

## 4. Canonical endpoint identity

### 4.1 Capability key

Every governed account is mapped to a versioned canonical identity. The unique
key is:

```text
capability_key = SHA256(canonical_json(identity))
```

The identity contains every non-secret fact that can change which native wire
protocols the endpoint accepts:

```json
{
  "key_schema_version": 1,
  "platform": "openai",
  "endpoint_profile": "custom_api_key",
  "channel_type": "openai",
  "protocol_endpoints": {
    "chat_completions": {
      "url": "https://example.test/v1/chat/completions",
      "api_version": ""
    },
    "responses": {
      "url": "https://example.test/v1/responses",
      "api_version": ""
    }
  },
  "upstream_request_profile": "openai_json_v1",
  "routing_headers": {}
}
```

The exact fields are built by one identity builder owned next to the capability
repository. They include:

- `key_schema_version`;
- platform;
- stable account type or compile-time endpoint profile;
- channel type;
- a protocol-sorted map of normalized URL and API version;
- the upstream request/wrapper profile;
- routing-affecting non-authentication headers or profiles.

They exclude:

- account ID, token, API key, OAuth token, or service-account secret;
- balance, quota, rate limit, priority, cooldown, and account status;
- proxy selection;
- model mapping and requested model;
- dynamic customer request features.

URL normalization lowercases scheme and host, removes default ports, normalizes
empty/trailing path forms without changing endpoint meaning, sorts semantic
query parameters, and excludes credentials. Protocol and header maps are sorted
before canonical JSON serialization. Authentication headers are never included
or persisted in identity or evidence.

Changing a URL, API version, channel type, endpoint/request profile, or
routing-affecting non-auth header produces a new key. Rotating credentials,
changing balance or priority, changing proxy, or changing model mapping does
not.

Two accounts with the same key must reference the same capability row and see
the same `supported_protocols`. Administrators cannot type or edit the key.

### 4.2 Persisted schema

The additive table is conceptually:

```text
protocol_endpoint_capabilities
  id                  primary key
  capability_key      unique, not null
  identity            jsonb, not null
  supported_protocols jsonb, not null, default []
  probe_evidence      jsonb, not null, default {}
  revision            bigint, not null
  last_probed_at      timestamp nullable
  probe_lease_owner   text nullable
  probe_lease_until   timestamp nullable
  probe_generation    bigint, not null
  created_at
  updated_at
```

`identity` is immutable for a row. Configuration changes compute a new key and
atomically relink the account; they never mutate an existing row into another
identity.

`supported_protocols` is the complete, validated, duplicate-free,
deterministically sorted native protocol set. Semantics are binary:

- present: the endpoint may be considered as that native target;
- absent: the endpoint is ineligible as that native target.

There is no persisted `unknown`. An empty set is fail-closed. Probe status and
evidence may be inconclusive, but they cannot act as a third routing state.

`revision` changes whenever the sorted protocol set or conflict state changes.
It is probe compare-and-swap and audit state only. Plans and `Execute` do not
compare it. `probe_generation` increments for every accepted probe request and,
together with the lease fields, coordinates probe work; it does not affect
routing directly.

Orphaned capability rows are retained for audit and possible account relinking.
Garbage collection is outside this change.

## 5. Canonical request and route registry

Ingress validates the original wire body once and constructs one immutable
`CanonicalRequest`. It is a wrapper around the validated inbound body, not a
cross-protocol intermediate representation.

```go
type CanonicalRequest struct {
    inboundProtocol Protocol
    requestedModel  string
    responsesPath   ResponsesPathKind
    profile         RequestProfile
    body            []byte
    digest          RequestDigest
}
```

The constructor defensively copies the body. The digest covers protocol,
requested model, Responses path, typed feature profile, and body. The profile
distinguishes streaming, tool choice, continuation, reasoning, prompt cache,
and content kinds. A route may inspect the validated protocol-specific body
only when typed semantics are insufficient.

The application constructs one immutable router at the composition root:

```go
router := protocolrouter.New(adapterCatalog)
```

Request-time code uses exactly:

```go
plan, err := router.Plan(request, accountSnapshot)
result, err := router.Execute(ctx, plan, request)
```

`Plan` is pure and performs no network I/O. `Execute` follows the immutable
plan and cannot select, improve, or replace its route.

Identity is always attempted first. If identity is illegal, conversions are
tried in this fixed order:

| Inbound protocol | Conversion targets |
| --- | --- |
| `messages` | `responses`, then `chat_completions`, then `gemini_generate_content` |
| `chat_completions` | `responses`, then `messages`, then `gemini_generate_content` |
| `responses` | `chat_completions`, then `messages`, then `gemini_generate_content` |
| `gemini_generate_content` | identity only |

The first legal entry wins. Each registry entry names its target protocol,
allowed Responses paths, model policy, feature constraints, endpoint/profile
resolver, one route adapter, and one transport. Conversion routes are
deny-by-default and open only for semantics proven lossless by adapter contract
tests.

For one account, a route is legal only when:

```text
target protocol is in the linked capability.supported_protocols
AND the capability key is current
AND an ordered registry entry exists
AND model policy permits the resolved upstream model
AND the adapter preserves the concrete request features
AND the endpoint resolves explicitly from the same identity/account snapshot
AND the required adapter and transport exist
```

On a non-`OfficialEndpointAnthropic` identity, native `messages` is legal only
when the resolved upstream model is in the Claude family (`claude-*`). Dual-stack
OpenAI relays that advertise `messages` plus `chat_completions` and/or
`responses` therefore convert inbound Claude Code `/v1/messages` for GPT and
other non-Claude models instead of identity-forwarding onto a Claude-only
native path. TokenSea and CloudWise also omit native `responses` from Plan
because their advertised Responses support is a probe false-positive; the
legal conversion for those identities is `chat_completions`. The
`messages → chat_completions` conversion preserves Claude Code function tools
and images, matching the already-proven `messages → responses` surface, so a
tools-bearing Claude Code body does not fail closed back onto identity.

Endpoint resolution must reproduce the identity used to obtain the capability
key. A configurable endpoint with an empty or mismatched URL never falls back
to an official host. For NewAPI channel adaptors, exact URLs are attached only
for Chat/Responses protocols already in the linked capability. A declared
protocol that cannot resolve is omitted from both the exact-endpoint map and
the Plan-facing supported set; undeclared protocols are not resolved.
A channel that cannot resolve Responses does not fail Chat snapshot
construction or the account's remaining legal routes, and must not invent a
Responses URL from the channel base.

## 6. Scheduling and execution

Protocol legality is a scheduler hard gate, not a ranking signal:

```text
construct immutable CanonicalRequest
  -> Plan every candidate against its linked capability
  -> discard candidates with no legal plan
  -> apply independent account-runtime hard gates
     (authorization, schedulable, cooldown, quota, concurrency, capacity)
  -> apply existing priority/sticky ordering unchanged
  -> Execute the selected candidate's already-created plan
```

A candidate is eligible only when it has a legal `protocolrouter.Plan` and then
passes those account-runtime gates.

Runtime readiness cannot create, replace, or improve a route. Route planning
cannot treat transient authorization, quota, cooldown, concurrency, or capacity
as endpoint protocol capability.

The plan stores the request digest, capability key, resolved model, inbound and
target protocols, endpoint, adapter, transport, route kind, and reason. It does
not store an account or capability revision token. Before constructing a
network request, execution reloads the authoritative account and re-plans it.
A plan is stale only when the fresh account no longer produces an equivalent
route (capability key, resolved model, endpoint, adapter, transport) or
authorization is missing. `Execute` verifies the captured request digest and
capability key. Account `updated_at`, last-used, token rotation, capability
`revision` bumps, and other health writes do not by themselves invalidate a
still-equivalent plan; the reload exists to attach current credentials, not to
treat every account or probe write as a protocol change. A stale route or
missing credential fails closed and enters normal account failover.

Execution invariants:

- the same immutable request is used across account attempts;
- one registered route adapter is invoked per attempt;
- credentials are attached only after endpoint and identity validation;
- transports cannot change protocol or endpoint;
- no handler or transport retries another protocol on the same account;
- usage, billing, QA, and error records use the actual plan facts.

Conversion adapters must also preserve failure semantics. A streaming attempt
that reaches a terminal marker with no text, reasoning, tool call, error, or
token usage is not a successful empty answer. Before semantic output is
committed, the adapter keeps protocol preamble frames attempt-local and returns
the existing silent-refusal failover error. If a keepalive has already committed
the downstream stream, the terminal result is a protocol-shaped error; the
adapter must never synthesize `end_turn` or an equivalent success terminal.
Non-zero usage and explicit non-stop terminal reasons remain valid evidence and
are not classified as this empty-attempt failure.

Planning and pre-send validation failures perform no network I/O. Upstream
failures retain existing cooldown and account-failover classification.

## 7. Capability discovery (contract only)

Probe coordination is **endpoint-scoped** (`capability_key + probe_generation`), never
account-scoped. Triggers: new key without conclusive/seed evidence; admin re-probe;
structured endpoint drift; repaired witness while capability still empty/inconclusive.
Same-key account add and credential rotation do **not** probe.

Witness selection is deterministic (auth → schedulable → no error → priority → id).
Verdicts: `positive` adds; `endpoint_negative` removes; `model_specific` /
`inconclusive` preserve membership. Contradictory positive+negative →
`identity_conflict` (fail-closed). Customer failures may enqueue probes but never
directly mutate capability.

## 8. Governed profiles and admin surface

Governance is compile-time account shape. Governed profiles include configurable
text/NewAPI endpoints, Antigravity OAuth (`antigravity_cloudcode`), exact Vertex
service-account, and exact TokenKey Antigravity edge-relay stubs. Official seed only
for immutable compile-time official endpoint profiles.

Admin entry remains `POST /api/v1/admin/accounts/:id/protocol-probe` (endpoint-scoped
job). `account.supported_protocols` is a derived projection, never admin-written.
Outcomes: `updated` / `unchanged` / `inconclusive` / `not_applicable`. No protocol
ordering/force/mode UI.

## 9. Cutover and rollback projection

New code always reads the capability table — no fallback to
`accounts.extra.supported_protocols`. `/health` is drain-only. CutoverReady is
publication-completeness, not traffic admission. Missing/illegal capability stays
fail-closed at schedule/execute without 503'ing the process.

`accounts.extra.supported_protocols` is a one-window write-only rollback projection
for the previous image; after the window, deleting it is a separate change.

## 10. Mechanical verification and approval

CI uses registry-derived route tests and mock upstreams. Preflight/sentinels fail when
handlers choose protocols, capability is read from legacy account fields, identity is
rebuilt outside its owner, or `ForwardAs*` bypasses the router.

Approval of this document authorizes planning only — not production migration, probes,
deploy, or merge.

high-risk-anchor: protocol-routing-ssot
