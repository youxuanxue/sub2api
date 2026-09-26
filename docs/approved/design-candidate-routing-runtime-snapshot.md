---
title: Candidate routing runtime snapshot and one-pass request path
status: approved
approved_by: "user (conversation approval: 同意。请乔布斯，拆解方案计划，编排 Agent 军团实现目标。开 pr)"
created: 2026-09-25
authors: [codex]
risk: high
related_docs:
  - candidate-eligibility-ssot.md
  - protocol-routing-ssot.md
  - universal-key-routing.md
  - qa-redaction-identified-formats-only.md
---

# Candidate routing runtime snapshot and one-pass request path

## 1. Decision

The gateway currently recomputes static account and request facts on every
request. Candidate selection reloads account membership and capability data,
request parsing is repeated across ingress and handlers, and QA capture scans
the same body through several redaction paths. The CPU cost is therefore a
shared data-flow problem rather than a collection of independent slow
functions.

The target design separates three data planes:

1. an immutable candidate read snapshot for authorization and static routing
   facts;
2. a runtime readiness view for capacity, quota, cooldown and health;
3. one immutable `protocolrouter.CanonicalRequest` for the whole attempt.

The snapshot is a materialized read model. It is not a new eligibility,
protocol, billing or security owner. Existing SSOT owners continue to decide
the outcome.

## 2. SSOT and ownership boundaries

| Fact or decision | Owner | Snapshot/request role |
| --- | --- | --- |
| Native endpoint capability | `protocol_endpoint_capabilities` | Read-only materialized copy keyed by capability identity |
| Model and converter legality | `protocolrouter.Router.Plan` | Must be evaluated for every final candidate; may use a bounded pure-result cache |
| Canonical request semantics | `protocolrouter.CanonicalRequest` | Constructed once at ingress and reused by candidate, handler and execution |
| Candidate static-fact read model | proposed `candidate_read_snapshot_tk.go` under the candidate owner, using scheduler outbox/cache delivery only | Materializes complete account, group and membership facts; never decides readiness, Plan legality or billing |
| Authorization paths and account selection | `candidate_request_tk.go`, `candidate_selection_tk.go` | Consumes authorized memberships and static account facts |
| Current readiness | scheduler, quota, cooldown, concurrency and health owners | Read at selection and rechecked before execution |
| Billing source and reservations | `candidate_billing_tk.go`, `CandidateRequest.bind` and existing billing owners | Never decided by the snapshot |
| Sticky identity and continuation | existing candidate identity owners | Snapshot data cannot bypass revalidation |
| QA record and session export | existing QA Bundle and trajectory owners | Redaction optimization cannot change blob/export ownership |

No second protocol graph, universal alias table, global billing cache or
parallel candidate eligibility service is introduced.

## 3. Candidate read snapshot

The read model is rebuilt from existing database and outbox facts by the
scheduler snapshot infrastructure and published atomically. It may be stored
in the existing scheduler cache and held behind an in-process immutable
pointer. The materializer must not store credentials or other secrets.

Conceptually:

```go
type CandidateReadSnapshot struct {
    ReadRevision   uint64 // read-model publication revision only
    SourceWatermark uint64
    PublishedAt    time.Time
    Complete       bool
    Accounts       map[int64]CandidateAccountFacts
    Capabilities   map[string]CandidateCapabilityFacts
    Groups         map[int64]CandidateGroupFacts
    Memberships    map[int64][]CandidateMembership
    Indexes        CandidateIndexes
}

type CandidateAccountFacts struct {
    ID               int64
    Platform         string
    CapabilityKey    string
    ModelMapping     ModelMappingProjection
    AccountGroupIDs  []int64
}

// CandidateCapabilityFacts is shared by all accounts with the same canonical
// capability identity. It is a complete non-secret projection of the endpoint
// inputs that protocolrouter.Plan needs; the snapshot does not define a second
// protocol representation or a second write path for these fields.
type CandidateCapabilityFacts struct {
    CapabilityKey        string
    NativeProtocols      []protocolrouter.Protocol
    EndpointIdentity     EndpointIdentityProjection
    EndpointPolicyDigest string
}

type CandidateGroupFacts struct {
    ID             int64
    Active         bool
    ModelAllowlist ModelAllowlist
    EndpointPolicy GroupEndpointPolicy
    DirectPolicy   DirectCandidatePolicyProjection
}
```

