---
title: Pricing & Serving — One Owner Per Fact (consolidated SSOT decision)
status: approved
approved_by: "xuejiao (design directive, this session)"
approved_at: "2026-06-17"
authors: [agent]
created: 2026-06-17
related_prs: []
related_commits: []
related_stories: []
related_design: docs/approved/pricing-registry-hot-reload.md, docs/approved/newapi-as-fifth-platform.md, docs/approved/pricing-availability-source-of-truth.md, docs/approved/newapi-served-models-reconciler.md, docs/approved/channel-pricing-refund-gate-and-runtime-pricing.md
supersedes: none
---

# Pricing & Serving — One Owner Per Fact

> **The SSOT verdict is ONE OWNER PER FACT, not "collapse everything to upstream."**
> Two facts govern whether a model works in TokenKey, and they have two different owners:
> **SERVING** (does this account serve this id?) and **PRICE** (what does it cost?).
> Every drift incident in this area traces back to a third party silently claiming
> ownership of one of those facts. This doc names the owners, rejects the two tempting
> wrong answers, and maps the remaining work.

## 1. The two facts and their owners

| Fact | Question | OWNER (single source) | Thin projection / cache |
| --- | --- | --- | --- |
| **SERVING** | Does account N serve client-id `m`? | per-account `credentials.model_mapping` (identity whitelist `key==value`) | `tk_served_models.json` manifest — CI-time **intent** projection only, NOT runtime |
| **GLOBAL PRICE** | What does `m` cost by default? | active complete `tk_pricing_overlay.json` registry snapshot | embedded copy is last-known-good fallback; provider/LiteLLM data is sensor evidence |
| **SCOPED PRICE OVERRIDE** | Does one commercial channel override `m`? | `channel_model_pricing` for that explicit scope | resolver/cache only |

**Price precedence (highest wins):**

```
matching channel_model_pricing scope
  > active complete registry snapshot
    > embedded complete registry fallback
```

There is one editable global owner. A channel row is not a second global writer: it
applies only when the resolver selects that commercial scope. Provider/LiteLLM files may
produce a candidate diff, but no value from them participates in effective billing.

**Serving owner is genuinely per-account.** `Account.IsModelSupported`
(`backend/internal/service/account.go:639`) returns `true` on an **empty** mapping —
"无映射 = 允许所有" — for every non-antigravity platform. A populated `model_mapping` is an
identity WHITELIST; an empty one is allow-all. This asymmetry is load-bearing for §2's
rejection of an auto-sync reconciler and is documented in
`docs/approved/newapi-served-models-reconciler.md`.

## 2. Two wrong answers we explicitly REJECT

### REJECTED: "align the whitelist to the price registry"

