---
title: Protocol Routing Single Source of Truth
status: approved
approved_by: "feng (conversation approval, 2026-08-27)"
authors: [codex]
created: 2026-08-24
revised: 2026-08-30
revision_note: >
  Plans and Execute no longer compare account or capability revision tokens.
  Send-time freshness is authoritative reload plus route-fact equivalence.
  /health follows process TrafficReady, not one account's missing route.
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

Endpoint resolution must reproduce the identity used to obtain the capability
key. A configurable endpoint with an empty or mismatched URL never falls back
to an official host.

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

Planning and pre-send validation failures perform no network I/O. Upstream
failures retain existing cooldown and account-failover classification.

## 7. Capability discovery and re-probe

### 7.1 Coordination unit and triggers

Probe coordination is endpoint-scoped, never account-scoped. The logical job
key is `capability_key + probe_generation`. A database lease and
compare-and-swap write ensure that concurrent instances do not probe or
overwrite the same generation twice.

A probe may be requested only when:

- a new capability key is first created and has neither a conclusive probe nor
  an allowed official seed;
- an administrator explicitly re-probes an account or capability;
- structured runtime evidence indicates endpoint drift;
- a previously unusable witness becomes healthy and the shared capability is
  still empty, unprobed, or inconclusive.

Adding another account with an established key reuses the existing row and
does not probe. Credential rotation does not probe. There is no periodic
per-account scan.

A successful account connection/recovery test may enqueue the last trigger,
but it still creates at most one endpoint-scoped job. This makes an endpoint
whose first witnesses had broken authorization recover automatically after an
operator fixes one account, without turning account tests into a second
capability writer.

### 7.2 Witness selection

The coordinator chooses a witness account from all accounts linked to the key.
The deterministic order requires:

1. complete and non-expired authorization;
2. active and schedulable state;
3. no current account error;
4. existing priority order;
5. account ID as the final tie-breaker.

The exact production transport, wrapper, endpoint builder, routing headers,
and real resolved upstream models are reused. Within one witness and protocol,
the historical first representative model stays first; a deterministic,
deduplicated candidate set derived from concrete text `model_mapping` values is
capped by the compile-time `protocolProbeMaxModels` policy. The probe advances to the
next model only after model-specific evidence. Authentication, rate limit,
server, network, timeout, and malformed-response failures do not fan out across
models. A provider-owned relay profile may nominate one canonical representative
from its existing model SSOT; CloudWise Messages may try that representative
after its known model-routing 401, while a 401 from that representative remains
inconclusive. Alternate witnesses may be tried only after an inconclusive
account-specific failure and stop at one compile-time `maxWitnessAttempts`
policy owned by the coordinator. All model candidates within one witness share
that protocol attempt's existing timeout.

### 7.3 Verdicts

Each protocol observation is classified as:

- `positive`: a recognized protocol response proves endpoint support;
- `endpoint_negative`: structured provider evidence proves that the endpoint
  identity does not implement the protocol/path;
- `model_specific`: the protocol exists but the selected model or action is
  unsupported;
- `inconclusive`: authentication, authorization, rate limit, server, network,
  timeout, token exchange, malformed response, or ambiguous provider error.

Only `positive` adds a protocol. Only `endpoint_negative` removes it.
`model_specific` and `inconclusive` preserve existing membership. Raw 401/403,
429, 5xx, timeout, network error, or generic 404/405 never clears a shared
capability because it may describe the witness, model, project, or transient
service state rather than endpoint protocol support. A classified provider
model-routing response may select another bounded model candidate, but it still
cannot remove protocol membership.

One narrow managed-relay exception applies: for an exact TokenKey Antigravity
Edge relay stub, the canonical `No available accounts` capacity response is
`positive` endpoint evidence. That response is emitted only after the Edge
gateway accepted the concrete protocol path and request shape; it proves the
protocol endpoint while saying nothing about the current native OAuth/Kiro
pool. Arbitrary provider 429/5xx responses remain `inconclusive`.

If the same key obtains both conclusive positive and conclusive endpoint
negative evidence for one protocol, the protocol is removed from routing and
the row records `identity_conflict`. That protocol remains fail-closed until a
new probe or identity correction resolves the conflict. The system never
silently splits capability by account; a real difference means the identity
builder is missing a key dimension and must be fixed.

