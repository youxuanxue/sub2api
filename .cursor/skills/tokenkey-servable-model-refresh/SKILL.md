---
name: tokenkey-servable-model-refresh
description: >-
  Refresh TokenKey's shared public catalog and user-menu empirical model sets after the
  modelops hub selects the catalog branch. Use for probe/run/apply review and transient
  probe judgment; not for account mapping activation or pricing publication.
---

# Catalog and menu empirical-set refresh

Enter through `tokenkey-modelops-planner`. This skill only refreshes the empirical sets
consumed by both `FilterPublicCatalogToServable` and
`supportedCatalogModelIDsForPlatform`; it does not decide request-time delivery.

The deterministic workflow is owned by:

- `ops/pricing/refresh-servable-allowlist.py`
- `ops/pricing/probe-servable-models.sh`
- `ops/pricing/probe_reserved_resources.sh`
- `ops/pricing/servable-reprobe-ledger.json`

Run the Python entrypoint with `--help` for current commands and options. The script owns
candidate derivation, traffic short-circuiting, probe batching, verdict parsing,
de-duplication and Go-map splicing. Do not reproduce those algorithms in this skill.

## Invariants

- A real successful request is positive evidence. No recent traffic is not negative
  evidence and must not remove a candidate by itself.
- Apply projects `current + positive evidence - reviewed structurally_gone`; probe output
  alone never authorizes removal.
- `--skip-video` must preserve existing video entries; video probes may create paid
  upstream tasks.
- 401/403 means probe setup or credential failure. 429/5xx/timeout is inconclusive.
  Only an explicit retired/not-found response is structural negative evidence.
- A local floor rejection is not an upstream verdict. Use the account-model probe or a
  provider-direct probe before changing the empirical set.
- Platform-specific endpoints and reserved source groups are resolved by the scripts and
  live configuration. Do not hard-code account IDs, group IDs, edge IDs or fleet snapshots
  in skill text.
- Probe results for a platform that is not supported by the automatic splice remain
  review evidence; do not manually reinterpret them as an automatic catalog write.
- Antigravity literal chat-vs-`generateContent` diagnosis uses
  `probe-antigravity-gemini25pro-literal.sh`; its output is review evidence only and never
  enters the automatic splice.
- Paid or high-cost media probes must be limited to models actually under review.
- Review unexpected bulk additions/removals before opening a PR. Merge authorization stays
  with a human.
- Durable explicit candidates belong in ledger `probe_candidates`; time-bounded follow-up
  belongs in `watchlist`. Use `watchlist-status` for its machine-readable stale set.

## Pricing findings

Pricing-missing notifications have distinct outcomes: a request may be rejected by the
priced-serving gate, billed at a family floor, or reach a true zero-cost leak path. Read
the notification reason before acting. Global price changes modify the complete registry
and publish through protected main; `channel_model_pricing` is only a deliberate scoped
override.

## Validation

After a refresh, inspect the allowlist diff and run the script selftest plus the catalog
service tests named by `ops/pricing/README.md`. Tests must derive positive sets from the
canonical allowlist owner.
