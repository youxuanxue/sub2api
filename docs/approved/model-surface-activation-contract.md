---
title: Model surface activation contract
status: approved
approved_by: "xuejiao (design directive, 2026-07-15)"
approved_at: "2026-07-15"
authors: [agent]
created: 2026-07-15
related_design: docs/approved/served-model-reconcile-planner.md, docs/approved/pricing-serving-single-source-of-truth.md, docs/approved/priced-or-it-doesnt-ship.md
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
   check/apply. There is no strict-vs-floor mode switch.
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
- Routine apply preserves compatible extras and removes forbidden entries.
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
  "schema_version": 1,
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
      "source": "probe_account_model.sh",
      "account_id": "7"
    }
  ]
}
```

Pricing evidence uses `kind=model_activation_pricing`, `verdict=priced`, and the
same `scope/model_id/target/source` identity (without `account_id`). Both files
must bind the exact current and target digests, cover every added/retargeted
mapping, and be no older than 24 hours. The probe result must come from a real
account path; its `source` must differ from the pricing source. Repository tests
and bundle membership are not probe evidence.

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
