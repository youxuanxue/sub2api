---
title: Protocol Routing Single Source of Truth
status: approved
approved_by: "feng (conversation approval, 2026-08-26)"
authors: [codex]
created: 2026-08-24
related_stories: []
---

# Protocol Routing Single Source of Truth

## 1. Problem

TokenKey currently decides an upstream text protocol in several places: handler
fallbacks, platform-derived endpoints, engine bridge capabilities,
`openai_compat` helpers, account legacy fields, and `ForwardAs*` methods. These
decisions can disagree. A valid credential can then be sent through the wrong
wire protocol or to the wrong host, and an upstream merge can restore an old
branch without breaking compilation.

The fix is one protocol-routing decision owner. It must decide whether an
account has a legal path for the concrete request before scheduling or network
I/O. Existing transports and converters remain reusable implementation details;
they stop choosing routes.

This is a high-risk design because it changes the core inference path,
credential destination selection, and gateway ownership boundary.

## 2. Scope

This design covers these customer-facing and upstream text wire protocols:

- `messages`
- `chat_completions`
- `responses`
- `gemini_generate_content`

`gemini_generate_content` means Gemini `generateContent` and
`streamGenerateContent`; it does not encode a Google product, host, credential
type, or account class. The protocol may resolve to an Antigravity Code Assist
profile or an exact NewAPI Vertex service-account profile.

TokenKey's exact Antigravity edge-relay API-key stubs remain configurable text
upstreams rather than Code Assist profiles. They are governed through their
explicit relay base URL and the ordinary Messages, Chat Completions, and
Responses probes; capability is never copied from the native OAuth account on
the remote edge.

The design includes native forwarding and explicitly registered conversions
among these protocols, including protocol-specific Responses path variants.
Only text generation is governed. Embeddings, image/video/audio generation,
OCR, rerank, and Gemini multimodal identity remain outside this router until
their own capability owner is designed. One ingress scope predicate owns that
boundary; handlers cannot independently choose between this router and legacy
text routing.

This design does not introduce a universal protocol graph, a universal
intermediate representation, administrator route ordering, or adaptive/fixed
modes.

## 3. Canonical account fact

The canonical persisted account field is:

```json
{
  "supported_protocols": [
    "messages",
    "chat_completions",
    "responses",
    "gemini_generate_content"
  ]
}
```

It is stored at `accounts.extra.supported_protocols`; no database column is
added. It means only that the account can natively receive those wire
protocols. It is coarse endpoint capability, not proof that every mapped model
or every request feature is legal on that protocol.

Semantics are binary:

- present: the protocol may be considered during planning;
- absent: the protocol is ineligible.

There is no persisted `unknown`, `preferred_protocols`, `forced_protocol`, or
`adaptive` state. An empty or missing set is fail-closed and makes the account
ineligible for requests governed by this router.

One repository writer validates protocol identifiers, removes duplicates,
sorts deterministically, and atomically replaces the value. Production code
must not write the JSON key directly or reconstruct this fact from legacy
fields.

## 4. One decision owner

The focused owner is:

```text
backend/internal/engine/protocolrouter
```

It is a sub-owner of the existing engine boundary. It owns:

- the four protocol identifiers;
- the fixed ordered route registry;
- route-level model and request-feature constraints;
- explicit endpoint resolution;
- protocol route planning;
- the only execution entry that can reach text-protocol transports.

The application constructs one router with an immutable adapter catalog at the
composition root:

```go
router := protocolrouter.New(adapterCatalog)
```

Request-time code has deliberately two calls:

```go
plan, err := router.Plan(request, accountSnapshot)
result, err := router.Execute(ctx, plan, request)
```

`Plan` is pure and performs no network I/O. `Execute` follows the supplied
immutable plan; it does not select, improve, or replace the route.
`protocolrouter` owns the adapter interfaces; existing implementations satisfy
those interfaces and are wired at the composition root, preserving the current
engine-to-service dependency direction. The catalog cannot be mutated after
construction; there is no package-global adapter registration surface.

## 5. Existing-owner migration

The new module replaces existing route decisions rather than becoming another
registry beside them.

