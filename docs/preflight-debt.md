# Preflight debt

Gaps a gate has *detected* but that are not fixed yet, plus interception a gate cannot
currently express. One entry per gap. Close the entry in the PR that fixes the
underlying problem — this file is not a changelog.

## Python toolchain is implicit

Preflight's Python gates assume what CI provides (`actions/setup-python` `'3.12'` plus
PyYAML), but nothing pinned it locally. On 2026-11-04/05 three agent runs hit it:
system Python 3.9.6 failed six unrelated checks on 3.10+ syntax (`zip(strict=)`,
`X | None`, `hashlib.file_digest`, `enterContext`, `extractall(filter=)`), and Homebrew
3.14/3.15 without PyYAML failed 18 checks. Only a uv-managed 3.12 with PyYAML passed.

Partial fix: `scripts/preflight.sh` now fails fast with one actionable line when
`python3` is older than 3.12 or lacks PyYAML. Still open: preflight does not select or
provision the interpreter itself (no pinned `.python-version` / uv project, no
dependency list). Close this entry when preflight resolves a pinned 3.12 environment
on its own.

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
