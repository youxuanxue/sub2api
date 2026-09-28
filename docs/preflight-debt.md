# Preflight debt

Gaps a gate has *detected* but that are not fixed yet, plus interception a gate cannot
currently express. One entry per gap. Close the entry in the PR that fixes the
underlying problem — this file is not a changelog.

## `cleanup-ingress-reject-logs` — decided: drop on next upstream merge

`backend/cmd/cleanup-ingress-reject-logs` is upstream's row-pruning tool for historical
ingress-reject rows. It is absent from `.goreleaser.*.yaml` and from the released image
(`/app/` holds only `sub2api` and `qa-archive`), so there is no way to run it against
prod without building it ad hoc. It prunes rows, not columns, so it never blocked the
tk_101 column drop — but the pruning it was meant to do has never been possible in TK.

**Decision (2026-09-28):** do **not** add a TK delivery path. On the next
`merge/upstream-*`, delete the cmd (and any companion upstream finalizer scripts that
only exist to feed it) under CLAUDE.md §5.x with a `docs/DEPRECATIONS.md` ledger entry.
Until then leave the tree as-is; `ops_error_logs` 30-day expiry already covers the rows
this tool would have pruned. Do not schedule ad-hoc `go run` cleanups against prod.

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