`CandidateAccountFacts` and `CandidateCapabilityFacts` together contain the
complete non-secret facts needed for safe prefiltering and Plan input
construction. Capability facts are shared by canonical capability key so the
snapshot does not copy endpoint data once per account. Both projections are
derived from the existing capability, endpoint and account-mapping owners; they
are not new protocol facts. Current credentials, proxy credentials, capacity,
quota, cooldown, transient health and billing state are loaded from their
authoritative owners when needed. Billing origins are never materialized in
this snapshot.

Indexes may narrow the candidate set by group, account membership, platform,
native protocol or other facts whose semantics already belong to an existing
owner. An index can never grant a route, grant authorization, select a billing
origin or replace `Plan`.

The read revision, source watermark and publication time are used for
observability and shadow comparison. They are not new stale-plan rules.
Protocol routing continues to follow the existing contract: execution reloads
the authoritative account and accepts the plan only when the fresh route facts
are equivalent.

This is a candidate read model, not a scheduler readiness snapshot. It must
retain authorized accounts whose current state is disabled, errored or
temporarily unschedulable so support and readiness remain separate. It may
reuse the scheduler cache transport and outbox delivery, but it has its own
materializer and bucket semantics; a scheduler bucket containing only
currently schedulable accounts cannot substitute for it.

The materializer publishes a complete generation only. If an outbox gap,
capability lookup failure or membership hydration error is detected, it keeps
the previous complete generation, marks the new generation unavailable and
emits a lag/degraded signal. It never publishes a partially populated account
or group map.

The single read owner exposes the following boundary:

```go
type CandidateReadSnapshotProvider interface {
    Snapshot(context.Context, []int64) (CandidateReadSnapshot, error)
}
```

The provider first reads a complete published snapshot. On a miss it may use
one bounded batch repository fallback for that request, subject to the existing
fallback limiter; it does not rebuild or publish a snapshot from the request
goroutine and it does not issue one query per group. An incomplete or invalid
snapshot is an error, never an authorization grant. The provider owns the
fallback metric and error classification; candidate selection does not invent a
second fallback path.

## 4. Request and selection flow

```text
HTTP / WS ingress
  -> PrepareCandidateIngress
  -> one CanonicalRequest + request digest
  -> authorized group span
  -> snapshot index prefilter
  -> Plan every final candidate
  -> independent runtime readiness checks
  -> candidate-owned ordering and billing bind
  -> slot acquisition / wait recheck
  -> reload selected account and revalidate group membership/authorization
  -> compare fresh route facts
  -> Execute(selected Plan)
```

The request state follows these transitions:

```text
Unprepared
  -> Prepared(request, authorized scope, snapshot handle)
  -> Planned(legal candidate paths)
  -> Bound(account, billing origin, reservation)
  -> Rechecked(fresh account, equivalent Plan, current slot)
  -> Executing
```

Every retry or wait consumer reuses the same immutable request digest and
original body. A changed authorization path, missing account, inactive group,
missing membership, non-equivalent route or failed billing rebind releases
acquired resources before reselection. The final check uses the authoritative
user/key scope, group and account repositories; the read snapshot cannot bypass
that check. “Authoritative” means the existing owner and its cache-backed
fresh-read path; it does not require a direct Postgres round trip on every
request. A direct repository fallback is reserved for cache misses or detected
drift and remains bounded.

The snapshot provider must expose bounded fallback behavior:

- a missing or incomplete snapshot cannot silently authorize a new path;
- a bounded repository/scheduler fallback may rebuild the read model or serve
  one request, with explicit metrics and rate limiting;
- outbox lag, fallback use and snapshot publication failures are observable;
- final execution always reloads the selected account.

## 5. Plan result reuse

The existing request-local plan cache remains the only plan cache in this
change and the correctness boundary. It already prevents repeated planning
within one candidate selection. A cross-request Plan cache is deliberately
out of scope: it would add a second invalidation problem before the snapshot
and route-equivalence work is measured in production.

If a later profile proves Plan construction is still a material hotspot, it
requires a separate design and approval. Its key would need every immutable
input that affects planning:

```text
canonical request digest
+ account model-mapping facts
+ capability identity
+ endpoint policy
+ native-only/conversion permission
```

Such a future cache may store success and failure, but never credentials,
runtime readiness or billing state. Capability `revision` may partition or
retire entries, but a revision change alone does not define route invalidity.
Regardless of cache shape, before network construction the authoritative
account is reloaded and the fresh plan is compared by capability key, resolved
model, endpoint, adapter and transport, as required by the protocol-routing
SSOT.

## 6. One canonical request parse

Ingress creates the existing `protocolrouter.CanonicalRequest` once. The typed
request profile contains stream, tool, thinking, continuation, reasoning,
content and Responses-path semantics. Handlers and converters consume that
object and the immutable body instead of independently rebuilding routing
metadata. Protocol-specific decoding that is required to execute a route may
remain at the handler or adapter boundary; `CanonicalRequest` is not a second
cross-protocol intermediate representation.

`WithRequestProfile` becomes an adapter to this canonical request owner. It must
not become a second routing parser or a cross-protocol intermediate
representation. Plan remains pure and network-free; Execute consumes the
selected immutable Plan and request.

The implementation must preserve Direct and Universal differences:

- Direct keeps its bound-group mapping behavior.
- Universal does not read group model aliases.
- `DirectPolicy` is a read-only projection for Direct path preparation only;
  Universal candidate evaluation ignores it.
- group platform labels do not select the execution platform.
- account mappings and protocol legality still flow through their existing
  owners.

## 7. QA redaction path

QA capture keeps the current Bundle, blob, session and export contracts. The
internal optimization is a one-pass result:

```text
bounded bytes
  -> format classifier
  -> one JSON/SSE/assignment/known-token pass
  -> RedactionResult
  -> existing blob writer
```

The result may be reused by request/response/upstream fields with the same
digest and the same redaction options within one capture. No process-wide cache
of redacted user content is introduced.

The implementation must preserve:

- `logredact-v4` metadata and existing blob shape;
- SSE framing and thinking-signature restoration;
- upstream divergence markers;
- known key-name and known-token coverage;
- the approved behavior that unknown formats are retained without the old
  regular-expression fallback.

Asynchronous writing can reduce contention, but it is not counted as a CPU
optimization unless the number of scans and parsed representations decreases.

## 8. Online impact and long-term constraints

This design is intended to reduce steady-state CPU and database pressure, but
its long-term risks are stale authorization, duplicated routing facts, memory
growth, cache drift and permanent fallback complexity. The following
constraints are part of the design, not operational advice:

| Long-term risk | Required design constraint | Residual online effect |
| --- | --- | --- |
| Revoked membership remains in a read model | Final authorization and membership fresh-read before bind/execute; invalidation events advance a monotonic watermark | A revoked path can remain in the prefilter briefly, but cannot execute |
| New authorization is missing from a lagging model | Complete generations only; bounded fallback on miss; lag alert and a hard freshness budget | A new grant may become usable slightly later |
| Capability or mapping facts drift | Source-owned projections, outbox gap detection, periodic full rebuild, route-fact equivalence before transport | A changed route may be rejected and fail over while the model catches up |
| A second protocol truth emerges | Shared capability projection keyed by canonical identity; no manual writes or policy logic in the snapshot | Snapshot rebuild remains tied to existing owners |
| Memory grows with memberships and duplicated endpoint data | Store IDs and shared capability facts; indexes contain references, not copied account objects; publish immutable generations and retire old ones promptly | Rebuilds may briefly use extra memory; steady-state size is bounded by account and membership cardinality |
| Snapshot outage becomes a database storm | One provider owns fallback, batch loads, singleflight and a rate budget; no caller-specific fallback | Some requests may receive the existing capacity/error response during degraded mode |
| Blue/green or rollback reads an incompatible cache | Versioned cache schema and generation keys; old binaries ignore unknown generations | Rollback may temporarily use the old database path |
| Degraded reads change customer-facing error semantics | Preserve the existing distinction between authorization, unsupported model, capacity and infrastructure errors; never turn a read failure into an empty entitlement set | Degraded mode may fail a request, but must not misclassify it as a client error |
| QA unknown-format passthrough becomes invisible privacy debt | Count unknown format bytes and records asynchronously, alert on drift, retain redaction version in every Bundle and keep lifecycle controls unchanged | Unknown formats remain an explicitly documented residual exposure — implemented, see §9.5 |

The snapshot is not an authorization cache with an independent TTL. Its
freshness is governed by source watermarks and invalidation coverage. A periodic
full rebuild is required even when the outbox is healthy; an outbox gap or
failed rebuild prevents publication of a new generation. The previous complete
generation may remain available only for prefiltering, never as proof of final
authorization.

The emergency repository fallback is not a second production path. It is one
bounded owner used for cache misses, degraded snapshots and rollback. Once the
snapshot has passed shadow acceptance, the old candidate query path must be
removed from normal traffic and retained only behind that owner. This prevents
two independently evolving eligibility implementations from becoming a
permanent maintenance burden.

Long-lived in-process snapshots must use immutable structural sharing and
prompt generation retirement. They must not retain request bodies, credentials,
redacted content or user-specific authorization results. Redis/cache entries
must use the existing internal access controls and a bounded retention policy.

## 9. Rollout and validation

Implementation is staged behind read-only shadow comparison:

1. build the snapshot and compare candidate facts and Plan outcomes with the
   current database path;
2. switch candidate reads to the snapshot with bounded fallback;
3. make `CanonicalRequest` the only routing parser;
4. measure Plan construction after the first two stages; propose a separate
   cache design only if it remains a material hotspot;
5. replace QA repeated scans with the one-pass internal result;
6. remove steady-state database fallback only after lag and drift metrics remain
   within the agreed budget.

Shadow comparison has an explicit exit condition. It is disabled after the
candidate, Plan, authorization and billing projections meet the acceptance
matrix; it is not a permanent duplicate production evaluator.

### 9.1 Measured cost slope and resequencing (2026-09-26)

The list above was written before the candidate path had a repeatable cost
measurement, so it ordered the snapshot first. `BenchmarkCandidateAdmissionCost*`
in `candidate_selection_cost_bench_tk_test.go` now measures the two dimensions
that actually grow, and they disagree with that order:

- growing the account x group fan-out at a fixed body *lowers* cost per
  evaluation (28.5us at 1x1 to 11.1us at 32x4), so per-candidate account reuse
  is already effective and a read model removes little CPU;
- growing the body at a fixed fan-out raises cost nearly linearly, because the
  per-route compatibility pass re-scanned one immutable body.

A single production profile cannot show this: `anthropicpolicy.Normalize` was
4.38% of samples on 2026-09-26 prod traffic but 64% of a 64KiB-body benchmark,
because the sampled window carried small bodies. Removing the repeated
validation (step 1 below) measured -37% to -55% per evaluation at 8KiB and above,
`p=0.002, n=6`.

The user therefore approved this order in the 2026-09-26 conversation, replacing
the sequence above without changing any ownership boundary in this document:

0. a repeatable CPU benchmark over (accounts x groups) and body bytes;
1. remove per-candidate repeated work over the immutable body;
2. make `CanonicalRequest` the only routing parser (was step 3);
3. re-decide the snapshot on PostgreSQL load and tail-latency evidence rather
   than on CPU profiles (was steps 1-2); build it only if those metrics justify
   it;
4. QA one-pass is demoted: regex is 1.39% of samples in total and `buildBlob`
   already runs on the async capture pool. Only the unknown-format byte/record
   accounting in section 8 remains required, as privacy debt.

The snapshot's approval boundary in section 10 is unchanged: it stays read-only
and shadow-only, and a production read-source switch still needs its own
acceptance evidence and release approval.

### 9.2 What step 2 turned out to be (2026-09-26)

Step 2 above was written as "make `CanonicalRequest` the only routing parser",
on the assumption that the duplicated parse sites were the remaining cost. The
post-step-1 profile disagreed. `pathContext` is already memoized per group by
`candidatePathContextPreparer`, so the repeated parse is bounded by group count,
not by fan-out; what dominated instead was `anthropicpolicy` walking the body
again on every route, which after step 1 was 43.6% of the benchmark and entirely
`gjson.parseSquash`.

Those reads depend only on the request bytes and the inbound protocol, both
immutable within one request, while `Capabilities` supplies the per-route half.
Splitting them (`anthropicpolicy.Facts` / `InspectValidated` /
`NormalizeWithFacts`, derived once in the canonical request constructor and only
for a body whose validity is proven) measured, against the step-1 baseline at
`n=6`:

- geomean `sec/op` -42.8%; 64KiB -69.6%, 256KiB -77.1%, 32x4 fan-out -42.4%,
  all `p=0.002`;
- `accounts_1/groups_1` *regressed* 6.5% (`p=0.000, n=10`), because a single
  evaluation pays the up-front derivation without amortizing it. This is the
  accepted trade: the fan-out the gateway actually serves is many accounts per
  group, and the 1x1 case is the cheapest absolute case anyway.

Re-measured after rebasing onto a later `main`, at `n=12`, the same comparison
gives geomean -37.9%, with 64KiB -66.8%, 256KiB -74.9% and 32x4 fan-out -36.0%
(`p=0.000`), and `bytes_1024` no longer separable from its baseline. Two runs of
the *identical* binary differed by geomean 7.3% on that host, individual cases up
to 13%, so any single-run figure below roughly 13% is not distinguishable there.
Read these numbers as: the large-body and high-fan-out gains are far outside the
noise and reproduce; the smallest cases are within it and should not be quoted as
either a gain or a loss. A regression check on this benchmark therefore needs
repeated runs, not one `count=6` pair.

After this change `gjson` does not appear in the admission profile at all; the
remaining samples are GC and scheduler. The parser-convergence work named in
section 6 therefore stands on its own correctness argument (one owner for routing
metadata), not on a CPU argument, and is no longer a performance step.

### 9.3 Production baseline on 1.8.259, and what it reordered (2026-09-26)

The earlier production profile was taken over a 40s window carrying 59 mostly
small requests, which is why `anthropicpolicy.Normalize` measured 4.38% there
while the same work was 64% of a 64KiB benchmark. A profile without a body-size
dimension cannot say which size bracket it represents, so the 1.8.259 capture
records both. Over 30 minutes of production traffic (2370 requests):
`input_tokens` p50 3263 / p90 12258 / p99 51159 / max 246954, i.e. roughly 13KiB
/ 49KiB / 205KiB of body at ~4 bytes per token. Inbound mix was
`/v1/chat/completions` 1698, `/v1/responses` 428, `/v1/messages` 330. The real
p90 therefore sits in the 49KiB bracket, where step 2 measured -66.8% — not in
the small-body bracket the earlier window suggested.

The 120s / 20.32s-sample profile on 1.8.259 then showed:

- `anthropicpolicy` is now `InspectValidated` at 5.12%, and it runs once per
  request rather than once per route, so it no longer amplifies with fan-out;
