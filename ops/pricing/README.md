# Servable-model allowlist refresh

The public `/pricing` catalog and the per-user "Your Menu" only show claude +
gpt models that are **currently servable through TokenKey** — established by a
live prod probe, not by the canonical `DefaultModels` lists. The two allowlists
live in
`backend/internal/service/pricing_catalog_supported_models_tk.go`
(between `servable-allowlist:begin/end <platform>` markers) and are
**regenerated** by the tooling here. Last probe: 2026-06-05.

## Files

| File | Role |
| --- | --- |
| `probe-servable-models.sh` | Runs ON the prod host (via `ops/observability/run-probe.sh`). Pulls the edge-us7 relay key + the GPT-line key from the DB (never printed), sends one minimal real request per candidate model, emits `platform⇥model⇥http⇥verdict` TSV. A model is **servable** iff it returns a real `200`. |
| `refresh-servable-allowlist.py` | Orchestrator: derives candidates from the litellm catalog, runs the probe, keeps `verdict==servable`, de-duplicates dated snapshots, and splices the two Go maps. `selftest` covers all deterministic glue (no prod). |
| `reconcile-served-models.py` | Read-only planner for newapi long-tail reconcile: compares upstream/admin discovery, probe TSV, pricing state, manifest intent, optional live `model_mapping` snapshots, and mirror policies such as `60 -> 72`. Prints probe commands and guarded apply dry-runs; never writes accounts or pricing. |
| `apply-pricing-hotfix.py` | Companion runbook for the **"模型缺价（已记零成本）" Feishu alert** (PricingMissingNotifier). Hot-applies channel pricing via the prod admin API (immediate, no release) and stages the durable fill-only entry into `tk_pricing_overlay.json`. `selftest` covers all pure logic (no network). See "Pricing-missing hotfix" below. |

## Re-run (operator, needs AWS creds for prod SSM)

```bash
# 0. preview the candidate split (no prod)
python3 ops/pricing/refresh-servable-allowlist.py candidates

# 1. probe + rewrite the Go allowlist in one shot
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

## Served-model reconcile planner (read-only)

For newapi long-tail and mirror-account operations, use the planner to turn
discovery/probe/pricing/runtime facts into a reviewable plan:

```bash
# Generate read-only SQL for a live model_mapping snapshot, then run it through
# the normal prod DB access path and save the JSON result locally.
python3 ops/pricing/reconcile-served-models.py snapshot-sql --accounts 60,72

# Compare upstream discovery, probe results, live mapping, and Qwen -> Qwen-2 mirror drift.
python3 ops/pricing/reconcile-served-models.py plan \
  --upstream 60:/tmp/qwen_upstream_models.json \
  --probe-results /tmp/qwen_probe.tsv \
  --live-mapping /tmp/qwen_mapping_snapshot.json \
  --mirror 60:72
```

The planner's output is intentionally an operator plan, not an apply loop:
`probe_needed` includes grouped `run-probe.sh` commands, `price_missing` points
to `apply-pricing-hotfix.py lookup`, `mapping_missing` prints guarded
`apply-model-mapping-live.py --dry-run` commands, and `mirror_drift` reports
exact key/value differences. Apply still goes through migrations, the guarded
live model-mapping tool, or pricing-hotfix after review.

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
  `*image*` → `/v1/images/generations` (best-effort — the GPT-line group has no
  image account, so these read inconclusive and drop), everything else →
  `/v1/chat/completions`.

## Caveats

- The probe tests anthropic through the **edge-us7** relay and openai through
  the **GPT-line** group only. Models served exclusively by another group
  (e.g. an image / dedicated-codex pool) read inconclusive here and are
  dropped; provide that group's key and extend the probe to re-add them.
- This is a snapshot. Re-run after the served fleet changes (new model family,
  account/tier changes, an upstream sunset).

See also `.cursor/skills/tokenkey-online-log-troubleshooting` for the prod
read-only access posture and `ops/observability/run-probe.sh` for the SSM
transport. Probe-shape gotchas (claude-cli UA gate, `metadata.user_id` string,
codex `/v1/responses`) are documented in the probe script header.
