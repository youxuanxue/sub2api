---
title: QA Phase 2 Archive Closeout
status: approved
approved_by: "feng (conversation approvals 2026-08-07 through 2026-08-15; Phase 2 evidence retained, later runtime target delegated to the primary QA design)"
date: 2026-08-07
last_reviewed: 2026-08-16
supersedes: null
related:
  - docs/approved/design-prod-qa-24h-s3-lifecycle.md
---

# QA Phase 2 Archive Closeout Design

**Approval baseline:** `docs/approved/design-prod-qa-24h-s3-lifecycle.md`

## Scope Boundary

This document is a historical/superseded evidence record. It preserves Phase 2 archive,
recovery, historical-gap, no-rehome migration, IAM, and production closeout facts. It is not
an operator runbook and does not authorize current or future lifecycle actions.

The primary QA design exclusively owns the current target: S3-only user list/detail/export,
durable capture ledger, archive-gated partition DROP, the single
`tokenkey-qa-maintenance` lifecycle owner, retirement of the independent boundary runtime,
and absence of automatic destructive emergency cleanup. If this record conflicts with that
design, the primary design wins.

## Historical Purpose

Phase 2 established that an hourly raw archive was not complete until immutable artifacts
could be read back, verified, and reconstructed. It also closed historical repair rather than
fabricating missing evidence. The closeout retained explicit terminal failures for damaged or
missing historical material and left their immutable S3 evidence unchanged.

The implementation used one forward archive cutover, a normal sealed-hour path, and at most
one bounded post-cutover compensation candidate after normal success. Those facts describe
the retired Phase 2 state machine only. They are not deletion authorization for the current
runtime.

## Historical Archive Contract

The verified archive model had these properties:

- immutable base/delta segments with manifest, checksum, and compare-and-swap commit;
- read-after-write verification followed by independent local restore verification;
- explicit control states for writing, verified, committed, failed, and orphaned material;
- no historical backfill, repair-apply, cutover move/unset, or fabricated zero-row success;
- bounded compensation selected from the UTC-hour timeline, not only surviving source rows;
- terminal gaps stayed visible and could not silently become successful archive evidence;
- raw archive covered all production users and API keys independently of user entitlement;
- host receipt, database heartbeat, control rows, and systemd state were correlated for health.

Archive success never independently authorized source deletion. The current target additionally
requires the durable capture seal and the single-owner activation contract defined by the
primary design.

## Historical Storage Transition Facts

Phase 2 observed a no-move transition from legacy monthly/default-backed storage to exact
UTC-hour children. Existing rows were not copied or rehomed. Production closeout evidence later
showed a default-free hourly layout with future coverage and a completed whole-partition drop.

The former multi-mode cutover, temporary legacy cleanup, temporary export-staging cleanup,
inventory/plan/apply surfaces, and transition receipts are retired implementation history.
The only surviving compatibility behavior is defined by the primary design: before
`single_owner_activate`, one timer path continues the already-running 24-hour whole-partition
cleanup while provisioning future hours; after activation it is permanently disabled. This
record intentionally omits the old commands and confirmations so it cannot recreate an
alternate deletion owner.

The immutable hourly storage cutover setting remains readable only for capture-layout
compatibility. It cannot mutate lifecycle state or authorize deletion.

## Historical Recovery And Security Evidence

The closeout verified workstation recovery directly from S3 without routing through the
production API, host, or database. Restoring request/response bodies required explicit privacy
confirmation and secure local file modes. Missing or corrupt evidence remained visible and
failed closed.

Application permissions were suffix-scoped to the archive artifacts needed at runtime and did
not claim process isolation from a shared EC2 instance role. The recovery role remained
read-only and separately audited. Bucket-policy evidence and shared-role limitations remain
historical security facts, not authority for current lifecycle changes.

## Production Evidence Record

The retained production evidence established:

- the approved forward archive cutover was committed and restore-verified;
- known historical commit-mismatch and missing-evidence windows remained explicit failures;
- normal plus bounded compensation behavior was observed without reopening historical repair;
- raw archive IAM and workstation recovery were independently verified;
- the former hourly layout and deletion runtime completed a real partition lifecycle;
- host receipts, database heartbeats, archive control, and catalog facts were correlated;
- historical terminal gaps remained degraded instead of being relabeled as success.

These statements are point-in-time evidence. They do not claim that the current repository's
single-owner activation has occurred. The observed live state remains
`single_owner_not_activated` until a separately approved rollout records otherwise.

## Production Rollout (Historical Evidence Only)

The Phase 2 rollout is complete historical evidence. No command, timer transition, fixed-age
threshold, legacy cleanup switch, default-partition transition, or export-staging cleanup from
that rollout may be replayed from this document. Current rollout and rollback decisions must
use the primary QA design, append-only `single_owner_activate` receipt, current policy, and
current automation.

Repository readiness and observed production state must remain separate. Editing rollout YAML
or this evidence record cannot claim activation, deployment, or successful production mutation.

## Current Ownership Delegation

The target contract delegated away from this document is:

- `tokenkey-qa-maintenance.timer` is the only lifecycle owner after activation;
- the transition boundary continues provision plus fixed-age whole-partition cleanup before
  activation and permanently fails closed after the activation receipt exists;
- partition DROP requires committed raw archive, restore verification, and a durable capture
  seal revalidated under the child lock;
- exact-hour Blob/DLQ cleanup resumes idempotently from durable source-drop state;
- user QA reads committed S3 Bundle artifacts with no production database fallback;
- disk pressure is observable but never triggers automatic deletion or capture pause.

## Completion Record

Phase 2 is closed as evidence. Its remaining value is diagnostic: it records what was verified,
which historical gaps were accepted, and which security limitations were explicit. All present
and future behavior, tests, sentinels, deployment gates, and operator decisions belong to
`docs/approved/design-prod-qa-24h-s3-lifecycle.md` and its current implementation.
