---
title: QA Phase 2 Archive Closeout
status: approved
approved_by: "feng (conversation approval, 2026-08-07)"
date: 2026-08-07
supersedes: null
related:
  - docs/approved/design-prod-qa-24h-s3-lifecycle.md
---

# QA Phase 2 Archive Closeout Design

**Approval baseline:** `docs/approved/design-prod-qa-24h-s3-lifecycle.md`

## Purpose

Complete the production QA raw-archive phase so an hourly shard is not reported as
complete until its immutable artifacts can be read back, verified, and reconstructed.
The implementation must repair the known `2026-08-07 01:00 UTC` late-row gap without
rewriting its existing base segment and must keep the `2026-08-04 04:00 UTC` shard
blocked because 96 referenced evidence files are missing.

This delivery does not enable physical QA deletion. The legacy stale-cleanup timer,
the hourly maintenance timer, and the generic ops cleanup hold remain disabled until
the rollout checks in this document pass and a separate Phase 4 approval authorizes
deletion.

## Scope

### Included

- sealed-window selection for regular and backfill maintenance;
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
- an explicit repair command for the two known malformed production shards.

### Excluded

- confirming the existing 96 missing files as an accepted archive gap;
- deleting any `qa_records`, local evidence, quarantine, or S3 object;
- enabling the Phase 4 24-hour cleanup path;
- Phase 3 off-production user export and its UI/API;
- Phase 5 emergency deletion and P0 delivery;
- releasing the generic usage/ops cleanup hold;
- partitioning `usage_logs` or enabling telemetry shadow archive.

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
7. Backfill only selects a window when `window_end + seal_delay <= now`.
8. Failed maintenance writes a failed heartbeat even when failure occurs before upload,
   including advisory-lock contention and window-selection failure.
9. Resource limits apply to scheduled and operator-invoked maintenance.
10. Rollback may stop archive writes, but it must not overwrite the newer live policy or
    re-enable either cleanup path.

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
- `cleanup_eligible`, default and forced to false in Phase 2.

The allowed Phase 2 states remain `pending`, `writing`, `verified`, `committed`, and
`failed`. The operator/UI term **blocked** is a derived condition:

```text
state = failed AND verification_error_code IN (missing_evidence, corrupt_artifact,
commit_mismatch, restore_failed)
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

Do not create `qa_archive_gaps` or `qa_cleanup_receipts` in Phase 2. They have no active
writer until an independently approved deletion phase, so adding them now would create
an unused second lifecycle surface. Missing/corrupt evidence remains represented by the
shard verification failure and `cleanup_eligible=false`. A future gap command and cleanup
receipt schema require their own deletion-phase approval.

## Archive Pipeline

### Window selection

Regular maintenance selects the previous sealed hour. Backfill selects the oldest hour
that has source rows, is not fully committed, and satisfies the same seal cutoff. A
committed but incomplete hour is selected for reconcile rather than treated as done.

The maintenance advisory lock is released only when acquisition succeeded. Every exit
path records a bounded failure heartbeat after releasing the database connection.

### Segment construction

The archiver reads `qa_records` by `(created_at, request_id)` keyset pages. It writes
Parquet, evidence pack, and compressed evidence index to mode-0600 temporary files in a
dedicated scratch directory. The process never holds the full hour, full Parquet file,
or full evidence pack in memory.

For a new shard, all source identities enter one base segment. For a shard with an
existing commit, the archiver loads committed memberships and selects only uncommitted
source identities into a delta segment. An empty diff performs verification only and
does not create an object.

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

Add a repo-owned Go CLI under `backend/cmd/qa-archive`:

- `inspect`: print window, state, segment metadata, counts, and missing/corrupt status;
- `verify`: perform commit/manifest/artifact/checksum verification without writing prod;
- `restore`: reconstruct records and evidence into an explicit local isolated directory;
- `repair-plan`: produce a no-write plan comparing prod control state, S3 commit, and
  current source identities;
- `repair-apply`: require an exact window-bound confirmation and active safety holds,
  then call the same reconcile service used by hourly maintenance. It cannot confirm
  gaps or delete source data.

The CLI is a thin adapter over the archive/reconcile packages. It does not duplicate
window selection, segment construction, verification, commit, or heartbeat logic.

The CLI defaults to metadata-only output. Restoring evidence bodies requires an explicit
privacy confirmation. Local directories are mode 0700, files are mode 0600, and receipts
contain no bodies, credentials, cookies, or API keys.

## Known Production Repair

### `2026-08-07 01:00 UTC`

Treat the existing 407-row segment referenced by `commit.json` as the base. Import its
source identities into segment membership after independently verifying the existing
artifacts. Ignore the unreferenced 884-row base-shaped object as an orphan. Build a delta
for the source identities absent from the 407-row base, verify it, and CAS-update the
commit. The expected aggregate is derived at repair time; no hard-coded row count is used
as an authorization gate. The repair receipt records before/after ETags and counts.

### `2026-08-04 04:00 UTC`

Reverify the existing commit and manifest. Because 96 evidence references are missing,
set the shard to `failed` with `verification_error_code=missing_evidence`, derive
`blocked=true`, and keep `cleanup_eligible=false`. Do not alter the S3 commit, create a
gap receipt, or delete any source row. The repair output lists only safe request IDs and
counts needed for later human impact review.

## Heartbeats and Receipts

Success records the committed window, commit ETag, segment count, aggregate records,
aggregate evidence counts, verification duration, restore result, and
`cleanup_eligible=false`.

Failure records `last_run_at`, `last_error_at`, a bounded machine-readable stage/error
code, and a redacted message. It does not overwrite the last successful result. Lock
contention is a failed run, not a silent skip.

Receipts always state `deletion_authorized=false`. Repair receipts additionally bind the
window, source-control checksum, before/after commit ETags, manifests, and active hold
evidence.

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
and a scratch-space preflight. Insufficient scratch space fails before upload.

The raw archive stack must also provide:

- a non-empty ops recovery principal/role binding with read-only raw access and KMS
  decrypt permission;
- an S3 Gateway VPC Endpoint attached to the prod route table;
- CloudTrail S3 object-level data events for the raw bucket;
- app-role permissions limited to immutable artifact writes, commit/manifest reads, and
  conditional commit writes required by maintenance;
- no general evidence-pack read through the prod API role outside maintenance verification.

Deployment scripts resolve and print the exact role, VPC, route table, bucket, key, and
trail before change and fail closed when a production security parameter is blank.

## Testing

### Unit and contract

- backfill refuses current/unsealed hours;
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
- IAM/KMS/bucket/trail/VPC endpoint templates enforce the intended principals and actions.

### Integration

Use isolated PostgreSQL and an S3-compatible test service to run:

```text
archive base -> read-back verify -> restore
late insert -> delta -> CAS commit -> aggregate restore
concurrent CAS conflict -> bounded retry
missing/corrupt artifact -> failed/blocked
crash after upload -> orphan discovery -> source row remains retryable
```

The resource test generates a dense shard larger than the memory limit expectation and
asserts bounded resident memory rather than merely checking that output files exist.

### Production rollout

1. Keep both QA timers and generic ops cleanup disabled.
2. Deploy schema and archive code with no scheduled execution.
3. Run read-only `inspect` and `repair-plan` for all existing shards.
4. Restore a seeded sample plus the complete repaired 01:00 shard to an isolated host.
5. Apply the 01:00 delta repair and reverify the committed aggregate.
6. Mark the 04:00 shard blocked after revalidation; do not confirm its gap.
7. Run one sealed new hour manually through archive, verify, and restore.
8. Observe database latency, WAL, scratch usage, RSS, CPU, S3 objects, and heartbeat.
9. Re-enable only `tokenkey-qa-maintenance.timer` after all checks pass.
10. Keep `tokenkey-qa-stale-cleanup.timer` disabled and keep
    `cleanup_eligible=false`; Phase 4 requires separate approval.

## Rollback

Disable the hourly maintenance timer and roll back the application image. Do not revert
additive control tables or delete immutable S3 objects. The old application ignores the
new tables. Keep both cleanup paths disabled. Any segment uploaded but not referenced by
a valid commit remains invisible and expires through lifecycle policy.

## Completion Criteria

Phase 2 archive closeout is complete only when:

- every non-blocked sealed source hour has a commit that passes independent restore;
- the repaired 01:00 aggregate contains the base plus append-only delta and matches its
  source identity set at final reconcile;
- the 04:00 shard is visibly blocked with 96 missing references and no gap approval;
- no unsealed-hour selection or repeated orphan-base creation is possible;
- success/failure heartbeats reflect actual commit state;
- runtime resource limits and the raw-bucket security/audit controls are deployed;
- the hourly archive timer completes a new sealed hour under observation;
- both QA cleanup and generic ops cleanup remain disabled.
