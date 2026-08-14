---
title: Pricing Registry SSOT and Protected Hot Reload
status: approved
approved_by: "feng (chat approval, 2026-08-03)"
approved_at: "2026-08-03"
authors: [agent]
created: 2026-08-03
related_prs: ["#1524 (superseded implementation)"]
related_commits: []
related_stories: ["US-043"]
related_design: docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/priced-or-it-doesnt-ship.md
supersedes: "The global price-owner and runtime-overlay sections of pricing-serving-single-source-of-truth.md and channel-pricing-refund-gate-and-runtime-pricing.md"
---

# Pricing Registry SSOT and Protected Hot Reload

## Decision

`backend/internal/service/tk_pricing_overlay.json` keeps its historical path to
avoid an upstream-conflict rename, but its meaning changes from a fill-only
overlay to the complete global pricing registry. It is the only editable owner
of global price numbers and executable global pricing policy.

Provider and LiteLLM documents are sensors. They may open a diff PR, but they
never become runtime billing input. A protected-main merge is the only global
price decision. `channel_model_pricing` remains an explicitly scoped commercial
override; it is not a second global registry.

## Focus and non-goals

This change does:

- materialize the current effective global pricing map into one complete
  registry without an unapproved billing delta;
- preserve compatibility/family alias behavior while moving every canonical
  numeric owner out of Go and into the registry;
- publish the exact protected-main registry as an atomic runtime snapshot;
- retain provider/LiteLLM comparison as automated evidence and PR generation;
- keep upstream-owned pricing download code structurally intact, but prevent
  its rows from becoming effective prices.

This change does not:

- infer serving from price presence or modify account `model_mapping`;
- auto-accept provider price changes;
- make a local operator file an alternative global owner;
- rename or delete upstream pricing files merely to make the architecture look
  cleaner;
- change a known price while migrating ownership unless that price correction
  is explicitly listed and approved.

## Data model

The repository registry remains a top-level JSON object:

- `_meta`: provenance and owner semantics, never read as a price;
- `_config`: executable global pricing policy;
- every other key: one normalized billing model owner with every billable
  dimension required by that mode.

The runtime setting stores a transport envelope whose fields are all prefixed
with `_`, so a pre-migration binary ignores it safely:

```json
{
  "_snapshot": {
    "schema_version": 1,
    "source_commit": "<protected-main sha>",
    "registry_sha256": "<sha256 of exact registry bytes>"
  },
  "_registry_gzip_base64": "<exact registry bytes>"
}
```

The digest is calculated over the exact merged registry artifact, not a second
canonicalized representation. The embedded fallback computes the same digest
at process start. Snapshot metadata is observable but does not own pricing.

## Runtime state machine

```text
embedded registry --startup--> active last-known-good
	   runtime envelope absent --------> keep embedded at startup
	   valid supported envelope ------> atomic full replacement
	   later absent/empty envelope ----> keep last-known-good + error
	   legacy/invalid envelope --------> keep last-known-good + error
	   corrected newer envelope -------> atomic full replacement
```

Runtime replacement is exact: it never unions an independently editable blob
with the embedded registry. Prices, executable policy, catalog metadata and
snapshot metadata swap together under one lock. Billing and catalog caches are
rebuilt only after validation succeeds. Setting absence selects the embedded
registry only before any runtime envelope has been accepted; a later transient
miss cannot silently roll active pricing back to release-time bytes.

Runtime precedence is:

```text
scoped channel_model_pricing override
  > active complete registry snapshot
    > embedded complete registry fallback
```

The deploy workflow never writes the runtime price setting. Therefore an older
application release cannot overwrite a newer active price snapshot. Deploy may
audit the active snapshot but must not synchronize the deployed tag into it.

## Protected publication and rollback

A push to protected `main` that changes the registry or the publisher workflow
starts the publisher. The protected-main merge is the only price approval; the
publisher has no second Environment approval gate. It fetches current
`origin/main`, verifies that the local artifact is byte-identical to that head,
runs the registry gate, builds the envelope, writes it through the existing SSM
settings path, publishes `settings_updated`, and reads the row back to verify
commit and digest. A workflow-only change intentionally reconciles the current
snapshot and becomes an idempotent no-op when it is already exact.

New runs supersede obsolete waiting or running workflow jobs, but cancellation
is not the write-safety boundary because an older runner may already have issued
a detached SSM command. Each write uses the settings row's PostgreSQL version as
a compare-and-set token; a conflict triggers a fresh read, exact-current check,
and ancestry check before a bounded retry. Redis invalidation is emitted only
after a successful compare-and-set. A candidate source commit must descend from
the active runtime source commit; stale publication cannot overwrite a newer
snapshot, while a Git revert remains valid because it creates a newer descendant.
Unmerged branches and arbitrary local files fail publication.

Rollback is a Git revert followed by the same publisher. The reverted content
is still published from a new protected-main commit, so rollback does not need
an out-of-band price owner. The process retains its previous good snapshot until
the reverted snapshot passes complete validation.

## Sensor loop

The scheduled sensor downloads configured provider/LiteLLM evidence, compares
all billable dimensions and metadata against the registry, and produces a
deterministic report. Drift may create or refresh one bot PR that changes only
the registry. It never publishes the candidate and never changes serving.

Rows that are not valid serving owners, including a provider-advertised model
that TokenKey deliberately aliases to another routed model, remain report-only
until a human decides the requested/routed/billing relationship.

## Migration

1. Capture the current external runtime document and current embedded overlay.
2. Materialize the current effective precedence plus existing family-floor
   owners into the complete registry.
3. Compare requested model, routed model, resolved owner and every billed
   dimension against the pre-change implementation. Unapproved differences
   block activation.
4. Deploy the reader while legacy raw runtime blobs are ignored in favor of the
   complete embedded fallback.
5. Enable protected-main publication. The first valid envelope replaces the
   embedded snapshot atomically.
6. Remove numeric Go fallbacks; keep only alias-to-registry-owner policy.
7. Enable the scheduled sensor-to-PR loop.

The migration includes one explicit price correction: `kimi-k2.6` keeps the
reviewed Moonshot China list price already documented in its registry source
(`¥6.50` cache-miss input, `¥27.00` output, `¥1.10` cache-hit input per MTok,
converted at TokenKey `CNY/USD=6.7`). The materialized provider row had rounded
USD values inconsistent with that declared formula; the registry uses the exact
conversion and the existing billing regression test pins it.

## Hard gates

- Registry validation rejects malformed, non-finite, negative, empty-owner and
  unsafe media rows; deliberate free rows require explicit metadata.
- Runtime validation rejects unsupported schema, non-protected source shape,
  digest mismatch, decompression failure and invalid registry content.
- A legacy raw overlay cannot override a complete registry.
- Requested model to routed model to billing owner is covered for compatibility
  aliases, including `gpt-5.5-pro` routing and billing through `gpt-5.5`.
- Balance hold, final settlement, catalog display and channel override tests use
  the same registry snapshot.
- A normal price-only change requires changing only the registry file.
- Publisher ancestry validation and PostgreSQL compare-and-set reject stale
  writers; workflow cancellation is only queue cleanup, not write safety.
- Deploy, sensor and workstation paths cannot publish the runtime snapshot.
- Upstream merge-tree conflict surface may not grow beyond the minimum existing
  TK companion hook; deletions of upstream pricing machinery are forbidden.
