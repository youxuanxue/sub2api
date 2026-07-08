# TokenKey model operations

TokenKey model operations keep four facts separate:

| Fact | Owner |
| --- | --- |
| Runtime serving | per-account `accounts.credentials.model_mapping` |
| Price | `channel_model_pricing` + `tk_pricing_overlay.json` + litellm mirror |
| Public catalog + user menu surface | `pricing_catalog_supported_models_tk.go` |
| Curated newapi intent | `tk_served_models.json` |

The public `/pricing` catalog and the per-user "Your Menu" have converged on
the same servable surface: `supportedCatalogModelIDsForPlatform` feeds the menu
fallback, and `FilterPublicCatalogToServable` filters the public catalog from
the same empirical sets. These sets live in
`backend/internal/service/pricing_catalog_supported_models_tk.go` between
`servable-allowlist:begin/end <platform>` markers. The refresh tool rewrites the
anthropic/openai/gemini blocks from live probes; antigravity and grok are still
hand-maintained empirical sets in the same file.

## Files

| File | Role |
| --- | --- |
| `probe-servable-models.sh` | Runs on prod or an edge via `ops/observability/run-probe.sh`. Sends one minimal real request per candidate model and emits `platform⇥model⇥http⇥verdict` TSV. A model is **servable** iff it returns a real `200`. Always auto-ensures reusable `__tk_probe_<scope>_group` / `__tk_probe_<scope>_key` per platform via `probe_reserved_resources.sh` (no direct-key fallback, no dependency on `TK_SMOKE_API_KEY` or customer keys). The companion is mandatory — deliver it with `run-probe.sh --with ops/pricing/probe_reserved_resources.sh` (the orchestrator and every manual invocation below do). |
| `probe-antigravity-gemini25pro-literal.sh` | Focused prod probe for literal Antigravity chat ids on the `Google-Gemini` source group (default: `gemini-pro-agent`, `gemini-2.5-pro`). Hits both `/v1/chat/completions` and `/antigravity/v1beta/models/{id}:generateContent`, emits TSV plus a non-secret account snapshot. Companion: `probe_reserved_resources.sh`. Used when the broad servable refresh batch cannot distinguish generateContent timeout vs routing gaps for `gemini-2.5-pro`. |
| `probe_reserved_resources.sh` | Shared DB helpers for reserved probe groups/keys (same namespace as `tokenkey-account-model-probe`). Per-scope `flock` on `/tmp/tokenkey-account-model-probe-<scope>.lock` serializes `account_groups` mutations vs account-model probes. Catalog refresh copies schedulable accounts from canonical source group ids by default, probes, then clears `account_groups` bindings and releases locks on EXIT. Group-name overrides are legacy diagnostics only. |
| `probe-traffic-proven-models.sh` | Runs on prod via `ops/observability/run-probe.sh`. Read-only over `usage_logs`: emits `platform⇥model⇥hits` for every model that served **real successful traffic** in the last `TRAFFIC_HOURS` (default 24). Feeds the `--skip-proven-by-traffic` short-circuit below. Positive evidence only — a model with no recent traffic is simply absent (never an unsupported signal). |
| `refresh-servable-allowlist.py` | Refreshes the shared public-catalog/user-menu servable sets. It derives candidates, runs probes (uploads `probe_reserved_resources.sh` via `run-probe.sh --with`), keeps `verdict==servable`, de-duplicates dated snapshots, and splices the anthropic/openai/gemini Go blocks. `selftest` covers deterministic glue (no prod). Optional `--skip-proven-by-traffic` short-circuits candidates already proven by 24h traffic out of the probe batches. |
| `modelops.py` | Read-only planner for model operations: compares upstream/admin discovery, probe TSV, pricing state, manifest intent, optional live `model_mapping` snapshots, and mirror policies such as `60 -> 72`. Prints probe commands and guarded apply dry-runs; never writes accounts or pricing. |
| `reconcile-served-models.py` | Compatibility wrapper for `modelops.py`. New runbooks should call `modelops.py`. |
| `manage-account-model-mapping-runtime.py` | Hot-pushes optional runtime replacement scopes to `settings.tk_account_model_mapping_runtime` across prod + deployable edges, validates/diffs runtime blobs, runs post-release read-only `check-accounts` (prod by default), and applies reviewed account/group diffs only through explicit `apply-accounts --confirm ...`. |
| `apply-pricing-hotfix.py` | Companion runbook for the **"模型缺价（已记零成本）" Feishu alert** (PricingMissingNotifier). Hot-applies channel pricing via the prod admin API (immediate, no release) and stages the durable fill-only entry into `tk_pricing_overlay.json`. `selftest` covers all pure logic (no network). See "Pricing-missing hotfix" below. |

