---
title: newapi Served-Models Reconciler + Runtime-Config Decision (Phase 3, design only)
status: approved
approved_by: xuejiao (design directive, this session)
approved_at: 2026-06-17
authors: [agent]
created: 2026-06-17
related_prs: []
related_commits: []
related_stories: []
related_design: docs/approved/newapi-as-fifth-platform.md
supersedes: none
---

# newapi Served-Models Reconciler + Runtime-Config Decision (Phase 3, design only)

> Status: **APPROVED DESIGN, DEFERRED IMPLEMENTATION.** This is a decision record, not
> a shipped feature. It depends on the keystone artifact
> `backend/internal/service/tk_served_models.json` (the served-models MANIFEST) and its
> drift guard `scripts/checks/catalog-serving-drift.py` (the "Foundation PR"). Build the
> reconciler described in §1 only when the trigger in §2 fires; until then the
> `tokenkey-onboard-model` skill + the static drift guard are the operating mechanism.

## 0. Context and the problem this solves

TokenKey serves a TK-curated long-tail of `newapi` (fifth-platform) models — Qwen and
DeepSeek families — on **two dedicated single-account groups**:

| account | name | platform | channel_type | group |
| --- | --- | --- | --- | --- |
| 60 | Qwen | `newapi` | 17 (Ali/DashScope) | 18 |
| 39 | ds-官 DeepSeek | `newapi` | 43 (DeepSeek) | 11 |

A model is "served" on one of these accounts only when its client-facing id appears as a
key in that account's `credentials.model_mapping` (an identity WHITELIST: `key == value`).
Today that whitelist is set by numbered `tk_NNN_*_model_mapping*.sql` migrations
(`tk_021`, `tk_022`, `tk_024`, `tk_027`) and by ad-hoc admin-UI edits. **Adding a model
therefore requires a new migration** — a release — even though the price can be hot-added
via channel pricing or the overlay.

This is the exact failure mode behind the **#812-class regression**: `qwen3-8b`,
`qwen3-14b`, `qwen3-32b` are priced in `tk_pricing_overlay.json` (added 2026-06-17) but
**no migration maps them onto account 60** — the highest mapping migration is `tk_027`
(`tk_028` only touches the `model_availability` platform CHECK for grok). The pool is
empty for those three ids → `429`/`503` in prod, with the price present making it look
served. `ls backend/migrations/tk_029*` returns nothing as of this writing — the gap is
real and is what the static drift guard hard-fails on.

Two questions follow, and this record answers both:

1. **Can we make "add a model" need no per-account migration?** → Yes, via a
   reconciler that converges accounts 39/60 toward a canonical whitelist derived from the
   manifest. **Recommended (§1).**
2. **Can we make pricing truly runtime (no release at all)?** → Not yet. The refund-safety
   validation that gates video pricing is preflight-time; moving pricing to runtime means
   moving that validation to write-time and accepting `channel_model_pricing`'s
   silent-shadow precedence. **Explicit blocker, DEFERRED with a measurable trigger (§2).**

---

## 1. Decision: a newapi whitelist reconciler, canonical = manifest-DERIVED domain constant

### 1.1 What it does

A per-node `NewAPIServedModelsReconciler` modeled **byte-for-byte** on
`backend/internal/service/antigravity_config_reconciler.go`. Each tick (and once
immediately on boot), under a Redis leader-lock, it:

1. `accounts.ListByPlatform(ctx, PlatformNewAPI)` — list this deployment's newapi accounts.
2. For each account whose id is a manifest `served_on` target (39, 60), compute the
   **desired** `credentials.model_mapping` = the canonical identity whitelist for that
   account derived from the manifest.
3. Skip-if-aligned: only accounts whose current whitelist is **missing** a desired key are
   rewritten (drift probe; idempotent, no write thrash — same posture as
   `antigravityCanServeExcluded` at `antigravity_config_reconciler.go:229`).
4. `accounts.BulkUpdate(ctx, drifted, AccountBulkUpdate{Credentials: {"model_mapping": ...}})`
   — the JSONB shallow-merge replaces the whole `model_mapping` sub-object and enqueues a
   `scheduler_outbox` event so the change takes effect without a restart
   (`antigravity_config_reconciler.go:277-282`).

The whole point: **adding `qwen3-8b` becomes one manifest row + (its derived constant
flowing through automatically), not a `tk_029` migration.** The reconciler closes the
#812 gap structurally instead of per-incident.

### 1.2 Canonical source: manifest-DERIVED domain constant (recommended) vs DB-backed config

This is the load-bearing choice. Two candidates:

