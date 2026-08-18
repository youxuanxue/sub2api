---
title: Edge Stage0 env secrets off-box recovery
status: approved
approved_by: "feng (direct execution approval, 2026-08-18)"
approved_at: 2026-08-18
risk: high
---

# Edge Stage0 Env Secrets Off-box Recovery

Each deployable Edge owns one platform-neutral SSM SecureString at
`/tokenkey/edge/<edge-id>/stage0/env-secrets-backup`. It contains exactly
`POSTGRES_PASSWORD`, `JWT_SECRET`, and `TOTP_ENCRYPTION_KEY`; no workflow output,
receipt, or repository artifact may contain their values.
Values retain the existing bootstrap contract: 48 lowercase hexadecimal characters
for the PostgreSQL password and 64 for each application key. This makes the restored
file safe to source and rejects executable shell syntax.

The instance writes and immediately reads back the encrypted parameter after every
provision, upgrade, rollback, and smoke operation. A rejected write, malformed value,
or failed readback fails the workflow. The shared Lightsail Hybrid role receives only
`GetParameter` and `PutParameter` on this fleet prefix.

Replacement bootstrap keeps an existing local `.env.secret`. If it is absent, bootstrap
tries the Edge SecureString before starting PostgreSQL. A valid value is restored with
mode `0600`; only an explicit `ParameterNotFound` result authorizes new random secrets.
Access denial, transport failure, or malformed content stops bootstrap so a recoverable
database can never be paired silently with new credentials.

Rollout order is: merge and deploy the IAM addon, run the Edge smoke workflow once per
deployable Edge to create and verify all backups, then permit replacement operations.
Repository merge alone does not prove that a live parameter exists.