## Re-run (operator, needs AWS creds for prod SSM)

```bash
# 0. preview the candidate split (no prod)
python3 ops/pricing/refresh-servable-allowlist.py candidates

# 1. probe + rewrite the Go allowlist blocks in one shot
python3 ops/pricing/refresh-servable-allowlist.py run

# 2. review the Go diff, then open a PR (or pass --open-pr to step 1)
cd backend && go test -tags=unit ./internal/service/ -run PublicCatalog
git diff backend/internal/service/pricing_catalog_supported_models_tk.go
```

Split the steps when you want to inspect the raw verdicts first:

```bash
python3 ops/pricing/refresh-servable-allowlist.py probe | tee /tmp/servable.tsv
python3 ops/pricing/refresh-servable-allowlist.py apply --results /tmp/servable.tsv
```

### 24h-traffic short-circuit (`--skip-proven-by-traffic`)

A full probe is ~160 models in ~16 SSM batches (8–15 min). Many of those models
already served **real successful traffic** in the last day — re-probing them is
pure latency. `--skip-proven-by-traffic` (or env `REFRESH_SKIP_PROVEN_BY_TRAFFIC=1`)
queries `usage_logs` once up front and skips the probe for any candidate already
proven servable, cutting the batch count:

```bash
python3 ops/pricing/refresh-servable-allowlist.py run --skip-proven-by-traffic
# tune the window (default 24h):
python3 ops/pricing/refresh-servable-allowlist.py run --skip-proven-by-traffic --traffic-hours 48
```

It logs exactly what it skipped, for human review:

```
[refresh] skipping 37 models proven by 24h traffic: anthropic/claude-opus-4-8, openai/gpt-5.4, …
[refresh] probing 41 models in 5 batch(es) of <= 12 …
```

**Why it is safe (purely additive — read the contract before changing it):**

- **Traffic success = servable** is firm positive evidence; **no traffic ≠
  unsupported**. A candidate that did not show up in the window is *still probed*
  normally — the short-circuit only ever removes probes, never marks anything
  unsupported. Default is **off** so the conservative full probe stays the baseline;
  enable it as a graduated rollout.
- **Only candidates can be skipped/added.** The proven set is intersected with the
  derived candidate set, so traffic can never inject a model that is not already a
  priced/known candidate.
- **Platform bucket comes from the candidate set, not the serving account.** Vertex
  is served under `accounts.platform='newapi'`, so a served `gemini-2.5-pro` is
  bucketed as `gemini` because that is its *candidate* platform. The script's
  `usage_logs` row only needs the model id to match.
- **Blocked models cannot revive.** skiplist/deadlist entries are already absent
  from the candidate set, and the proven set is additionally re-checked against the
  reprobe ledger (`validate_results_against_reprobe_ledger`) — one successful
  request cannot bring a deadlisted model back.
- A `usage_logs` row is a **metered** request (errors that burn no tokens are not
  logged); the query additionally requires real generation (tokens / image / video)
  so a `$0` placeholder row never counts as proof.

## Modelops planner (read-only)

**Operator entry:** skill `tokenkey-modelops-planner` (`.cursor/skills/tokenkey-modelops-planner/SKILL.md`).
Script implementation below; do not treat this README as the primary runbook.

For newapi long-tail, live runtime mapping checks, and mirror-account
operations, use the planner to turn discovery/probe/pricing/runtime facts into
a reviewable plan:

```bash
# Generate read-only SQL for a live model_mapping snapshot, then run it through
# the normal prod DB access path and save the JSON result locally.
python3 ops/pricing/modelops.py snapshot-sql --accounts 60,72

# Compare upstream discovery, probe results, live mapping, and Qwen -> Qwen-2 mirror drift.
python3 ops/pricing/modelops.py plan \
  --upstream 60:/tmp/qwen_upstream_models.json \
  --probe-results /tmp/qwen_probe.tsv \
  --live-mapping /tmp/qwen_mapping_snapshot.json \
  --mirror 60:72
```

The planner's output is intentionally an operator plan, not an apply loop:
`probe_needed` includes grouped `run-probe.sh` commands, `price_missing` points
to `apply-pricing-hotfix.py lookup`, `mapping_missing` prints guarded
`apply-model-mapping-live.py --dry-run` commands, and `mirror_drift` reports
exact key/value differences. It also names the shared catalog/menu surface so
operators do not hand-maintain a second menu list. Apply still goes through
migrations, the guarded live model-mapping tool, `refresh-servable-allowlist.py`,
or pricing-hotfix after review.

## Account model_mapping runtime hot update

Operator entry: skill `tokenkey-modelops-planner`, branch D. The runtime JSON is
a scope replacement layer: each listed platform or newapi `channel_type`
replaces the compiled account mapping floor for that scope; omitted scopes keep
the compiled floor.

```bash
python3 ops/pricing/manage-account-model-mapping-runtime.py --selftest
python3 ops/pricing/manage-account-model-mapping-runtime.py validate --file /tmp/account-model-mapping-runtime.json
python3 ops/pricing/manage-account-model-mapping-runtime.py check --file /tmp/account-model-mapping-runtime.json

# after review, only updates settings on prod + deployable edges (does not mutate accounts):
python3 ops/pricing/manage-account-model-mapping-runtime.py sync-runtime --file /tmp/account-model-mapping-runtime.json

# post-release / post-hotfix read-only diff (prod only; add --include-edges for deployable edges):
python3 ops/pricing/manage-account-model-mapping-runtime.py check-accounts --json

# after reviewing the diff, explicitly apply account/group changes:
python3 ops/pricing/manage-account-model-mapping-runtime.py apply-accounts \
  --target all-deployable-and-prod \
  --confirm yes-apply-account-model-mapping
```

## Pricing-missing hotfix (Feishu「模型缺价」告警的处置 runbook)

Unpriced models are **served and recorded at zero cost** (never refused —
pricing data lag must not become a customer-facing outage); the
`PricingMissingNotifier` Feishu card tells you which `(platform, model)` is
leaking. Remediation is two-step, mirroring the TLS-fingerprint / tiers
"repo baseline + live push" hot-update shape:

```bash
# 0. what does litellm (FULL source, incl. provider-prefixed keys the trimmed
#    mirror drops) say this model costs?
python3 ops/pricing/apply-pricing-hotfix.py lookup --model doubao-seedream-9

# 1. HOT (immediate, no release): upsert channel pricing via prod admin API.
#    Channel pricing (DB) overrides every other pricing source; the channel
#    cache invalidates on write. Dry-run by default; --yes to commit.
export TOKENKEY_ADMIN_API_KEY=...   # settings.admin_api_key
python3 ops/pricing/apply-pricing-hotfix.py channels   # pick --channel-id
python3 ops/pricing/apply-pricing-hotfix.py apply \
  --model doubao-seedream-9 --channel-id 4 --platform newapi --from-litellm --yes

# 2. DURABLE (next release): append the overlay entry to
#    backend/internal/service/tk_pricing_overlay.json and open a PR.
#    Fill applies when the mirror key is absent OR an all-zero placeholder
#    (litellm's "cost unknown" — see tkIsEffectivelyUnpriced); self-deprecating:
#    the day the mirror carries a real non-zero price under the bare key, the
#    source value wins. For models litellm lacks entirely, use
#    --entry-json with the provider's official list price.
python3 ops/pricing/apply-pricing-hotfix.py stage-overlay \
  --model doubao-seedream-9 --from-litellm
python3 scripts/checks/pricing-overlay.py && bash scripts/preflight.sh
```

