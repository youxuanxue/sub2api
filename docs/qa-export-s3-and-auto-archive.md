# QA trajectory export — durable jobs, S3 artifacts, daily auto-archive

This feature persists trajectory-export jobs, moves the export ZIP off the
Postgres-shared data volume (optional S3), and can archive each user/key's
conversations daily. The code ships **safe-by-default**: with no config it
behaves exactly as before but with durable job state (the download no longer
vanishes on redeploy). S3 + auto-archive are opt-in.

## What ships on by default (no infra needed)

- **Persistent jobs** (`qa_export_jobs`, migration `tk_030`). Manual exports and
  their download links survive restart/redeploy — fixing the orphaned-download
  bug where a prod redeploy wiped the in-memory job map and reset "导出中…" to
  "导出" with no way to re-find the artifact.
- **Startup reconciler**: any job left `pending`/`running` by a dead process is
  marked `failed` (`interrupted`) so the UI never polls forever.
- **"My exports" panel**: `GET /api/v1/users/me/qa/traj/export/jobs[?api_key_id=]`
  lists a user's recent exports; done & unexpired ones carry a fresh download URL.
- Export ZIP still written to localfs (capture store), download via the existing
  proxy route. Same behavior as before, just durable + listable.

## Opt-in: move the export ZIP to S3

Set a **separate** export store so the (large) archive leaves the
Postgres-shared EBS volume. Capture blobs still use the primary `storage`.

```yaml
qa_capture:
  storage: { driver: localfs }          # capture blobs stay local
  export_storage:                        # NEW — export ZIPs go here
    driver: s3
    region: us-east-1
    bucket: tokenkey-prod-qa-exports
    # access via the instance role (preferred) — leave keys empty
```

Infra to provision first (do this in waking hours, then flip config):

1. **Bucket** `tokenkey-prod-qa-exports` (us-east-1), Block Public Access ON.
2. **Lifecycle rule**: expire objects under prefix `traj-exports/` after **7
   days** (object age). This is the real expirer; the DB `expires_at` mirrors it
   for the UI.
3. **IAM**: grant the prod EC2 instance role `s3:GetObject/PutObject/DeleteObject/
   ListBucket` on that bucket — write the role ARN directly as the Principal
   (account-root + wildcard ⇒ AccessDenied; see ops memory).
4. **S3 Gateway VPC Endpoint** so S3↔EC2 traffic is free (avoids ~$0.045/GB if it
   would otherwise egress via a NAT gateway).

Cost at current volume (~164k objects/day, ~31 KB each): the export bill is
**PUT-dominated only if you mirror every capture blob** (~$25/mo). This design
PUTs only the finished ZIP (a few per day), so S3 cost is **≈ $0–1/mo**;
storage under a 1-day/7-day TTL is ~$0.16–0.3/mo. The S3 key layout is
`traj-exports/<user_id>/<api_key_id>/{manual/<unix_nanos>|auto/<YYYY-MM-DD>}.zip`
— `user_id` first so the download ownership prefix check holds.

## Opt-in: daily auto-archive cron

```yaml
qa_capture:
  auto_export_enabled: true   # default false
  export_storage: { driver: s3, ... }   # only meaningful with durable S3
```

Behavior: every day at **02:00 UTC**, for each `traj_export_enabled` user and
each of their API keys that captured records the previous UTC day, enqueue an
idempotent dated archive (`auto/<YYYY-MM-DD>.zip`). Re-running a day upserts the
same row/object (deterministic `job_id = auto:<user>:<key>:<date>`).

### ⚠ Purge-race coordination (must read before enabling)

The host script `deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh` deletes
`qa_records` + `qa_blobs` older than `TOKENKEY_QA_STALE_RETENTION_DAYS`
(**default 1**). The auto-archive at 02:00 reads *yesterday's* blobs from
localfs — but with 1-day retention, yesterday's early-morning records are
already >24h old and may be purged before the archive runs.

When enabling auto-archive:

1. Set `QaStaleRetentionDays = 2` (boot env `/etc/tokenkey/qa-stale-retention.env`)
   so a full prior day is always still on disk at 02:00.
2. Schedule the cleanup timer to run **after** 02:00 UTC (e.g. 04:00) so the
   archive captures the day before cleanup removes it.

Without S3 + retention≥2, leave `auto_export_enabled: false` — the manual
("立即导出") path is fully functional on its own.
