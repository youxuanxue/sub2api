---
title: QA Phase 2 Archive Closeout
status: approved
approved_by: "feng (conversation approvals, 2026-08-07 and 2026-08-08)"
date: 2026-08-07
last_reviewed: 2026-08-10
supersedes: null
related:
  - docs/approved/design-prod-qa-24h-s3-lifecycle.md
---

# QA Phase 2 Archive Closeout Design

**Approval baseline:** `docs/approved/design-prod-qa-24h-s3-lifecycle.md`

## Purpose

Complete the production QA raw-archive runtime so a newly archived hourly shard is not
reported as complete until its immutable artifacts can be read back, verified, and
reconstructed. The 2026-08-07 production closeout decision stops historical repair:
`2026-08-07 01:00 UTC` remains `failed/commit_mismatch`, and `2026-08-04 04:00 UTC`
remains `failed/missing_evidence`. Neither S3 commit is changed and historical backfill
is retired.

Physical QA deletion is independently age-based under the approved 24-hour lifecycle.
Archive incompleteness remains visible through control state and `cleanup_eligible=false`,
but does not prevent expired source rows or files from normal retention.

The 2026-08-08 production-integrity revision closes the gap between a successful root
`docker exec` and the recently enabled systemd timer. It introduces one host runner for
scheduled and operator execution, fixes the real bind-mounted scratch path and UID, adds
an atomic host receipt, and permits one bounded post-cutover compensation window only
after the normal window succeeds. It also assigns stale export-temp cleanup and records
the honest shared-EC2-role IAM boundary. This revision does not reopen historical
backfill, Phase 3 user export, or Phase 5 emergency work.

## Scope

### Included

- sealed-window selection for regular forward maintenance;
- append-only base and late-delta segments;
- S3 ETag compare-and-swap for `commit.json`;
- `writing -> verified -> committed` archive transitions;
- read-after-write artifact verification and local restore verification;
- fail-closed missing/corrupt evidence handling;
- durable segment and source-row membership control data;
- success and failure heartbeats that report the committed aggregate;
- bounded database pagination and file-backed artifact generation;
- systemd CPU, memory, process-priority, and I/O-priority limits;
- operator `inspect`, `verify`, and `restore` commands that read S3 directly;
- raw-bucket recovery IAM, VPC endpoint, and S3 data-event audit wiring;
- a fixed operator that records the two known malformed production shards as failed without repairing them.
- one host runner shared by systemd and the prod operator, fixed to UID/GID `1000:1000`;
- real mount/scratch self-test and atomic host last-run receipt;
- one unique forward cutover and at most one oldest post-cutover compensation window per run;
- systemd/host-receipt/DB-heartbeat/control-row health correlation;
- stale-cleanup ownership of crash-orphan files in the effective `qa_exports_tmp` directory;
- least-privilege app-role tightening that reflects, but does not overstate, the shared EC2 role boundary.

### Excluded

- recovering or fabricating the existing 96 missing files;
- repairing the 477 late identities or replacing either historical S3 commit;
- implementing the independent age-based cleanup path;
- Phase 3 off-production user export and its UI/API;
- Phase 5 emergency deletion and P0 delivery;
- releasing the generic usage/ops cleanup hold;
- partitioning `usage_logs` or enabling telemetry shadow archive.
- arbitrary-window repair, historical backfill, or a second catch-up script;
- deleting `qa_export_jobs` rows or defining a new export-job retention policy;
- process-level IAM isolation between gateway and maintenance on the current shared EC2 host.

## Safety Invariants

1. No command in this delivery deletes QA source rows or evidence files.
2. A shard with missing or corrupt evidence is not committed and is not cleanup eligible.
3. `confirmed gap` requires a separate immutable approval receipt. Detection alone never
   creates that receipt, and this rollout creates no confirmed-gap receipt.
4. Once a base segment is referenced by a commit, late records create delta segments;
   they never replace or rewrite the base segment.
5. S3 `commit.json` is the reader marker. A segment is invisible until an ETag-guarded
   commit references its verified manifest. Seeing `commit.json` is necessary but not
   sufficient: every reader rejects a commit whose referenced manifest reports missing
   or corrupt evidence.
6. Database control rows and the S3 commit must describe the same ordered segment set,
   aggregate counts, and checksums before a shard reaches `committed`.
