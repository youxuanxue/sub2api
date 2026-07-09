---
title: Modelops Planner — read-only automation for discovery/probe/price/mapping/catalog drift
status: approved
approved_by: "xuejiao (implementation directive, 2026-06-25)"
approved_at: "2026-06-25"
authors: [agent]
created: 2026-06-25
related_design: docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/newapi-served-models-reconciler.md, docs/approved/pricing-availability-source-of-truth.md
---

# Modelops Planner

This implements the allowed half of automatic model operations:

- YES: automatically compare candidate models, real probe results, pricing state, manifest
  intent, and live account mapping snapshots.
- YES: automatically compare an explicitly reviewed mirror-account policy from a live
  mapping snapshot.
- YES: name the shared public catalog / user menu surface so operators do not create a
  second menu list.
- YES: automatically print the next probe commands and existing guarded apply commands.
- NO: do not run a background job that writes `accounts.credentials.model_mapping`.
- NO: do not let upstream `/models` or pricing presence write SERVING.

That boundary follows `pricing-serving-single-source-of-truth.md`: SERVING is owned by
per-account `model_mapping`; PRICE is owned by overlay / channel pricing; PUBLIC SURFACE
is owned by `pricing_catalog_supported_models_tk.go`. Upstream `/models` is discovery, not
authority.

The Jobs cut is one entry, four facts:

| Fact | Owner | Planner role |
| --- | --- | --- |
| Runtime serving | `accounts.credentials.model_mapping` | diff live snapshots and print guarded dry-runs |
| Price | `channel_model_pricing` + `tk_pricing_overlay.json` + litellm mirror | classify priced/missing and point to pricing-hotfix |
| Public catalog + user menu | `pricing_catalog_supported_models_tk.go` | identify the shared surface; refresh remains a separate apply path |
| Curated newapi intent | `tk_served_models.json` | compare manifest intent with candidates and live mapping |

## Tool

**Operator/agent entry:** `.cursor/skills/tokenkey-modelops-planner/SKILL.md` (hub; catalog refresh = branch B → `tokenkey-servable-model-refresh`).

Primary command:

```bash
python3 ops/pricing/modelops.py plan \
  --upstream "$QWEN_ACCOUNT_ID":/tmp/qwen_upstream_models.json \
  --probe-results /tmp/qwen_probe.tsv \
  --live-mapping /tmp/model_mapping_snapshot.json \
  --mirror "$SOURCE_QWEN_ACCOUNT_ID":"$TARGET_QWEN_ACCOUNT_ID"
```

Inputs:

- `--upstream ACCOUNT:PATH`: upstream/admin-discovered list. Accepts JSON arrays,
  `{models:[...]}`, OpenAI-style `{data:[...]}`, `{"model": "priced"}`-style maps, or
  newline lists.
- `--probe-results PATH`: TSV from `ops/pricing/probe-servable-models.sh`.
- `--live-mapping PATH`: read-only JSON snapshot of prod account `model_mapping`.
- `--mirror SOURCE:TARGET`: explicit mirror policy between two accounts from the same
  live snapshot.
- `--candidate ACCOUNT:MODEL`: ad hoc candidate for customer-requested models.

Outputs:

- `probe_needed`: candidates that are priced or known but still need a real 200 probe.
- `probe_commands`: grouped `run-probe.sh` commands using the right probe family.
- `ready_for_onboard`: probed 200 and priced, but not yet in manifest for that account.
- `mapping_gap_candidates`: probe returned TokenKey empty-pool `not_allowlisted`.
- `price_missing`: discovery/probe found a candidate with no price; use pricing-hotfix.
- `mapping_missing`: manifest says served, live snapshot lacks it; prints guarded dry-run
  `apply-model-mapping-live.py` commands.
- `mapping_extra_review`: live mapping has ids absent from manifest or suspicious state.
- `mirror_drift`: exact mapping diff for source/target mirror accounts.
- `surfaces`: names the owner files/tools for served intent, pricing, runtime mapping, and
  the public catalog/user menu surface.

The planner is a review surface, not an apply surface.

## Catalog/Menu Surface

The public `/pricing` catalog and the per-user "Your Menu" are already converged:

- `FilterPublicCatalogToServable` filters public catalog rows.
- `supportedCatalogModelIDsForPlatform` feeds user-menu fallback.
- Both consume the empirical sets in `pricing_catalog_supported_models_tk.go`.

Therefore #997 must not introduce another catalog/menu list. The planner only points to
the shared surface. Refresh still goes through `ops/pricing/refresh-servable-allowlist.py`
for the probe-generated blocks, or a reviewed code change for hand-maintained empirical
sets such as antigravity and grok.

## Snapshot SQL

For live prod read-only mapping snapshots:

```bash
python3 ops/pricing/modelops.py snapshot-sql --channel-type 17
```

Run the SQL through the existing prod DB access path, store the JSON object locally, then
feed it to `--live-mapping`.

## Qwen Mirror Policy

For a reviewed Qwen/DashScope mirror pair, the desired invariant is:

```text
target account model_mapping == source account model_mapping
```

The planner enforces this as a diff only:

```bash
python3 ops/pricing/modelops.py plan \
  --live-mapping /tmp/qwen_mapping_snapshot.json \
  --mirror "$SOURCE_QWEN_ACCOUNT_ID":"$TARGET_QWEN_ACCOUNT_ID"
```

If drift appears, apply still uses the guarded live tool or a migration after review. The
planner does not copy API keys or mutate credentials.

## Preflight

`scripts/preflight.sh` runs:

```bash
python3 ops/pricing/modelops.py --selftest
```

The selftest covers parser normalization, Qwen thinking/nonthinking probe aggregation,
pricing checks, probe-family selection, live snapshot parsing, and guarded dry-run command
generation, and the catalog/menu surface declaration.
