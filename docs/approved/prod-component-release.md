---
status: approved
approved_by: "feng (conversation 2026-09-10: agreed to implement the three focused release changes)"
approved_at: 2026-09-10
scope: "prod component classification, independent QA runtime, parallel gateway acceptance and drain"
---

# Independent Prod Component Releases

The single `deploy-stage0.yml` entry selects components automatically. A gateway-only
change does not update QA infrastructure, replace QA runners, execute maintenance,
or run the full Bundle canary. A QA-only change preserves the serving gateway.
Shared build, schema, and QA protocol changes coordinate the necessary components.
General cleanup/aggregation services remain in the gateway in this change.

`ops/stage0/prod_release_plan.py` owns classification. It consumes the Bundle
worker/publisher paths from `ops/qa/qa_bundle_release_surface.py`, shared runtime
dependencies, the actual serving gateway, verified live ECS worker, and pinned
maintenance tag. Publisher comparison starts from the last verified publisher
baseline, never implicitly from the worker tag. Unknown Git evidence fails closed;
missing or drifted verification state requires QA acceptance.

`/var/lib/tokenkey/qa-release/verified.json` records the accepted component versions,
runtime container ID, host artifact fingerprint, plan ID, and verification timestamp.
It is committed only after all selected component gates pass. Failed acceptance
cannot advance the baseline. Runtime drift invalidates reuse of this evidence.

## QA Runtime

`tokenkey-qa-runtime` is a stopped Docker container, not a second gateway process.
It pins an immutable local image and a configuration snapshot rendered from
`deploy/aws/stage0/qa-runtime-compose.yml`. The installer obtains the data network
from PostgreSQL, without consulting the active gateway color. Timer invocations
use that network, immutable image ID, and configuration, with the existing resource
limits and local mounts. Gateway replacement does not replace this pin.

The maintenance installer drains the timer and acquires the existing QA lifecycle
lock before replacement. Failed installation restores the previous pin, host files,
units and timer state; failed restoration leaves the timer disabled. Boundary retains the durable activation-receipt checks.
There is still one lifecycle owner after activation. Archive, restore verification,
capture seal, and DROP ordering are unchanged. No new deletion authorization is added.

First rollout installs this runtime and performs full QA acceptance. Rollback to
a release without the independent runtime contract preserves the pinned QA runtime
and worker, disables boundary, and writes a durable maintenance DROP pause before
changing the gateway. Successful acceptance of a compatible release clears that
pause. It does not undo a previous DROP or activate single ownership.

## Gateway Completion

The original SSM command holds the host deployment lock throughout cutover,
observation, and old-request drain. A command-specific receipt allows CI to begin
gateway smoke immediately after cutover while the same remote command drains.
CI joins that command before claiming deployment success. Gateway checks include
the existing cutover-plus-five-minute observation; early cutover is not success.

A durable pending-drain receipt pins the outgoing container identity. After failure
or runner cancellation, another invocation must safely finish that drain before
reusing the color. Identity drift or a pending pointer to the active color blocks
replacement. Drain failure never authorizes force-stopping active requests.

## Validation

Behavior and regression tests are in `ops/stage0/test_prod_component_release.py`,
`ops/stage0/test_deploy_via_ssm_bluegreen.py`, the workflow contracts, and existing
QA runner/lifecycle tests. No user-facing UI is changed. Production rollout and
elapsed-time targets require an actual release; repository implementation is not
deployment evidence.