The coordinator atomically replaces the complete sorted set and evidence only
when both the leased `probe_generation` and the previously read `revision`
remain current. A route-relevant change increments `revision`; an evidence-only
refresh does not. Customer-request failures may enqueue a probe but cannot
directly mutate capability or retry a second protocol on the same account.

Real probes are runtime lifecycle operations. CI uses mocks and never calls
production upstreams.

## 8. Governed account profiles

Governance is determined by stable compile-time account shapes, not current
health. Once an account shape is governed, missing credentials, an empty
capability, or an illegal route makes it fail closed; it cannot escape to a
legacy route.

The governed profiles include:

- existing configurable text API-key/upstream/NewAPI account shapes with
  explicit endpoint identities;
- supported Antigravity OAuth accounts, mapped to the
  `antigravity_cloudcode` endpoint/request profile and probed through the real
  Code Assist wrapper;
- exact `IsNewAPIVertexServiceAccount()` accounts, mapped to the
  `vertex_service_account` profile and probed through the real JWT exchange,
  project/location resolver, and Vertex Gemini endpoint;
- exact TokenKey Antigravity edge-relay API-key stubs, treated as configurable
  text endpoints using their explicit relay URLs.

Arbitrary Antigravity API-key accounts and arbitrary service accounts are not
silently included. A service account is governed only when it matches the exact
supported Vertex shape. Adding another stable shape requires an explicit
identity/profile implementation and tests.

Official capability seeding is allowed only for an immutable compile-time
official endpoint profile whose host, protocol contract, and inability to be
administrator-overridden are all enforced by code. Platform or URL similarity
is never an official seed.

## 9. Account lifecycle and administrator surface

Account create/import/edit computes the canonical identity, upserts the shared
capability row by key, and atomically links the account. Token rotation keeps
the link. An identity-affecting edit links a new or existing key. No API accepts
`capability_key` or `supported_protocols` as administrator-written input.

The existing account action remains the human entry point:

```text
POST /api/v1/admin/accounts/:id/protocol-probe
```

Internally it resolves the account's capability key, acquires or joins the
shared job, selects a witness, and refreshes every linked account's projection.
The compatibility `account.supported_protocols` response field is derived from
the linked capability row, never read from account JSON. The response includes:

```json
{
  "account": {},
  "capability": {
    "capability_key": "...",
    "supported_protocols": ["responses"],
    "revision": 7,
    "last_probed_at": "2026-08-27T00:00:00Z",
    "affected_account_count": 3
  },
  "outcome": "updated",
  "reason": "positive_evidence"
}
```

Outcomes are `updated`, `unchanged`, `inconclusive`, or `not_applicable`.
Execution and persistence failures are non-2xx responses. The UI must not show
an inconclusive, unavailable, or failed probe as a successful capability
update.

Account pages show read-only shared capability chips, the shared-account count,
last probe time, and a re-probe action. They do not provide protocol editing,
ordering, forcing, or mode controls.

If a governed account currently links to an empty capability after upstream
authorization or scheduling is repaired, the operator may run the normal
account connection test or the explicit protocol re-probe. Both converge on
the same endpoint-scoped coordinator; only the latter is synchronous.

## 10. Migration, readiness, and rollback

### 10.1 Additive migration

Migration is idempotent and runs before the new image can receive traffic. It
has two phases with one explicit publication boundary.

The silent preparation phase:

1. create the capability table and account foreign key;
2. compute canonical identity for every governed account;
3. upsert one row per distinct key and link all matching accounts;
4. seed each row from the positive union of historical account-level
   `supported_protocols` values for that key;
5. treat missing or empty historical values as no evidence, never as negative;
6. probe each distinct unverified key once, subject to witness availability;
7. evaluate preliminary readiness from the new table only.

Silent preparation may create capability rows, account links, leases, and probe
evidence. It must not change `accounts.extra.supported_protocols`, account
business revision timestamps, or scheduler outbox state. A failed candidate may
therefore leave reusable preparation facts without changing anything consumed
by the previous image.

Only after preliminary readiness succeeds, one transaction publishes the
release boundary:

1. write the complete rollback projection to every linked governed account
   whose existing projection differs;