| Existing surface | Responsibility after migration |
| --- | --- |
| `backend/internal/engine/capability.go` | Text entries move into the router or become transport-availability data consumed by it. They no longer decide a text route. |
| `backend/internal/handler/endpoint.go` | Keeps inbound path normalization. Platform-derived upstream text endpoint selection is removed. |
| `backend/internal/pkg/openai_compat` | Keeps parsers, converters, and probe-verdict helpers. It cannot choose a target protocol or fallback. |
| `ForwardAs*` methods | Become internal transport/converter adapters reachable only through `Execute`. |
| NewAPI bridge checks | Become planner constraints describing whether a transport/adaptor exists for the selected route. |
| Gemini/Antigravity/Vertex compatibility services | Keep pure conversion, auth, streaming, and transport details behind registered adapters. They cannot choose a protocol, account, endpoint, or fallback. |
| Gateway handlers | Parse and normalize the inbound request, then consume scheduler/router results. They cannot select protocol, endpoint, converter, or fallback. |

Direct production calls that bypass `Plan` or `Execute` are removed or
mechanically rejected.

## 6. Canonical request and model-aware legality

Ingress validates the original wire body once and constructs one immutable
`CanonicalRequest`. This is a wrapper around the inbound protocol body, not a
cross-protocol intermediate representation.

```go
type CanonicalRequest struct {
    // Private fields; constructed through the validated ingress boundary.
    inboundProtocol Protocol
    requestedModel  string
    responsesPath   ResponsesPathKind
    profile         RequestProfile
    body            []byte
    digest          RequestDigest
}

type RequestProfile struct {
    Stream       bool
    ToolChoice   ToolChoiceKind
    Continuation ContinuationKind
    Reasoning    ReasoningKind
    PromptCache  PromptCacheKind
    ContentKinds ContentKindSet
}
```

The constructor defensively copies the body. Accessors cannot expose mutable
storage. The digest covers the protocol, requested model, Responses path,
profile, and body. The profile uses typed semantic variants, not presence
booleans, so it can distinguish cases such as automatic, required, and named
tool choice; continuation forms; reasoning shapes; cache placement; and
supported content block kinds. A route constraint may inspect the validated
protocol-specific request when the typed profile alone is insufficient.

`ResponsesPath` distinguishes the root endpoint and supported subresources such
as compact or input-token operations. A route is not legal for a path variant
unless its registry entry explicitly permits that variant.

Gemini ingress normalizes `generateContent` to `Stream=false` and
`streamGenerateContent` to `Stream=true`. No second Gemini-action field is
stored: for the two in-scope actions, `RequestProfile.Stream` is the canonical
semantic fact. The selected target profile may still use a streaming upstream
for a non-streaming customer request when its registered adapter performs the
aggregation.

`Plan` resolves the account-specific upstream model through the existing model
mapping owner. The resulting plan records the request digest, resolved model,
and the account snapshot revision. That revision covers every account fact used
by planning, including credentials, endpoint configuration,
`supported_protocols`, and model-mapping inputs.

For one account, a route is legal only when all conditions hold:

```text
target protocol is in account.supported_protocols
AND an ordered route entry exists
AND model policy permits the resolved upstream model
AND the adapter preserves the request features
AND the endpoint resolves explicitly
AND the required transport/adaptor exists
```

Model policies and feature constraints are route data owned by the router. When
they depend on an existing model catalog or account configuration owner, the
route references that owner rather than copying its lists. Missing required
policy, endpoint, route-adapter, or transport data makes the route illegal.

An official provider host may be an explicit default only when the account
matches a compile-time `OfficialEndpointProfile`: a fixed built-in account type
whose endpoint cannot be overridden by an administrator. Accounts with a
configurable base URL, including API-key, upstream, and NewAPI accounts, are
always custom and must provide a valid explicit URL even when they target an
official provider. An empty or unresolved custom URL never defaults to an
official host.

For `gemini_generate_content`, the route resolves exactly one compile-time
endpoint/auth profile from the immutable account snapshot:

- `antigravity_cloudcode`: supported Antigravity OAuth account shape; endpoint,
  wrapper, OAuth token, CLI fingerprint, headers, proxy, and project binding are
  supplied by the existing Code Assist owners;
- `vertex_service_account`: exact `IsNewAPIVertexServiceAccount()` shape;
  project, per-model location, service-account JWT exchange, fixed Google token
  endpoint, proxy, and model/action URL are supplied by the existing Vertex
  owners.

An exact `tkIsAntigravityEdgeRelayStub()` is not assigned either Gemini
profile. It remains a configurable upstream whose three ordinary text
endpoints are planned from its explicit base URL and probed per relay account.

