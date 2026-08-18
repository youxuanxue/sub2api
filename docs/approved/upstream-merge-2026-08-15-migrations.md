---
title: Upstream Merge 2026-08-15 Migration Approval Anchor
status: pending
approved_by: pending
authors: [tk-upstream-agent]
created: 2026-08-17
related_prs: []
related_commits: []
---

# Upstream Merge 2026-08-15: Migration Approval Anchor

This is the pending human approval anchor for migrations imported by PR #1684 from the dated upstream snapshot `baeac1f3`. It records mechanical evidence and the rollout boundary; it is not an approval.

## Migrations

| File | Change | Risk |
|------|--------|------|
| `tk_080_ops_partition_utc_boundary_repair.sql` | Repair ops partition bounds created when historical migrations 035/037 ran in a non-UTC session, before migration 081 completed | Forward-only hot-table catalog repair. It validates the current writer before taking short `ACCESS EXCLUSIVE` parent locks, drops only exact empty shifted future children, and preserves current table/index OIDs and rows while correcting the writer's UTC upper bound. |
| `222_group_usage_daily_rollups.sql` | Create `usage_group_daily_rollups` and singleton `usage_group_rollup_state`; install INSERT/DELETE/UPDATE invalidation triggers on `usage_logs` | Additive schema, but trigger creation takes a lock on the hot source table. Historical backfill remains a background job. |
| `223_group_usage_rollup_timezone.sql` | Add `timezone_name` to the singleton state row and replace invalidation functions to use the configured PostgreSQL timezone | Expand-only state change. Existing Beijing-time buckets are rebuilt by the background sync when the configured timezone differs. |
| `tk_086_openai_oauth_codex_fingerprint_device.sql` | Backfill missing/invalid `accounts.extra.codex_fingerprint_mode` to `device` for live OpenAI OAuth accounts | Idempotent JSONB merge. Explicit `off` / `session` / `full` / `device` rows are left unchanged. Numbered after shipped `tk_085_channel_monitor_terminal_outcomes.sql`. |

Migrations 222 and 223 run in the migration runner's single transaction and set:

```sql
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';
```

A conflicting hot-table lock therefore fails the migration instead of waiting indefinitely. PostgreSQL rolls back the migration transaction, including tables, functions, triggers, and the `schema_migrations` insert.

The ops repair sorts before the already-published `tk_081_ops_daily_partition_cutover.sql`, runs transactionally in the same UTC-owned migration session, and sets a 2-second lock timeout. It is intentionally narrower than migration 081:

- nodes whose current writer already ends at the next UTC month are strict no-ops;
- affected nodes must have exactly one current writer with a sub-day timezone skew;
- future children may only be absent, exact canonical UTC monthly children, or exact monthly children with that same skew;
- only shifted future children are required to be empty and dropped; canonical UTC children remain owned by migration 081;
- a validated `CHECK` proves the repaired current bound before parent locking, then OID, bounds, and emptiness are rechecked under lock;
- any non-empty shifted child, unexpected child, concurrent catalog change, validation failure, or lock timeout rolls back the complete repair and leaves no `schema_migrations` record.

The repair preserves the current child relation and its inherited indexes, but it does change that child's partition bound. That hot-table DDL and its deployment window require the same named human approval as the trigger migrations.

## Mechanical Evidence

The following evidence must be green on the exact candidate commit before approval:

```bash
go -C backend test -tags=unit ./migrations ./internal/repository \
  -run 'GroupUsageRollup|ApplyMigrationsFS_TransactionalMigration' -count=1

SUB2API_TEST_POSTGRES_IMAGE=postgres:16-alpine \
  go -C backend test -tags=integration ./internal/repository \
  -run 'GroupUsage|GroupRollup|Migration' -count=1
```

Required scenarios:

- the migration runner owns UTC for the complete lifecycle and restores the original application session timezone;
- fresh `Asia/Shanghai` installs replay 035/037/041/080/081 with UTC catalog bounds and complete daily coverage;
- upgrades with already-shifted 035/037 bounds execute 080 then 081 while preserving current table/index OIDs and rows;
- non-empty shifted future children reject migration 080 atomically, and an already-completed 081 topology is a strict no-op;
- migrations 222 and 223 apply successfully and are repeatable on PostgreSQL 16;
- a conflicting lock on `usage_logs` makes migration 222 fail at `lock_timeout` rather than hang;
- the failed transaction leaves no partial rollup table or invalidation trigger and does not record migration 222;
- historical INSERT/UPDATE/DELETE invalidation, retention rewind, timezone changes, and DST boundaries continue to pass.

Focused evidence run on the local candidate tree on 2026-08-18:

```text
go -C backend test -tags=unit ./migrations ./internal/repository \
  -run 'TestMigration22|TestApplyMigrationsFS_TransactionalMigration' -count=1
ok github.com/Wei-Shaw/sub2api/migrations
ok github.com/Wei-Shaw/sub2api/internal/repository

DOCKER_HOST=$HOME/.colima/default/docker.sock \
TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock \
SUB2API_TEST_POSTGRES_IMAGE=docker.m.daocloud.io/library/postgres:16-alpine \
  go -C backend test -tags=integration ./internal/repository \
  -run 'TestMigration222LockTimeoutRollsBackAllArtifacts|TestGroupUsage|TestUsageLogRepoSuite/TestGroupUsageSummaryIgnoresDashboardDistributionRows' \
  -count=1
ok github.com/Wei-Shaw/sub2api/internal/repository 10.107s

DOCKER_HOST=$HOME/.colima/default/docker.sock \
TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock \
SUB2API_TEST_POSTGRES_IMAGE=docker.m.daocloud.io/library/postgres:16-alpine \
  go -C backend test -tags=integration ./internal/repository \
  -run 'TestMigrationRunnerUTCSession_(AsiaShanghaiFreshOpsChain|RepairsAsiaShanghaiUpgradeOpsChain|RepairRejectsNonEmptyShiftedFutureAtomically|RepairIsStrictNoopAfterDailyCutover)' \
  -count=1
ok github.com/Wei-Shaw/sub2api/internal/repository 7.660s
```

The mirror-qualified image is the cached official `library/postgres:16-alpine` image used because direct Docker Hub access timed out. Colima requires the two socket environment variables shown above; they do not change the PostgreSQL image or test behavior. The broader repository gates listed above still run on the final candidate tree before this document can be approved.

## Rollout Gate

Before deployment, query every persistent target database:

```sql
SELECT filename, checksum
FROM schema_migrations
WHERE filename IN (
  'tk_080_ops_partition_utc_boundary_repair.sql',
  'tk_081_ops_daily_partition_cutover.sql',
  '222_group_usage_daily_rollups.sql',
  '223_group_usage_rollup_timezone.sql'
);
```

Expected before first rollout of this candidate: no rows for migration 080, 222, or 223. Migration 081 may be absent (the affected upgrade topology) or already recorded (the strict no-op topology); if it is recorded, its checksum must match the immutable repository file. If 080, 222, or 223 already exists, or 081 has a different checksum, do not edit migrations in place or bypass the checksum guard; stop and design a forward-only corrective migration.

Run the migrations during a low-traffic or maintenance window because the ops repair briefly locks both ops parents and trigger DDL briefly requires a lock on `usage_logs`. Monitor lock waits, migration duration, application error rate, and database load. A lock-timeout failure is a safe retry signal after the competing transaction clears; it is not authorization to raise or disable the timeout ad hoc.

Rollback boundary:

- before transaction commit: PostgreSQL rollback is complete and deployment can retry unchanged;
- after commit: do not manually drop tables, functions, or triggers while old and new application instances may coexist; roll forward with a reviewed migration or roll back the application only after confirming schema compatibility;
- historical rollup rows are derived data, but source `usage_logs` and migration history are not disposable.

## Human Approval

Keep this document `pending` until a named human approves the hot-table trigger design, the maintenance window, and the rollback boundary. At that point, update `status`, `approved_by`, `approved_at`, `related_prs`, and `related_commits` with the actual approval evidence.

high-risk-anchor: upstream-merge-2026-08-15
