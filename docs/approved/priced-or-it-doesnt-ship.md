---
title: Priced or It Doesn't Ship — Runtime Price Gate on Serving Admission
status: draft
approved_by: pending
approved_at: pending
authors: [agent]
created: 2026-06-26
related_prs: []
related_commits: []
related_stories: []
related_design: docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/pricing-availability-source-of-truth.md, docs/approved/channel-pricing-refund-gate-and-runtime-pricing.md, docs/approved/newapi-served-models-reconciler.md
supersedes: none
---

# Priced or It Doesn't Ship — Runtime Price Gate on Serving Admission

> **One rule, every platform: a model that resolves to no price is not served.**
> Today the system does the opposite — *unpriced never blocks*: a model with no
> resolvable price is forwarded, billed `$0`, and a P0 alert fires *after the fact*.
> That is a silent revenue leak AND it ships an un-vetted model to a paying customer.
> This doc makes **price a precondition of serving** at request admission (fail-closed),
> and pairs it with **auto-pricing on first sight** so availability never regresses.
> Ships behind a default-OFF setting; the default flips per platform only after the
> auto-pricing half is live and soaked.

## 0. TL;DR

- **Close** the native-platform "empty `model_mapping` = catch-all passthrough" hole:
  an account with an empty mapping serves *any* client model id, including an
  upstream-new id that has **no price** → billed `$0` (`served_zero_cost` is
  observability-only; it never rejects).
- **Decision:** at serving admission, if the billing model has **no resolvable price**
  (`!IsModelPriced`), **reject** with a clear 4xx instead of serving `$0`. Gate is
  **setting-gated, default OFF** (`SettingKeyPricedServingGateEnabled`) so P0 ships zero
  behavior change.
- This is the **runtime counterpart of the CI-time A1 guard**
  (`catalog-serving-drift.py`: every catalog/manifest id is price-resolvable). A1 only
  protects *onboarded* ids at CI; the catch-all path serves *non-manifest* ids at runtime
  with no such check. This doc closes that runtime gap.
