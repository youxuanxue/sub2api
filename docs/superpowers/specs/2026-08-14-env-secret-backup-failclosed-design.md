# Env Secret Backup Fail-Closed Design

## Background

`ops/stage0/backup-env-secrets-via-ssm.sh` renders a host-side script that
copies `POSTGRES_PASSWORD`, `JWT_SECRET`, and `TOTP_ENCRYPTION_KEY` into an SSM
`SecureString`. The host script currently continues after a failed
`ssm:PutParameter` call and can return exit code 0 even though no parameter was
written. Operators and automation must never treat a missing off-box secret
backup as success.

## Decision

Keep the existing single-file operator entry point and make its rendered
host-side script fail closed:

- Preserve the current CLI and parameter naming behavior.
- Abort with a nonzero status when `PutParameter` fails.
- Require the final decrypted parameter to contain exactly the three expected
  secret assignments; otherwise return nonzero.
- Never print secret values.
- Remove temporary plaintext files on every exit path.

The initial `GetParameter` remains best-effort because a missing parameter is
the expected first-run state. A failed write or failed final verification is
not best-effort and must fail the SSM command.

## Test Strategy

Add a shell behavioral regression test beside the Stage0 scripts. The test
executes the actual rendered host-side payload with temporary command stubs and
a temporary secret source file.

The test covers:

- `PutParameter` rejection returns nonzero and does not report success.
- A successful first write returns zero and verifies exactly three lines.
- An unchanged existing value returns zero without writing a new version.
- Temporary plaintext files are removed after both success and failure.

The regression test must fail against the current implementation before the
production script is changed.

## Scope

This change does not alter IAM policies, SSM parameter paths, secret values,
the restore procedure, or Edge backup scheduling. It only corrects the exit
status and verification contract of the existing secret-backup command.