7. Historical backfill is retired. A run selects the previous sealed hour first and, only
   after that normal window is committed and restore-verified, may select at most one
   oldest retryable incomplete hour strictly after the unique forward cutover. The
   post-cutover hourly timeline is derived from time, not only from surviving source or
   control rows; terminal failed hours remain visible without starving later retryable work.
8. Failed maintenance writes a failed heartbeat even when failure occurs before upload,
   including advisory-lock contention and window-selection failure.
9. Resource limits, image, UID/GID, mounts, environment, scratch, receipt, and command are
   identical for scheduled and operator-invoked maintenance; root `docker exec` is not an
   operator entrypoint.
10. Rollback may stop archive writes, but it must not overwrite the newer live policy,
    disable the active age-based stale cleanup, or enable unapproved emergency deletion.
11. A failed normal window suppresses compensation. A failed compensation keeps its
    machine-readable control state and makes the overall run fail.
12. Stale cleanup remains active during this closeout and is the only owner allowed to
    remove `qa_exports_tmp` crash orphans.

### Catchup gap policy (`accepted_terminal`)

Runtime policy lives in `ops/qa/policy.yaml` as `archive.catchup_gap_policy`. Production
uses `accepted_terminal`:

- **Host runner / receipt:** compensation failure still makes the overall run nonzero;
  `source_unavailable_after_retention` on a *new* selection still persists durable
  terminal control for that hour.
- **Health correlation:** pre-cutover and historical post-cutover hours that are already
  terminal in control rows may remain in the `terminal_failures_after_cutover` inventory.
  When systemd, host receipt, DB heartbeat, and control rows **agree** on those terminal
  facts, correlated health is `degraded` with `catchup_terminal_gaps_present`, not
  `healthy`. Any contradiction across the four sources is `failed` (fail closed).
- **Rollout stop vs degraded:** `accepted_terminal` does **not** treat an already-terminal
  historical gap such as `2026-08-07 22:00 UTC` as a blocker for forward scheduled runs
  once facts correlate. A **new** gap discovered during closeout still requires a separate
  immutable gap decision before compensation can be retried; until then rollout remains
  `observed_live_state: pending_live_reconciliation`.
- **`strict` alternative:** inventory presence or any terminal gap fails correlated health
  and blocks rollout observation gates.

Repository SSOT (`ops/qa/deploy_rollout.yaml`) separates `repository_closeout_state`
(implementation ready in code) from `observed_live_state` (live probe reconciliation).
Neither field may read `production_recloseout_verified` until live probes, IAM contract,
partition owner, and completion criteria below are satisfied on production.

## Persistent Model

### `qa_archive_shards`

Keep one row per `(window_start, generation)`. Add fields needed to bind database state
to the current S3 commit:

- `commit_etag`: ETag observed after the successful conditional write;
- `aggregate_record_count`, `aggregate_blob_ref_count`, and aggregate present/missing
  counts derived from the committed segment set;
- `verified_at` and `restore_verified_at`;
- `verification_error_code` for machine-readable fail-closed decisions;
- `last_reconciled_at` and `final_reconciled_at`;
- `cleanup_eligible`, default and forced to false in Phase 2;
- `forward_cutover boolean NOT NULL DEFAULT false`, with a unique partial index on
  rows where it is true and a row-level check requiring a marked shard to be `committed`
  with `restore_verified_at IS NOT NULL`.

The only Phase 2 command that sets the cutover requires the selected shard to be
`committed`, to have `restore_verified_at IS NOT NULL`, and to match the approved exact
`2026-08-07 21:00 UTC` window confirmation. Repeating that same setting is idempotent;
this closeout exposes no move or unset operation, and rollback must preserve it. Selection
rechecks the database constraint and exact window so a corrupt control-row update fails
closed. The marker defines only the automatic compensation lower bound. It neither
asserts that earlier history is complete nor grants deletion authority.

The allowed Phase 2 states remain `pending`, `writing`, `verified`, `committed`, and
`failed`. The operator/UI term **blocked** is a derived condition:

```text
state = failed AND verification_error_code IN (missing_evidence, corrupt_artifact,
commit_mismatch, restore_failed, source_unavailable_after_retention)
```

This avoids introducing a second state-machine vocabulary while making the deletion
gate explicit.

### `qa_archive_segments`

Add one immutable control row per uploaded segment:

- shard identity, segment ID, and `base` or `delta` kind;
- state `writing`, `verified`, `committed`, `failed`, or `orphaned`;
- manifest/artifact keys, sizes, checksums, counts, and verification timestamps;
- upload attempt ID and failure code;
- commit ETag that first referenced the segment.

