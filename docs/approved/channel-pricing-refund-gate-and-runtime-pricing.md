---
title: Channel-Pricing Refund Gate (investigated — no live leak) + Runtime Pricing ② design
status: approved
approved_by: "xuejiao (design directive, this session)"
approved_at: "2026-06-17"
authors: [agent]
created: 2026-06-17
related_prs: []
related_commits: []
related_stories: []
related_design: docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/pricing-availability-source-of-truth.md
supersedes: none
---

# Channel-Pricing Refund Gate (investigated) + Runtime Pricing ② design

> **Headline correction.** An earlier analysis named `channel_model_pricing` "the real
> remaining money hole" — an ungated admin writer at the top of the price-precedence stack
> that could re-enable the video terminal-failure refund leak. **A direct code read
> retracts that claim:** `channel_model_pricing` is structurally incapable of carrying a
> video / per-second / thinking price today, so the leak path does not exist. The refund
> gate is therefore **not urgent** and is **folded into ②** (runtime pricing), where the
> channel writer first *gains* those price dimensions and the gate's invariants become
> load-bearing alongside them. This doc records the investigation, the gate design (built &
> verified as a prototype this session), and the ② runtime-pricing design that the gate
> gates.

## 1. The investigation — why there is no live leak

The precedence + bypass facts are real and worth stating:

- `channel_model_pricing` (raw-SQL, resolved in `model_pricing_resolver.go`) **WINS**
  precedence: the overlay header says so literally (`pricing_service_tk_overlay.go:32-33`:
  *"The DB-backed ModelPricing override … still sits above everything."*).
- It **bypasses the preflight refund gate**: `scripts/checks/pricing-overlay.py` runs
  CI-only against `tk_pricing_overlay.json`. It never sees a DB write.

But the *dangerous* write — an ungated **video** price that re-enables the terminal-failure
full refund — is **structurally unreachable** through the channel writer:

| # | Fact | Evidence |
| --- | --- | --- |
| 1 | `ChannelModelPricing` has **no per-second / failure_billing / thinking** field | `channel.go:75` — only `input/output/cache_write/cache_read/image_output/per_request` |
| 2 | `BillingMode` has **no `video`** mode | only `token / per_request / image` |
| 3 | The resolver routes **no per-second branch**; `ResolvedPricing` has **no per-second field** | `model_pricing_resolver.go:75,139`; struct at `:16` |
| 4 | Video cost (and its refund) is **overlay-only** | `billing_service.go:976` reads `pricing.OutputCostPerSecond` from the overlay/litellm `ModelPricing` (`pricing_service.go:84`), already gated by `pricing-overlay.py`; `openai_gateway_service_tk_video_refund.go` reverses that overlay-derived cost |

The same holds for **thinking**: `ThinkingOutputPricePerToken` is sourced only from
litellm/overlay (`billing_service.go:412`), never from a channel row.

**So `channel_model_pricing` can only carry `token / per_request / image` prices today**, and
for those the existing admin validator (`channel_service.go validatePricingBillingMode:613`)
already enforces *mode-has-price* (`checkBillingModeRequirements`), *non-negative*
(`checkPricesNotNegative`), and *intervals-have-prices* (`checkIntervalsHavePrices`). The
only ungated residue is an **undercharging $0 token price** — `checkPricesNotNegative`
allows `>= 0`, so an accidental `output_price: 0` would bill nothing. That is a **revenue
guardrail (undercharge), not a refund leak (overcharge-then-over-refund)** — low severity,
admin-only, and not the money-leak class the gate was framed to close.

**Verdict: no live channel-pricing refund leak.** The gate is deferred into ②.

## 2. The gate design (folded into ②, prototyped this session)

When ② makes `channel_model_pricing` able to own video/per-second + thinking prices, the
write path must enforce the same refund invariants the overlay enforces at preflight —
**otherwise ② would create the leak that does not exist today.** The gate is designed and
was prototyped (compiles clean, `go vet` clean, `TestValidateChannelRefundSafety` 15/15
passing) so its implementability is proven; it ships **with ②**, not before.

### 2.1 New columns (make the invariants expressible)

Add to `channel_model_pricing` (and its `channel_account_stats_model_pricing` mirror) via a
TK-only additive migration:

| column | type | mirrors overlay field | invariant |
| --- | --- | --- | --- |
| `failure_billing` | text (nullable) | `failure_billing` | video mode ⇒ must equal `success_only` |
| `output_cost_per_second` | numeric (nullable) | `output_cost_per_second` | video mode ⇒ must be `> 0` |
| `thinking_output_cost_per_token` | numeric (nullable) | `thinking_output_cost_per_token` | when present ⇒ must be `> 0` |

Nullable + additive ⇒ no backfill, §5.x-safe net-add to a TK-owned table. **Critically,
these columns must land in the SAME change that adds the resolver branches that READ them**
(②) — adding them standalone now would freeze dead columns into the live schema (migration
immutability) ahead of any reader, which is why this is not a separate PR.

### 2.2 Write-time validator mirroring `pricing-overlay.py`

Extend the admin write path (the shared `validatePricingEntries` chokepoint reached by both
Create and Update **and** the account-stats rules) with a refund-safety check that is a
**1:1 port** of `pricing-overlay.py`'s anchors, so the JSON gate and the DB gate enforce
byte-equivalent rules:

- **video_generation:** `output_cost_per_second > 0` AND `failure_billing == 'success_only'`
  — else reject with a `VIDEO_REFUND_UNSAFE` BadRequest (same message class as the preflight
  check).
- **thinking rate:** if `thinking_output_cost_per_token` present, must be `> 0`.
- (existing mode-has-price / non-negative / intervals checks stay.)

### 2.3 Keep the two gates from drifting

Anchor the JSON gate (`pricing-overlay.py`) and the Go write-time gate so they cannot
diverge: a sentinel pin on the validator file + a unit test asserting a charges-on-failure
video write is rejected. (`scripts/sentinels/pricing-availability.json` + the validator's
unit test.)

## 3. ② Runtime pricing — STAGED-NEXT

**Goal:** route new long-tail model prices to `channel_model_pricing` so onboarding a model
needs **no release** — the price is a hot-added DB row, not an overlay rebuild. This is the
true-runtime half that the served-models reconciler doc defers to here.

② is the unit of work that lands **together**: (a) §2.1 columns, (b) §2.2 write-time gate,
(c) the resolver branches that make the new columns load-bearing, (d) the media-guard teach
below. None of these ship alone — the gate without readers is dead schema; the readers
without the gate are the leak.

### 3.1 The runtime media-unpriced guard must learn to consult channel pricing

The runtime media-unpriced guard currently **fail-closed-ignores** `channel_model_pricing`
(`openai_gateway_service_tk_media_unpriced_guard.go:47-49`: channel DB pricing "cannot be a
model's sole price"). That assumption is exactly what ② inverts. Once a media price can live
*only* in a DB row, the guard must **consult channel pricing before declaring a model
unpriced** — otherwise a legitimately DB-priced media model is gated out at serve time.

### 3.2 Deploy-ordering hazard (state it loudly)

**MERGE ≠ DEPLOY.** The §2 gate only protects production once the *binary running in prod*
contains it. Because ② ships the columns + readers + gate as one unit, the hazard is simpler
than a split would be — but still real for any **operator runbook** that says "price models
via the admin API":

```
② merged → ② deployed → operator prices a charges-on-failure video model at runtime
```

is safe **only if** the deployed binary contains §2's gate. Treat "② merged" and "②
deployed" as separate checkpoints; do not publish a "price via admin API" runbook until the
gate is **confirmed in the running image** — verify against live `/version` and a probe write
that `VIDEO_REFUND_UNSAFE` actually fires in prod, not just in CI.

## 4. Sequencing summary

| Step | Precondition | Effect |
| --- | --- | --- |
| **(now)** channel pricing carries only `token/per_request/image` | — | no video/thinking path ⇒ no live refund leak; existing validator suffices |
| **② runtime pricing** (columns + resolver + gate + media-guard, one unit) | current cadence acceptable; build when model-add frequency justifies it | new model prices via DB row, no release; gate prevents the leak ② would otherwise create |
| **② deployed + verified** | ② merged + released + probe-confirmed in prod binary | "price via admin API" runbook may be published |

Until ② is built, onboarding a model still costs an overlay rebuild for its price — a normal
reviewed release, acceptable at current cadence. The gate is a prerequisite **of ②**, not a
standalone batch.
