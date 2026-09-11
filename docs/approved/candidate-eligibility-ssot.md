---
title: Candidate Eligibility SSOT
status: approved
approved_by: "feng (conversation approvals, 2026-09-07, 2026-09-08, 2026-09-09 and 2026-09-10)"
created: 2026-09-07
---

# Candidate Eligibility SSOT

## Approval and scope

The user approved converging on four existing owners in the 2026-09-07
conversation, with the instruction to implement this SSOT principle.
That approval authorized implementation only; merge and deployment were separate
decisions, and both have since been taken — see the release status below.
The Chinese collaboration name is recorded in project AGENTS.md. Use
`candidate-eligibility-ssot` as the stable search term.

This contract owns evaluated-request candidate selection;
[universal-key-routing.md](universal-key-routing.md) owns key authorization and
billing binding. It does not
replace the protocol-routing SSOT, endpoint authorization, or billing owners.

## Thinking and tool compatibility (2026-09-10 approval)

The user approved shared capability evaluation for admission and scheduling,
with priority: satisfy client requirements, then preserve thinking, then honor
forced tool invocation. The gateway returns tool calls; clients execute them.

`protocolrouter.Plan` evaluates each target protocol against the resolved model.
`internal/pkg/anthropicpolicy` owns the thinking/tool conflict decision. Messages
uses its native conflict rule, including Fable 5 always-on thinking already
recorded in `docs/spec-delta/cc-fable-5.md`; this is not applied to Chat targets.
Endpoint-specific evidence may override that baseline in the existing JSON
`probe_evidence.model_capabilities[resolved_model][target_protocol]`, using the
complete `always_thinking` / `adaptive_only_thinking` /
`forced_tools_with_thinking` capability record.
Endpoint identity and exact resolved model scope this evidence. Account IDs,
supplier names, billing groups and public aliases are not capability evidence.

Within each admitted payment tier, available unadjusted Plans precede adjusted
Plans, then existing account priority/stickiness apply. Capacity races and
failed-account exclusions may select an adjusted peer. Authorization, hard
continuation affinity, subscription preference and window-reserve rules retain
their existing ownership. Within an account, an unadjusted legal route precedes
an adjusted route; equivalent routes retain registry/native preference.
Endpoint conversion permission constrains Plan before that comparison: disabling
Messages dispatch retains a legal native Messages fallback even when an exact
conversion exists. The request-local Plan cache is isolated by this permission,
and authoritative pre-send planning consumes the same restriction.
For one account reached through multiple authorized origins in the same payment
tier, compare billing origins only among its best-compatible legal Plans. A
lower multiplier must not replace an available exact request with an adjusted one.

Only conflicting forced choice (`required`, named function, Messages `any` or
`tool`) becomes `auto`. Thinking, tools, cache placement, tool history, parallel
tool restrictions and `none` remain intact. Explicit thinking-off remains intact
on models that support it. A confirmed always-on model omits unsupported explicit
disabled; an adaptive-only model converts manual thinking to adaptive and omits
its unsupported budget. Both count as adjusted Plans. Chat-to-Messages additionally preserves the validated
Messages `thinking` extension; unsupported reasoning formats remain excluded by
converter admission. Native Chat `reasoning_effort` and Responses
`reasoning.effort` participate in the same endpoint capability decision; an
explicit `none` does not enable thinking. These fields are preserved verbatim.
This change does not claim universal reasoning conversion.

Plan binds the original digest and an immutable effective request. Admission,
selection and authoritative pre-send rechecks share that Plan. Execution uses
its effective body without mutating retry input. Changes are audited by account,
resolved model, target protocol and adjustment reason, without logging bodies.
Legacy Messages normalization reuses the same policy and cannot undo a selected
Plan. Endpoint evidence changes that alter the effective request invalidate the
selected Plan before transport. This 2026-09-10 approval covered implementation
and tests only; the resulting change has since merged and released.
The supplier's exact
`[preflight:R3.forced_tool_choice_incompatible]` 400 is eligible for another
account without credential penalties; unrelated client 400s remain terminal.

## Approved policy: authorization-scoped account scheduling