Only a `verified` segment can be added to a commit. After the CAS succeeds, the segment
and shard control rows become committed in one database transaction.

### `qa_archive_segment_records`

Track source identity `(created_at, request_id)` for each segment. Membership rows are
written in bounded batches while records are streamed. Delta selection excludes only
memberships whose segment is verified or committed; failed/orphaned segment membership
never hides a source row from a retry.

This table is operational control data, not a recovery source. S3 remains the sole QA
history source. Membership retention is handled in a later non-destructive maintenance
step after the corresponding S3 lifecycle has elapsed; this delivery does not delete it.

### Deferred gap and cleanup state

Do not create `qa_archive_gaps` or database `qa_cleanup_receipts` in Phase 2. The active
age-based stale cleanup keeps its existing host receipt and remains independent of archive
completeness. Missing/corrupt evidence remains represented by shard verification failure
and `cleanup_eligible=false`. If a post-cutover hour has neither control state nor source
rows and is already older than the database retention cutoff, maintenance creates a durable
failed shard with `verification_error_code=source_unavailable_after_retention`; an existing
uncommitted/retryable shard that reaches the same condition becomes terminal as well. Neither
case creates an empty commit or synthetic success. This terminal failure remains part of
archive health but does not starve later retryable hours. A future immutable gap command or
emergency receipt schema requires separate approval.

## Archive Pipeline

### Window selection

Every scheduled or operator run first selects the previous sealed hour as its normal
window and ensures its control row before inspecting source data. If normal construction,
commit, or restore verification fails, the run records failure and exits nonzero without
compensation. A timely normal window with zero source rows writes and verifies a valid
zero-row base commit, so zero traffic is a durable success rather than an absent fact.

Only after normal succeeds does the same transactionally guarded service enumerate the
UTC-hour sequence strictly after the unique `forward_cutover` and strictly before the
current normal window. It selects the oldest retryable hour for which any of these is true:

- no control row exists and the hour is still inside the database retention boundary;
- the control state is pending, writing, verified, or retryable failed; or
- a committed, restore-verified shard has source identities not covered by verified or
  committed segment membership.

An enumerated uncommitted hour with no source rows is handled by its age: before the retention
cutoff it receives a verified zero-row base commit; after the cutoff it receives the terminal
`source_unavailable_after_retention` failed control described above, whether its control row
was absent or held a retryable failure.
Terminal failed hours remain visible in archive health but are not retried automatically.
At most one candidate is reconciled or durably classified per run. A source-bearing hour
with no commit creates a base; an hour with a valid existing commit and uncovered source
identities creates only the required delta. Compensation failure writes machine state and
makes the run nonzero even though normal succeeded. There is no arbitrary window flag,
historical selector, or separate backfill executable.

The maintenance advisory lock is released only when acquisition succeeded. Every exit
path records a bounded failure heartbeat after releasing the database connection.

### Segment construction

The archiver reads `qa_records` by `(created_at, request_id)` keyset pages. It writes
Parquet, evidence pack, and compressed evidence index to mode-0600 temporary files in a
dedicated scratch directory. The process never holds the full hour, full Parquet file,
or full evidence pack in memory.

For a new shard, all source identities enter one base segment. A timely source-empty shard
still emits a valid zero-row `records.parquet`, manifest, base descriptor, and commit; this
is allowed only while the complete source window is still inside retention. For a shard
with an existing commit, the archiver loads committed memberships and selects only
uncommitted source identities into a delta segment. An empty diff on an existing commit
performs verification only and does not create an object.

If any referenced evidence file cannot be read or its digest cannot be established,
segment construction fails before the segment can become verified. Diagnostic metadata
records counts and source identities without storing request/response bodies in logs or
heartbeats.

### Upload and verification

Artifacts upload under an immutable segment UUID. The manifest uploads last within the
segment prefix. Upload uses readers/files and bounded multipart concurrency rather than
`[]byte` payloads.

Verification opens every artifact through the object-store read API, recomputes SHA-256,
parses Parquet and evidence index, checks pack offsets and lengths, and confirms source
membership/count parity. It materializes a mode-0700 isolated restore directory and
reconstructs each indexed evidence payload from S3 bytes. The directory is removed after
verification unless an explicit operator restore command selected a destination.

