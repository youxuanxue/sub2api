---
title: Served-Model Reconcile Planner — read-only automation for discovery/probe/price/mapping drift
status: approved
approved_by: "xuejiao (implementation directive, 2026-06-25)"
approved_at: "2026-06-25"
authors: [agent]
created: 2026-06-25
related_design: docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/newapi-served-models-reconciler.md, docs/approved/pricing-availability-source-of-truth.md
---

# Served-Model Reconcile Planner

This implements the allowed half of "automatic reconcile":

- YES: automatically compare candidate models, real probe results, pricing state, manifest
  intent, and live account mapping snapshots.
- YES: automatically print the next probe commands and existing guarded apply commands.
- NO: do not run a background job that writes `accounts.credentials.model_mapping`.

That boundary follows `pricing-serving-single-source-of-truth.md`: SERVING is owned by
per-account `model_mapping`; PRICE is owned by overlay / channel pricing. Upstream
`/models` is discovery, not authority.

## Tool

`ops/pricing/reconcile-served-models.py`

Primary command:

```bash
python3 ops/pricing/reconcile-served-models.py plan \
  --upstream 60:/tmp/qwen_upstream_models.json \
  --probe-results /tmp/qwen_probe.tsv \
  --live-mapping /tmp/model_mapping_snapshot.json \
  --mirror 60:72
```

Inputs:

- `--upstream ACCOUNT:PATH`: upstream/admin-discovered list. Accepts JSON arrays,
  `{models:[...]}`, OpenAI-style `{data:[...]}`, `{"model": "priced"}`-style maps, or
  newline lists.
- `--probe-results PATH`: TSV from `ops/pricing/probe-servable-models.sh`.
- `--live-mapping PATH`: read-only JSON snapshot of prod account `model_mapping`.
- `--mirror SOURCE:TARGET`: mirror policy, e.g. Qwen account `60` -> Qwen-2 `72`.
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

The planner is a review surface, not an apply surface.

## Snapshot SQL

For live prod read-only mapping snapshots:

```bash
python3 ops/pricing/reconcile-served-models.py snapshot-sql --accounts 60,72
```

Run the SQL through the existing prod DB access path, store the JSON object locally, then
feed it to `--live-mapping`.

## Qwen-2 Backup Policy

For `Qwen-2` account `72`, the desired invariant is:

```text
account 72 model_mapping == account 60 model_mapping
```

The planner enforces this as a diff only:

```bash
python3 ops/pricing/reconcile-served-models.py plan \
  --live-mapping /tmp/qwen_mapping_snapshot.json \
  --mirror 60:72
```

If drift appears, apply still uses the guarded live tool or a migration after review. The
planner does not copy API keys or mutate credentials.

## Preflight

`scripts/preflight.sh` runs:

```bash
python3 ops/pricing/reconcile-served-models.py --selftest
```

The selftest covers parser normalization, Qwen thinking/nonthinking probe aggregation,
pricing checks, probe-family selection, live snapshot parsing, and guarded dry-run command
generation.
