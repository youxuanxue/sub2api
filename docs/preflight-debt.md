# Preflight debt

Gaps a gate has *detected* but that are not fixed yet, plus interception a gate cannot
currently express. One entry per gap. Close the entry in the PR that fixes the
underlying problem — this file is not a changelog.

Currently empty: the `ops_error_logs` column contract is exact (every declared column
has a writer), enforced by `scripts/checks/ops-error-log-column-writers.py`.

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