Successful verification changes the segment to `verified`; failures change it to
`failed` and leave it unreferenced. An interrupted `writing` segment is recorded as
`orphaned` on the next reconcile, but Phase 2 neither deletes nor retags its immutable
objects: object tags and `commit.json` cannot be updated atomically, so a one-day orphan
tag could delete a segment after a successful commit. Objects already uploaded under a
formal shard therefore remain unreferenced and expire with the seven-day raw lifecycle;
the one-day `raw/partial/` lifecycle applies only before an object enters a formal shard.

### Commit CAS

The object-store interface returns object bytes plus ETag and exposes:

```go
CompareAndSwap(ctx, key, expectedETag, reader, size, contentType) (newETag, error)
```

Creation uses `If-None-Match: *`; update uses `If-Match: <etag>`. The proposed commit
contains the ordered existing segments plus the new verified delta, aggregate counts,
and an aggregate checksum over canonical segment descriptors. On a precondition failure,
maintenance rereads the commit, rebuilds the proposal, and retries a bounded number of
times. It never performs an unconditional overwrite.

After CAS, maintenance rereads the commit and all referenced manifests. Only then does a
single database transaction mark segment and shard committed and persist the same ETag,
segment set, counts, and checksums. Heartbeat and command receipt use this committed
aggregate, not the pre-upload source count.

## Operator Commands

Use the repo-owned Go CLI under `backend/cmd/qa-archive` for recovery reads:

- `inspect`: print window, state, segment metadata, counts, and missing/corrupt status;
- `verify`: perform commit/manifest/artifact/checksum verification without writing prod;
- `restore`: reconstruct records and evidence into an explicit local isolated directory;
- `repair-plan`: remain a no-write diagnostic comparing control state, S3 commit, and
  current source identities.

`inspect`, `verify`, and `restore` gain an ops-workstation mode that assumes the dedicated
recovery role and reads a specified S3 window directly, without prod SSH/SSM, API,
PostgreSQL, disk, CPU, or network. The write-capable arbitrary-window `repair-apply`
entrypoint is retired; the 22:00 repair is selected only by the normal runner's bounded
post-cutover compensation state machine. The CLI remains a thin adapter over shared
verification/restore packages and does not duplicate selection or commit logic.

The CLI defaults to metadata-only output. Restoring evidence bodies requires an explicit
privacy confirmation. Local directories are mode 0700, files are mode 0600, and receipts
contain no bodies, credentials, cookies, or API keys.

Workstation `inspect`, `verify`, and `restore` use the same operator-generated
`--recovery-run-id`; the receipts bind distinct commands to one window, bucket and recovery
role. Workstation restore requires an explicit local `--restore-root` and a new direct-child
`--output`, rather than inheriting the container-only `/app/data` default. Synthetic receipt
bundles can validate repository shape only. A production retirement plan additionally
requires exact expected window/bucket/role values and a separate unexpired human high-risk
approval record hash-bound to the evidence bytes and issued after the final receipt. The
production receipts expire from this gate after 24 hours; changing a scope label or copying
one command receipt cannot claim production success.

After an independent workstation verify/restore succeeds, retire the transitional
prod QA break-glass dump tooling. Before that gate it remained manual read-only break-glass and was not
a timer, lifecycle owner, or deletion prerequisite. **Shipped 2026-08-10:** break-glass retired.

## Known Production Repair

### Forward cutover: `2026-08-07 21:00 UTC`

The 21:00 shard was committed by the 22:54 manual run. Independently verify its commit
and restore before setting `forward_cutover=true`; the unique marker is not inferred from
the manual command's zero exit status. The historical malformed windows below remain
outside automatic compensation.

### First compensation: `2026-08-07 22:00 UTC`

The audit observed 2217 source rows and no shard/control row. After the runner and cutover
are deployed, one normal window must succeed before the same run selects 22:00 as the
oldest post-cutover gap. It then performs base construction, commit verification, and
restore through the normal service. If age retention has already removed the source,
write the terminal `source_unavailable_after_retention` failed control, stop without
creating a zero-row commit or changing failure to success, and request a separate gap
decision.

### `2026-08-07 01:00 UTC`

Do not repair this hour. Keep the existing 407-row committed S3 base and the unreferenced
884-row object unchanged. Set the control state to `failed` with
`verification_error_code=commit_mismatch`, record the observed 407/884/477 counts, keep
`cleanup_eligible=false`. No historical selector or backfill entrypoint remains.