On 2026-09-08 the user approved the policy below and its ownership in this
contract. This branch implements the shared candidate handoff and account pool;
local validation is recorded in US-050. The follow-up conversation also approved the balance-origin rule in
[universal-key-routing.md](universal-key-routing.md#已确认的余额计费归属),
group-independent session affinity with a one-time soft-cache cold start, and
the common account ordering below. A conversation approval is never by itself
deployment evidence; the release facts below are what record what shipped.

The subsequent review accepts multiplier-read fallback to 1 and normal
administrative rate changes between reservation and settlement; billing details
remain in the Universal contract. The user subsequently approved retaining
Direct group model mappings while Universal ignores them, as defined in
[candidate-request-policy-convergence.md](candidate-request-policy-convergence.md).
That supplement also assesses continuation-order migration impacts. Direct
compatibility does not require client migration or deletion of group fields.
The mapping-mode guard, global scheduling and continuation-storage migration
are merged and released. The release prohibition recorded here applied to the
original implementation task and has been lifted: every owner in the table below
is contained in a released tag (`v1.8.204` through `v1.8.217`) and prod
(`api.tokenkey.dev`) has been serving `v1.8.219`, an ancestor-inclusive
superset, since 2026-09-11. Local implementation and tests remain separate from
deployment evidence — the acceptance boundaries in
[US-050](../../.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md)
now describe live-traffic verification still to be gathered, not unshipped code.

### Scope and decision order

1. Direct keys contribute only their authorized bound-group scope. Universal
   keys contribute the authenticated user's effective authorized-group scope.
   Both then consume the same candidate and scheduling policy. Active status,
   endpoint opt-ins, forced platform and reserved probe isolation remain gates.
   Direct retains its configured group model preprocessing; Universal does not
   consume group substitutions or their defaults, even after billing binds a
   group. Shared eligibility/Plan comparisons use the same effective model and
   request policy, not necessarily the same raw model string. Account mappings
   and protocol conversion remain available to both modes.
2. A candidate must have a complete valid authorization path for the actual
   account, requested model, endpoint and request policy. Union these paths;
   never form the Cartesian product of separately unioned accounts and models.
   Request/model legality still belongs to `protocolrouter.Plan` and the
   existing native/media capability owners.
   The follow-up approval intentionally broadens platform pools: group platform
   and name do not restrict the models of an authorized member account. Old
   group/account platform-equality and mixed-pool opt-in filters are superseded
   in the target. Account adaptor validity, actual request capability, explicit
   endpoint permissions and forced-account-platform constraints remain gates.
   Handler/adaptor dispatch must consume the actual account and request Plan;
   the billing group's platform label is not an execution-routing input.
   Endpoint permission must distinguish native requests from protocol conversion
   using the actual Plan, including existing native/media capability owners.
   A legacy `allow_messages_dispatch=false` is not a blanket denial of native
   Messages. Neither a billing-platform exemption nor handler choice may silently
   grant conversion permission. The migration must cover account 115 through
   group 1 and preserve Direct/Universal parity.
3. Apply the explicit payment policy: usable subscription capacity first;
   otherwise use eligible balance capacity under the existing fallback rules.
   A soft saturation penalty alone cannot displace a usable subscription with
   a balance route. This is a payment constraint on the candidate set.
   Subscription reads, window maintenance or candidate evaluation may fail;
   record the failure and try other verified authorized candidates, including
   balance routes. Fallback must pass normal wallet, key quota and billing
   admission checks before execution. Without a verified alternative, propagate
   the infrastructure error instead of asserting missing entitlement. Failure
   to establish the authorization scope itself does not permit unverified access.
   For a required continuation, the user agrees in principle that validated
   ownership and the actual execution account restrict the legal paths before
   this payment-tier filter. Migration must satisfy the impact constraints in
   the supplement; unrelated subscription capacity cannot continue an
   execution it does not own.
4. Within that payment tier, all eligible accounts compete across authorized
   groups using one account scheduling policy. Group `sort_order`, ID, name,
   enumeration order and number of memberships supply no scheduling weight.
   `sort_order` remains a display field. Choosing a group winner first, even
   randomly or by group ID, does not implement this policy.
5. Equivalent execution candidates reached through multiple authorized groups
   are represented once, retaining their valid authorization and billing
   origins. Additional memberships do not multiply selection probability,
   concurrency capacity or retry allowance. Different effective request
   policies are not equivalent candidates and must not be silently merged.
6. Account priority, scoped failure feedback, session affinity and live load remain
   scheduling inputs. Compare them over the admitted candidate set, not by
   comparing separately normalized scores from individual group pools. Legal
   converters carry no group/platform-hint penalty. Existing quota, credential,
   capability and cooldown owners continue to decide hard eligibility.
   Preserve required execution affinity. Among accounts able to accept work now,
   compare lower effective priority first, then ordinary session affinity,
   randomly breaking equivalent ties. Effective priority is configured priority
   plus the existing temporary penalty (three attributable failures in a fixed
   90-second window add 1000). Ordinary affinity cannot defeat that penalty.
   Configured concurrency only caps admission; normalized occupancy is not a
   quality signal and does not rank ready accounts. This simplification was
   explicitly approved in the 2026-09-09 conversation.
   A full preferred account must not force waiting while a lower-priority
   eligible account can accept the request. If all eligible accounts are busy,
   waiting remains bounded by the existing wait policy. Shared, bounded
   saturation preference remains in force within the payment tier.
   Existing empty-pool recovery runs once over that admitted pool, after
   authorization and hard eligibility: keep window-guard reserve accounts out
   while a normal candidate remains, and consider them only if none remains.
   Recovering each group's pool before union would let extra memberships admit
   reserve accounts early. This changes the existing check's scope, not its
   hard gates or sticky-only permissions.
   Snapshot occupancy is advisory: slot acquisition must recheck capacity and
   try another eligible account after a race instead of assuming the snapshot
   reserved a slot.
7. The selected execution path and its billing attribution must agree before
   upstream execution and any required billing reservation. Subscription,
   balance, quota, price and profit checks consume that same attribution.
   Selecting through one group and charging an unrelated group's entitlement
   is forbidden. Losing a slot or failing a final check must release acquired
   resources before reselection.
8. Sticky hits and retries must revalidate against the same authorized scope
   and payment policy. Rebinding a path requires corresponding billing checks
   and reservation cleanup; it cannot bypass a group restriction. Existing
   non-replayable requests, response continuations, started streams and stored
   media tasks retain their execution-affinity and retry-safety contracts.

Moving between authorized group origins is ordinary candidate reselection, not
a separate group-failover scheduling policy. Legacy fallback-group pointers must
converge on the same candidate owner; a pointer alone does not enlarge the
Direct bound-group scope or Universal effective authorized scope. Preserve
execution and billing checks on every reselection.

## Approved failure recovery supplement (2026-09-09)

Attributable failures on every account platform use the existing Redis counter
infrastructure, scoped to account and the actual Plan's resolved upstream model.
Native Gemini, Kiro and Bedrock paths without a Plan reuse their forwarding model
resolvers for the same read/write scope. This all-platform extension was approved
in the follow-up conversation on 2026-09-09. Counter reads fail
open to configured priority; fixed-window expiry restores preference without
configuration writes. Existing credential, explicit quota and provider cooldown
owners remain authoritative. Caller cancellation, invalid input, policy refusal
and request-scoped failures do not create this account penalty. Multiple feedback
owners combine by maximum count, never by adding duplicate penalties.
One completed execution records at most one failure, including a native execution
without a protocol Plan. Shared observations bypass the legacy OpenAI account-wide
health breaker; explicit quota and credential handling remain intact. Transport
errors retain sanitized output and are attributed only while the caller is alive;
caller cancellation/deadline and local validation cannot become transport penalties.

A replayable NewAPI Chat request may immediately try another eligible account
on its first pre-output failure; it does not wait for three cross-request failures.
The first useful output timeout defaults to 60 seconds
(`gateway.newapi_chat_first_output_timeout`, zero also means 60), with no more
than three attempts/two switches and a shared budget of three times that timeout.
Headers, heartbeats, empty deltas and usage-only frames do not stop this timer.
Actual content, reasoning, refusal or function-tool output commits the response
and stops pre-output retry. Started output is never replayed. Hard continuation,
WebSocket, server-side tools and non-text modalities retain their existing owners.
Partial output keeps known usage and cannot acquire a synthetic successful end
marker after an interrupted upstream. NewAPI transport cancellation is opt-in
so unrelated relay callers preserve their lifecycle.

These are implementation and local test changes, not production acceptance.
Production cutover remains forbidden until the user reviews the prepared
candidate and explicitly authorizes switching traffic.

The 2026-09-10 SSOT repair work reconnects existing owners: candidate readiness
installs profit control from the actual billing origin, carries it through slot
and wait rechecks, and shares the pricing instant with settlement. User pricing
menus consume candidate discovery's verified origins, including cross-origin
billing-policy agreement, before attaching official catalog metadata. Neither
price rows nor group platform labels independently grant model support.

Group configuration must be classified by responsibility during implementation:
authorization/endpoint restrictions remain gates, prices and subscriptions stay
with billing, and account-ordering settings such as preferred-account routing
and sticky strategy move to an explicit scheduling owner. Copying conflicting
group policies into an account score is not a migration strategy. The public
group schema need not be deleted to establish these runtime boundaries.

### Session and resource identity

Session lookup maps an authenticated user/key scope plus session identity to
the actual account. Neither the current billing group nor a hash of the user's
changing authorized-group set belongs in that lookup identity. Account ID alone
is not a session key; ownership and session isolation remain required. Each hit
must revalidate account authorization and request policy before it can be used.

Group-independent lookup and sticky-aware eligibility are separate obligations:
the pre-billing candidate evaluation must recognize a validated existing
session's sticky-only eligibility. Treating it as a new session can reject
usable capacity even after group IDs are removed from the cache namespace.
New sessions must not inherit another session's sticky-only allowance.

The user accepts a one-time cold start of ordinary soft-sticky bindings during
cutover. A new namespace may let those bindings expire naturally; dual-reading
or migrating the old soft-sticky cache is not required. Subsequent billing-origin
changes must preserve the new binding. This concession does not discard
Responses continuation ownership, active execution state, or submitted media
task routing: those records retain their actual account and owner.

Kiro session recovery exclusions follow the same stable session/account scope.
Physical account concurrency, cooldown and retry accounting remain shared across
memberships and request-policy variants. Wallet, subscription and configured
group limits retain their declared billing/limit scope. Changing an authorization
origin cannot create additional physical capacity or duplicate a shared limit.

The follow-up identifies same-credential, multi-protocol accounts as supplier-
managed projections. Reuse the supplier owner's normalized endpoint and existing
credential fingerprint for confirmed credential-wide failures and recovery.
Supplier source ID alone is insufficient because source identity includes
channel type. Model limits retain the actual upstream model dimension; protocol
errors and candidate Plans remain separate. Ordinary 429s and timeouts do not
prove a credential-wide failure. Neither shared credentials nor supplier
membership alone justify merging configured concurrency capacity.
Production examples from the 2026-09-08 review are accounts 115/124 (same upstream
model, different protocols) and 123/126 (different upstream model mappings).
`supplier_credential_fault.go` now propagates confirmed credential failures and
recovery through conditional repository writes; independent pauses, model limits,
and configured concurrency remain intact.

Group-specific request transformations, such as messages compaction, remain
explicit path policy. Different effective transformations are not equivalent
execution candidates, even when their account ID and model match.

### Request ingress and transport

The 2026-09-08 follow-up approves one authorization/candidate/payment policy for
HTTP and WebSocket. Their input becomes available at different times:

- HTTP must finish the bounded raw-body read before candidate lookup or billing
  binding. Pre-reading may buffer the complete original bytes for replay; a
  partial read and its error must never become a successful truncated request.
  Oversize reads return 413; incomplete/I/O reads reject the request. Repeated
  peeks reuse the buffer and retain errors, including reads by the OpenRouter
  preprocessing consumer. Decoding/model normalization remains owned by the
  existing parser and mapping owners, not by the raw-body replay buffer.
- WebSocket authenticates the connection first. The first `response.create`
  frame supplies the model, request features and continuation identifiers.
  Before upstream forwarding or charge reservation, validate execution ownership
  and the actual continuation account, evaluate authorized paths with the
  supported transport/Plan, then apply payment priority and billing admission.
  A zero wallet must not block an otherwise valid subscription-only connection
  before its request is available; key validity and connection limits still
  apply. Subsequent turns preserve execution affinity and enforce applicable
  authorization and billing checks. Missing authorization cannot become a nil
  group query against ungrouped accounts. Errors after upgrade use WebSocket
  error/close semantics, not writes of HTTP responses to an upgraded stream.

Raw-body replay and WebSocket first-frame admission are implemented in this
branch. GET `/responses` defers request admission until the first frame; every
turn refreshes the key, authorization and payment state before forwarding.
Changed execution mappings or API credentials require reconnecting, and current
account concurrency applies to the next turn. Merely resolving a billing group
after upgrade does not satisfy this contract.

User/platform quota semantics and the dated production usage evidence are owned
by the Universal contract's `Platform quota boundary` section. Grok account 65
and the other Grok account mapping/default/UI inconsistencies are a separate
follow-up; this change does not alter live account mappings or introduce a
candidate-level model-prefix exclusion to compensate for them.

### Implementation and acceptance boundary

Authentication prepares a request-local `CandidateRequest` with an execution
candidate and billing origin. Selectors repeat admission and own slot acquisition;
retry and wait consumers use the same request state. A group summary alone is
insufficient to authorize execution or quote its price.

Extend US-050 with behavioral checks for group-order/topology invariance,
duplicate-grant invariance, cross-group account competition, payment-tier
preservation, same-path authorization/billing, and Direct/Universal parity.
Tests must exercise the production handoff and selectors, including slot races
and stale sticky authorization. Model discovery must use the same authorization
and support projection without presenting a provisional group as a guaranteed
execution or price. Existing native/media and protocol-plan coverage remains
required. Add semantic sentinel anchors for the actual new consuming call sites
when those call sites exist.

The approved balance-origin, identity and ordering policies above must be
implemented together with the production handoff. Non-equivalent tariffs,
subscription entitlements and request policies are outside the lowest-multiplier
equivalence rule; do not silently collapse them or invent a price comparison.
Acceptance requires the actual request's execution, admission and billing to
consume the same selected path, including retries and final slot checks.

## Implementation

### Owners

| Fact or decision | Unique owner | Consumer contract |
| --- | --- | --- |
| Model mapping, native/converter legality | `protocolrouter.Router.Plan` via `protocol_routing_context.go` | Parse the actual request using `protocolrouter.ParseCanonicalRequest`; a valid converter is equally eligible. |
| Authorization paths and account selection | `candidate_request_tk.go`, `candidate_selection_tk.go` | HTTP auth, Responses WS, both schedulers and retries consume one request-local state. |
| Inference ingress into candidate state | `candidate_ingress_tk.go` (`PrepareCandidateIngress`) | The single HTTP entry: existing client and session parsers run before billing admission, when sticky-only eligibility already matters. A new ingress joins this owner instead of preparing its own state. |
| Account pool admission projection | `candidate_eligibility.go` (`candidateSupportsRequest`) | Support and readiness stay separate; an empty live pool is never missing entitlement. Legacy adapters in `universal_routing_tk_serving.go` share this admission but keep their callers' snapshot/fallback semantics and are not the selection owner. |
| Current availability | `gateway_candidate_eligibility.go`, `openai_candidate_eligibility.go` and existing quota/auth/capability helpers | The global selector supplies the actual account platform and sticky identity, then applies recovery over the admitted payment tier. |
| Empty-pool feedback and expiring preference | `candidate_saturation.go` over existing Redis counters | Account scoring consumes shared scoped feedback; groups do not receive scheduling votes. |
| Billing origin and reservations | `candidate_billing_tk.go`, `BillingCacheService`, existing hold lifecycle | Compare only equivalent origins, rebind reservations, snapshot the final path before async settlement. |
| Profit admission | `candidate_profit_tk.go` delegates to the existing gateway profit owner | Actual billing origin and request pricing instant follow selection, slot checks and WS turns. |
| Stable affinity and continuation | `candidate_identity_tk.go`, `candidate_ws_identity_tk.go`, `candidate_ws_authorization_tk.go` | User/key/session soft affinity; user-owned response continuation with authorized legacy lookup and dual writes. |
| Supplier credential faults | `supplier_credential_fault.go`, `account_repo_supplier_fault.go` | Conditional updates share confirmed credential faults and preserve independent model limits, Plans and account concurrency. |
| Attributable failure observation and deprioritization | `candidate_failure_tk.go` | One attribution rule per account and resolved model. Caller cancellation, credential faults, request-scoped transients, same-account retries and caller errors keep their dedicated owners; an observed failure must not also feed the legacy OpenAI health breaker. |
| Per-turn RPM admission | `candidate_rpm_tk.go` | Peek before binding, count once after. Universal drops the auth snapshot's group override because it belongs to the original bound group. |
| Replayable Chat attempt budget | `candidate_chat_attempt_tk.go` | Pre-output failover only, bounded by the shared attempt cap; handlers still own switching. Never replay or synthesize completion after content or tool output. |
| Relay-scoped model rejection | `candidate_edge_model_rejection_tk.go` | A relay's own model verdict covers that path only, and must not exclude other authorized candidates at the main gateway. |
| Discovery request/account snapshot | `candidate_discovery_snapshot_tk.go` | One immutable request and account set per discovery shape; prepare each group's request policy once while runtime selection keeps fresh account validation. |
| Model discovery | `candidate_discovery_tk.go` | Direct and Universal model/capability surfaces and `me_pricing_candidate_tk.go` project the same authorization and support paths without live payment or slot admission. |

### Implemented behavior

1. Admission unions authorized memberships and evaluates complete paths. Groups
   do not filter accounts by platform label, model prefix or display order.
2. Direct preprocessing precedes Plan. Universal ignores group aliases. The
   selected canonical request and final channel mapping follow retries into
   execution and settlement; handlers do not repeat the mapping after Plan.
3. Subscription admission precedes balance, after hard continuation ownership.
   Non-equivalent same-account origins reject that candidate in that tier;
   independent usable peers remain eligible.
4. The pool follows the account ordering policy above. Occupancy determines
   capacity, not ready-account rank. Ready peers and slot-race alternatives precede bounded waiting.
   Acquired and waited slots recheck authorization, runtime state and route facts.
5. Window reserves recover only when the admitted global tier has no ordinary
   candidate. Shared saturation feedback remains bounded and expiring.
6. Responses WS validates the first frame and every subsequent turn, including
   fresh Key status, budget, user authorization and pinned execution account.
   Existing response ownership remains readable during migration and rollback.
7. Missing capability and infrastructure errors remain observable. Supported
   but unavailable pools return capacity errors; unsupported models return 400.
8. Scoped requests disable old CC-only and prompt-too-long group replacement.
   Ordinary retries stay in the authorized pool and rebind billing through the
   shared owner. Universal settlement no longer uses billing-platform quotas.

### Existing validation

Acceptance criteria and runnable test commands:
[US-050](../../.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md).
Coverage includes production auth/selector handoff, account order and duplicate
membership invariance, billing and hold failures, actual HTTP forwarding, real
WebSocket ingress/upstream sockets, discovery and legacy continuation storage.
Gateway sentinels protect consuming call sites as well as shared owners and tests.

This is a backend/API change with no new UI surface, schema or live configuration.
Local unit and middleware integration tests are required.

### Release status (verified 2026-09-11)

All 16 production `backend/internal/service/candidate_*.go` owners named in
§Implementation/Owners are contained in a released tag, so the "deploy the
reviewed change before a Universal-key probe can prove the new behavior"
prerequisite is satisfied — that probe is runnable against prod, not blocked:

| Owners introduced | Commit | Tag |
| --- | --- | --- |
| `candidate_eligibility.go`, `candidate_saturation.go` (#2032) | `b2e8681443` | `v1.8.204` |
| `candidate_request_tk.go`, `candidate_selection_tk.go`, `candidate_ingress_tk.go`, `candidate_billing_tk.go`, `candidate_discovery_tk.go`, `candidate_identity_tk.go`, `candidate_ws_authorization_tk.go`, `candidate_ws_identity_tk.go` (#2051) | `321a7ff334` | `v1.8.208` |
| `candidate_rpm_tk.go` (#2053) | `928316bc02` | `v1.8.208` |
| `candidate_failure_tk.go`, `candidate_chat_attempt_tk.go` (#2067) | `651593de38` | `v1.8.213` |
| `candidate_discovery_snapshot_tk.go`, `candidate_profit_tk.go` (#2097) | `9b72ce6bfc` | `v1.8.216` |
| `candidate_edge_model_rejection_tk.go` (#2100) | `f5921f5232` | `v1.8.217` |

Each tag's Release workflow run succeeded. Prod (`api.tokenkey.dev`) has been
serving `v1.8.219` — which contains all of the above — since the successful
Stage0 Deploy of 2026-09-11, with a multi-arch (amd64 + arm64) manifest verified
by that run and `/health` returning 200 under live traffic.

What remains open is live-traffic acceptance evidence, not shipping: US-050's
`AC-013/017/018/027` and `AC-036/037` still need real price/cost comparison
against production data, and `AC-032/033/034` still need the production
comparison recorded. Those are measurements to take on the deployed build.