Profiles are not persisted administrator configuration and are not a second
capability SSOT. If the profile is ambiguous or cannot resolve its required
facts, the account remains governed but the concrete route is illegal.

## 7. Fixed route registry

Identity is always tried first. If identity is illegal, conversions are tried
in this fixed order:

| Inbound protocol | Conversion targets |
| --- | --- |
| `messages` | `responses`, then `chat_completions`, then `gemini_generate_content` |
| `chat_completions` | `responses`, then `messages`, then `gemini_generate_content` |
| `responses` | `chat_completions`, then `messages`, then `gemini_generate_content` |
| `gemini_generate_content` | identity only |

The first legal entry wins. There is no score, quality weight, account-level
preference, tie-breaker, or graph search.

Each registry entry names its target protocol, allowed Responses path kinds,
model policy, feature constraints, endpoint/profile resolver, one
`RouteAdapterID`, and one transport. `Execute` invokes that registered route
adapter once. The adapter may internally compose existing pure conversion
stages, but the composition is an implementation detail covered by that route
entry's end-to-end mock test; it cannot select another target protocol,
profile, endpoint, or fallback.

Conversion routes are deny-by-default. A route opens only semantics proven
lossless by its adapter contract tests; unsupported tools, continuation,
reasoning, cache, multimodal content, and path variants fail during `Plan`.
This rule lives in registry constraints and tests, not in a second prose
capability matrix or handler switch.

## 8. Scheduling and execution

Protocol legality is a scheduler hard gate, not a ranking signal:

```text
construct the immutable CanonicalRequest
  -> Plan for every candidate account and resolve its upstream model
  -> discard accounts with no legal plan
  -> run existing priority/sticky/capacity/cooldown ordering unchanged
  -> Execute the selected account's already-created plan
```

The selected plan and the same `CanonicalRequest` are passed unchanged to
`Execute`; no second route decision is allowed. The plan contains the request
digest, account snapshot revision, resolved model, protocols, endpoint, route
adapter, transport, route kind, and reason. Before creating a network request,
`Execute` verifies both digests/revisions. A different request, stale account
snapshot, or missing credential fails closed and enters normal account failover.

Execution invariants:

- the `CanonicalRequest` is immutable across account attempts;
- one registered route adapter is invoked per attempt; planner and handler
  cannot start a second conversion or fallback;
- credentials are attached only after endpoint validation;
- transports cannot change protocol or endpoint;
- no handler or transport retries a second protocol on the same account within
  the same customer request;
- usage, billing, QA, and error records use the actual protocol and endpoint
  from the plan.

Planning failure and pre-send validation failure cause no network I/O. Upstream
failures retain existing classification, cooldown, and account failover rules.

## 9. Probe contract

Real probes are runtime account-lifecycle operations, not CI tests. They run on
account onboarding, capability-affecting configuration changes, explicit
re-test, or a scheduled response to observed protocol drift.

One probe job evaluates a candidate protocol/path set for one account and
configuration revision. Its inputs include the exact credential binding,
normalized base URL, and a real resolved upstream model. For each protocol, a
conclusive positive verdict adds it and a conclusive endpoint-negative verdict
removes it; an inconclusive or model-specific rejection preserves its prior
membership. Model compatibility remains a planner policy, not account endpoint
capability. The coordinator atomically writes the resulting complete set only
if the configuration revision is still current. A new account with no
conclusive positive verdict remains empty and ineligible.

The protocols inside one account job may be probed concurrently, but the job
still has one account identity, one configuration revision, and one atomic
complete-set write. Startup preparation bounds account-level concurrency so a
release cannot create an unbounded upstream burst.

Results are never copied between accounts, even when base URLs match. The probe
coordinator may coalesce only identical concurrent jobs; its key includes
account identity and configuration revision, so coalescing suppresses duplicate
work for the same account rather than turning one account's result into another
account's fact.

A release smoke may exercise each distinct normalized base URL once to reduce
operational cost. That smoke is endpoint evidence only: it does not write or
backfill any account's `supported_protocols`.

A customer-request failure may enqueue a probe but cannot directly mutate the
field or try another protocol on the same account.

Gemini candidates are provider-specific and never use a generic OpenAI probe:

- `antigravity_cloudcode` reuses the production Code Assist wrapper, OAuth
  token, project binding, CLI fingerprint, headers, proxy, endpoint builder,
  and streaming parser;