### `2026-08-04 04:00 UTC`

Because 96 evidence references are missing, set the shard to `failed` with
`verification_error_code=missing_evidence`, derive `blocked=true`, and keep
`cleanup_eligible=false`. Do not alter the S3 commit, create a gap receipt, or schedule
historical repair. Independent age-based retention may delete the expired source data.

## Heartbeats and Receipts

Success records the committed window, commit ETag, segment count, aggregate records,
aggregate evidence counts, verification duration, restore result, and
`cleanup_eligible=false`.

Failure records `last_run_at`, `last_error_at`, a bounded machine-readable stage/error
code, and a redacted message. It does not overwrite the last successful result. Lock
contention is a failed run, not a silent skip.

Receipts always state `deletion_authorized=false`. Cutover and compensation receipts also
bind the window, source-control checksum, before/after commit ETags, manifests, and runner
evidence.

The host runner atomically writes `/var/lib/tokenkey/qa-maintenance-last-run.json` through
a mode-0600 temporary file, `fsync`, and rename. It contains a schema version, run ID,
trigger, timestamps, active container/image, runner UID/GID, normal result, optional
compensation result, child and runner exit codes, and redacted error codes. The runner
writes it even when image/mount/scratch preflight fails before the Go process can update
the database. A host receipt may prove an attempted failure; it never proves a commit.

Health evaluation correlates systemd timer/service state and last result, this host
receipt, the DB heartbeat with the same run/window, and the corresponding shard/segment
control rows. Missing or stale evidence, a systemd success with no DB/control progress,
or a receipt/heartbeat/control contradiction is degraded or failed. The last successful
DB heartbeat cannot mask a newer host-side failure. Any unresolved terminal failed hour
strictly after the cutover keeps archive health degraded even when later normal runs pass.

## Runtime and Infrastructure

The systemd service sets:

```ini
Nice=15
IOSchedulingClass=idle
CPUQuota=20%
MemoryMax=1G
PrivateTmp=true
```

The service uses one worker, bounded keyset page size, bounded multipart concurrency,
and a scratch-space preflight. The repo-owned host runner is the only timer/operator
entrypoint. It resolves the active app container and immutable image, then starts the
same constrained sibling container as UID/GID `1000:1000`, with the app network and
mounts. The only approved scratch mapping is:

```text
host:      /var/lib/tokenkey/app/qa_archive_tmp
container: /app/data/qa_archive_tmp
```

`--selftest` resolves the live `/app/data` bind source and launches the same image/user/
mount combination to create, read, and remove a sentinel through the container path,
then verifies the host path. Wrong ownership, mode, mount source, or image fails before
timer activation. Insufficient scratch space fails before upload. Operator automation
calls this runner remotely and validates its receipt; it never issues root `docker exec`.

The raw archive stack must also provide:

- a dedicated ops recovery role, assumed only by a non-empty approved principal, with
  read-only raw access and KMS decrypt permission;
- a dedicated retained audit bucket that accepts writes only from this stack's trail;
- an S3 Gateway VPC Endpoint attached to the prod route table;
- CloudTrail S3 object-level data events for the raw bucket;
- app-role permissions with no `s3:ListBucket`, no `raw/partial/` read, exact immutable
  artifact/manifest/commit writes, and GetObject resources restricted to the object
  suffixes read by commit verification and restore;
- an explicit security statement that the Stage0 gateway and maintenance sibling inherit
  one EC2 instance role. These policies reduce whole-host privilege but do not provide
  process-level IAM isolation; separate compute credentials are deferred beyond this
  closeout.

Deployment scripts resolve and print the exact role, VPC, route table, bucket, key, and
trail before change and fail closed when a production security parameter is blank.

The active stale-cleanup runner also owns crash-orphan cleanup under the effective export
temp path. With no non-empty `QA_EXPORT_TMP_DIR`, the effective container path is
`/app/data/qa_exports_tmp` and the host path is
`/var/lib/tokenkey/app/qa_exports_tmp`. It may select only regular files older than the
same 24-hour cutoff and with no open handle. The first deletion of the observed
`traj-export-4288971549.zip` (1,041,960,960 bytes) requires a no-write plan and exact plan
hash confirmation. This delivery diagnoses `qa_export_jobs` but does not delete its rows
or add job-history retention semantics.

## Testing

### Unit and contract

