---
title: Edge Stage0 env secrets off-box recovery
status: approved
approved_by: "feng (written design approval, 2026-08-18)"
approved_at: 2026-08-18
risk: high
---

# Edge Stage0 Env Secrets Off-box Recovery

## Scope and invariants

Each deployable Edge owns one SSM Hybrid managed-instance role named
`tokenkey-lightsail-ssm-hybrid-<edge-id>` and one platform-neutral SSM
SecureString at `/tokenkey/edge/<edge-id>/stage0/env-secrets-backup`. An Edge
role cannot read or write another Edge's parameter or pg_dump prefix.

The SecureString contains exactly `POSTGRES_PASSWORD`, `JWT_SECRET`, and
`TOTP_ENCRYPTION_KEY`; no workflow output, receipt, or repository artifact may
contain their values. Values retain the existing bootstrap contract: 48
lowercase hexadecimal characters for the PostgreSQL password and 64 for each
application key. This makes the restored file safe to source and rejects
executable shell syntax.

## IAM boundary

Every per-Edge role keeps `AmazonSSMManagedInstanceCore` and receives only:

- `ssm:GetParameter` and `ssm:PutParameter` on its own env-secrets parameter;
- `s3:PutObject` and `s3:GetObject` on its own
  `edge/<edge-id>/pgdump/*` objects; and
- `s3:ListBucket` constrained to its own `edge/<edge-id>/pgdump/` prefix.

The GitHub Actions role may pass only the named per-Edge role family to SSM and
may call `ssm:UpdateManagedInstanceRole` for the managed instances it operates.
The existing shared `tokenkey-lightsail-ssm-hybrid` role remains temporarily for
SSM core connectivity while live registrations are migrated, but it owns no env
secret or pg_dump permissions. The migration explicitly deletes the live,
manually-created `EdgePgdumpPutOnly` inline policy after every managed instance
has moved and verifies that the shared role has no remaining non-core policy.
The QA raw-archive and QA Bundle bucket policies deny both that shared role and
`tokenkey-lightsail-ssm-hybrid-*`; this is the post-#1703 boundary, with no
legacy QA exports bucket in the design.

## First provision and replacement

`restore-edge-env-secrets.sh` restores an existing valid local file first, then
the Edge SecureString. It generates new values only when the caller explicitly
passes `--allow-generate`; `ParameterNotFound` alone never authorizes generation.
Access denial, transport failure, or malformed content always stops bootstrap.

The provision workflow derives first-provision status before creating or
destroying anything:

- no Lightsail instance and no persistent `${SSM_PREFIX}/instance_name` marker:
  this is a new Edge identity and may pass `--allow-generate`;
- an instance exists, or the persistent marker exists: this is an existing Edge
  identity and the SecureString must already be valid.

For `RECREATE=true`, the workflow first resolves the current managed instance,
switches it to its per-Edge role, backs up the current `.env.secret`, and reads it
back for validation. Any failure aborts before the Static IP is detached or the
instance is deleted. The replacement activation uses the same per-Edge role and
must restore the validated SecureString before PostgreSQL starts.

## Rollout and verification

Deploy the IAM addon first. For each deployable Edge, update the existing `mi-*`
registration to the per-Edge role without recreating the host, then run the
backup/readback and smoke paths. Upgrade, rollback, smoke, backup, and restore
workflows all verify the target role before operating. Only after every live
registration has moved may `EdgePgdumpPutOnly` be deleted and the shared role be
retired.

Behavior tests must prove cross-Edge IAM denial, shared-role loss of secret and
pg_dump access, first-provision generation, existing-identity fail-closed
behavior, and `RECREATE=true` abort-before-delete when backup validation fails.