- **Not** the rejected "price ⇒ serving" auto-mapping (§3). The gate is fail-closed
  *subtraction* (unpriced → don't serve); it never *adds* a model to any account's serving
  whitelist. It **reads** the PRICE and SERVING facts and **owns neither**.
- **Availability is preserved by auto-pricing** (§4): the first request for an unpriced —
  but candidate — model triggers a price fetch from the trusted source (official page /
  litellm mirror), writes it to the **PRICE owner only** (`channel_model_pricing` /
  overlay, aligning with the "② runtime pricing" track), and the model passes the gate on
  the next request — minutes, no human, no release. A model with **no sourceable price**
  stays blocked (loud 4xx) instead of leaking `$0`.

## 1. The gap (grounded in code)

| Fact | Evidence | Consequence |
| --- | --- | --- |
| Empty `model_mapping` = allow-all | `Account.IsModelSupported` (`account.go:639`): `len(mapping)==0 → return true // 无映射 = 允许所有` | A native catch-all account (e.g. a Vertex account with mapping cleared) serves *any* client model id, including upstream-new ids. |
| Unpriced never blocks | `gateway_service_tk_served_zero_cost.go`: *"计价不确定时系统选择免费放行(unpriced never blocks)… 不拒绝服务、不改金额，纯可观测性"* | An unpriced served id is billed `$0`; the only response is an after-the-fact P0 Feishu alert. |
| Price resolution can fail open | `billing_service.go:744`: `GetModelPricing` returns `ErrModelPricingUnavailable` when neither dynamic (litellm/overlay/`channel_model_pricing`) nor fallback pricing exists; the funnel records `$0` and serves. | Revenue leak window = time from upstream ships a model → operator notices the P0 → hot-prices it. |
| A1 is CI-time only | `pricing-serving-single-source-of-truth.md` §3: A1 asserts every catalog/manifest id is price-resolvable — **at CI**. | The catch-all serves *non-manifest* ids that A1 never sees. The runtime has no equivalent gate. |
| newapi is already gated | `account_service_tk_newapi_mapping.go` (`validateNewapiAccountModelMapping`) + `universal_routing_tk_serving.go` (`groupServesModel`): empty mapping on multi-vendor `newapi` is a config error, blocked at write + routing. | The gap is **native single-vendor platforms only** (anthropic / openai / gemini / antigravity), where empty = intentional passthrough. |

**The hole is narrow and specific:** native-platform catch-all accounts serving
upstream-new, unpriced ids at `$0`. Everything else (newapi, onboarded manifest ids) is
already covered.

## 2. The decision — price-as-gate at serving admission

**Invariant (the one rule):** for every gateway request, after the billing model id is
resolved and before the upstream forward, if `!PricingCatalogService.IsModelPriced(billingModel, platform)`
then **reject** with `4xx model_not_priced` ("this model is not currently available")
— *unless the gate is disabled* (default).

- **Gate point:** request admission, reusing the existing price predicate
  `PricingCatalogService.IsModelPriced(modelID, platform)` (`pricing_catalog_membership_tk.go:51`),
  already the catalog/`/v1/models` filter (`model_list_filter_tk.go:48`). Same predicate,
  now also enforced on the *serving* path — so "listed" and "servable" finally agree.
- **Setting:** `SettingKeyPricedServingGateEnabled`, resolved via
  `SettingService.IsPricedServingGateEnabled(ctx)` (the `IsSignupBonusEnabled` template,
  `setting_service_tk_cold_start.go:84`). **Default `false`** → zero behavior change at P0.
- **Companion file:** a `*_tk_*.go` admission helper (e.g.
  `gateway_handler_tk_priced_serving_gate.go`) called from the gateway entry; the upstream
  handler gains one import + one guard call (rule §5 minimal-invasion).
- **Reject shape:** a real 4xx with a stable error code the client can read, not a 5xx and
  not a silent `$0` success. Emit a structured `priced_serving_gate.rejected` log
  (model, platform, api_key/group) so ops sees enforcement, symmetric to `served_zero_cost`.

**Why fail-closed, not "serve + alert":** "serve + alert" optimizes for *never rejecting a
request* at the cost of (a) silent revenue leak and (b) shipping an un-vetted model to a
paying customer. The Jobs verdict: never ship what you have not priced/tasted. The cost of
fail-closed — rejecting a just-released model — is removed by §4 auto-pricing, not paid.

## 3. Reconciliation with the existing approved design (no contradiction)

This change sits **on top of** the SSOT body, not against it.

- **`pricing-serving-single-source-of-truth.md` — "One Owner Per Fact".** SERVING is owned
  by per-account `model_mapping`; PRICE by overlay + `channel_model_pricing`. **The gate
  owns neither fact.** It is a cross-cutting *billing-integrity admission rule* — "we do not
  serve what we cannot bill" — that **reads** both facts and **mutates** neither. It never
  writes `model_mapping` and never writes a price.
- **It is NOT the REJECTED "align the whitelist to the overlay."** That rejection forbids
  *price-presence ⇒ auto-map onto an account* (the #812 illusion: priced-but-not-served →
  `429/503`). This gate is the **opposite polarity**: *price-absence ⇒ do-not-serve*. It
  *subtracts* from what a catch-all would serve; it never *adds* a serving claim. The
  #812 failure mode (priced but unmapped) is unaffected — that id is mapped or it isn't,
  independent of this gate.
- **It is the runtime counterpart of A1.** `catalog-serving-drift.py` A1 already asserts
  *every manifest/catalog id is price-resolvable* — but only at CI, and only for onboarded
  ids. The catch-all serves non-manifest ids at runtime. This gate enforces the **same
  predicate at runtime**, closing the one surface A1 cannot see.
- **`pricing-availability-source-of-truth.md`** already made `/pricing` and every model-list
  endpoint emit only `priced` ids, with the stated goal *"Empty pools surface as errors, not
  silently-served unreachable models."* This gate extends that goal from the **list** surface
  to the **serving** surface: a model the catalog won't *list* (unpriced) is now also a model
  the gateway won't *serve*. Same predicate, both surfaces — "listed ⟺ servable" becomes true.
- **"Upstream is a feed into a human decision, never the decision"** (§2.4 / R-002) is
  respected by §4: auto-pricing fetches a *price* from a trusted source and writes the PRICE
  fact; it **never** auto-onboards serving (no `model_mapping` write). Serving stays a human
  / migration decision; only price is automated, and only from a trusted source.

## 4. Auto-pricing on first sight (phase 2 — what makes fail-closed safe)

Fail-closed without this = "reject every just-released model" = availability regression.
With it, the gate's only visible effect is that the *first* request for a brand-new model
may reject for a few minutes while the price lands.

**Pipeline (PRICE owner only, never serving):**

1. **Signal.** A gate rejection (or the existing `served_zero_cost` / `PricingMissing`
   signal) names an unpriced model that is a **candidate** (known to the catalog candidate
   set — not arbitrary client garbage).
2. **Fetch (trusted source, 禁臆造).** Resolve a price from, in order: the model's
   **official price page** (with `source` URL + capture date) → the **litellm mirror**. This
   is the exact contract the `apply-pricing-hotfix.py` runbook already encodes; phase 2
   wires it to the signal so it runs without a human for sourceable prices.
3. **Apply to the PRICE owner.** Write to `channel_model_pricing` (runtime, no release —
   the "② runtime pricing" track) or stage the durable overlay fill. **No `model_mapping`
   write.** Price precedence is unchanged (`channel_model_pricing` > overlay > litellm > Go
   fallback).
4. **Serve.** The next request resolves a price → passes the gate. A model whose price is
   **not sourceable** stays blocked (loud 4xx), surfaced for a human to price or decline —
   never a silent `$0`.

**Alignment, not duplication:** this is the demand-driven trigger for the already-staged "②
runtime pricing" work in `channel-pricing-refund-gate-and-runtime-pricing.md`. The refund
gate / validator invariants there become load-bearing here. Phase 2 does not invent a new
price writer; it triggers the planned one.

## 5. Phasing & rollout (each phase independently safe)

| Phase | What ships | Behavior change | Gate to next |
| --- | --- | --- | --- |
| **P0** | `SettingKeyPricedServingGateEnabled` (default **false**) + admission gate companion + reject-shape + structured log + tests + sentinel | **none** (gate off) | gate code reviewed; `served_zero_cost` baseline captured |
| **P1** | Auto-pricing trigger wired to the unpriced signal (§4); price-source trust contract enforced | none to serving; prices start landing automatically | auto-pricing observed to fill real gaps within minutes; no mis-sourced price |
| **P2** | Flip the default **per platform** (start with the catch-all platform, e.g. gemini/Vertex), with soak | unpriced ids now rejected instead of `$0`-served | per-platform: `served_zero_cost` for that platform reads ~0 over the soak window |

P2 is per-platform and reversible (flip the setting back). The manual catch-all "safety
ritual" in `tokenkey-servable-model-refresh` (probe → price → soak → clear mapping) is
**retired** once P2 holds: the machine enforces *priced ⟺ servable*; humans only approve
prices the auto-fetch cannot source. **No `allow_unpriced` escape hatch** — one rule, no
per-account flag (flags are where the discipline dies); the only knob is the global
platform-scoped setting, used for staged rollout, not as a permanent bypass.

## 6. Risks & non-goals

- **R1 — availability regression.** Fail-closed before auto-pricing = reject new models.
  *Mitigation:* default-OFF at P0; default flips only after P1 (auto-pricing) is live and
  per-platform soaked at P2.
- **R2 — a legit free model.** A genuinely-free model (rate-multiplier 0 group, a `$0`-by-
  policy id) must not be rejected as "unpriced". *Mitigation:* the gate keys on
  `IsModelPriced` (price *entry exists*), not on `cost==0`. A priced-at-zero policy id still
  has an entry; `negative_multiplier` / free-group semantics (`served_zero_cost`) are
  untouched.
- **R3 — predicate drift.** If `IsModelPriced` and the actual billing resolver
  (`GetModelPricing`) disagree, the gate could admit a model that then bills `$0` (or reject
  one that would price). *Mitigation:* a test asserting `IsModelPriced(m) ⟺ GetModelPricing(m) != ErrModelPricingUnavailable`
  for the candidate set; both already share the catalog parse (`pricing_catalog_supported_models_tk.go:230`).
- **Non-goal — auto-onboarding serving.** This never writes `model_mapping`. A model becomes
  *servable* only by the existing human/migration path; this only governs *whether an
  already-mapped/passthrough model with no price is allowed through*.
- **Non-goal — converging serving to upstream.** Unchanged from the rejected option in the
  SSOT doc; upstream stays a feed into a human decision.

## 7. Mechanical enforcement (every rule a gate)

- **Sentinel** (`scripts/sentinels/*.json`): pin the gate call site + `IsModelPriced` usage
  in the admission helper, so an upstream merge / refactor cannot silently drop the gate.
- **Preflight test:** the R3 predicate-parity test + a gate-on/gate-off unit test (gate off ⇒
  unpriced served as today; gate on ⇒ unpriced rejected 4xx, priced served).
- **Setting default test:** assert `SettingKeyPricedServingGateEnabled` cold-start default is
  `false` (the P0 safety invariant), mirroring the §9.1-style "default stays safe" guards.

## 8. Open questions (for approval)

1. **Reject status/code.** `403 model_not_priced` vs `404 model_not_found` vs `400`? (Lean
   `403` — the model exists upstream but is not *permitted* until priced; distinguishable
   from a true unknown-model `404`.)
2. **P2 first platform.** Confirm gemini/Vertex (the catch-all hot spot) as the first
   default-flip, others to follow.
3. **Auto-pricing autonomy.** Fully auto-apply a litellm-sourced price, or auto-apply only
   official-sourced and queue litellm-only for one-click human confirm? (Trust vs latency.)
4. **Scope of P1 price writer.** Land "② runtime pricing" (`channel_model_pricing` gains the
   write path) as the P1 dependency, or have P1 stage overlay fills until ② is ready?
