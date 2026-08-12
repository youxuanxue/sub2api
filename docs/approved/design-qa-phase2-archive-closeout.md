---
title: QA Phase 2 Archive Closeout
status: approved
approved_by: "feng (conversation approvals 2026-08-07 through 2026-08-11; UTC-hour partition lifecycle and no-rehome cutover 2026-08-11)"
date: 2026-08-07
last_reviewed: 2026-08-11
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

Physical QA deletion is independently time-based under the approved 24-to-25-hour
lifecycle. New hot rows and capture files are owned by exact UTC-hour partitions and
directories. At the hourly boundary, an expired database partition is dropped as a whole
and its matching hot-file directories are removed; there is no steady-state row DELETE,
rehome, copy, or move. Archive incompleteness remains visible through durable control
state but does not prevent expired hot data from normal retention.

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
- boundary-lifecycle ownership of crash-orphan files in the effective `qa_exports_tmp` directory;
- least-privilege app-role tightening that reflects, but does not overstate, the shared EC2 role boundary.
- exact UTC-hour `qa_records` partition provisioning through a 72-hour future horizon;
- a no-rehome transition from DEFAULT/monthly storage to default-free hourly storage;
- whole-partition and matching hot-file cleanup at the UTC hour boundary;
- one QA lifecycle owner with `*:00` boundary and `*:15` sealed-archive phases.

### Excluded

- recovering or fabricating the existing 96 missing files;
- repairing the 477 late identities or replacing either historical S3 commit;
- Phase 3 off-production user export and its UI/API;
- Phase 5 emergency deletion and P0 delivery;
- releasing the generic usage/ops cleanup hold;
- partitioning `usage_logs` or enabling telemetry shadow archive.
- arbitrary-window repair, historical backfill, or a second catch-up script;
- deleting `qa_export_jobs` rows or defining a new export-job retention policy;
- process-level IAM isolation between gateway and maintenance on the current shared EC2 host.
- moving or copying existing DEFAULT/monthly QA rows into hourly partitions;
- steady-state row-by-row QA retention or a rehome/staging state machine;

## Safety Invariants

1. The archive phase never deletes QA source rows or hot evidence files. The boundary
   phase may remove only an expired, catalog-validated UTC-hour partition and its exact
   matching hot-file directories under the lifecycle rules below.
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
    strand a default-free parent without future partition provisioning, or enable an
    unapproved row-deletion/rehome path.
11. A failed normal window suppresses compensation. A failed compensation keeps its
    machine-readable control state and makes the overall run fail.
12. The lifecycle boundary phase is the only owner allowed to remove expired hot
    partitions, hot blob/DLQ directories, and `qa_exports_tmp` crash orphans.
13. Steady state has no DEFAULT partition. A missing hourly partition fails QA capture
    persistence and raises a hard alert without failing the user request path.
14. Retention eligibility comes from database UTC time and catalog partition bounds, not
    table-name parsing. Archive failure never extends the 24-to-25-hour hot retention.
15. DEFAULT exists only during the no-move cutover. It must drain through the previously
    approved age cleanup and be empty under lock before removal.

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
hourly lifecycle owner, and completion criteria below are satisfied on production.

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
- `source_partition_name` and `source_dropped_at`, written in the same transaction as
  the corresponding hourly partition DROP;
- `hot_files_cleaned_at` and a bounded, redacted `hot_cleanup_error`, recording the
  independently retryable blob/DLQ directory cleanup after the database transaction;
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

This avoids introducing a second state-machine vocabulary while making the archive
integrity condition explicit; it does not gate time-based hot retention.

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

### Gap and hot-source cleanup state

Do not create a parallel `qa_archive_gaps` vocabulary or a second cleanup-control table.
The hourly shard row is the control owner for both archive outcome and hot-source removal.
Missing/corrupt evidence remains represented by shard verification failure and
`cleanup_eligible=false`; cleanup eligibility does not grant or deny time-based retention.