The registry is a PRICE source. Letting price-presence imply serving (auto-mapping every
priced id onto an account) inverts the fact ownership: it makes the PRICE owner silently
write the SERVING fact. This is exactly the #812-class confusion — `qwen3-8b/14b/32b` were
priced in the registry but not mapped onto account 60, and the price-present-looks-served
illusion produced `429`/`503` in prod. The fix is **not** to let the price drag serving
along; it is to keep them separate facts and let a guard assert they agree
(A1/A3 below, shipped in #819).

### REJECTED: "converge everything to upstream capability"

Tempting because upstream `/v1/models` *looks* authoritative. It is not a source of truth —
it is an **unvetted discovery feed** (`docs/approved/pricing-availability-source-of-truth.md`
§2.4 / R-002). It over-reports:

- deprecated / retired / disabled ids the operator must not serve,
- served-but-unpriced ids (a money hole if auto-onboarded),
- embedding-only ids that have no chat surface.

`FetchUpstreamSupportedModels` (`backend/internal/service/upstream_models.go:76`) returns a
bare `[]string` — no pricing tag, no availability tag — and is deliberately **NOT wired**
to `DiscoveryFilter`. The discovery filter
(`backend/internal/integration/newapi/discover_filter_tk.go`) treats the upstream list as a
**weak suggestion** requiring human confirm: it drops explicitly-unavailable ids, drops
`model_availability='unreachable'` ids, and tags the rest `priced | missing` so a human sees
the gap. Converging serving to upstream would re-import every over-report and re-open the
money hole. **Upstream is a feed into a human decision, never the decision.**

## 3. What is already shipped (do not re-litigate)

| PR | What landed | SSOT role |
| --- | --- | --- |
| **#818** | `tk_029` maps `qwen3-8b/14b/32b` onto account 60 | point-fix the #812 serving gap |
| **#819** | `scripts/checks/catalog-serving-drift.py` (A1/A2/A3) + `tk_served_models.json` manifest (13 entries) | mechanical drift guard + intent projection |
| **#820** | `tokenkey-onboard-model` skill + dashscope probe | the human-in-the-loop onboarding mechanism |

The **#812-class drift is now mechanically blocked** by the #819 guard's three assertions:

- **A1** — every catalog/manifest id is **price-resolvable** (price owner agrees).
- **A2** — `display=true` ⇒ present in the Go servable map (display agrees with code).
- **A3** — `served_on` ⇒ a migration maps it onto the account (serving owner agrees).

A3 is the specific catcher for #812: a manifest row claiming an account serves an id, with
no migration putting it there, hard-fails CI. The manifest is **NOT** `//go:embed`-ed — it
is CI-time intent only; runtime serving still comes from the per-account `model_mapping`,
which keeps the owner single.

## 4. Remaining work (mapped, not all in this batch)

| Track | What | Owner-fact it closes | Status |
| --- | --- | --- | --- |
| **FE catalog (PR-B)** | newapi whitelist picker offers the served long-tail (qwen3 dense ids) | SERVING fact *offerable* in admin UI | **this batch (PR-B)** |
| **channel-pricing refund gate** | validate dimensions that `channel_model_pricing` can override in its explicit scope | keeps scoped overrides settlement-safe without making them a global owner | **deferred** — investigated, no live refund leak today (§4.1) |
| **protected registry publication** | merge a registry-only PR and atomically publish the exact protected-main artifact | makes the single global PRICE owner hot without an application release | **implemented** — see `pricing-registry-hot-reload.md` |
| **provider price sensor** | compare external evidence and prepare a draft registry-only candidate | removes manual discovery toil without delegating the price decision | **implemented** — sensor cannot publish or access AWS |

See `docs/approved/channel-pricing-refund-gate-and-runtime-pricing.md` for the full gate +
② design and the investigation below.

### 4.1 The channel-pricing "hole" — investigated, found INERT today (no live leak)

The prior framing of this work called `channel_model_pricing` "the real remaining money
hole": it **WINS within its selected scope** and **bypasses** the registry's preflight refund gate
(`scripts/checks/pricing-overlay.py` runs CI-only against the JSON, never a DB write). The
precedence + bypass are real. **But a direct code read retracts the "live money-leak"
claim** — the dangerous write (an ungated *video* price re-enabling the terminal-failure
refund leak) is **structurally unreachable** through `channel_model_pricing` today:

1. **No column.** `ChannelModelPricing` (`channel.go:75`) has `input/output/cache_write/
   cache_read/image_output/per_request` price fields only — **no `output_cost_per_second`,
   no `failure_billing`, no thinking field.**
2. **No billing mode.** `BillingMode` has `token / per_request / image` — **no `video`.**
3. **No resolver branch.** `ModelPricingResolver` (`model_pricing_resolver.go:75,139`) routes
   only `token / per_request / image`; `ResolvedPricing` (`:16`) has **no per-second field**.
4. **Video cost is registry-only.** `billing_service.go:976` reads `pricing.OutputCostPerSecond`
   from the active registry `ModelPricing` (`pricing_service.go:84`) — which
   `pricing-overlay.py` **already gates**. The video refund
   (`openai_gateway_service_tk_video_refund.go`) reverses that registry-derived cost. A
   `channel_model_pricing` row never feeds video cost or its refund. Thinking rate is the
   same story: `ThinkingOutputPricePerToken` is sourced only from the registry
   (`billing_service.go:412`), never from a channel row.

So the only price dimensions `channel_model_pricing` can actually carry are
`token / per_request / image`, and for those the existing admin validator already enforces
*mode-has-price* / *non-negative* / *intervals-have-prices* (`channel_service.go:613`). The
single ungated residue is an **undercharging $0 token price** (a revenue guardrail, not a
refund leak). **Conclusion: there is no live channel-pricing refund leak; the refund-safety
gate is not urgent and is folded into ②**, where channel pricing first *gains* the
video/per-second + thinking resolver paths and the gate's invariants become load-bearing
together with them. A prototype gate (3 columns + write-time validator + 15/15 unit tests)
was built and verified this session as proof it is implementable; it is intentionally **not
shipped standalone** to avoid freezing dead columns into the live schema (migration
immutability) ahead of the resolver work that makes them load-bearing.

## 5. The real FE gap (one, and it is narrow)

The admin-UI "drops qwen3-8b" claim is a **phantom** for account 60: pure-identity
whitelists round-trip intact, out-of-catalog ids render as removable chips from
`props.modelValue` and survive a save, and `addCustom` is a zero-validation escape hatch.
The single real gap is the **picker offer set**: the newapi selector is hardcoded
`platform='newapi'` → `newapiModels` (`frontend/src/composables/useModelWhitelist.ts`),
which was **GPT-only**. A separate `qwenModels` array exists but is reachable only for
`platform==='qwen'`, never for `newapi`, and it too lacked the three dense ids. So the picker
could not *offer* `qwen3-8b/14b/32b` — an operator had to use the `addCustom` escape hatch.
**PR-B (this batch)** teaches the newapi picker to offer the served long-tail; the deeper
convergence (derive the picker from the backend manifest via a servable endpoint, like
`useServableModels` for the API-backed platforms) is a follow-on, not done here.

## 6. The one-line test for any future change here

Before adding code, ask: **which of the two facts does this write, and is it the owner?**
If a change makes the PRICE source write SERVING, or makes upstream `/v1/models` write
either, it is the wrong primitive — stop. Every incident in this area is a third party
quietly claiming a fact it does not own.
