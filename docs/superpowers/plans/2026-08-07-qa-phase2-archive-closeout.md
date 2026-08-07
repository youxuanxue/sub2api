# QA Phase 2 Archive Closeout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the QA raw archive state machine, append-only late-row reconciliation, conditional S3 commit, independent restore verification, and guarded production repair without enabling deletion.

**Architecture:** `archive.Reconciler` is the single archive owner used by hourly maintenance and operator repair. It builds immutable file-backed base/delta segments, verifies them through the object-store read boundary, conditionally updates the hour commit, rereads the committed set, and persists matching PostgreSQL control state. Operator commands are thin adapters; all receipts deny deletion.

**Tech Stack:** Go 1.24, PostgreSQL, parquet-go, zstd, AWS SDK v2 S3/KMS/CloudFormation, systemd, Python/shell ops tests.

## Global Constraints

- Approval anchor: `docs/approved/design-qa-phase2-archive-closeout.md`.
- No code path may delete `qa_records`, local QA evidence, or S3 raw objects.
- Existing 96 missing references remain blocked and are not confirmed as a gap.
- `cleanup_eligible` is always false in this delivery.
- Existing committed base segments are immutable; late rows use delta segments.
- Every production mutation requires an exact confirmation and emits `deletion_authorized=false`.
- Both QA timers and generic ops cleanup remain disabled until rollout completion.

---

### Task 1: Additive Archive Control Schema

**Files:**
- Create: `backend/migrations/tk_070_qa_archive_closeout_control.sql`
- Create: `backend/internal/observability/qa/archive/control_schema_test.go`
- Modify: `backend/ent/schema/qa_archive_shard.go`

**Interfaces:**
- Produces `qa_archive_segments` and `qa_archive_segment_records` tables.
- Extends `qa_archive_shards` with commit/verification aggregate columns.
- Does not create gap or cleanup receipt tables.

- [ ] Write a migration integration/contract test that applies `tk_069` then `tk_070` twice and checks constraints, indexes, defaults, and absence of destructive SQL.
- [ ] Run the focused test and observe failure because `tk_070` is missing.
- [ ] Add the idempotent expand-only migration and align the Ent shard schema.
- [ ] Run focused schema tests and migration safety checks.
- [ ] Commit with `feat(qa): add archive closeout control schema` and `Web impact: none` in the body.

### Task 2: Conditional Streaming Object Store

**Files:**
- Modify: `backend/internal/observability/qa/archive/store.go`
- Modify: `backend/internal/observability/qa/archive/memory_store.go`
- Create: `backend/internal/observability/qa/archive/store_test.go`

**Interfaces:**
- Produces `ObjectInfo`, `ObjectReader`, `PutReader`, `Create`, and `CompareAndSwap`.
- `ErrPreconditionFailed` is matchable with `errors.Is`.
- Keeps bounded readers; no artifact API requires `[]byte`.

- [ ] Write tests proving create-only semantics, ETag changes, stale ETag rejection, and bounded reader round-trip.
- [ ] Run tests and observe compile failures for the missing interface.
- [ ] Implement memory-store behavior, then S3 PutObject/GetObject/HeadObject conditional behavior.
- [ ] Run focused tests and `go test -tags=unit ./internal/observability/qa/archive`.
- [ ] Commit `feat(qa): add conditional archive object store`.

### Task 3: File-Backed Segment Builder

**Files:**
- Replace responsibilities in: `backend/internal/observability/qa/archive/writer.go`
- Create: `backend/internal/observability/qa/archive/segment_builder.go`
- Create: `backend/internal/observability/qa/archive/segment_builder_test.go`
- Modify: `backend/internal/observability/qa/archive/manifest.go`

**Interfaces:**
- Produces `BuildSegment(ctx, conn, BuildInput) (BuiltSegment, error)`.
- `BuildInput` includes window, kind, excluded committed identities, blob root, scratch root, page size.
- `BuiltSegment` owns mode-0600 files and a `Close` cleanup method.
- Missing evidence returns typed `IntegrityError{Code: "missing_evidence"}` before upload.

- [ ] Write failing tests for paged ordering, delta exclusion, missing evidence fail-closed, and scratch cleanup.
- [ ] Run RED tests.
- [ ] Implement keyset-paged DB reads and file-backed Parquet/evidence/index writers.
- [ ] Verify no full-hour `[]RecordRow`/artifact byte accumulation remains in production path.
- [ ] Run focused tests and a generated dense fixture RSS test.
- [ ] Commit `feat(qa): stream immutable archive segments`.

### Task 4: Read-Back Verification and Restore

**Files:**
- Create: `backend/internal/observability/qa/archive/verifier.go`
- Create: `backend/internal/observability/qa/archive/verifier_test.go`
- Modify: `backend/internal/observability/qa/archive/manifest.go`

**Interfaces:**
- Produces `VerifySegment(ctx, store, descriptor, restoreDir) (VerifiedSegment, error)`.
- Produces `VerifyCommit(ctx, store, commitKey, restoreDir) (VerifiedCommit, error)`.
- Every reader rejects missing/corrupt counts, checksum mismatch, invalid pack ranges, duplicate identities, or aggregate mismatch.

- [ ] Write corrupt-manifest/parquet/index/pack and missing-evidence RED tests.
- [ ] Write a successful restore fixture with literal expected records/evidence.
- [ ] Implement streaming SHA-256, Parquet identity decode, pack range verification, and mode-0700/0600 restore.
- [ ] Run focused and archive package tests.
- [ ] Commit `feat(qa): verify and restore raw archive commits`.