2. advance the affected account revisions;
3. enqueue scheduler invalidation for the affected accounts;
4. reload and evaluate final readiness without creating or repairing links;
5. commit all effects together only when final readiness succeeds, or roll all
   legacy-visible effects back together.

Final readiness runs inside the publication transaction and sees its
uncommitted projection/outbox writes. It is read-only with respect to
capability/link facts: a concurrently introduced missing or mismatched link
fails CutoverReady instead of being repaired after publication. Only that
final successful evaluation may commit the transaction. `/health` follows
process TrafficReady independently and does not wait for publication.
Repeating publication with already-matching projections is a no-op: it
does not advance account revisions or enqueue duplicate scheduler events.

Historical union is a migration seed, not permanent evidence ownership. Once
an accepted probe result has been persisted, migration never merges historical
account projections into that capability again. A conclusive endpoint-negative
observation may therefore remove a seeded protocol even when another protocol
in the same generation remains inconclusive.

### 10.2 Cutover rule

New application code always reads the capability table. It has no fallback to
`accounts.extra.supported_protocols`, even during migration. Preparation,
probing, and hard routing cutover may ship in one image because readiness is the
traffic-admission boundary.

The image reports `/health` as `503 not_ready` only for a process-wide
blocker: drain, missing router, or startup evaluation abort. An individual
active, schedulable, governed account that:

- lacks a valid capability link;
- has neither completed initial probing nor an allowed official seed;
- is linked to an identity conflict relevant to its served routes;
- has no legal native or convertible route for its served models;
- fails its independent authorization/schedulability gate;

is recorded as remediation, stays fail-closed at schedule and execute, and
does not 503 the process. CutoverReady remains the publication-completeness
signal; it is not the traffic-admission boundary.

Disabled or deliberately unschedulable accounts do not block release
publication. They remain fail-closed if re-enabled before their identity,
capability, and account gates are valid.

The candidate image never serves customer traffic through legacy routing. A
failed preparation, probe, preliminary readiness, publication, or final
readiness check keeps the previous image serving. In those failure cases the
previous image continues to observe the same legacy projection, account
revision, and scheduler snapshots that existed before the candidate started.

### 10.3 Rollback projection

For one release rollback window,
`accounts.extra.supported_protocols` is a write-only projection of the linked
capability set for the previous image. New routing, scheduling, APIs, and UI do
not read it. Normal post-cutover capability changes and their linked rollback
projections commit in one database transaction. During candidate startup,
capability preparation is intentionally durable but unpublished; the complete
projection, account revision advances, and scheduler invalidations are instead
published together only after preliminary readiness succeeds. A publication
failure rolls back every legacy-visible effect and blocks readiness because an
image rollback must not restore divergent account facts.

After the rollback window, deleting the legacy field, projection writer, and
compatibility code is a separate reviewed change. The shared capability table
remains the permanent SSOT.

No production schema application, external probe, deployment, or data mutation
is authorized by approval of this document alone.

## 11. Mechanical verification

CI derives exhaustive route behavior from the immutable registry and uses mock
upstreams. It does not enumerate production accounts, base URLs, or
credentials, and it does not call external providers.

Required tests include:

- canonical identity normalization and stable key generation;
- same-key account sharing and unique-row upsert under concurrency;
- token rotation retaining the key and avoiding a probe;
- identity-affecting configuration creating or reusing a different key;
- empty capability fail-closed behavior;
- deterministic witness selection and bounded alternate-witness fallback;
- 401/403, 429, 5xx, timeout, and network failures not mutating shared facts;
- exact TokenKey Antigravity Edge relay capacity responses adding endpoint
  facts while arbitrary 429/5xx responses remain inconclusive;
- positive addition, structured endpoint-negative removal, and model-specific
  preservation;
- positive/negative identity conflict failing closed;
- lease, generation, and compare-and-swap race handling;
- migration positive union and empty-as-no-evidence semantics;
- silent candidate preparation preserving legacy projection, account revision,
  and scheduler outbox state when readiness fails;
- atomic release publication of all rollback projections, account revisions,
  and scheduler invalidations only after preliminary readiness succeeds;
- idempotent publication skipping unchanged projections, revisions, and
  scheduler invalidations;
- publication failure rolling back every legacy-visible effect;
- final-readiness failure rolling back the publication transaction, with final
  validation unable to repair capability links;