- `gjson.parseSquash` remains the top flat cost at 12.70%, but 60.5% of it is
  that single per-request derivation. The rest is a long tail of roughly twenty
  independent functions each walking the same body again (about 0.4s of 20.32s);
- `cursorProtocolContentSupported` rose from 7.17% to 8.56% and became the
  largest business hotspot — not a regression, but the share left standing once
  the surrounding work was removed.

Two things this changes. First, parser convergence gains a second argument
beyond correctness: with no single owner for "parse this body once", the
scattered read sites are a structural source of long-tail CPU. It stays behind
the Cursor work at about 2% of samples. Second, and more important for section
10, CPU is not the tail-latency story. `gateway_latency_ms` correlates weakly
with body size — a 460-token request measured 457ms while a 217k-token request
measured 461ms — and the >64KiB bucket shows p50 55ms against p95 380ms, a
sevenfold spread inside one size bucket. Weak size correlation plus large
within-bucket spread is the signature of waiting, not computing, and the whole
profile accounts for only 16.93% of wall time. Any work on that tail needs its
own plan measured in `gateway_latency_ms` percentiles; it is not covered by this
document and must not be folded into it.

### 9.4 Cursor content admission: one projection per body (2026-09-26)

`cursorProtocolContentSupported` was cached on `{digest, resolvedModel}`, and
`resolvedModel` comes from each account's own model mapping, so one request
evaluated against accounts mapping to different upstream models re-ran the
entire projection per distinct model. The production profile showed 1.75s
entering the cache and 1.74s (99.4%) passing through it, spent on
`ResponsesToAnthropicRequest` (43.1%), `cursor.ValidateMessagesContent` (19.0%),
`adaptResponsesClientToolsForAnthropic` (17.2%) and the `json.Unmarshal` /
`json.Marshal` pair — full codec work, not `gjson` scanning.

The model's entire influence on that decision is one bool. It reaches the filter
only through `ResolveThinkingProtocol(mappedModel) ==
ThinkingProtocolPassbackRequired`, now named `WebSearchHistoryStripsAllBlocks`,
and `StripEmptyTextBlocks` does not read the model at all. So the decision splits
the same way section 9.2's did: `cursorExecutionWire` derives the Messages wire
from body and inbound protocol alone, while `cursorWireContentSupported` applies
the per-route half. The cache keys the outcome on `stripAll` instead of the model
string, because models that agree on it cannot disagree on the answer.
Anthropic-strict models — which is what a Cursor account maps to — all collapse
to one entry, so the wire is derived once no matter how many models are judged.

That single outcome key is the whole mechanism. A first version also memoized the
projection itself in a `wires` map, described as what let distinct models share
one projection. It was not: with the outcome cache in place, an instrumented
build derived the wire exactly once at 1, 4 and 16 models, and `wires` never took
a second hit. It could only earn one when a single body met both a
passback-required and an anthropic-strict model, and to serve that it retained
whole projected bodies — 1,632,592 bytes measured across 8 groups at 205KiB,
where the pre-change cache held only bools. Removing it left the timings
indistinguishable (interleaved in one process: -0.47%, -2.95%, +3.45% at 1, 4 and
16 models, inconsistent in sign) while dropping one map and one allocation per
request. A cross-session comparison had first reported +10.84% on `models_1` for
the removal, which is impossible — that path does strictly less work and its
allocation count falls — and was section 9.2's between-run bias again.

Measured at `n=8`, against a baseline re-measured on unmodified code:

- `models_4` -75.5%, `models_16` -93.0%, both `p=0.000`; `ns/eval` falls from a
  flat ~136µs (the signature of a cache that never helps) to 4.8µs at 16 models;
- across production-bracketed body sizes, 8KiB -74.1%, 49KiB -75.8%,
  205KiB -73.5%, all `p=0.000`;