At `*:00`, the lifecycle boundary phase locks the QA lifecycle, resolves the exact expired
hour from database time, and validates the child bound through the PostgreSQL catalog. If
the shard is not committed and restore-verified, or committed membership does not cover
the remaining source identities, the same transaction first writes
`verification_error_code=source_unavailable_after_retention`, then drops the child and
sets `source_dropped_at`. A failure to persist that terminal fact or to DROP rolls back
both. After commit, matching hot-file directories are removed and
`hot_files_cleaned_at` is written. A crash between those steps is recovered idempotently
from `source_dropped_at`; an already absent directory is success. The terminal archive
failure remains visible without starving later retryable hours.

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
times. It never performs an unconditional overwrite. Under the approved no-ListBucket app
role, a missing-key `GetObject` 403 means commit existence is unknown, not absent. The
archiver may use conditional create to disambiguate a nonempty source window, but it must
not infer `source_unavailable_after_retention` from an expired zero-row source while commit
visibility is denied. That case remains retryable as `commit_existence_unknown`, and generic
failure persistence cannot downgrade an already committed shard.
Maintenance may attempt only `If-None-Match: *` creation when existence is unknown.
Success establishes the first commit; a conflict requires a successful reread before CAS and
otherwise fails closed. No `HeadObject` preflight, unconditional overwrite, or blanket
AccessDenied-as-NotFound rule is allowed.
Unknown existence does not extend hot retention: boundary still drops the expired catalog
child and records the source-drop facts, while preserving `commit_existence_unknown` instead
of fabricating a terminal gap.

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

Archive receipts always state `deletion_authorized=false`. Cutover and compensation
receipts also bind the window, source-control checksum, before/after commit ETags,
manifests, and runner evidence. Boundary receipts separately bind the database UTC anchor,
provisioned horizon, catalog-derived partition bound, archive disposition, partition DROP,
hot-file cleanup, and any idempotent recovery attempt; an archive receipt can never be
reinterpreted as boundary deletion authority.

The host runner atomically writes `/var/lib/tokenkey/qa-maintenance-last-run.json` through
a mode-0600 temporary file, `fsync`, and rename. It contains a schema version, run ID,
trigger, timestamps, active container/image, runner UID/GID, normal result, optional
compensation result, child and runner exit codes, and redacted error codes. The runner
writes it even when image/mount/scratch preflight fails before the Go process can update
the database. A host receipt may prove an attempted failure; it never proves a commit.

The boundary host runner independently writes `/var/lib/tokenkey/qa-boundary-last-run.json`
with the same atomic mode-0600 temporary-file, `fsync`, rename, and parent-directory `fsync`
discipline. It binds run ID, trigger, active container and immutable image, runner UID/GID,
child/runner exits, redacted error code, provisioning facts, exact catalog-bound expiry,
partition DROP, and hot-file cleanup. A pre-app resolver/image/mount failure must still
advance this host receipt; a scheduled boundary run never overwrites it with an operator
cutover receipt.

Health evaluation correlates both systemd phases and last results, their host receipts,
DB heartbeats, catalog bounds, shard/segment control rows, and hot-file cleanup state.
Missing or stale evidence, a systemd success with no DB/control progress, or a
receipt/heartbeat/control contradiction is failed. The last successful DB heartbeat
cannot mask a newer host-side failure. `source_unavailable_after_retention` is the only
accepted degraded terminal failure. Lifecycle health derives `pre_activate`,
`scheduled_activation`, `draining`, or `finalized` from append-only receipt T0 facts.
Before finalize the boundary timer must remain disabled; scheduled activation requires exact
`[T0,T0+72h)` coverage, while draining requires the current child and zero rows routed to
DEFAULT at or after T0. Only finalized steady state requires a fresh boundary receipt,
current-plus-72-hour coverage, no DEFAULT, no overdue attached partition, and no lingering
hot-file cleanup. `archive_failed` is always failed and returns a nonzero health exit.