- `vertex_service_account` reuses the production service-account parser, fixed
  Google token endpoint, token cache, project/per-model location resolver,
  Vertex URL builder, proxy, and Gemini response parser.

Exact Antigravity edge-relay API-key stubs use the same generic three-protocol
probe path as other configurable text upstreams. Each relay row is probed and
persisted independently; its result is not inferred from another relay sharing
an edge or from the native OAuth pool behind that edge.

A 2xx response is positive only when it parses as a recognized Gemini response
envelope (including the Antigravity `response` wrapper); a generic JSON or
error envelope is not positive evidence. Authentication, rate-limit, server,
timeout, token-exchange, network, and malformed-response failures are
inconclusive and preserve the prior fact. A Gemini 404/405 is
`endpoint_negative` only when a provider-specific reason proves that the
protocol method/path is unsupported; project, location, model, or action
failures are `model_specific` or inconclusive. Raw status codes alone never
remove `gemini_generate_content`.

## 10. Conservative migration

Migration is additive and idempotent.

The governance predicate identifies stable account classes, not current route
health. It includes existing governed text accounts, supported Antigravity
OAuth account shapes, exact Antigravity edge-relay API-key stubs, and exact
NewAPI Vertex service-account shapes. Other Antigravity API-key accounts,
service accounts, and non-text-only accounts remain outside this text router.
Once an account class is governed, missing credentials, unresolved profiles,
missing probe models, empty `supported_protocols`, or illegal model/features do
not make it ungoverned: they produce no legal route and therefore fail closed.

- Only accounts matching a compile-time `OfficialEndpointProfile` may be seeded
  from the router registry. The same predicate controls official endpoint
  defaults, so migration and runtime cannot disagree.
- For custom or third-party upstreams, `api_protocol`, `adaptive`, configured
  URLs, platform, account type, channel type, and legacy capability fields are
  probe hints only. None is proof of native support.
- Ambiguous existing API-key, upstream, or NewAPI accounts are never inferred to
  be official. Migration reports them for an explicit base URL and per-account
  probe instead of guessing a provider host.
- Custom accounts are populated only by conclusive per-account probes or retain
  an already verified canonical value.
- Every active account governed by this router must have at least one legal
  route for its served models before the candidate release may receive traffic;
  otherwise release readiness fails and the account is reported for
  remediation. Once that image receives traffic, an account cannot escape to
  legacy routing by becoming "ungoverned" because configuration is incomplete.

After cutover, production text routing reads only
`accounts.extra.supported_protocols` for account-level native capability. Legacy
fields remain for one rollback window but are not router inputs. Their eventual
removal is a separate reviewed change.

## 11. Admin surface

Account create/edit surfaces show `supported_protocols` as read-only capability
chips and provide the existing test/re-probe action. An empty set is displayed
as no usable text protocol detected and the account is excluded from the
corresponding candidate pools.

The synchronous re-probe API returns the refreshed account plus one explicit
outcome: `updated`, `unchanged`, or `not_applicable`. Internal execution or
persistence errors are non-2xx failures. The UI must not present `unchanged`,
`not_applicable`, a missing probe runner, or an internal failure as a successful
capability update.

The UI and API do not add protocol ordering, `preferred_protocols`,
`forced_protocol`, or adaptive/fixed controls.

## 12. Mechanical verification

CI derives deterministic cases from the route registry and uses mock upstreams.
It neither enumerates production accounts or base URLs nor calls external
providers.

Required behavior coverage includes:

- identity and conversion selection in the fixed order;
- resolved-model allow and deny cases;
- streaming, tools, continuation, reasoning, cache, multimodal, and Responses
  path constraints;
- no-route, missing endpoint, stale plan, and missing transport failures before
  network I/O;
- exact outbound host, path, body shape, credentials boundary, streaming
  events, and response shape;
- immutable `CanonicalRequest` across failover;
- route facts propagated to usage, billing, QA, and errors;
- deterministic account-field writes and conservative migration.

Registry self-tests reject entries with missing policies, endpoint resolvers,
adapters, or transports. Tests derive route cases from the registry instead of
maintaining a second route matrix.

A separate, deliberately small policy-contract test does not derive its
expectations from the registry. It asserts only the product invariants: the
four protocol identifiers, identity-first behavior, and the fixed fallback
order for each inbound protocol. Exhaustive adapter/model/feature cases remain
registry-derived, so there is no second implementation matrix.

