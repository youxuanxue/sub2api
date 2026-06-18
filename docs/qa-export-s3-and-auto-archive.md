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

### Enabling on prod — env binding + deploy-sed injection (both shipped)

`qa_capture.export_storage.*` has `viper.SetDefault`s (`internal/config/config.go`,
#836), so `QA_CAPTURE_EXPORT_STORAGE_*` binds under `AutomaticEnv` (`.`→`_`),
regression-guarded by `TestQAExportStorageEnvBinding`. #829 shipped the struct +
S3 driver but omitted these defaults, so a prod env cutover silently no-ops (reads
empty → export stays localfs → the per-key download stays a ~30 s in-memory
gateway proxy read of the whole ZIP — the "download does nothing" symptom on a
20k-record key). **Enabling S3 needs a release of an image carrying that fix (NOT
a same-image restart).**

**Why the env is NOT in the shared compose.** Stage0 prod and edge SHARE
`deploy/aws/stage0/docker-compose.yml`: edge Lightsail embeds it gzip|base64 into
`generated-launch-script.sh` (`render-bootstrap.sh`), and that script already sits
**~46 B under the 14336 B Lightsail user-data cap**. The 4 export_storage vars
(~588 B) would push it over (`test_render_bootstrap` fails). Edges are pure
cc-relay mirrors that never capture/export QA, so they have no use for the config
anyway — putting it in the shared compose would be paying the edge size cost for a
prod-only feature.

**How it ships instead (deploy-sed, prod-only).** `ops/stage0/deploy_via_ssm.sh`
self-heals the 4 vars onto the LIVE prod host on every deploy — the exact
mechanism already used for `SERVER_FRONTEND_URL`: a guarded (`grep -q`), additive,
idempotent `.env` backfill + a `/^      - TZ=/a\` insertion of the 4 compose
mappings, with a tagged compose backup. It is gated to the prod EC2 node
(`INSTANCE_ID == i-*`); every edge is a Lightsail `mi-*` and gets an empty
injection (byte-identical command list). Values default to the prod bucket but are
env-overridable. **Credentials stay empty on purpose** — the prod instance role +
the bucket policy below grant `s3:PutObject`, so no static keys ever land in
`.env`. Regression-guarded by `ops/stage0/test_deploy_via_ssm_qa_export.py`
(render-asserts both arms of the gate; on Linux also executes the two commands and
asserts the resulting `.env` + compose).

Rollout (each step needs operator authorization; read-only diagnosis: skill
`tokenkey-online-log-troubleshooting`):

1. **Release** an image carrying the #836 fix (VERSION bump → tag → `release.yml`).
2. **Provision infra**: bucket `tokenkey-prod-qa-exports` (us-east-1, Block Public
   Access ON) + prefix-`traj-exports/` 7-day lifecycle + bucket policy granting the
   prod instance role (`tokenkey-prod-stage0-InstanceRole-*`)
   `s3:GetObject/PutObject/DeleteObject/ListBucket` — Principal = the resolved role
   ARN literally (account-root + wildcard ⇒ AccessDenied; ops memory). The instance
   role has no S3 identity policy today (mirrors pgdump's bucket-policy grant), so
   this is the only grant; no prod-instance CFN stack update.
3. **Deploy** (e.g. `deploy-stage0` workflow): `deploy_via_ssm.sh` injects the 4
   vars into the live host's `.env` + compose and force-recreates the container.
   No manual SSH step — the deploy primitive is the enablement path.
4. **Verify**: the per-key `download_url` is now an `https://…s3…X-Amz-Signature…`
   presigned URL fetched directly from S3 (sub-second), not the
   `/api/v1/users/me/qa/traj/exports/…` gateway proxy path. Capture blobs stay
   localfs; only the export ZIP + its download move to S3. Rollback = drop the 4
   env (+ the compose mappings) + recreate.

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
(CFN param `QaStaleRetentionDays`, **default 2**). The auto-archive at 02:00
reads *yesterday's* blobs from localfs; with <2-day retention, yesterday's
early-morning records can age past the threshold and be purged out from under a
long-running archive.

Already satisfied by the Stage0 template — keep it that way:

1. `QaStaleRetentionDays` defaults to **2** (CFN), so a full prior day is always
   on disk when cleanup runs. The live value is in
   `/etc/tokenkey/qa-stale-retention.env`; change it on a running host via SSM
   (the cleanup reads it fresh each run — no restart). Do **not** drop below 2
   while auto-archive is on.
2. The cleanup timer already runs at **04:15 UTC** (`tokenkey-qa-stale-cleanup.timer`,
   +30min jitter) — well after the 02:00 archive. No schedule change needed.

Disk cost: QA blobs run ~6 GB/day on the 50 GB data volume, so retention 2
≈ ~12 GB — comfortable. Without S3, leave `auto_export_enabled: false`; the
manual ("立即导出") path is fully functional on its own.
