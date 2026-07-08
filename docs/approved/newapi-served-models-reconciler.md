---
title: newapi Served-Models Reconciler — DECIDED: do NOT build an unattended auto-sync
status: approved
approved_by: "xuejiao (design directive, this session)"
approved_at: "2026-06-17"
authors: [agent]
created: 2026-06-17
updated: 2026-06-17
related_prs: []
related_commits: []
related_stories: []
related_design: docs/approved/newapi-as-fifth-platform.md, docs/approved/pricing-serving-single-source-of-truth.md
supersedes: none
---

# newapi Served-Models Reconciler — DECIDED: do NOT build an unattended auto-sync

> **DECISION CHANGED (2026-06-17).** The prior revision of this record left an
> unattended whitelist reconciler as an "approved design, deferred implementation"
> waiting on a churn trigger. **That stance is now reversed.** After auditing the
> serving primitive against `account.go`, R-002, and the `BulkUpdate` JSONB semantics,
> the decision is: **do NOT build an unattended auto-sync reconciler at all.** It is not
> "defer until the trigger fires" — it is "this is the wrong tool, full stop." The safe
> mechanism is human-in-the-loop: the admin "fetch upstream models" discovery button +
> the `tokenkey-onboard-model` skill, gated by the #819 static drift guard.

## 0. Context (unchanged)

TokenKey serves a TK-curated long-tail of `newapi` (fifth-platform) models — Qwen and
DeepSeek families — on two dedicated single-account groups:

| account | name | platform | channel_type | group |
| --- | --- | --- | --- | --- |
| 60 | Qwen | `newapi` | 17 (Ali/DashScope) | 18 |
| 39 | ds-官 DeepSeek | `newapi` | 43 (DeepSeek) | 11 |

A model is "served" on one of these accounts only when its client-facing id appears as a
key in that account's `credentials.model_mapping` (an identity WHITELIST: `key == value`).
The #812-class regression (qwen3 dense ids priced but unmapped) motivated asking whether a
reconciler could make "add a model" need no per-account migration. The answer below is **no
— building it is actively harmful.** #818's `tk_029` point-fixed the gap; #819's guard
mechanically blocks its recurrence; #820's skill is the ongoing mechanism.

## 1. DECISION: do NOT build an unattended newapi auto-sync reconciler

Three independent reasons, each sufficient on its own.

### 1.1 Reason 1 — allow-all inversion (the reconciler does the OPPOSITE of "auto-sync new models")

`Account.IsModelSupported` (`backend/internal/service/account.go:639`):

```go
func (a *Account) IsModelSupported(requestedModel string) bool {
	mapping := a.GetModelMapping()
	if len(mapping) == 0 {
		return true // 无映射 = 允许所有
	}
	...
}
```

For a non-antigravity platform, an **empty** `model_mapping` means **allow-all**. An
empty-mapping newapi account **already serves whatever the upstream channel exposes** —
including new models the day upstream adds them, with zero TK action. Auto-populating the
mapping does not *add* models; it **NARROWS allow-all into a snapshot-frozen restrictive
allowlist**. The reconciler's effect is the exact opposite of its stated goal: instead of
"auto-sync new models in," it would freeze the account to the manifest snapshot and start
*rejecting* anything not in it. The naming ("served-models reconciler", "auto-sync") hides
that inversion — which is precisely why it must not be built unattended.

### 1.2 Reason 2 — there is no trustworthy source to converge toward (R-002)

A converging reconciler needs an authoritative desired-state. Upstream `/v1/models` is
**not** it — per `docs/approved/pricing-availability-source-of-truth.md` §2.4 / R-002 it is
an unvetted discovery feed that over-reports deprecated, retired, disabled, served-but-
unpriced, and embedding-only ids. `FetchUpstreamSupportedModels`
(`backend/internal/service/upstream_models.go:76`) returns a bare `[]string` with no pricing
or availability tag and is deliberately not wired to `DiscoveryFilter`. An unattended
reconciler converging toward this feed would freeze the account to a snapshot polluted with
ids the operator must not serve — and freezing *to* a polluted snapshot is worse than the
allow-all it replaces.