- failed-candidate restart reusing completed endpoint probes without exposing
  partial publication;
- identity-first and fixed conversion order;
- model, feature, Responses-path, endpoint, adapter, and transport constraints;
- plan capture and inequivalent-route rejection before network I/O;
- exact outbound host/path/body/credential boundary and response shape;
- immutable request behavior across account failover;
- account-level authorization gates remaining independent of shared capability;
- readiness failure for missing links, unresolved initial capability, conflict,
  no legal path, broken account gates, or failed rollback projection;
- administrator re-probe returning key, affected-account count, and honest
  outcome;
- Antigravity, exact Vertex service-account, and edge-relay profile boundaries.

A small independent policy-contract test asserts only the product invariants:
the governed protocol identifiers, identity-first behavior, and fixed fallback
order. Detailed adapter/model/feature cases remain registry-derived so CI does
not become a second route matrix.

The project preflight adds syntax-aware guards and sentinels that fail when:

- a production handler, compatibility helper, or transport chooses a protocol
  or fallback;
- capability is read from an account legacy field or probe evidence;
- endpoint identity is rebuilt outside its owner;
- `ForwardAs*` bypasses the router execution boundary;
- platform-derived custom endpoints return;
- the capability owner, router owner, or their load-bearing tests disappear.

API/service integration tests are not described as e2e. The read-only admin
projection and re-probe journey use the repository's normal UI test level.

## 12. Observability

Logs and metrics record the capability key prefix, capability revision,
selected witness account ID, probe generation/outcome/reason, affected-account
count, selected inbound/target protocol, endpoint profile, and route failure
reason. They never record credentials or authentication headers.

Operational views distinguish:

- no endpoint capability;
- no legal conversion for the concrete request/model;
- account authorization or health rejection;
- endpoint identity conflict;
- probe inconclusive or lease contention;
- stale plan/capability rejection.

These distinctions are diagnostics only. They do not create alternate routing
or capability state.

## 13. Acceptance criteria

- One canonical endpoint identity produces one unique capability row.
- All accounts with the same key observe the same sorted native protocol set.
- Adding a same-key account or rotating credentials does not trigger redundant
  capability probing.
- Adding a new key, explicit re-probe, or structured endpoint drift can trigger
  exactly one coordinated endpoint probe generation.
- Account authorization, balance, limit, cooldown, and health remain
  independent hard gates and cannot mutate shared endpoint capability.
- Empty capability and identity conflict are fail-closed; there is no persisted
  `unknown` or legacy-routing escape.
- Every governed request receives one deterministic legal plan or fails before
  network I/O.
- The plan selected during scheduling is the exact immutable plan executed.
- No configurable endpoint can default to an official host, and credentials are
  attached only after endpoint identity validation.
- Handlers, compatibility helpers, transports, and forwarding methods contain
  no independent route, endpoint, or fallback decision.
- Antigravity OAuth and exact Vertex service-account profiles are governed;
  arbitrary service accounts remain outside the boundary until explicitly
  supported.
- Administrator re-probe is account-addressed but endpoint-scoped and reports
  the shared key, affected accounts, and an honest result.
- Migration probes each distinct key rather than each account, and positive
  historical union cannot turn empty/absent values into negative evidence.
- Candidate preparation and probing can persist new-table facts but cannot
  change the legacy projection, account revision, or scheduler outbox before
  preliminary readiness succeeds.
- A successful candidate publishes rollback projections, account revisions,
  and scheduler invalidations in one transaction after CutoverReady.
- A failed candidate can be retried from persisted preparation facts while the
  previous image continues to observe its pre-candidate routing state.
- The new image reads only the capability table. `/health` admits traffic when
  the process can serve. An account without a legal route fails closed at
  selection or execute and does not 503 the machine.
- The account legacy field exists only as a one-window rollback projection and
  is never a new-version routing input.
- CI uses mock upstreams and mechanically rejects competing protocol owners.
- An upstream merge that restores account-level capability ownership or a
  parallel routing decision path fails preflight.

## 14. Approval boundary

Approval authorizes implementation planning only. Implementation, production
migration, external probes, deployment, PR merge, and release retain their
normal review and approval gates.

high-risk-anchor: protocol-routing-ssot