- historical backfill flags, selectors, and scripts are absent;
- exactly one valid cutover is possible, the marker satisfies its database validity check,
  and no command can move/unset it or select its window or any earlier window;
- normal failure performs no compensation; normal success performs zero or one oldest
  compensation; compensation failure makes the run nonzero;
- post-cutover selection is derived from the hourly timeline, not only source/control
  existence; a crash before control creation followed by retention becomes a durable
  `source_unavailable_after_retention` failure and cannot disappear;
- a timely zero-row normal/catch-up hour commits and restore-verifies one empty base, while
  an unknown hour first observed after retention can never produce that empty commit;
- a committed and restore-verified hour with one uncovered late source identity is selected,
  creates exactly one delta, and stops qualifying after membership converges;
- lock acquisition failure does not unlock and writes a failure heartbeat;
- missing evidence cannot reach verified or committed;
- retry with no source delta creates no second base;
- late rows create one delta and preserve the base descriptor;
- CAS conflict rereads and merges without unconditional overwrite;
- DB aggregate/checksum/ETag equal the reread S3 commit;
- corrupt manifest, Parquet, index, pack range, or checksum fails restore;
- existing 96-file gap derives blocked and never confirmed-gap;
- every receipt denies deletion;
- service unit resource controls are behaviorally asserted;
- timer and operator render the same runner command/image/user/mount/env contract;
- self-test performs a real UID/GID `1000:1000` write/read/delete through the live mount;
- host receipt rename is atomic and each pre-app failure remains observable;
- health fails on every systemd/receipt/heartbeat/control contradiction;
- default export-temp path and configured override resolve to their effective host mount;
- export orphan plan requires age, regular-file, no-open-handle, and exact plan hash; no
  `DELETE FROM qa_export_jobs` is introduced;
- IAM/KMS/bucket/trail/VPC endpoint templates enforce the intended principals and actions;
- the app role has no ListBucket or partial read and only suffix-scoped GetObject, including
  the declared `orphan-evidence-index.jsonl.zst` artifact; tests label the shared
  instance-role boundary as non-isolated.

### Integration

Use isolated PostgreSQL and an S3-compatible test service to run:

```text
archive base -> read-back verify -> restore
late insert -> delta -> CAS commit -> aggregate restore
concurrent CAS conflict -> bounded retry
missing/corrupt artifact -> failed/blocked
crash after upload -> orphan discovery -> source row remains retryable
normal failure -> no compensation
normal success + missing 22:00 control -> one base compensation -> restore
compensation failure -> normal retained + overall failed receipt
normal zero-row hour -> empty base commit -> restore
pre-control crash -> source cleanup -> durable source_unavailable_after_retention failure
committed hour + late source identity -> one delta -> membership convergence
host preflight failure -> host receipt advances while DB heartbeat does not
```

The resource test generates a dense shard larger than the memory limit expectation and
asserts bounded resident memory rather than merely checking that output files exist.

### qa_records DEFAULT rehome (OpsCleanup owner)

`qa_records` rows that landed in the DEFAULT partition must be moved into bounded monthly
partitions without disappearing from parent-table reads mid-run. Only **OpsCleanupService**
runs this work (`partitionmaintenance.OpsCleanupOptions`, 20k rows per tick); the QA
`:15` host runner and `--partition-maintenance-once` never invoke rehome.

**State machine (per month):**

```text
DEFAULT has month rows
  -> copy-only into {partition}_rehome_staging (rows remain visible via DEFAULT)
  -> when month fully copied OR orphan staging rediscovered:
       single finalize transaction (pg_advisory_xact_lock):
         sync copy -> delete month from DEFAULT -> CREATE PARTITION OF (if missing)
         -> INSERT partition <- staging -> DROP staging
  -> attached partition already exists: DELETE+INSERT move via parent visibility
```

**Parent visibility:** copy into detached staging never deletes DEFAULT rows. Reads through
`qa_records` stay complete until finalize commits. Budget exhaustion after copy leaves
rows in DEFAULT (and duplicate-safe staging) — not invisible.