### Task 5: Single Reconcile State Machine

**Files:**
- Create: `backend/internal/observability/qa/archive/reconciler.go`
- Create: `backend/internal/observability/qa/archive/reconciler_test.go`
- Replace legacy commit logic in: `backend/internal/observability/qa/archive/writer.go`

**Interfaces:**
- Produces `Reconciler.Reconcile(ctx, conn, Window) (Receipt, error)`.
- Imports and verifies existing v1 commits before control bootstrap.
- Builds base only when no commit exists; otherwise builds delta for missing identities.
- CAS retries are bounded and reread/merge the latest commit.
- Receipt aggregate comes from the final reread commit and always denies deletion.

- [ ] Write RED tests for base, no-op retry, late delta, stale CAS retry, crash/orphan retry, DB/S3 parity, and blocked missing evidence.
- [ ] Implement control repository helpers and transactionally persist verified/committed states.
- [ ] Remove `reconcileCommitJSON` acceptance of a different same-window commit.
- [ ] Run archive package tests with race detection.
- [ ] Commit `feat(qa): reconcile verified base and delta commits`.

### Task 6: Maintenance Window and Failure Heartbeats

**Files:**
- Modify: `backend/cmd/server/qa_maintenance.go`
- Modify: `backend/cmd/server/qa_maintenance_test.go`

**Interfaces:**
- Backfill query receives `sealedBefore` and excludes unsealed windows.
- Maintenance calls `archive.Reconciler`.
- Deferred unlock only runs after successful lock acquisition.
- Every failure writes `LastRunAt`, `LastErrorAt`, redacted `LastError`, duration, and stage result.

- [ ] Add RED tests for current-hour rejection, lock contention heartbeat/no unlock, upload/verify/CAS failure heartbeat, and committed aggregate success heartbeat.
- [ ] Implement one exit recorder and sealed-window query.
- [ ] Run focused server unit tests and archive tests.
- [ ] Commit `fix(qa): make maintenance fail closed and observable`.

### Task 7: Operator Inspect, Verify, Restore, and Repair

**Files:**
- Create: `backend/cmd/qa-archive/main.go`
- Create: `backend/cmd/qa-archive/main_test.go`
- Create: `ops/qa/prod_qa_archive_closeout.py` <!-- script-ref-allow-missing -->
- Modify: `ops/qa/test_qa_phase_ops.py`

**Interfaces:**
- Read-only commands: `inspect`, `verify`, `restore`, `repair-plan`.
- Write command: `repair-apply --window-start ... --confirm tokenkey-prod-qa-archive-repair-v1`.
- Repair calls `Reconciler`; it checks both QA timers disabled and generic cleanup hold active.
- All output is structured JSON and denies deletion.

- [ ] Write RED tests for command safety, privacy confirmation, exact window token, and hold/timer refusal.
- [ ] Implement thin Go CLI and SSM controller.
- [ ] Run unit and ops tests.
- [ ] Commit `feat(qa): add guarded archive closeout operator`.

### Task 8: Runtime Limits and Raw Archive Security

**Files:**
- Modify: `deploy/aws/stage0/tokenkey-qa-maintenance.sh`
- Modify: `deploy/aws/cloudformation/stage0-qa-raw-archive.yaml`
- Modify: `ops/qa/deploy_qa_raw_archive_cfn.sh`
- Modify: `ops/qa/test_qa_phase_ops.py`

**Interfaces:**
- Service unit enforces `Nice=15`, `IOSchedulingClass=idle`, `CPUQuota=20%`, `MemoryMax=1G`, `PrivateTmp=true`.
- CFN requires recovery principal, VPC ID, route table IDs, and audit trail destination; creates S3 gateway endpoint and S3 data-event selector.
- Deployment refuses blank security parameters and prints a no-change plan before apply.

- [ ] Add behavioral template/unit RED tests for resource controls and missing parameter refusal.
- [ ] Implement service and additive CloudFormation resources with least-privilege policies.
- [ ] Run `cfn-lint`/AWS template validation when available and focused ops tests.
- [ ] Regenerate any embedded Stage0 artifact required by preflight.
- [ ] Commit `feat(qa): bound maintenance and audit raw archive access`.

### Task 9: Full Verification, Review, and Production Rollout

**Files:**
- Modify as required by review findings only.
- Create rollout receipts outside git or under the approved evidence path where required.

**Interfaces:**
- No timer enable or source cleanup before all checkpoints pass.

- [ ] Run Go archive/server/CLI unit and integration tests, Python ops tests, shell syntax, migration checks, race tests, and full `./scripts/preflight.sh`.
- [ ] Perform code review focused on data loss, retry idempotency, security boundaries, and resource limits; fix findings with RED tests.
- [ ] Push branch, open one Chinese PR, monitor CI, obtain merge/release approval, and deploy schema/code with timers still disabled.
- [ ] Run read-only all-shard inspect and repair plans.
- [ ] Apply 01:00 delta repair and verify aggregate restore; mark 04:00 blocked without gap confirmation.
- [ ] Manually reconcile one new sealed hour, independently restore it, and inspect RSS/CPU/WAL/latency/S3/heartbeat.
- [ ] Re-enable only the hourly archive timer when evidence passes; verify stale cleanup remains disabled and ops cleanup hold remains active.
- [ ] Report remaining blocked data and explicitly state that Phase 4 deletion is not authorized.