## Runtime and Infrastructure

The archive systemd service sets:

```ini
Nice=15
IOSchedulingClass=idle
CPUQuota=20%
MemoryMax=1G
PrivateTmp=true
TimeoutStartSec=40min
```

The service uses one worker, bounded keyset page size, bounded multipart concurrency,
and a scratch-space preflight. The repo-owned archive host runner is the only archive timer/operator
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

Boundary scheduling and all cutover modes (`inventory`, `activate plan/apply`, pre-finalize
`provision-only`, and `finalize plan/apply`) likewise enter through
`/usr/local/bin/tokenkey-qa-boundary.sh`.
The runner resolves the same active immutable image and hardened sibling-container runtime;
the app CLI is not a supported direct prod operator surface. Exactly one mode is accepted
per invocation. Apply consumes a hash-bound plan and confirmation; after a durable phase
receipt exists, the same phase/hash/T0 replay succeeds without rebuilding mutable inventory,
while a different hash or T0 fails closed.

The hash binds only decision-bearing facts, not live row counts that continue changing between
plan and apply. Activation binds the database hour anchor, T0, horizon, and exact empty monthly
children/bounds to remove. Finalize binds the database hour anchor, T0, future coverage,
DEFAULT presence/count, the exact remaining empty legacy monthly children/bounds, legacy
blob/DLQ counts, and archive-heartbeat gate. Apply reconstructs
those facts and rechecks all catalog/data preconditions under lock; unrelated DEFAULT writes
before T0 or active-hour writes after T0 cannot make an otherwise valid plan impossible to apply.

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

The `*:00` lifecycle boundary runner also owns crash-orphan cleanup under the effective
export temp path. With no non-empty `QA_EXPORT_TMP_DIR`, the effective container path is
`/app/data/qa_exports_tmp` and the host path is
`/var/lib/tokenkey/app/qa_exports_tmp`. It may select only regular files older than the
same retention boundary and with no open handle. The first deletion of the observed
`traj-export-4288971549.zip` (1,041,960,960 bytes) requires a no-write plan and exact plan
hash confirmation. Scheduled cleanup also executes plan followed by apply with the same
cutoff, exact hash and expected count, then validates the deletion receipt. This delivery diagnoses `qa_export_jobs` but does not delete its rows
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
- every archive receipt denies deletion, while boundary receipts bind one exact catalog
  hour and cannot authorize arbitrary row or file deletion;
- service unit resource controls are behaviorally asserted;
- timer and operator render the same runner command/image/user/mount/env contract;
- self-test performs a real UID/GID `1000:1000` write/read/delete through the live mount;
- host receipt rename is atomic and each pre-app failure remains observable;
- boundary operator modes use the same hardened host runner as the timer and cannot overwrite
  the scheduled boundary receipt;
- provision-only is accepted only at or after activate T0 and before finalize, takes the QAMA
  lock, creates current-plus-72-hour children, and cannot run expiry DROP or file cleanup;
- activate/finalize phase receipts are append-only; same phase/hash/T0 replay is idempotent,
  different facts fail, and finalize without a matching activate receipt is rejected;
- finalize rejects stale/failed archive host receipt or DB heartbeat, any legacy blob/DLQ
  file, nonzero export-orphan plan, DEFAULT rows, nonempty or unknown-layout legacy child,
  empty-monthly inventory drift, or future coverage gap;
- owner switch consumes the durable finalize receipt before disabling legacy cleanup and
  keeps legacy disabled while best-effort preserving boundary if enable/verification fails;
- health fails on every systemd/receipt/heartbeat/control contradiction;
- default export-temp path and configured override resolve to their effective host mount;
- export orphan plan requires age, regular-file, no-open-handle, and exact plan hash; no
  `DELETE FROM qa_export_jobs` is introduced;
- UTC-hour names and bounds remain correct across day/month/year boundaries and every
  configured local timezone/DST transition;
