# US-069 QA independent local lifecycle

- ID: US-069
- Priority: P1
- Title: Keep QA capture writable while archive uploads are paused
- As a / I want / So that: As an operator, I want an independent recurring provision job so that pausing S3 never exhausts future QA partitions.
- Trace: docs/approved/qa-raw-only.md, 2026-10-21 local lifecycle authorization.
- Risk Focus:
  - 逻辑错误: Wrong parent, missing hourly coverage, repeated execution.
  - 行为回归: Existing historical rows and current-hour inserts remain intact.
  - 安全问题: Legacy dual owner, credential exposure, wrong artifact, failed deployment.

## Acceptance Criteria
1. AC-001 (正向): Given a valid PostgreSQL hourly QA parent, two executions cover 72 hours and accept current writes without deleting historical rows.
2. AC-002 (负向): Given an incompatible parent or an unconfirmed command, execution returns nonzero before creating partitions.
3. AC-003 (负向): An active legacy owner or wrong binary digest blocks execution; the child environment excludes cloud/application secrets.
4. AC-004 (回归): A failed first service run restores previous units and timer state; monitoring treats this timer independently of paused archive jobs.

5. AC-005 (正向): Cleanup plan preserves all data; apply deletes unarchived expired hours and paired raw/DLQ files, retaining the current 24-hour window and unrelated data.
6. AC-006 (负向): Locks, symlinks or an ambiguous DEFAULT partition prevent deletion; partial DROP/file cleanup resumes on a later execution.

## Assertions
Assert real pg_inherits coverage, preserved row IDs, current-hour INSERT success, failed return codes, secret exclusion, original unit contents and timer restoration.

## Linked Tests
- AC-005: `ops/qa/test_local_maintenance_integration.py`::`test_cleanup_plan_then_drop_unarchived_preserves_retention_boundary_and_other_data`
- AC-006: `ops/qa/test_local_maintenance_integration.py`::`test_orphan_files_and_interrupted_drop_receipt_are_cleaned_without_a_partition`
- AC-006: `ops/qa/test_local_maintenance_integration.py`::`test_symlink_and_default_partition_block_orphan_deletion`
- AC-006: `ops/qa/test_local_maintenance_integration.py`::`test_locked_partition_keeps_rows_and_files_for_next_run`
- AC-006: `ops/qa/test_local_maintenance_host.py`::`test_cleanup_mounts_reject_symlinks_and_expose_only_qa_directories`
- Run command: `python3 -m unittest discover -s ops/qa -p test_local_maintenance_host.py`
- AC-001: `ops/qa/test_local_maintenance_integration.py`::`test_provision_is_idempotent_accepts_live_writes_and_preserves_old_records`
- AC-002: `ops/qa/test_local_maintenance_integration.py`::`test_wrong_parent_key_fails_before_creating_children`
- AC-002: `backend/cmd/qa-local-maintenance/main_test.go`::`TestRejectUnconfirmedOrUnknownPhase`
- AC-003: `ops/qa/test_local_maintenance_host.py`::`test_active_legacy_owner_blocks_local_work`
- AC-003: `ops/qa/test_local_maintenance_host.py`::`test_wrong_binary_digest_blocks_before_docker_or_database`
- AC-003: `ops/qa/test_local_maintenance_host.py`::`test_database_environment_excludes_cloud_and_application_secrets`
- AC-004: `ops/qa/test_local_maintenance_host.py`::`test_failed_service_install_restores_prior_units_and_timer`
- AC-004: `ops/observability/test_host_https_watch.py`::`test_local_provision_is_monitored_independently_of_paused_archive`

```bash
make -C backend test-qa-local
python3 -m unittest discover -s ops/observability -p test_host_https_watch.py
```

## Evidence
CI `test-integration` invokes `test-qa-local` with the Go version from `backend/go.mod` and a Linux binary built for the Docker server architecture. Local runner/test changes require backend CI; a lifecycle gate rejects removal of the integration prerequisite.

Real PostgreSQL 18 fixture verifies idempotence, preserved historical rows, current writes and wrong-parent refusal. Host tests cover credential filtering, legacy owner/digest guards and deployment rollback. No product UI changes.

## Status
- Done

Provision and cleanup timers verified in production; 99 expired partitions and their hot files cleared, with zero remaining expired hours. Evidence: `docs/ops/qa-local-retention-20261021.md`.