**Partial progress / EnsureMonthly:** while DEFAULT still holds in-progress months,
staging tables exist, or the per-run row budget is exhausted, partition maintenance
records a partial `default_rehome` receipt and **skips** `EnsureMonthly` for
`qa_records` (PostgreSQL rejects creating a monthly partition while DEFAULT still holds
that month's rows). Subsequent OpsCleanup ticks resume copy and finalize.

**Crash recovery:** orphan `{table}_YYYY_MM_rehome_staging` tables are rediscovered via
catalog scan and finalized; copy-only staging never removes DEFAULT rows until finalize.
If finalize fails mid-transaction, the next tick retries copy + finalize. Concurrent
capture during copy is absorbed by request_id dedup and a transaction-local sync copy
before DELETE.

**Concurrency:** finalize holds `pg_advisory_xact_lock(qa_records)` for the duration of
delete + attach + staging load so only one finalize proceeds at a time.

### Production rollout

The age-based stale-cleanup timer is already active and healthy; keep it running throughout
this closeout. The maintenance timer was enabled only recently: a 22:54 root manual run
succeeded, while the 23:01 Persistent invocation and the first regular 23:15 invocation
failed. This is one failed normal scheduled window, not evidence of long-term instability.

1. Stop and verify `tokenkey-qa-maintenance.timer` inactive; verify stale cleanup remains active.
2. Deploy the additive cutover schema, single host runner, real self-test, atomic receipt,
   correlated health probe, and export-temp plan changes with scheduled maintenance off.
3. Run self-test against the live `/var/lib/tokenkey/app:/app/data` mount as UID/GID 1000.
4. Preserve the 01:00 and 04:00 failed control states and their immutable S3 evidence.
5. Independently verify/restore 21:00, then set it as the one exact cutover.
6. Through the host runner, archive and restore the normal window; only after success let
   bounded compensation select, commit, verify, and restore 22:00. If its source expired on
   a **new** selection attempt, persist `source_unavailable_after_retention`, stop that
   compensation attempt, and request a separate gap decision. Under `accepted_terminal`, an
   already-terminal 22:00 hour recorded in control rows does not block forward runs once
   the four-source health probe correlates; contradictory facts fail closed.
7. From an ops workstation, assume the recovery role and verify/restore S3 directly; after
   success retire the transitional prod QA break-glass dump tooling.
8. Enable the maintenance timer and observe at least two consecutive regular scheduled
   runs while correlating systemd, host receipt, DB heartbeat/control, DB latency, WAL,
   scratch, RSS, CPU, and S3 objects.
9. Apply the IAM tightening as the final CloudFormation change set and run an archive
   canary. On failure stop maintenance and restore the previous IAM policy; stale cleanup
   remains active.
10. Only after closeout may deploy rollout move the archive default from conservative off
    to the policy target. Delete the 1GB export orphan only through its separately confirmed
    stale-cleanup plan.

## Rollback

Disable the hourly maintenance timer and roll back the application image. Do not revert
additive control tables, remove/move the cutover, delete immutable S3 objects, delete the
host receipt, or overwrite a valid commit. The old application ignores the new fields.
Keep the independent age-based stale cleanup active and emergency deletion disabled. Any
segment uploaded but not referenced by a valid commit remains invisible and expires
through lifecycle policy. Before maintenance is re-enabled, rerun the real self-test and
the four-source health correlation.

## Completion Criteria

Phase 2 archive closeout is complete only when:

- every newly processed sealed source hour has a commit that passes independent restore;
- every post-cutover hour before the current normal window has durable control: a valid
  restored commit or an explicit terminal failure; no hour can disappear merely because
  source retention ran before control creation;
- the 01:00 shard is visibly failed with a 477-row commit mismatch and no S3 mutation;
- the 04:00 shard is visibly failed with 96 missing references and no S3 mutation;
- no unsealed-hour selection or repeated orphan-base creation is possible;
- 21:00 is the sole committed, restore-verified cutover and 22:00 is restored through the
  bounded compensation path, or rollout has stopped for a separately approved gap decision;
- success/failure heartbeats reflect actual commit state;
- timer/operator share one host runner, the live-mount UID self-test passes, and the atomic
  host receipt agrees with systemd, DB heartbeat, and control rows;
- runtime resource limits and the raw-bucket security/audit controls are deployed;
- the hourly archive timer completes at least two consecutive regular sealed hours under observation;
- app IAM has no ListBucket/partial read and suffix-scoped GetObject, with shared-role risk
  explicitly retained rather than reported as process isolation;
- stale cleanup remains active, owns `qa_exports_tmp`, and the first 1GB orphan is removed
  only by exact confirmed plan; `qa_export_jobs` history is untouched;
- workstation S3 restore succeeds and transitional break-glass dump tooling is retired;
- emergency QA cleanup and generic ops cleanup remain disabled.
