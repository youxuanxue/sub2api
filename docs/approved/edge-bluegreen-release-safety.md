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
`docs/approved/deploy-stage0-workflow.md` is the single owner of deployment
state, capacity admission, cutover, rollback, observation, and canary selection.
This record does not define a second state machine.

The approved product rules are:

1. Prod and Edge call one blue/green primitive with thin environment profiles.
2. The serving app is never stopped before the candidate is healthy.
3. A failed cutover commit restores the old Caddy route only when both route
   and durable active state can be restored. If either restoration fails, keep
   the target route and both colors for explicit recovery. A committed cutover
   is never automatically flipped back.
4. After 30 seconds of successful routed observation, the old app is drained
   and stopped. At steady state one app is running.
5. Canary selection first rejects hosts without enough capacity, then chooses
   the lowest recent traffic, greatest memory headroom, and matrix order.

On host restart, systemd starts only the color jointly identified by Caddy and
`active-color`. If they disagree or either value cannot be resolved, it starts
both colors before Caddy so the persisted route remains serviceable while an
operator performs explicit recovery.

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

This approval includes the shared primitive, Edge workflow wiring, Edge
capacity admission, deterministic canary selection, and mechanical regression
guards. It excludes permanent dual instances, a second Edge deploy mode,
upstream account probing, protocol inference in operations scripts, automatic
post-commit rollback, and new infrastructure.

Implementation, merge, release, and rollout retain their normal approval
gates. The failed `v1.8.178` rollout remains frozen; the implementation requires
a new release and an explicitly approved canary.
