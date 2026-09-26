# Preflight debt

Gaps a gate has *detected* but that are not fixed yet, plus interception a gate
cannot currently express. One entry per gap. Close the entry in the PR that fixes the
underlying problem — this file is not a changelog.

## ops_error_logs: migration 145 deleted-key attribution has no producer

Detected by `scripts/checks/ops-error-log-column-writers.py`.

Migration `145_deleted_api_key_audit.sql` added three columns to `ops_error_logs`:
`attempted_key_prefix`, `deleted_key_owner_user_id`, `deleted_key_name`. The feature
commit `ddf063352` shipped both halves — a `deleted_api_key_audits` table plus
handler-side code that extracted the attempted key, reverse-looked-up the deleted
key's owner, and set those three fields.

Current state: the **producer half is gone**. At HEAD there is no caller of
`LookupDeletedKeyAudit` outside its own repository/service definitions, the
attempted-key extraction helper no longer exists, and the feature's 118-line
attribution test has shrunk to a 14-line `keyPrefix` unit test. The three columns are
absent from the `INSERT INTO ops_error_logs` column list, and `enqueueOpsError`
explicitly zeroes them before the telemetry snapshot. Prod confirms: 0 non-null rows
for all three across 2,213,414 rows / 30d.

Why it still matters: the columns are read as if they worked.
`internal/repository/ops_repo_user_visible_failure_tk.go` — the registered owner of
"user-visible failure", i.e. the alert/SLA numerator — attributes users with
`COALESCE(user_id, deleted_key_owner_user_id)` and names keys with
`COALESCE(ak.name, l.deleted_key_name, '')`. `ops/observability/probe-user-billing-watch.sh`
mirrors that predicate verbatim by design. Both fallbacks are inert, so a failure on a
since-deleted key is attributed to no user instead of to its former owner, and silently
drops out of the numerator.

Resolution options, in preference order:

1. Restore the producer (re-wire attempted-key extraction + `LookupDeletedKeyAudit`
   into the ops error logger, add the columns to the INSERT, restore the attribution
   test). The reads then start working with no change to the owner or the probe.
2. If the feature is deliberately abandoned, remove the reads from the owner, drop the
   probe's opt-out markers, and retire the columns — but that loses deleted-key
   attribution, so it needs a product decision, not a cleanup commit.

Do **not** close this by deleting the opt-out markers alone; that only hides the gate
finding while leaving the attribution gap in the SLA numerator.
