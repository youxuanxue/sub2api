# Phase 1 Production Activation Gates Design

## Background

PR #1536 merged the first-stage data protection and archive groundwork, but
read-only production review found four activation blockers:

- the safety probe assumes the legacy tokenkey app container even though
  production uses a blue/green container;
- daily diagnostics snapshots the instance's first block device instead of the
  CloudFormation DataVolumeId;
- the active capacity probe performs unbounded COUNT(*) scans;
- partition maintenance starts only on the daily cron, so a newly deployed
  version cannot repair missing current/future partitions immediately.

This PR repairs those gates. It does not deploy code, run a production command,
change configuration, delete data, or authorize archive cleanup.

## Considered Approaches

### Chosen: focused activation repair

Promote the already-approved bounded capacity prototype into the active probe,
reuse the existing blue/green container-resolution behavior, resolve the
snapshot volume from the stack output, and add a separately invoked partition
maintenance operator command. This has the smallest runtime surface and keeps
all write behavior behind an explicit one-shot command.

### Rejected: run partition maintenance on application startup

This would eventually self-heal missing partitions, but every restart would
gain a DDL side effect and a lock-contention path on the request-serving
process. It also would not provide a deterministic operator receipt.

### Rejected: expose an admin API

An API would add authentication, authorization, routing, and public-contract
surface for an operation that should be rare. A local operator command is
simpler and easier to keep fail closed.

## Design

### Active application container

probe-data-layer-safety.sh accepts APP_CONTAINER=auto by default. Resolution
first reads /var/lib/tokenkey/active-color and accepts only blue or green when
the matching container exists. It then falls back through the legacy, blue,
and green names. An explicit APP_CONTAINER remains available for diagnostics
and tests.

The probe must emit an unusable signal when no app container can be resolved;
it must not silently read configuration from an arbitrary stopped or missing
container.

### Snapshot target

Daily diagnostics reads DataVolumeId from the already-described CloudFormation
stack. A missing output produces a missing snapshot signal and therefore a
fail-closed safety verdict. It must not fall back to BlockDeviceMappings[0],
because that position is the root volume in the current stack.

### Bounded capacity signal

The active probe adopts the approved prototype contract:

- PostgreSQL sessions set default_transaction_read_only=on;
- lock_timeout=100ms and statement_timeout=2s;
- total usage_logs rows come from pg_stat_user_tables;
- relation size and row estimates sum leaf partitions when applicable;
- one bounded 30-day query derives both 30-day and 7-day growth;
- catalog failure, timeout, missing statistics, or invalid values produce
  unknown, never a guessed green verdict.

The existing thresholds and daily workflow schedule do not change.

### One-shot partition maintenance

A dedicated production operator CLI uses the repository's existing SSM
transport pattern to run a remote implementation on the Stage0 host. It
requires the exact confirmation token
tokenkey-prod-partition-maintenance-v1.

The remote implementation:

- sets lock_timeout=100ms and statement_timeout=5s;
- verifies each target is a partitioned table before changing it;
- creates only current and future partitions for ops_system_logs,
  ops_error_logs, and usage_logs;
- uses the same horizons as the application maintenance owner: current plus
  three future months for ops tables and current plus seven future days for
  usage;
- treats an existing or overlapping partition as already covered only after
  catalog verification proves the target time range is covered;
- never issues DELETE, DROP, TRUNCATE, DETACH, table rewrite, container restart,
  or configuration mutation;
- writes the existing ops_partition_maintenance heartbeat only after all target
  ranges are verified;
- returns a field-named JSON receipt with deletion_authorized=false.

Any lock timeout, unexpected schema, uncovered range, malformed receipt, or
transport ambiguity fails closed with a non-zero exit.

## Validation

Behavior tests will prove:

- blue/green resolution selects the active app container and fails closed when
  none exists;
- workflow snapshot lookup uses DataVolumeId and contains no positional block
  device lookup;
- capacity SQL contains the read-only and timeout guards, avoids full-table
  counts, and reports unknown for partial or invalid signals;
- the maintenance command rejects a missing or wrong confirmation, generates
  create-only SQL with the fixed horizons and timeouts, validates its receipt,
  and contains no destructive SQL;
- existing data-layer verdict, workflow, partition, and preflight checks remain
  green.

No test or validation step connects to production.

## Non-Goals

- No production execution, deployment, restart, schema migration, or setting
  change.
- No telemetry activation, cleanup release, retention change, archive deletion,
  volume resize, or RDS work.
- No change to request handling, billing, authentication, or user-visible APIs.