- every new row's `retention_until` equals its UTC partition upper bound plus 24 hours;
- DEFAULT nonempty blocks removal; default-free steady state rejects a write when the
  matching hourly child is absent without failing the gateway request;
- current through future 72-hour coverage, no overlap, and no overdue attached hour are
  mechanically asserted by the health contract;
- repository sentinels reject QA rehome/staging code and steady-state row DELETE cleanup;
- blob/DLQ hour-directory cleanup rejects noncanonical paths, symlinks, and any path
  outside the one receipt-bound hour;
- systemd contract tests fix `*:00`, `*:15`, no randomized delay, the shared lock, and the
  archive hard timeout;
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
T0 cutover -> old DEFAULT rows stay put -> new writes route hourly -> DEFAULT drains
T0+25h provision-only -> current-plus-72-hour coverage without DROP -> finalize plan
nonempty DEFAULT -> removal rejected; empty DEFAULT + 72h coverage -> removal succeeds
archive read racing expired DROP -> shared lock serializes both phases
uncommitted/late-delta hour at retention -> terminal gap + DROP in one transaction
terminal-gap write or DROP failure -> transaction rollback leaves source attached
DROP commit -> process crash -> exact blob/DLQ hour cleanup resumes idempotently
missing current child -> QA persistence alert while gateway response remains successful
```

The resource test generates a dense shard larger than the memory limit expectation and
asserts bounded resident memory rather than merely checking that output files exist.

### UTC-hour hot-source lifecycle (single owner, no rehome)

QA hot storage follows the same one-hour UTC window as raw archive. Database children are
named `qa_records_YYYYMMDD_HH` and have exact half-open bounds `[hour, hour+1h)`. Capture
blobs and DLQ files written after cutover use matching paths:

```text
qa_blobs/YYYY/MM/DD/HH/<request-prefix>/<request-id>.json.zst
qa_dlq/YYYY/MM/DD/HH/<request-id>.json.zst
raw/date=YYYY-MM-DD/hour=HH/...
```

The lifecycle has one logical owner and two deterministic systemd phases using the
existing `QAMA` advisory lock:

- `*:00` boundary: validate current coverage, provision through the next 72 hours,
  classify the exact expired source hour, DROP its whole database partition, and clean
  its matching blob/DLQ directories plus eligible export-temp orphans;
- `*:15` archive: reconcile the previous sealed hour and at most one oldest retryable
  post-cutover hour. It has a 40-minute hard runtime limit so it cannot hold the lock
  across the next boundary phase.

The boundary is `date_trunc('hour', clock_timestamp()) - interval '24 hours'`. Only a
canonical direct child whose catalog upper bound is at or before that boundary is
eligible. Names are corroborating evidence only; an unknown, malformed, overlapping, or
non-hour bound fails closed. Normal on-time execution retains each record for 24 to 25
hours. New rows set `retention_until` to their UTC hour upper bound plus 24 hours, so the
field describes the partition DROP boundary rather than the earlier
`created_at + 24 hours` eligibility instant. The timer has no randomized delay;
scheduling delay is surfaced by health.
Provision and expiry cleanup use separate transactions under the same run. Provisioning and
the complete current-plus-72-hour coverage check are hard prerequisites: either failure ends
the run before any archive inspection, partition DROP, or file cleanup.

Steady state has no DEFAULT partition. Provisioning is a hard prerequisite, and a missing
current partition makes QA persistence fail and alert without failing the gateway request.
There is no `RehomeDefaultMonthly`, staging table, copy budget, row-migration dedup/finalize path, or
steady-state row DELETE. Archive and export continue to query the parent `qa_records` and
remain independent of physical child names.

**No-move transition:** inventory current bounds and future-dated rows first. Remove only
empty monthly children that overlap the future hourly horizon; a nonempty child drains
through the existing age cleanup and is never moved. While DEFAULT and the legacy cleanup
remain active, provision hourly children beginning at a future exact UTC hour `T0`.
Writes at and after `T0` route naturally to hourly children; old DEFAULT/monthly rows and
day-layout files expire in place. The activation horizon intentionally decays during drain.
At or after T0, and before finalize, an explicitly confirmed provision-only operator command
extends coverage from the current database hour through 72 hours while the boundary timer
stays disabled; this command cannot inspect archive state, DROP partitions, or clean files.
After at least 25 hours, require DEFAULT row count zero, no nonempty or unknown-layout legacy
child, no expired legacy file, complete current-plus-72-hour coverage,
zero export-orphan plan count, a fresh successful archive host receipt for the active
container/image, a fresh successful archive DB heartbeat, and correlated healthy receipts.
Finalize also requires the durable activate receipt for the exact same T0. A database insert
trigger independently rejects a finalize receipt without that same-T0 activation. The finalize
plan hash binds every remaining empty monthly child by exact schema/name/bounds. Under the
parent/lifecycle lock, recheck that the bound set is complete, unchanged, and still empty, then
drop those children and the empty DEFAULT in the same transaction. Any missing, extra, changed,
or newly nonempty monthly child fails before the first DROP. Only then replace legacy row/file cleanup with the `*:00`
whole-hour boundary phase.

File cleanup follows the database transaction. The transaction records any terminal
archive gap, drops the child, and sets `source_dropped_at` atomically. Before archive
inspection, it takes `ACCESS EXCLUSIVE` on the selected direct child and rereads the catalog;
parent attachment, canonical name, and exact bounds must still match the enumerated hour.
Directory cleanup
then sets `hot_files_cleaned_at`. A failed or interrupted directory removal is retried
from control state; path canonicalization, exact-hour containment, and symlink rejection
prevent a cleanup scope escape. Export scratch remains file-oriented and retains its
age/open-handle/plan-hash checks because it is not partitioned source data.

### Production rollout

The transition is additive until the explicit DEFAULT-removal gate. The legacy age cleanup
stays active while old DEFAULT/monthly rows and day-layout files drain; the new boundary
phase remains disabled. No rollout step copies or moves source data.

1. Stop and verify the archive and new boundary timers inactive; verify legacy age cleanup
   remains active.
2. Deploy the additive archive/control schema, hourly provisioner, cutover-aware hourly
   blob/DLQ paths, lifecycle receipts, correlated health probe, and runner changes with
   scheduled lifecycle phases off.
3. Run self-test against the live `/var/lib/tokenkey/app:/app/data` mount as UID/GID 1000.
4. Preserve the 01:00 and 04:00 failed control states and their immutable S3 evidence.
5. Independently verify/restore 21:00, then set it as the one exact archive cutover.
6. Through the host runner, archive and restore the normal window; only after success let
   bounded compensation select, commit, verify, and restore 22:00. If its source expired on
   a **new** selection attempt, persist `source_unavailable_after_retention`, stop that
   compensation attempt, and request a separate gap decision. Under `accepted_terminal`, an
   already-terminal 22:00 hour recorded in control rows does not block forward runs once
   the four-source health probe correlates; contradictory facts fail closed.
7. From an ops workstation, assume the recovery role and verify/restore S3 directly; after
   success retire the transitional prod QA break-glass dump tooling.
8. Enable the `*:15` archive timer and observe at least two consecutive regular scheduled
   runs while correlating systemd, host receipt, DB heartbeat/control, DB latency, WAL,
   scratch, RSS, CPU, and S3 objects.
9. Apply the IAM tightening as the final CloudFormation change set and run an archive
   canary. On failure stop maintenance and restore the previous IAM policy; the pre-finalize
   legacy cutover drain remains active. A durable finalize receipt permanently closes it.
10. Produce a read-only hourly-cutover inventory: exact child bounds, row counts by child
    and hour, future timestamps, overlapping monthly children, current/future coverage,
    DEFAULT age, and legacy blob/DLQ/export paths. Bind the decision-bearing inventory facts and chosen future
    `T0` to a separate high-risk approval.
11. Under that approval, remove only empty overlapping monthly children and provision
    `[T0,T0+72h)`. Set the immutable application cutover hour to `T0`; PostgreSQL routes
    new rows and the capture writer uses hourly file paths from that boundary.
12. Keep legacy age cleanup active for at least 25 hours. Abort if DEFAULT grows after
    `T0`, the current-hour child is missing, or archive/cleanup evidence contradicts. The
    initial activation horizon may decay during this drain; the boundary timer remains disabled.
13. Through the boundary host runner, run `--qa-cutover-provision-only` with exact confirmation
    `tokenkey-prod-qa-cutover-provision-v1`. It requires the activate receipt, database time at
    or after T0, no finalize receipt, and provisions current-plus-72-hour coverage under the
    shared lock without DROP or cleanup. Then require fresh successful archive host receipt and
    DB heartbeat plus a zero export-orphan plan, build the finalize plan, and apply it under the
    same-T0 gate. Finalize revalidates DEFAULT empty, no nonempty or unknown-layout legacy child,
    legacy blob/DLQ files drained, the exact empty-monthly hash-bound set, and 72-hour coverage
    before atomically removing that monthly set plus DEFAULT and persisting the append-only
    finalize receipt.
14. The owner-switch SSM workflow must read that durable finalize receipt before disabling
    legacy DB/blob/DLQ cleanup and enabling the no-random-delay `*:00` boundary timer. If any
    enable/verification step fails, it keeps legacy disabled, best-effort leaves boundary enabled,
    and exits nonzero for hard intervention; finalized legacy cleanup is never a valid fallback.
    Export-orphan cleanup remains under the boundary runner.
15. Observe the archive and boundary phases for at least 26 hours, including one real partition DROP,
    blob/DLQ directory cleanup, archive success or durable gap classification, and continuous
    future provisioning. Only then may rollout report the hourly lifecycle complete.

## Rollback

Before DEFAULT removal, disable the new phases, roll back the application image, and keep
legacy age cleanup active; existing rows/files were never moved. Do not revert additive
control tables, remove/move either cutover, delete immutable S3 objects, delete receipts,
or overwrite a valid commit.

After DEFAULT removal, an application rollback must keep the compatible hourly provisioner
and boundary cleanup active: a default-free parent cannot be left to exhaust its future
horizon. Recreating DEFAULT changes the approved fail-closed contract and is a separate
high-risk recovery action, not an automatic rollback step. Any uploaded segment not
referenced by a valid commit remains invisible and expires through S3 lifecycle policy.
Before archive maintenance is re-enabled, rerun the real self-test and correlated health.

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
- cutover inventory/activate/provision-only/finalize run only through the boundary host runner;
  the durable same-T0 activate/finalize receipts support exact replay and gate the
  legacy-to-boundary owner switch;
- runtime resource limits and the raw-bucket security/audit controls are deployed;
- the hourly archive timer completes at least two consecutive regular sealed hours under observation;
- app IAM has no ListBucket/partial read and suffix-scoped GetObject, with shared-role risk
  explicitly retained rather than reported as process isolation;
- `qa_records` has canonical UTC-hour children through the future 72-hour horizon, no
  DEFAULT, no rehome/staging path, and no overdue attached child;
- at least one production hour completes the full boundary lifecycle: archive terminal
  fact, transactional partition DROP, and idempotent blob/DLQ directory cleanup;
- the boundary phase owns `qa_exports_tmp`, and the first 1GB orphan is removed only by
  exact confirmed plan; `qa_export_jobs` history is untouched;
- workstation S3 restore succeeds and transitional break-glass dump tooling is retired;
- no steady-state QA row DELETE exists; emergency QA cleanup and generic ops cleanup remain disabled.