Caveats: channel pricing is per-channel — if the leaking traffic spans several
channels, repeat `apply` per channel. Mirror entries that are all-zero
placeholders self-heal via the overlay (absent-or-zero fill) and now surface the
pricing-missing alert instead of silently billing $0; the overlay still cannot
fix WRONG **non-zero** mirror prices (the source stays authoritative there) —
channel pricing is exactly the tool for that. Alert digest cadence is
`feishu.pricing_missing_digest_seconds` (default 1800s).

## Classification & de-dup rules

- `200` → **servable** (kept). `400/404 + retired/not-found/"not supported when
  using Codex"` → unsupported. `429/502/503` → inconclusive (capacity / wrong
  protocol / no account on the probed group). `401/403` → auth_error (probe
  setup wrong, not a model signal — fix and re-run).
- De-dup: when both a non-dated form and its dated snapshot serve
  (`-YYYYMMDD` for anthropic, `-YYYY-MM-DD` for openai), keep only the
  non-dated; drop `-thinking` pricing pseudo-entries.
- OpenAI candidates are routed by family: `*codex*` → `/v1/responses`,
  `*image*` → `/v1/images/generations` (2026-07-02: group_id=2 returns 400
  missing `api.model.images.request` scope, so GPT image stays out until an
  image-scoped account probes 200), everything else → `/v1/chat/completions`.

## Caveats

- The probe's default source pools are group-id anchored: prod `openai=2`,
  `anthropic mirror=1`, `antigravity=21`, `Vertex/newapi=16`, `Qwen/newapi=18`,
  `GLM/newapi=26`, `VolcEngine/newapi=5`; edge-native probes use `anthropic=1`
  and `grok=4` on the target edge DB. Display names are operator-editable and
  only accepted through explicit legacy `PROBE_*_SOURCE_GROUP` overrides for
  diagnostics.
- Antigravity has two distinct probe surfaces: text/capability checks use
  `ANTIGRAVITY_CHAT_MODELS` on `/antigravity/v1beta`, while Studio
  gemini-native image uses `ANTIGRAVITY_IMAGE_MODELS` on `/v1/chat/completions`.
  Do not use a v1beta image 404 as a Studio image verdict.
- VolcEngine/Ark has two distinct probe surfaces: `ARK_*` calls the upstream Ark
  data plane directly and proves account activation; `VOLCENGINE_IMAGE_MODELS`
  and `VOLCENGINE_VIDEO_MODELS` call the prod TokenKey gateway through group_id
  `5` and prove end-to-end serving after pricing + `model_mapping` are live.
- The probe tests anthropic **edge-native** — rotated across the deployable edges
  (`deployable_edges()` from `edge-targets-lightsail.json`), servable if any edge serves.
  A separate warning-only pass re-probes the edge-servable set through the prod gateway
  per mirror sub-pool (`cc-*` anthropic-OAuth + `kiro-*` Kiro) and warns on
  "edge serves but prod relay does not"; those rows never enter the allowlist. Models served
  exclusively by yet another group read inconclusive here and are dropped; provide that
  group's group id and extend the probe to re-add them.
- This is a snapshot. Re-run after the served fleet changes (new model family,
  account/tier changes, an upstream sunset).

### Antigravity `gemini-2.5-pro` literal probe

The broad servable batch times out on `gemini-2.5-pro` generateContent (see
`docs/all-platform-model-inventory.md`). When you need a focused before/after
signal without rerunning the full refresh:

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/pricing/probe-antigravity-gemini25pro-literal.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --timeout-seconds 180
```

Optional: `PROBE_MODELS='gemini-2.5-pro'` or
`PROBE_ANTIGRAVITY_SOURCE_GROUP='Google-Gemini'`.

See also `.cursor/skills/tokenkey-online-log-troubleshooting` for the prod
read-only access posture and `ops/observability/run-probe.sh` for the SSM
transport. Probe-shape gotchas (claude-cli UA gate, `metadata.user_id` string,
codex `/v1/responses`) are documented in the probe script header.
