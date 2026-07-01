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

## Decision

Keep already-applied migrations immutable. Restore `tk_041` to the applied
content and move the overlap-safe partition provisioning logic into a new
`tk_053` migration.

## Validation

- `go test ./migrations -run 'TestMigrationTk041|TestMigrationTk053' -count=1`
- `bash scripts/preflight.sh`
