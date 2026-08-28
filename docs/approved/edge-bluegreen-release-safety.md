---
title: Edge Blue/Green Release Safety Decision
status: approved
approved_by: "feng (conversation approval, 2026-08-28)"
approved_at: 2026-08-28
authors: [codex]
created: 2026-08-28
related_incident: "v1.8.178 us3 rollout failure, GitHub Actions run 33158771468"
canonical_spec: docs/approved/deploy-stage0-workflow.md
---

# Edge Blue/Green Release Safety Decision

## Decision

Lightsail Edge reuses the prod same-host blue/green deployment primitive.
`docs/approved/deploy-stage0-workflow.md` is the only behavior specification.
This decision record adds no second state machine or duplicated rules.

## Incident

The `v1.8.178` canary selected `us3` by matrix order after every native
OAuth/Kiro pool reported empty. The Edge workflow then used the legacy
single-container primitive, which stopped the serving app before the target
image passed protocol SSOT readiness. An explicit `exit 1` bypassed the
`ERR`-only rollback trap, so the Edge remained unavailable until manual
rollback.

The fix is not to predict or weaken protocol readiness. The target app's
`/health` remains its only readiness owner, while blue/green keeps the previous
image serving when the target is inconclusive or unhealthy.

## Scope boundary

This approval includes Edge workflow wiring to the shared primitive, the
minimum capacity/canary checks required to choose a safe candidate, and the
recovery fix found during review. It excludes permanent dual instances, a
second Edge deploy mode, upstream account probing, protocol inference,
automatic post-commit rollback, and new infrastructure.

Implementation, merge, release, and rollout retain their normal approval
gates. The failed `v1.8.178` rollout remains frozen; the implementation requires
a new release and an explicitly approved canary.