Gemini contract tests cover both endpoint/auth profiles, positive and
non-destructive failure verdicts, exact host/path/credential binding, streaming
and non-streaming envelopes, and rejection of unsupported request semantics
before network I/O.

Endpoint contract tests also prove both sides of the identity boundary: a fixed
`OfficialEndpointProfile` may resolve its registered host, while every
configurable or ambiguous account with an empty URL fails before credential
binding or network construction.

The project preflight and CI add a syntax-aware SSOT guard plus existing
sentinel/contract checks. They fail when production handlers choose a protocol
or endpoint, legacy fields re-enter routing, `ForwardAs*` is called outside the
execution boundary, platform-derived text endpoints return, or the owner and
its core behavioral tests are removed.

These API-only checks are unit and service-integration tests, not UI e2e tests.
The read-only admin projection is covered at its normal UI-test level when
implemented.

## 13. Rollout and rollback

Rollout order:

1. The release image starts with the canonical field writer, registry, probes,
   router, scheduler hard gate, execution boundary, admin projection, and
   mechanical guard present together.
2. Before constructing text gateway handlers, the image seeds eligible official
   profiles, runs bounded per-account probes for every remediated custom,
   Antigravity, and Vertex account, and reruns the migration report against the
   persisted results. Platform, type, base URL, project, or provider similarity
   never seeds or copies a positive Gemini fact.
3. The candidate image constructs and injects the router unconditionally, but
   only `CutoverReady=true` makes the image ready for customer traffic. If any
   governed account remains unresolved, readiness fails and deployment keeps or
   restores the previous application image. The candidate image must never
   serve customer traffic through its legacy routing path.
4. Smoke each distinct normalized base URL and monitor no-route failures,
   selected protocols, actual hosts/paths, and failover.

Preparation and hard cutover may therefore ship in the same image. The
readiness result is a deployment traffic-admission gate, not a runtime switch
between the router and legacy routing.

Rollback is an application-image rollback. The additive JSON value and legacy
fields remain, so rollback requires no destructive data change. No production
migration, deployment, or external probe is authorized by approval of this
document.

## 14. Acceptance criteria

- Every governed customer request either receives one deterministic legal plan
  or fails before network I/O.
- Account eligibility includes native and convertible paths and is evaluated
  against the resolved upstream model, request features, Responses path, exact
  endpoint, and available transport.
- The scheduler uses legality only as a hard gate; existing business ordering
  is unchanged.
- The plan selected during scheduling is the same immutable plan executed.
- Planning and execution consume the same immutable `CanonicalRequest`, and
  `Execute` rejects a request whose digest differs from the plan.
- No unresolved custom endpoint can become an official provider host, and no
  credential is attached before endpoint validation.
- Handlers, compatibility helpers, transports, and legacy forwarding methods
  contain no independent route or fallback decision.
- `accounts.extra.supported_protocols` is the only account-level native
  protocol capability read by production text routing after cutover.
- A governed Antigravity or exact Vertex service account with incomplete auth,
  profile, model, or probe facts has no legal plan and cannot bypass the hard
  gate through the legacy route.
- Exact Antigravity edge-relay stubs are governed as configurable upstreams and
  keep per-account three-protocol evidence; arbitrary Antigravity API-key URLs
  remain outside the stable governed shape.
- Gemini probes use production provider identity and remove a prior capability
  only after a provider-specific endpoint-negative conclusion; raw 404/405 does
  not clear the fact.
- The admin re-probe action reports an explicit outcome and never displays an
  unexecuted, inconclusive, or failed probe as an updated capability.
- CI tests the registry with mocks; real capability probes persist facts per
  account; per-base-URL smoke never copies capability between accounts.
- A same-image rollout performs seed → per-account probe → revalidation before
  admitting customer traffic. A release that is not cutover-ready fails
  readiness and rolls back at the application-image boundary; it never serves
  through the candidate image's legacy routing path.
- An independent policy-contract test protects identity-first and the fixed
  fallback order without duplicating the registry's adapter matrix.
- An upstream merge that restores a competing decision path or removes the
  owner fails preflight.
- Gemini text `generateContent` is governed as the fourth protocol. Embeddings,
  images, video, audio, OCR, rerank, and Gemini multimodal identity are
  unchanged by this design.

## 15. Approval boundary

Approval authorizes implementation planning only. Implementation, production
migration, deployment, merge, and external probes retain their normal review
and approval gates.

high-risk-anchor: protocol-routing-ssot
