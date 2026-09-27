# Preflight debt

Gaps a gate has *detected* but that are not fixed yet, plus interception a gate cannot
currently express. One entry per gap. Close the entry in the PR that fixes the
underlying problem — this file is not a changelog.

## 3 declared `ops_error_logs` columns with no writer (phase 1 of a two-phase removal)

`attempted_key_prefix`, `deleted_key_owner_user_id`, `deleted_key_name` are exempted via
`ALLOWED_UNWRITTEN` in `scripts/checks/ops-error-log-column-writers.py`, so the contract
is *not* exact right now. Deleted-key attribution was removed (the writer is unreachable
behind the ingress-reject early return, and prod measured 0 non-null across 2.2M rows),
but the columns stay declared on purpose.

**Why not fixed now:** migrations run on new-color startup while the old color serves the
same DB, and `DROP COLUMN` on this partitioned table takes 93 `ACCESS EXCLUSIVE` locks
and removes columns the previous release's SQL still names. Dropping them in phase 1
would break both the blue/green window and an image rollback.

## 6 columns that exist in prod but not in the gate's declared set (tk_100)

`tk_100` is recorded without executing its `DROP`
(`migrations.RetainedOpsErrorColumnsMigration`), so `duration_ms`,
`network_error_type`, `provider_error_code`, `provider_error_type`, `account_status`
and `retry_after_seconds` are physically present in prod while the migration file says
they are gone. The gate derives `declared` from the migration files, so these six are
outside its declared set and outside `ALLOWED_UNWRITTEN` — there is nothing for it to
exempt.

**Interception this leaves open:** a new read of one of these six is invisible to the
gate (it is not in `dead`) and will not fail at runtime either, because the column still
exists — it only becomes a 42703 when phase 2 performs the real `DROP`. The one known
live site is pinned by a sentinel on `ops_repo_request_details.go`.

**Exit condition for both entries (phase 2):** once the rollback window for the release
carrying phase 1 has closed, one migration drops all nine columns plus
`DROP TABLE deleted_api_key_audits`, and both entries, the three `ALLOWED_UNWRITTEN`
entries and the tk_100 record-only branch go away together. Anchor:
[`docs/approved/ops-error-logs-column-contract.md`](approved/ops-error-logs-column-contract.md).

## Known limits of the column-writer gate

Not debt to pay down, but the boundaries of what that gate can decide. Recorded so the
next person does not assume coverage it does not have:

- **Unqualified reads in Go are not attributed to a table.** Go assembles SQL from
  fragments, so the governing `FROM` may live in another function than the column
  reference, and `duration_ms` existed on both `ops_error_logs` and `usage_logs`.
  Judging those produced false positives on every `usage_logs` latency query, so only
  alias-qualified reads are checked in Go. The column *contract* covers the gap
  structurally: a column with no writer cannot exist, so there is nothing to misread.
- **The gate checks that a column has a writer, not that the value is right.** A column
  written with the wrong semantics (status-at-failure vs status-now was the real case)
  passes. That class needs a test asserting meaning, not a column census.
- **Only `ops_error_logs` is covered.** The same "declared, read, never written" shape
  is possible on other raw-DDL ops tables (`ops_system_logs`, `ops_job_heartbeats`).
  Generalising means parameterising the table and its writer file.
