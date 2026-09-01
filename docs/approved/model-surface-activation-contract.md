---
title: Model surface activation contract
status: approved
approved_by: "xuejiao (design directive, 2026-07-15)"
approved_at: "2026-07-15"
authors: [agent]
created: 2026-07-15
related_design: docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/priced-or-it-doesnt-ship.md
---

# Model Surface Activation Contract

## Goal

Keep generic binary deploy and rollback independent from live account configuration,
while making a customer-visible model activation a deliberate, evidence-backed
operation. Go remains the model surface owner; generated artifacts and tests are
projections, not parallel model lists.

## Approved Decisions

1. **One live-validity rule.** The Go bundle owns the required account mapping floor
   and forbidden keys/prefixes. A live mapping is valid when it contains every
   required key with the required target and contains no forbidden entry. Other
   entries are compatible extras for preheat and rollback and must survive routine
   check/apply. There is no strict-vs-floor mode switch. An
   `account_overrides` entry is a full replacement for its property-based
   `account_override:<platform>:<channel_type>:<base_url>` scope and takes
   precedence over the shared platform/channel scope only when all selector
   properties match the live account. Account IDs are not selector keys. Vertex
   `newapi/channel_type=41` is the only capability-profile exception: its public
   catalog is the verified account union, its shared channel floor is the strict
   successful intersection, and `vertex_capability_profile` selects a complete
   named account floor. Missing or unknown profiles fall back to the shared floor
   and are reported as violations; the selector is never inferred from account ID
   or name. No other platform/channel may introduce per-account capability profiles.
2. **Build once.** CI/release generates a deterministic, checksummed model-surface
   bundle from the Go owner. Rollout tools consume that bundle and do not compile Go
   or discover a source checkout at rollout time.
3. **One activation entry.** `modelops activate` validates the target bundle,
   independent probe evidence, and independent pricing evidence before producing or
   executing the reviewed prod mapping plan and activation gate. Generic deploy and
   rollback never invoke this entry.
4. **Independent evidence.** Tests prove owner projections and transformation logic.
   Probe/pricing evidence proves real upstream capability and price readiness. Neither
   evidence source becomes a second serving/model list.
5. **One Admin DTO.** `GET /admin/accounts/:id/models` returns the same minimal model
   option shape for every platform: `id` and `display_name`.

## Explicit Non-goals

- Do not add a live `model_mapping` prerequisite to `deploy-stage0.yml`.
- Do not add a background writer or startup/tick account reconciler.
- Do not add a server-side model activation feature flag in this PR.
- Do not remove the existing Edge diagnostic/apply CLI surface in this PR; that
  proposal was not approved. Routine release and activation remain prod-only.
- Do not infer servability from upstream discovery or price presence. A real probe
  success is required evidence.

## Acceptance

- Bundle generation is deterministic and preflight fails on generated drift.
- Runtime tools can validate/check/apply from a bundle without Go or a sibling source
  checkout.
- Property-selected account bundle scopes override shared channel floors without
  producing a false-positive drift finding; same-platform/channel accounts with a
  different base URL continue to use the shared floor.
- Routine apply preserves compatible extras and removes forbidden entries.
- Guarded ch41 profile assignment is prod-only, verifies
  `id + name + platform + channel_type + current profile`, writes only
  `vertex_capability_profile`, performs no empty write, and verifies the read-back;
  account mapping changes remain a separate activation/apply operation.
- Activation refuses missing/stale/mismatched probe or pricing evidence before any
  write, defaults to dry-run, and requires an explicit confirmation phrase to write.
- Admin model option tests assert the exact cross-platform response contract.
- Release artifacts publish the exact bundle associated with the tag.

## Activation Evidence

`modelops activate` compares a validated `--current-bundle` with the validated
target `--bundle`. Only required mapping keys that are added or retargeted need
evidence. A delta with no such mapping is rejected so this command cannot become
a generic reconciliation shortcut.

Probe and pricing evidence are separate JSON objects with this common envelope:

```json
{
  "schema_version": 2,
  "kind": "model_activation_probe",
  "current_floor_sha256": "<current bundle floor_sha256>",
  "target_floor_sha256": "<target bundle floor_sha256>",
  "observed_at": "2026-07-15T08:00:00Z",
  "models": [
    {
      "scope": "openai",
      "model_id": "gpt-example",
      "target": "gpt-example-upstream",
      "verdict": "servable",
      "source": "probe_account_upstream_models.sh",
      "account_id": "7",
      "account_platform": "openai",
      "account_scope": "openai"
    }
  ]
}
```

Pricing evidence uses `kind=model_activation_pricing`, `verdict=priced`, and the
same `scope/model_id/target/source` identity (without `account_id`). Both files
must bind the exact current and target digests, cover every added/retargeted
mapping, and be no older than 24 hours. The probe result must come from a real
account path; `account_scope` must exactly match the bundle mapping scope and
must be a valid projection of `account_platform` (including explicit Anthropic
transport scopes such as `kiro`, exact `newapi_channel_type:*` scopes,
exact `newapi_vertex_profile:*` scopes, and property-based
`account_override:*` scopes). For an account override scope,
`account_platform` and normalized `account_base_url` must match the bundle
selector. `account_id` remains provenance for the real probe path only; it is not
used to select an override. The account probe derives these fields from the
target database; its `source` must differ from the pricing source. Repository
tests and bundle membership are not probe evidence.

Without `--confirm`, activation validates evidence, renders the prod apply plan,
and runs the prod release gate read-only. With
`--confirm yes-activate-model-surface`, it repeats the plan, applies only to prod,
then requires the post-apply release gate to pass. Edge diagnostic/apply commands
remain available directly in the mapping manager but are outside activation. A
live `tk_account_model_mapping_runtime` setting would shadow the immutable target
artifact, so activation rejects it before any write; fold the scope into the
target bundle or clear the runtime setting first. The first prod gate resolves
one instance and every dry-run/apply/post-gate command stays pinned to it. The
activation apply also locks the live settings table and rechecks that the runtime
replacement is absent inside the account-write transaction.
