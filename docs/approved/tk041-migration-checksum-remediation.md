---
title: tk_041 migration checksum remediation
status: approved
approved_by: xuejiao
approved_at: 2026-07-01
authors: [agent]
created: 2026-07-01
related_prs: []
related_commits: []
---

# tk_041 migration checksum remediation

## Incident

Release `v1.8.63` canary deploy to `edge-us5` failed before smoke because the
application refused to start on a migration checksum mismatch for
`tk_041_provision_ops_monthly_partitions.sql`.

The first remediation restored the immutable `tk_041` file, which fixed the
canary checksum mismatch but exposed the fresh-database path in CI: after
`tk_035` / `tk_037` convert the ops tables in July 2026, their legacy partitions
can already cover `2026-07-01..2026-08-01`, so executing the original static
`tk_041` raises PostgreSQL partition overlap error `42P17`.

## Decision

Keep already-applied migrations immutable. Restore `tk_041` to the applied
content and move the overlap-safe partition provisioning logic into a new
`tk_053` migration.

For fresh databases where both `ops_system_logs` and `ops_error_logs` have
already been converted to partitioned parents before `tk_041`, the migration
runner records the immutable `tk_041` checksum without executing its static
partition DDL. The immediately-following `tk_053` migration then provisions the
same monthly partitions with explicit overlap handling.

## Validation

- `go test ./migrations -run 'TestMigrationTk041|TestMigrationTk053' -count=1`
- `go test ./internal/repository -run 'TestApplyMigrationsFS_RecordsTk041WithoutExecution|TestApplyMigrationsFS_ExecutesTk041' -count=1`
- `bash scripts/preflight.sh`