**Option A — domain constant DERIVED from the embedded manifest (RECOMMENDED).**
Mirror the proven antigravity single-source pattern exactly. There,
`GeminiOnlyAntigravityModelMapping` is **not** a hand-maintained second list — it is
`buildGeminiOnlyAntigravityModelMapping()` filtering `DefaultAntigravityModelMapping`
(`constants.go:158-169`), so "adding a gemini wire id above flows into the gemini-only set
for free." The newapi analogue:

- The manifest `tk_served_models.json` is already `//go:embed`-able from
  `backend/internal/service/` (it sits beside `tk_pricing_overlay.json`, which
  `pricing_service_tk_overlay.go:49` already embeds with `//go:embed`).
- A new derivation function — call it `NewAPIServedWhitelistFor(accountID int64)` —
  parses the embedded manifest once and returns the identity whitelist
  `{model_id: model_id}` for every entry whose `served_on` contains that account id.
- That function IS the single source. The manifest is the only place a human edits; the
  Go canonical whitelist is *derived*, never hand-kept. This is the same
  "net-zero second list" discipline as the antigravity reconciler.

**Option B — read a DB-backed config table at runtime (REJECTED for now).**
Truly runtime (edit a row, no rebuild). Rejected because: (i) it duplicates intent that
already lives in the manifest, creating a two-writer drift surface the manifest guard
cannot see; (ii) the reconciler would then trust un-reviewed DB rows to rewrite
`credentials.model_mapping` — a self-modifying config loop with no code-review gate; (iii)
it buys true-runtime *whitelisting* but the **pricing** half is still release-bound (§2),
so it solves half a problem while adding a new failure mode. Option A keeps the
review gate (manifest PR) and the single source, at the cost of a rebuild to add a model —
which is acceptable until the §2 trigger says otherwise.

**Decision: Option A.** The manifest-derived constant is the canonical source; the
reconciler converges DB toward it.

### 1.3 §5.x framing: this is a NET-ADD reconciler, it MUST NOT delete

Per CLAUDE.md §5.x (default = keep upstream capability, override; never silent-delete),
the newapi reconciler's converge step is **add-only on the whitelist**:

- It **adds** missing desired keys to `credentials.model_mapping`. It does **not** remove
  keys the manifest omits. An operator who hand-added a model via admin-UI keeps it; the
  reconciler never "cleans up" a key just because it is not in the manifest.
  - This is a deliberate divergence from the antigravity reconciler, which *replaces* the
    whole `model_mapping` (dropping claude/gpt-oss). That replace is correct there because
    the policy is exclusionary ("gemini-only"). The newapi policy is **inclusionary**
    ("at least serve the curated long-tail") — so it unions, never subtracts.
  - Mechanically: read current `model_mapping`, union with the derived desired set, write
    back only if the union differs (still idempotent / skip-if-aligned).
- It touches **per-account DB rows only**, never a global default constant and never an
  upstream-owned symbol. The manifest itself is TK-scoped (`SCOPE` = only the 39/60
  long-tail; NOT the litellm catalog, NOT the four native platforms' Go allowlist maps,
  NOT grok — grok is served by platform routing + ch48, not by a 39/60 whitelist).
- Consequence: an upstream merge cannot regress this into a deletion, and the reconciler
  can never empty a pool. The worst case is a no-op (manifest empty / accounts absent),
  which is the safe direction.

### 1.4 Interaction with the static drift guard

