---
title: cn-prod logical backup activation
status: approved
approved_by: "feng — 2026-10-19：同意建议执行顺序：1，2，cn-prod适配pgdump，3，4"
risk: high
---

# cn-prod logical backup

Reuse `deploy/aws/stage0/tokenkey-pgdump.sh` for the independent domestic
PostgreSQL database, including usage details. No database restart, gateway
cutover, production restore or change to AWS prod backup frequency is required.

`deploy/domestic/pgdump.py` runs the shared dump through the existing cn-prod
SSM target, then grants one object upload through a 30-minute signed S3 URL.
The host receives no permanent AWS credentials. `pgdump-stack.yaml` owns the
private encrypted domestic bucket, seven-day object retention and narrowly
scoped runner access. The bucket is retained when the stack is deleted.

`.github/workflows/ops-cn-pgdump.yml` schedules backups every two hours at minute
17 after `CN_PGDUMP_ENABLED=true`. GitHub scheduling can be delayed; recovery
freshness follows the last verified object, never the nominal schedule. Six
local rolling copies use the shared retention owner. A failed upload keeps the
local copy and fails the workflow; only a successful remote size/checksum
comparison writes `offbox_verified`. S3 validates the signed SHA-256 on upload.
This is backup transfer evidence,
not a restore acceptance claim.

Before enabling scheduling, create one real backup, download it from S3,
restore into an isolated PostgreSQL instance and compare usage rows, timestamps
and exact costs against the dump snapshot using `pgdump_usage_evidence.py`.
Never compare a historical snapshot with the changing live database. Preserve
the receipt and verify destruction of the temporary restore instance.

Rollback disables `CN_PGDUMP_ENABLED`; retain local and S3 copies. No live
database replacement or deletion is part of rollback. The weekly-full and
two-hour-incremental pgBackRest rollout remains AWS-only under
`design-prod-physical-backup.md`.
