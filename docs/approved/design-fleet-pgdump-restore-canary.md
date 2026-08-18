---
title: Fleet S3 pg_dump restore canary
status: approved
approved_by: "feng (written design approval, 2026-08-18)"
approved_at: 2026-08-18
risk: high
---

# Fleet S3 pg_dump Restore Canary

## Goal and source of truth

The weekly Fleet canary proves that the backup operators would use in a disaster
is restorable. It never creates a fresh dump for its own test. On each prod or
Edge host it reads `TOKENKEY_PGDUMP_S3_URI` from `/var/lib/tokenkey/.env`, lists
that exact prefix, and selects the newest object whose basename matches
`tokenkey-YYYYMMDDTHHMMSSZ.sql.gz`.

The configured prefix must match the target identity: `prod/pgdump` for prod and
`edge/<edge-id>/pgdump` for an Edge. The selected object's S3 `LastModified` must
be no more than three hours old. Missing configuration, an empty prefix, an
unexpected key, a stale object, or an S3 read failure stops the canary.

## Restore isolation and capacity

The host downloads the selected encrypted-at-rest S3 object into a private
temporary directory under the canary root. It verifies the compressed byte count
against S3 metadata and records SHA-256 identity. Before creating the temporary
PostgreSQL data directory, it measures the uncompressed SQL stream and checks the
actual filesystem containing the canary root. Required free bytes are the
compressed object plus twice the uncompressed SQL size plus one GiB of headroom.
Insufficient capacity fails before PostgreSQL restore work begins.

The temporary PostgreSQL container uses the live container's already-present
image, no network, bounded CPU and memory, and a bind-mounted temporary data
directory. Restore uses the production backup format directly:

```text
gunzip -c <downloaded.sql.gz> | docker exec -i <temporary-postgres> \
  psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1
```

The live PostgreSQL container is read only for image identity and comparison
counts. No `pg_dump`, temporary file, schema change, or write is executed against
it. A successful restore must make every precious table queryable; live and
restored row counts are recorded as evidence but are not required to be equal,
because the tested S3 recovery point is older than live state.

## Cleanup and receipt

Cleanup is part of success, not best effort. The temporary container, downloaded
dump, and temporary data directory must be removed and verified absent before a
new `latest.json` receipt is atomically published. If restore or cleanup fails,
the previous successful receipt remains unchanged and the command exits nonzero;
the error reports both the primary failure and any cleanup failure without
leaking credentials or the webhook URL.

The receipt includes target, completion time, source S3 URI and `LastModified`,
compressed and uncompressed byte counts, SHA-256, restore image, live comparison
counts, restored counts, `source_mutated=false`, and
`deletion_authorized=false`.

## Fleet alert and diagnostics loop

The GitHub Actions control plane posts Feishu alerts; target hosts never receive
webhook credentials. Each matrix target keeps an independent dedup state. A
failed or missing verified receipt sends one firing message, repeated identical
failures stay quiet, and the first later success sends one recovery message.
Notification delivery must succeed before its dedup state advances.

The host's `latest.json` receipt freshness is also collected by the unified daily
diagnostics. Missing, malformed, stale, wrong-target, or incomplete receipts are
actionable findings, so a skipped weekly workflow or lost host state cannot hide
behind a green historical Actions run.

Behavior tests must cover real `.sql.gz` selection and streaming restore,
wrong-prefix and stale-object rejection, insufficient capacity, S3 size mismatch,
cleanup failure preserving the previous receipt, firing dedup, recovery delivery,
and receipt-freshness diagnostics.
