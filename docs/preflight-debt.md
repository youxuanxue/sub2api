# Preflight debt

Gaps a gate has *detected* but that are not fixed yet, plus interception a gate cannot
currently express. One entry per gap. Close the entry in the PR that fixes the
underlying problem — this file is not a changelog.

## `bluegreen-migration-safety.py` cannot see a migration until it is committed

`changed_migrations()` diffs `base..head` — commit to commit. A migration that is only
staged, including every brand-new one, is absent from that diff, so the gate reports
`0 changed SQL migration(s) are blue/green-safe` and passes. Verified against
`tk_101_ops_error_logs_finalize_unwritten_columns.sql`: with the file staged *and* its
`bluegreen-safe-destructive-ok` acknowledgement deleted, the gate still exited 0.

This is the same defect class as the upstream-deletion ledger fixed in #2354 (that one
reported deletions one commit late for the same reason). The pattern-matching half is
sound — `scan_file()` correctly flags `DROP TABLE` / `DROP COLUMN` and correctly honours
the acknowledgement — so the fix is the range, not the scanner.

**Interception this leaves open:** the pre-commit run of this gate is decorative for a
new migration. It does judge the file on the *next* commit, and CI's release-range
invocation (`--release-tag`) sees it, so a destructive migration cannot reach prod
unexamined — it just is not examined at the moment it is written, which is when the
author is still there to fix it. Until this is fixed, verify a new destructive migration
by calling `scan_file()` on it directly.

## `cleanup-ingress-reject-logs` has no delivery path

`backend/cmd/cleanup-ingress-reject-logs` is upstream's row-pruning tool for historical
ingress-reject rows. It is absent from `.goreleaser.*.yaml` and from the released image
(`/app/` holds only `sub2api` and `qa-archive`), so there is no way to run it against
prod without building it ad hoc. It prunes rows, not columns, so it never blocked the
tk_101 column drop — but the pruning it was meant to do has never been possible in TK.

**Decide before relying on it:** either add it to goreleaser/the image, or drop it on the
next upstream merge and let the 30-day expiry handle those rows.

## Known limits of the column-writer gate

Not debt to pay down, but the boundaries of what that gate can decide. Recorded so the
next person does not assume coverage it does not have:

- **`ALTER TABLE` variants have to be spelled out.** `declared_columns()` matched only the
  bare `ALTER TABLE <table>`, so `ALTER TABLE IF EXISTS` skipped the whole statement and
  its `DROP COLUMN`s were invisible — the gate went on reporting dropped columns as
  declared. Fixed for `IF EXISTS`; any further syntax variant needs the same treatment,
  since a missed statement fails open.
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