The Foundation PR's `catalog-serving-drift.py` already hard-fails when a `served_on`
entry has no migration (the #812 catcher). Once this reconciler ships, the guard's A3
(served_on ⇒ migration) assertion **changes meaning**: a manifest row no longer needs a
migration — it needs to be reconciled. The guard's A3 should then accept *either* a
matching `tk_*model_mapping*.sql` **or** the row being within the reconciler's derived set
(i.e. the reconciler is the migration). Until the reconciler ships, A3 stays migration-only
and `qwen3-8b/14b/32b` hard-fail — which is exactly why the hotfix `tk_029` (the
"Hotfix PR") lands first and independently of this design. **The reconciler is the
structural fix; `tk_029` is the point fix that unblocks CI now.**

---

## 2. The true-runtime (no-release) question: explicit blocker, DEFERRED

The reconciler in §1 removes the **migration** for whitelisting, but a new model still
needs a **rebuild** because (a) the manifest is `//go:embed`-ed and (b) its price comes
from the `//go:embed`-ed `tk_pricing_overlay.json` or the runtime mirror. "True runtime"
= add a model with neither a migration nor a release. That requires moving **pricing** to
runtime, and that is blocked by a refund-safety invariant.

### 2.1 The blocker: refund-safety validation is preflight-time, not write-time

Video pricing carries a money-loss invariant enforced today in
`scripts/checks/pricing-overlay.py:112-119`:

```py
if mode == "video_generation":
    failure_billing = pricing.get("failure_billing")
    if failure_billing != "success_only":
        errors.append(... "a provider that charges for failed tasks breaks the
                          terminal-failure refund — change the refund design before
                          pricing it")
```

TokenKey refunds the user in full when a video task ends `failed`; that is loss-free
**only if** the provider does not bill failed tasks. This is a **preflight-time** gate: a
human prices a video model in the overlay JSON, declares `failure_billing='success_only'`,
and CI proves it before the price can ship. It is the same class as the chat-mode
`input/output_cost_per_token > 0` and the `thinking_output_cost_per_token > 0` anchors —
all of which assume a human-reviewed, CI-gated edit.

**Moving pricing to runtime deletes that gate.** A runtime admin-API price write does not
pass through `scripts/preflight.sh`. To keep refund-safety, every check
`pricing-overlay.py` runs at preflight (recognized mode, non-zero in the mode's fields,
`failure_billing='success_only'` for video, positive thinking rate) would have to be
**re-implemented as a WRITE-TIME validator in the admin pricing API** — reject the write
at the handler before it reaches `channel_model_pricing`. That is a non-trivial,
high-blast-radius piece of new code (a refund-correctness gate on the money path), not a
config flip.

### 2.2 The second cost: accepting channel_model_pricing's silent-shadow precedence

Runtime pricing in this codebase means `channel_model_pricing` DB rows (raw-SQL, the
resolver in `model_pricing_resolver.go`), which **override everything** — including the
overlay (`pricing_service_tk_overlay.go` header: "The DB-backed ModelPricing override
... still sits above everything"). So a runtime price is a **silent shadow over the
overlay**: the overlay's CI-proven value can be masked by a DB row no guard ever saw. For
refund-safety that is the dangerous direction — a runtime row could price a
charges-on-failure video model and the preflight guard would never fire because the
overlay still says `success_only` while the live price comes from the shadowing DB row.
Accepting runtime pricing means **accepting that precedence and rebuilding the guard at
the write boundary** so the shadow itself is validated.

### 2.3 Decision: DEFER, with a measurable trigger

**Defer true-runtime pricing.** The cost is a new refund-correctness validator on the
money path plus owning the silent-shadow precedence — both justified only by sustained
add-model churn. The static guard + reconciler (§1) + the `tokenkey-onboard-model` skill
cover the current cadence with full CI safety.

**Trigger condition (build the write-time validator + runtime pricing when ALL hold):**

- Models are added to the manifest at a rate of **≥ 1/week sustained over a rolling
  month** (4+ adds in 30 days). Measure from `git log` on `tk_served_models.json` +
  `tk_pricing_overlay.json`.
- AND at least one of those adds was a **video model** (the refund-safety arm is the
  expensive part; if all adds are chat/overlay-priced the skill suffices indefinitely).
- AND a release-cadence pain signal exists: an add was blocked or delayed waiting on a
  release window (recorded in the onboarding skill's run log).

Until then, "add a model" = manifest PR + overlay/channel price + (`tk_029`-style migration
OR the reconciler once it ships) — a normal reviewed release. **The skill suffices.**

---

## 3. Mechanical add-a-reconciler checklist (8 steps)

The exact steps to land `NewAPIServedModelsReconciler`, each anchored to where the
antigravity reconciler does the same thing. This is the future implementer's recipe.

1. **Reconciler file** — new `backend/internal/service/newapi_served_models_reconciler.go`,
   structured as a copy of `antigravity_config_reconciler.go`: Redis leader-lock
   (`newapi:served:reconciler:leader`, compare-and-delete release script,
   `antigravity_config_reconciler.go:57-62`), ticker + **immediate boot pass**
   (`Start()` runs `runOnceLocked()` before the loop, `:145-164`), `Stop()` via
   `stopOnce`/`wg` (`:168-176`). Narrow store interface
   `{ ListByPlatform; BulkUpdate }` (`:67-70`) so `runOnce` is unit-testable without a
   full repo stub. Converge logic is **union, not replace** (§1.3).

2. **Canonical derivation** — add `NewAPIServedWhitelistFor(accountID int64) map[string]string`
   to a domain/service file that `//go:embed`s `tk_served_models.json`. It parses the
   manifest once (skip `_`-prefixed metadata keys, same as
   `pricing_service_tk_overlay.go:70`) and returns the identity whitelist for that account.
   This is the **single source** — no hand-kept second list (the `constants.go:160`
   `buildGeminiOnly...` discipline).

3. **Config field + default** — add
   `gateway.scheduling.newapi_served_models_reconciler_interval_seconds` to the config
   struct (beside `AntigravityConfigReconcilerIntervalSeconds`, `config.go:1181`) and
   register `viper.SetDefault("gateway.scheduling.newapi_served_models_reconciler_interval_seconds", 300)`
   (beside `config.go:2091`). `<=0` disables the goroutine (`tickInterval()` at
   `:124-133`).

4. **Provider** — add `ProvideNewAPIServedModelsReconciler(accountRepo AccountRepository,
   cfg *config.Config, redisClient *redis.Client) *NewAPIServedModelsReconciler` to
   `service/wire.go`, modeled on `ProvideAntigravityConfigReconciler` (`wire.go:278-287`):
   construct, `rec.Start()`, return.

5. **ProviderSet registration** — add `ProvideNewAPIServedModelsReconciler,` to
   `service.ProviderSet` (the `wire.NewSet(...)` block, beside
   `ProvideAntigravityConfigReconciler,` at `wire.go:645`).

6. **Cleanup edge** — add a `*service.NewAPIServedModelsReconciler` parameter to
   `provideCleanup` in `cmd/server/wire.go` (beside `antigravityConfigReconciler` at
   `cmd/server/wire.go:92`) AND a parallel `cleanupStep` that calls `.Stop()` (beside the
   `AntigravityConfigReconciler` step at `cmd/server/wire.go:218-223`). This is what forces
   wire to evaluate the side-effect provider; without the cleanup edge wire dead-codes a
   side-effect-only service.

7. **Regenerate + sentinel** — run `go generate ./cmd/server` (regenerate `wire_gen.go`;
   committing generated churn is expected per CLAUDE.md §2/§5). Then **add a sentinel
   entry** to `scripts/sentinels/newapi.json` pinning the new reconciler file and its
   load-bearing symbols (`ListByPlatform(ctx, PlatformNewAPI)`,
   `NewAPIServedWhitelistFor`, the union-not-replace marker) AND a `service/wire.go`
   `must_contain` of `ProvideNewAPIServedModelsReconciler,` (mirroring the existing
   `ProvideUpstreamBalanceSentinel,` sentinel at `newapi.json` lines 418-423 — guards the
   side-effect ProviderSet member that fails silently if an upstream merge drops it). The
   registry-update gate (`check-registry-update-gate.py`) requires this in the same PR.

8. **Unit test via narrow-interface fakes** — `newapi_served_models_reconciler_test.go`
   calling `runOnce(ctx)` directly with a fake store satisfying
   `{ ListByPlatform; BulkUpdate }`. Cases: (a) account missing a desired key ⇒ BulkUpdate
   called with the union; (b) account already a superset ⇒ no BulkUpdate (skip-if-aligned);
   (c) operator-added extra key is preserved (union, not replace — the §1.3 invariant);
   (d) non-39/60 newapi account untouched; (e) empty manifest ⇒ no-op. Run with
   `go test -tags=unit ./internal/service/ -run Reconciler`. Then `scripts/preflight.sh`
   for the sentinel + registry-update gates, and a full `go test -tags=unit ./...`
   (per the local-scope discipline: never just the service package).

---

## 4. Out of scope (recorded so the boundary is explicit)

- **The four native platforms** (anthropic/openai/gemini/antigravity) — they have Go
  servable-allowlist maps in `pricing_catalog_supported_models_tk.go` and the
  `tokenkey-servable-model-refresh` flow; the manifest's `display` field is reserved for a
  future curated newapi map but is `false` for every seed entry (newapi has no map; the
  public catalog renders these via price-presence passthrough).
- **grok** — native seventh platform (#791), served by platform routing + ch48 API-key
  relay, not by a 39/60 `model_mapping`. `grok-*` overlay rows are out of this manifest's
  mechanism by construction.
- **The runtime ops scan** (live `accounts.credentials.model_mapping` on prod 39/60 vs
  manifest) — that is a read-only observability check
  (`SELECT id, credentials->'model_mapping' FROM accounts WHERE id IN (39,60) AND
  deleted_at IS NULL`, note the soft-delete filter), never a PR gate. The static guard
  proves intent↔price↔(migration|reconciler) agree in the repo; the ops scan proves the
  repo agrees with live prod. Design-only here.