- allocations -72.8% geomean, and flat at 46/op regardless of model count;
- `models_1` is -3.6% (`p=0.050`), i.e. no material change: a single evaluation
  has no duplicate to remove.

A first comparison here reported -53.5% on `models_1` and -87.4% geomean. That
baseline was invalid: the benchmark ran in the background while the source was
already edited, so the "before" binary was partly the "after" code. The figures
above come from a baseline measured with the changes stashed. Section 9.2's
noise finding still applies — two runs of one binary differed by 7.3% geomean on
this host — so `models_1` at -3.6% is inside noise and is reported as unchanged.

### 9.5 Unknown-format accounting, the remaining §8 privacy debt (2026-09-26)

Section 8 required unknown-format passthrough to be counted and alerted on. The
redaction version was already persisted (`logredact-v4`), but the counting half
was absent: `RedactOnePass` returned `FormatUnknown` payloads verbatim and no
production code read that classification, so the residual exposure had no
observable size. `FormatUnknown` appeared nowhere outside its own definition and
a test.

Counting now happens at the classification that already ran, so it adds no scan
and no second pass over a body. Every sanitized payload adds to a redacted
record/byte total and unknown ones additionally to an unknown total; the
capture-local memo returns before classification, so a payload repeated across
fields is counted once, and empty payloads are not records at all.

Alerting reuses the existing channel rather than adding one. The counters ride
the `qa_capture` health payload that `OpsMetricsCollector.mirrorQACaptureHealth`
already writes to a job heartbeat, appended under a `redaction` key so existing
consumers still decode the ledger's own fields unchanged. A sustained unknown
share degrades a healthy status. Two boundaries are deliberate: drift never
escalates to `failed`, because capture itself is working and this is a privacy
signal rather than a capacity one, and it never softens an already-failed ledger.
A record floor keeps a handful of early unknown payloads from pinning the ratio
at 1.0 for the process lifetime.

Riding that channel required fixing it. `mirrorQACaptureHealth` special-cased
only `failed`, so a `degraded` ledger fell through to `LastSuccessAt` and drift
was recorded as a successful run — as was the pre-existing `evidence_dlq`
degradation. `LastErrorAt` is the only field a consumer acts on: `ops_health_score`
counts a heartbeat whose `LastErrorAt` is newer than its `LastSuccessAt` as a
failed job, while `LastResult` — which does carry `unknown_format_drift` — is
stored for display and alerts on nothing. Both attention-worthy statuses now
mirror as errors, and an unrecognized or empty status keeps the benign handling so
a future ledger status cannot turn every run into a false failure.

This closes the §8 row. Lifecycle controls, retention and redaction coverage are
untouched: unknown formats are still retained verbatim, which remains the
documented residual exposure — it is now an accounted one.

Required behavior checks include:

- group order and topology invariance;
- duplicate account membership does not multiply selection probability;
- Direct/Universal mapping parity and separation;
- payment-tier and billing-origin preservation;
- stale sticky and continuation authorization;
- slot races, retry rebinding and final route equivalence;
- missing, lagging and invalid snapshots fail safely;
- Plan remains the sole model/converter decision owner;
- unknown QA formats remain passthrough while identified formats retain
  differential redaction coverage.

Required performance observations include snapshot hit/fallback/lag, candidate
database reads, account materialization, routing-profile parse count, protocol
decode cost, Plan construction cost, QA scan count and bytes scanned. The pprof
acceptance target is removal of repeated account materialization and repeated
routing metadata parsing from the steady-state candidate path, rather than a
single synthetic percentage.

## 10. Approval boundary

This document is the approved high-risk design baseline. The candidate SSOT
owner table now registers the materialized read-model owner, and this PR adds
the shadow/fallback contract and its focused tests. The current implementation
keeps the snapshot read-only and shadow-only; it does not authorize a schema
migration, public API change, production switch or deletion of the existing
database path. Those rollout steps require their own acceptance evidence and
release approval.