The account model_mapping SSOT (`account_model_mapping_ssot_tk.go`) is safe
**only because** explicit ops apply converges toward reviewed in-repo desired-state:
`supported*CatalogModels`, `tk_served_models.json` display projection, and explicit
compatibility aliases. It performs **zero upstream discovery I/O**. Server runtime must
not run an unattended account-mapping reconciler toward `FetchUpstreamSupportedModels`,
because that feed can include unpriced, retired, or operator-unapproved ids.

### 1.3 Reason 3 — `BulkUpdate` is the wrong primitive for add-only grow

`AccountRepository.BulkUpdate` (`backend/internal/repository/account_repo.go:1548`) merges
credentials with PostgreSQL JSONB `||`:

```go
setClauses = append(setClauses,
  "credentials = COALESCE(credentials, '{}'::jsonb) || $N::jsonb")
```

`||` is a **top-level shallow** merge: writing `{"model_mapping": {...}}` **REPLACES the
whole `model_mapping` sub-object**, it does not deep-merge entries into it. So the only
write primitive available cannot do add-only grow — it clobbers. A reconciler built on it
would either (a) read-modify-write the full union each tick (re-introducing the read-state
trust problem and a write-thrash race with concurrent admin edits), or (b) replace and
silently **drop operator-added ids** — a §5.x silent-deletion violation. And the values in
`model_mapping` are **rename targets**, not boolean flags, so a generic "merge flags"
primitive does not even model the data correctly.

### 1.4 Cost/benefit — it is more work for less than assumed

Estimated **3–5 days** (reconciler + canonical derivation + wire DI + cleanup edge +
sentinel + tests, per the prior §3 checklist) for a feature that, because of Reason 1,
**earns less than assumed**: empty-mapping accounts already serve new models for free, so
the reconciler's net effect on a well-run account is to *restrict*, not enable. The honest
verdict: the reconciler solves a problem the allow-all primitive already solves, while
adding an inversion footgun, a polluted-snapshot risk, and a clobbering write.

## 2. The safe alternative (already shipped, human-in-the-loop)

Adding a newapi model stays a small reviewed action, not an autonomous loop:

1. **Discovery** — the admin "fetch upstream models" button surfaces the upstream list run
   through `DiscoveryFilter` (`discover_filter_tk.go`), which drops explicitly-unavailable /
   `unreachable` ids and tags the rest `priced | missing`. A human reads the badges and
   **confirms** — upstream is a suggestion, never an auto-apply.
2. **Onboard** — the `tokenkey-onboard-model` skill (#820) drives the add: map the id onto
   the account (migration or admin-UI edit), ensure a price exists (overlay 固化 or
   `channel_model_pricing` 热更), update the manifest intent row.
3. **Gate** — `scripts/checks/catalog-serving-drift.py` (#819) hard-fails CI if the result
   drifts: A1 price-resolvable, A2 display⇒Go map, A3 served_on⇒migration-mapped (the #812
   catcher). The static guard is the mechanical safety net the reconciler was supposed to
   provide — without the inversion, the pollution, or the clobber.

This keeps the SERVING fact owned by the per-account `model_mapping` and the human review
gate intact, which is the SSOT invariant in
`docs/approved/pricing-serving-single-source-of-truth.md`.

## 3. What about true-runtime (no-release) onboarding?

Whitelisting an id still costs a migration-or-admin-edit, and its price still costs an
overlay rebuild **unless** the price is hot-added via `channel_model_pricing`. The path to
true-runtime onboarding is **not** this reconciler — it is the staged ② runtime-pricing
track, which is blocked on a refund-safety gate. See
`docs/approved/channel-pricing-refund-gate-and-runtime-pricing.md` (PR-A gate, then
② runtime pricing, then the deploy-ordering hazard). The reconciler does nothing to unblock
that track and is not a prerequisite for it.

## 4. Out of scope (unchanged)

- **The four native platforms** (anthropic/openai/gemini/antigravity) — Go servable-
  allowlist maps in `pricing_catalog_supported_models_tk.go` + `tokenkey-servable-model-
  refresh`. Not governed by 39/60 `model_mapping`.
- **grok** — native seventh platform (#791), served by platform routing + ch48 API-key
  relay, not a 39/60 `model_mapping`.
- **The runtime ops scan** — read-only observability
  (`SELECT id, credentials->'model_mapping' FROM accounts WHERE id IN (39,60) AND
  deleted_at IS NULL`), never a PR gate. Confirms repo intent matches live prod; not a
  reconciler.
