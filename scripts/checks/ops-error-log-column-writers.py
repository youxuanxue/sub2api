#!/usr/bin/env python3
"""sub2api: ops_error_logs read-vs-write column gate.

`ops_error_logs` has no Ent schema — it is raw DDL in backend/migrations plus one
hand-written INSERT/UPDATE pair in backend/internal/repository/ops_repo.go. Nothing
mechanically ties the two together, so a column can be *declared* by a migration,
*read* by an ops probe, and never *written* by anyone. Reading such a column is
silently useless: it is NULL for every row, and a `COALESCE(col, fallback)` around
it makes the result look populated, which is exactly why review keeps missing it.

Observed failure mode (three separate rounds on one PR):
  - `provider_error_code` / `network_error_type` advertised as root-cause fields in a
    probe: declared by migration 033, zero writers, 0 non-null rows in prod.
  - `account_status` read as if it were the failure-time account status; its
    `COALESCE(a.status, e.account_status, '')` fallback silently served the *current*
    status from the joined `accounts` row instead, so the column looked alive.
Reviewer vigilance caught these only after they shipped, so this gate makes the
check mechanical.

Truth sources, both derived at runtime (nothing hand-maintained here):
  writers  = columns in the INSERT INTO ops_error_logs column list, plus columns
             assigned by any `UPDATE ops_error_logs SET ...`, in non-test backend Go.
  declared = CREATE TABLE body plus ALTER TABLE ADD COLUMN, minus DROP COLUMN,
             across backend/migrations/*.sql.
A column that is declared but has no writer is `dead`. This gate flags reads of
dead columns in hand-written operational SQL.

Escape hatch: a read that is deliberate (verifying the column really is empty,
backfill rehearsal, a probe that reports the gap itself) is marked
`ops-allow-unwritten-column: <col>[, <col>...]` in a comment inside the statement.

The marker names the columns it blesses. It is deliberately NOT a bare
statement-wide opt-out: these probe queries select 15+ columns, so blessing a whole
statement would silently also bless every future dead-column read added to the same
query — which is precisely the failure this gate exists to catch. (Verified: with a
statement-wide marker, re-introducing the original `provider_error_code` /
`network_error_type` / `account_status` regressions into the marked query went
undetected.)

Scope, all of it hard-failing:
  - SCAN_DIRS (ops/ + deploy/) x SCAN_EXTS (.sh/.py/.sql) — hand-written SQL that runs
    against a live prod/edge DB with no Go type-checking behind it. Both
    alias-qualified and unqualified reads are judged here (see `bare`).
  - backend/internal non-test .go — the SLA numerator lives there, so a dead read
    un-attributes failures rather than merely printing an empty probe column. Only
    alias-qualified reads are judged: Go assembles SQL from fragments, so an
    unqualified name cannot be attributed to a table. Those are covered instead by
    the column contract below, which needs no table attribution.
Excluded: scripts/ (CI helpers plus this gate's own fixtures).

Alias handling is per-statement, not per-file: `e` and `l` are reused as CTE aliases
in the same probes that alias `ops_error_logs`, so a file-level alias map would
produce false positives.

Usage:
  ops-error-log-column-writers.py [--quiet]     # exit 1 if a dead column is read
  ops-error-log-column-writers.py --list        # print declared/writer/dead sets
  ops-error-log-column-writers.py --selftest    # run embedded fixtures
"""
import argparse
import os
import re
import sys

MARKER = "ops-allow-unwritten-column"
TABLE = "ops_error_logs"

# `ops-allow-unwritten-column: col_a, col_b` — the named columns are exempt in that
# statement. A bare marker with no column list exempts nothing and is reported, so a
# copied-in marker cannot quietly disable the gate.
MARKER_RE = re.compile(
    re.escape(MARKER) + r"\s*:\s*([a-z_][a-z0-9_]*(?:\s*,\s*[a-z_][a-z0-9_]*)*)",
    re.IGNORECASE,
)

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SCAN_DIRS = ("ops", "deploy")
SCAN_EXTS = (".sh", ".py", ".sql")

MIGRATIONS_DIR = os.path.join(ROOT, "backend", "migrations")
WRITER_GLOB_DIR = os.path.join(ROOT, "backend", "internal")

# Columns the DB itself fills; absence from the INSERT is correct, not a bug.
DB_MANAGED = frozenset({"id", "created_at"})

# Columns deliberately allowed to have no writer, each with the reason. Empty on
# purpose: tk_100 dropped the six columns that used to sit here, so the contract is
# currently exact. An entry here is a standing exception — prefer wiring up a writer or
# dropping the column, and say why if neither is possible.
ALLOWED_UNWRITTEN = {}

_STMT_END = re.compile(r";")


# --- truth extraction --------------------------------------------------------


def declared_columns(migrations_dir=MIGRATIONS_DIR):
    """Columns of ops_error_logs per DDL: CREATE body + ADD COLUMN - DROP COLUMN."""
    cols = set()
    if not os.path.isdir(migrations_dir):
        return cols
    for fn in sorted(os.listdir(migrations_dir)):
        if not fn.endswith(".sql"):
            continue
        with open(os.path.join(migrations_dir, fn), encoding="utf-8", errors="replace") as fh:
            txt = fh.read()

        m = re.search(
            r"CREATE TABLE(?:\s+IF NOT EXISTS)?\s+" + TABLE + r"\s*\((.*?)\n\s*\);",
            txt,
            re.S | re.I,
        )
        if m:
            for line in m.group(1).splitlines():
                cm = re.match(r"\s+([a-z_][a-z0-9_]*)\s+[A-Za-z]", line)
                if cm and cm.group(1).upper() not in ("PRIMARY", "UNIQUE", "CONSTRAINT", "FOREIGN", "CHECK"):
                    cols.add(cm.group(1))

        for stmt in re.finditer(r"ALTER TABLE\s+" + TABLE + r"\b(.*?);", txt, re.S | re.I):
            body = stmt.group(1)
            for am in re.finditer(r"ADD COLUMN(?:\s+IF NOT EXISTS)?\s+([a-z_][a-z0-9_]*)", body, re.I):
                cols.add(am.group(1))
            for dm in re.finditer(r"DROP COLUMN(?:\s+IF EXISTS)?\s+([a-z_][a-z0-9_]*)", body, re.I):
                cols.discard(dm.group(1))
    return cols


def writer_columns(base_dir=WRITER_GLOB_DIR):
    """Columns any non-test backend Go actually persists (INSERT list + UPDATE SET)."""
    cols = set()
    if not os.path.isdir(base_dir):
        return cols
    for dirpath, dirnames, filenames in os.walk(base_dir):
        dirnames[:] = [d for d in dirnames if d != "testdata"]
        for fn in sorted(filenames):
            if not fn.endswith(".go") or fn.endswith("_test.go"):
                continue
            with open(os.path.join(dirpath, fn), encoding="utf-8", errors="replace") as fh:
                txt = fh.read()
            if TABLE not in txt:
                continue
            cols |= _writer_columns_in_text(txt)
    return cols


def _writer_columns_in_text(txt):
    cols = set()
    for m in re.finditer(r"INSERT INTO\s+" + TABLE + r"\s*\((.*?)\)", txt, re.S | re.I):
        for raw in m.group(1).split(","):
            name = raw.strip().strip('"')
            name = re.sub(r"--.*", "", name).strip()
            if re.fullmatch(r"[a-z_][a-z0-9_]*", name):
                cols.add(name)
    for m in re.finditer(
        r"UPDATE\s+" + TABLE + r"\b(?:\s+\w+)?\s+SET\b(.*?)(?:\bWHERE\b|\bRETURNING\b|`)",
        txt,
        re.S | re.I,
    ):
        for am in re.finditer(r"([a-z_][a-z0-9_]*)\s*=", m.group(1)):
            cols.add(am.group(1))
    return cols


def dead_columns(declared=None, writers=None):
    declared = declared_columns() if declared is None else declared
    writers = writer_columns() if writers is None else writers
    return declared - writers - DB_MANAGED


def contract_violations(declared=None, writers=None):
    """Three-way column contract: declared == written ∪ db-managed.

    This is the structural check, and it does not depend on finding a *read* anywhere.
    ops_error_logs has no Ent schema, so nothing otherwise ties the DDL to the single
    hand-written INSERT — a column can be added by a migration and never wired up, and
    the only symptom is a silently empty field somewhere downstream months later.

    It closes the read-detection blind spots rather than widening the regexes: an
    unqualified read, a read assembled from Go string fragments, a read in a dashboard
    or a notebook this gate never scans — none of them can slip through, because the
    column cannot exist unwritten in the first place. Reported as:
      - unwritten: declared, no writer  -> wire up a writer, or DROP it in a migration
      - undeclared: written, no DDL     -> the INSERT will fail at runtime
    """
    declared = declared_columns() if declared is None else declared
    writers = writer_columns() if writers is None else writers
    return {
        "unwritten": sorted(declared - writers - DB_MANAGED),
        "undeclared": sorted(writers - declared),
    }


# --- read detection ----------------------------------------------------------


def _statements(text):
    """Yield (start_lineno, statement_text) for `;`-terminated chunks.

    Column reads sit in the SELECT list *before* the FROM, so unlike the soft-delete
    gate this cannot scan forward from a FROM match — the whole statement is the unit.

    A `;` inside a `--` comment does not end the statement. Prose in an opt-out marker
    routinely contains one, and treating it as a boundary would split a statement in
    half and flag the tail as if it were unmarked — the exact false positive this gate
    would otherwise create for the code it is meant to bless.
    """
    lines = text.splitlines()
    buf, start = [], 0
    for i, line in enumerate(lines):
        if not buf:
            start = i
        buf.append(line)
        if _STMT_END.search(re.sub(r"--[^\n]*", "", line)):
            yield start + 1, "\n".join(buf)
            buf = []
    if buf:
        yield start + 1, "\n".join(buf)


def _table_aliases(stmt):
    """Aliases bound to ops_error_logs *within this statement only*."""
    aliases = set()
    reserved = {
        "as", "on", "where", "group", "order", "left", "join", "inner", "outer",
        "limit", "using", "select", "and", "or", "set", "from", "cross", "full",
        "right", "natural", "lateral", "offset", "having", "window", "union", "for",
        "with", "returning", "by", "asc", "desc", "tablesample", "except",
        "intersect", "into",
    }
    for m in re.finditer(
        r"\b(?:FROM|JOIN)\s+\"?" + TABLE + r"\"?(?:\s+(?:AS\s+)?([a-z][a-z0-9_]*))?",
        stmt,
        re.I,
    ):
        alias = m.group(1)
        if alias and alias.lower() not in reserved:
            aliases.add(alias.lower())
    return aliases


def scan_text(text, dead, bare=False):
    """Return [(lineno, column, alias, snippet)] for reads of dead columns.

    `bare=True` additionally reports unqualified reads (`MAX(deleted_key_name)` with no
    alias prefix) in statements that select from ops_error_logs directly. That form is
    how Go builds SQL fragments, and it is a real dead read — but it is only safe to
    flag when the statement has no *other* table in its FROM/JOIN list, otherwise an
    unqualified name could belong to the other table. Alias-qualified reads are always
    checked; `bare` only widens the unqualified case.
    """
    findings = []
    if not dead:
        return findings
    dead_alt = "|".join(sorted(dead, key=len, reverse=True))
    for lineno, stmt in _statements(text):
        if TABLE not in stmt:
            continue
        exempt = {
            c.strip().lower()
            for m in MARKER_RE.finditer(stmt)
            for c in m.group(1).split(",")
        }
        aliases = _table_aliases(stmt)
        # Strip SQL line comments so a column named inside prose does not count as a read.
        probe = re.sub(r"--[^\n]*", "", stmt)

        seen = set()

        def _report(col, alias, at):
            if col in exempt or (col, alias) in seen:
                return
            seen.add((col, alias))
            line_off = probe[:at].count("\n")
            body = stmt.splitlines()
            snippet = body[line_off].strip() if line_off < len(body) else ""
            findings.append((lineno + line_off, col, alias, snippet))

        for alias in sorted(aliases):
            pat = re.compile(r"\b" + re.escape(alias) + r"\.(" + dead_alt + r")\b", re.I)
            for m in pat.finditer(probe):
                _report(m.group(1).lower(), alias, m.start())

        if bare and _selects_only_this_table(probe):
            # `\b(?<!\.)col\b` — an unqualified occurrence, i.e. not `x.col`.
            pat = re.compile(r"(?<![\w.])(" + dead_alt + r")\b", re.I)
            for m in pat.finditer(probe):
                _report(m.group(1).lower(), "-", m.start())
    return findings


def _selects_only_this_table(stmt):
    """True when every FROM/JOIN target in the statement is ops_error_logs.

    Guards the unqualified-read check: if another real table is in scope, a bare column
    name may belong to it, so only alias-qualified reads can be judged. Derived table
    openers (`FROM (`) and CTE references are ignored — a bare name there resolves to a
    projection, not to this table's columns.
    """
    targets = re.findall(r"\b(?:FROM|JOIN)\s+\"?([a-z_][a-z0-9_]*)\"?", stmt, re.I)
    if not targets:
        return False
    return all(t.lower() == TABLE for t in targets)


def _iter_target_files():
    for scan_dir in SCAN_DIRS:
        base = os.path.join(ROOT, scan_dir)
        if not os.path.isdir(base):
            continue
        for dirpath, dirnames, filenames in os.walk(base):
            dirnames[:] = [d for d in dirnames if d not in ("__pycache__", "evidence")]
            for fn in sorted(filenames):
                if not fn.endswith(SCAN_EXTS):
                    continue
                if fn.startswith("test_") or fn.endswith("_test.py") or fn.endswith("_test.sh"):
                    continue
                yield os.path.join(dirpath, fn)


def _iter_backend_files():
    """Non-test Go under backend/internal that mentions the table.

    Go is in scope because that is where the SLA numerator lives: the owner repo reads
    deleted_key_owner_user_id, and a dead read there un-attributes failures instead of
    merely printing an empty probe column. Go SQL is assembled from fragments
    (`q + whereB + ...`), so the whole file is treated as one text body rather than
    trying to reconstruct statements across concatenation.
    """
    base = os.path.join(ROOT, "backend", "internal")
    if not os.path.isdir(base):
        return
    for dirpath, dirnames, filenames in os.walk(base):
        dirnames[:] = [d for d in dirnames if d != "testdata"]
        for fn in sorted(filenames):
            if not fn.endswith(".go") or fn.endswith("_test.go"):
                continue
            path = os.path.join(dirpath, fn)
            with open(path, encoding="utf-8", errors="replace") as fh:
                if TABLE in fh.read():
                    yield path


def scan_go_text(text, dead):
    """Reads of dead columns in Go-embedded SQL.

    Go fragments often lack a FROM (a bare `where +=` clause), so statement/alias
    scoping does not apply. Instead every backtick string literal that mentions a dead
    column is inspected: Go comments (`//`) are stripped first so documentation naming a
    column is not a read, and the per-column marker still applies.
    """
    findings = []
    if not dead:
        return findings
    dead_alt = "|".join(sorted(dead, key=len, reverse=True))
    # Blank comment bodies but keep the lines, so reported line numbers stay true.
    # Both comment styles matter: `//` for Go, `--` for the SQL inside its string
    # literals (a `--` comment explaining a column must not read as a use of it).
    stripped = re.sub(r"(?m)^(\s*)//.*$", r"\1", text)
    stripped = re.sub(r"--[^\n]*", "", stripped)

    # Aliases bound to OTHER tables anywhere in this file. A qualified read through one
    # of them (`ul.duration_ms` = usage_logs) is that table's column, not ours —
    # duration_ms exists on several ops tables, so ignoring alias ownership turns every
    # usage_logs latency query into a false positive.
    foreign = {
        m.group(2).lower()
        for m in re.finditer(
            r"\b(?:FROM|JOIN)\s+\"?([a-z_][a-z0-9_]*)\"?\s+(?:AS\s+)?([a-z][a-z0-9_]*)\b",
            stripped,
            re.I,
        )
        if m.group(1).lower() != TABLE
    }
    ours = {
        m.group(1).lower()
        for m in re.finditer(
            r"\b(?:FROM|JOIN)\s+\"?" + TABLE + r"\"?\s+(?:AS\s+)?([a-z][a-z0-9_]*)\b",
            stripped,
            re.I,
        )
    }
    foreign -= ours

    # Only alias-qualified reads (`l.deleted_key_name`) are judged in Go. An unqualified
    # name cannot be attributed here: Go assembles SQL from fragments, so the governing
    # `FROM usage_logs ul` may sit in a different function or a different string than the
    # column reference. duration_ms exists on ops_error_logs AND on usage_logs, and every
    # unqualified duration_ms in this tree turned out to be a usage_logs read. Judging
    # those would make the gate cry wolf, which is how gates get switched off — so the
    # unqualified case is covered by the column-contract check instead, which needs no
    # table attribution.
    pat = re.compile(r"\b([a-z][a-z0-9_]*)\.(" + dead_alt + r")\b", re.I)
    lines = stripped.splitlines()
    for i, line in enumerate(lines):
        if MARKER in line:
            continue
        exempt = {
            c.strip().lower()
            for m in MARKER_RE.finditer(line)
            for c in m.group(1).split(",")
        }
        seen = set()
        for m in pat.finditer(line):
            alias, col = m.group(1).lower(), m.group(2).lower()
            if col in exempt or col in seen:
                continue
            if alias in foreign:
                continue  # qualified through another table's alias
            seen.add(col)
            findings.append((i + 1, col, alias, line.strip()))
    return findings


def run(quiet):
    declared = declared_columns()
    writers = writer_columns()
    if not declared or not writers:
        print("ops_error_logs column-writer gate: FAIL")
        print(f"  Could not derive truth sets (declared={len(declared)} writers={len(writers)}).")
        print("  Expected DDL in backend/migrations/ and an INSERT in backend/internal/.")
        return 1
    dead = dead_columns(declared, writers)

    contract = contract_violations(declared, writers)
    unwritten = [c for c in contract["unwritten"] if c not in ALLOWED_UNWRITTEN]
    if unwritten or contract["undeclared"]:
        print("ops_error_logs column-writer gate: FAIL")
        print("  ops_error_logs has no Ent schema, so the column contract is enforced here:")
        print("  every declared column needs a writer, and every written column needs DDL.")
        if unwritten:
            print(f"  declared but never written ({len(unwritten)}):")
            for col in unwritten:
                print(f"    - {col}")
            print("    Fix: write it in ops_repo.go's INSERT/UPDATE, or DROP it in a migration.")
            print("    If it must stay empty for now, add it to ALLOWED_UNWRITTEN with a reason.")
        if contract["undeclared"]:
            print(f"  written but not declared ({len(contract['undeclared'])}):")
            for col in contract["undeclared"]:
                print(f"    - {col}")
            print("    Fix: add the column in a migration — the INSERT fails at runtime without it.")
        return 1

    findings = []
    for path in _iter_target_files():
        with open(path, encoding="utf-8", errors="replace") as fh:
            text = fh.read()
        for lineno, col, alias, snippet in scan_text(text, dead, bare=True):
            findings.append((os.path.relpath(path, ROOT), lineno, col, alias, snippet))
    for path in _iter_backend_files():
        with open(path, encoding="utf-8", errors="replace") as fh:
            text = fh.read()
        for lineno, col, alias, snippet in scan_go_text(text, dead):
            findings.append((os.path.relpath(path, ROOT), lineno, col, alias, snippet))

    if findings:
        print("ops_error_logs column-writer gate: FAIL")
        print(f"  Operational SQL reads {TABLE} columns that no backend writer ever populates.")
        print("  These are NULL for every row; a COALESCE around one hides the gap and the")
        print("  report silently shows a fallback value instead of the field you asked for.")
        print("  Fix: drop the read, or make the backend write the column, or — if the read is")
        print(f"  deliberate — add a `{MARKER}` comment inside the statement.")
        for rel, lineno, col, alias, snippet in findings:
            print(f"  - {rel}:{lineno}  column={col} (read as {alias}.{col}, no writer)")
            print(f"      {snippet}")
        return 1

    if not quiet:
        scanned = " + ".join(SCAN_DIRS) + " + backend"
        allowed = f", {len(ALLOWED_UNWRITTEN)} allowed-unwritten" if ALLOWED_UNWRITTEN else ""
        print(
            f"ops_error_logs column-writer gate: PASS "
            f"({len(declared)} declared, {len(writers)} written, "
            f"{len(dead)} unwritten{allowed}; contract exact; no unwritten column read "
            f"in {scanned})"
        )
    return 0


def report_sets():
    declared = declared_columns()
    writers = writer_columns()
    dead = dead_columns(declared, writers)
    print(f"declared ({len(declared)}): {', '.join(sorted(declared))}")
    print(f"writers  ({len(writers)}): {', '.join(sorted(writers))}")
    print(f"db-managed ({len(DB_MANAGED)}): {', '.join(sorted(DB_MANAGED))}")
    print(f"unwritten ({len(dead)}): {', '.join(sorted(dead))}")
    orphan = writers - declared
    if orphan:
        print(f"WARNING written but not declared ({len(orphan)}): {', '.join(sorted(orphan))}")
    return 0


# --- self-test ---------------------------------------------------------------

_DEAD = {"provider_error_code", "network_error_type", "account_status"}

_SELFTEST = [
    (
        "R-002 regression: dead column in probe select -> flag",
        "$PSQL -c \"SELECT e.provider_error_code FROM ops_error_logs e WHERE e.id=1;\"",
        1,
    ),
    (
        "written column -> ok",
        "$PSQL -c \"SELECT e.error_message FROM ops_error_logs e WHERE e.id=1;\"",
        0,
    ),
    (
        "R-007 regression: dead column hidden behind COALESCE -> flag",
        "SELECT row_to_json(t) FROM (SELECT\n  COALESCE(a.status, e.account_status, '') AS account_status_now\n  FROM ops_error_logs e\n  LEFT JOIN accounts a ON a.id = e.account_id\n) t;",
        1,
    ),
    (
        "marker naming the column -> ok",
        "SELECT count(e.account_status) FROM ops_error_logs e; -- ops-allow-unwritten-column: account_status verifying the column is empty",
        0,
    ),
    (
        "bare marker with no column list exempts nothing -> flag",
        "SELECT count(e.account_status) FROM ops_error_logs e; -- ops-allow-unwritten-column intentional",
        1,
    ),
    (
        "marker is per-column, not per-statement: unnamed dead column still flagged",
        "SELECT\n  e.deleted_key_name, -- ops-allow-unwritten-column: deleted_key_name\n  e.account_status\n  FROM ops_error_logs e WHERE e.id=1;",
        1,
    ),
    (
        "marker naming several columns -> all exempt",
        "SELECT\n  e.provider_error_code, e.network_error_type\n  -- ops-allow-unwritten-column: provider_error_code, network_error_type\n  FROM ops_error_logs e WHERE e.id=1;",
        0,
    ),
    (
        "marker naming a different column does not bless this one -> flag",
        "SELECT e.account_status FROM ops_error_logs e; -- ops-allow-unwritten-column: network_error_type",
        1,
    ),
    (
        "same dead column twice in one statement -> reported once",
        "SELECT e.network_error_type, e.network_error_type FROM ops_error_logs e WHERE e.id=1;",
        1,
    ),
    (
        "two distinct dead columns -> two findings",
        "SELECT e.provider_error_code, e.network_error_type FROM ops_error_logs e WHERE e.id=1;",
        2,
    ),
    (
        "CTE alias reuse: e bound to a CTE, not the table -> ok",
        "WITH errors AS (SELECT 1 AS account_status)\nSELECT e.account_status FROM errors e;",
        0,
    ),
    (
        "alias-free dead column reference -> not flagged (out of scope, needs an alias)",
        "SELECT account_status FROM ops_error_logs WHERE id=1;",
        0,
    ),
    (
        "dead column named only in a comment -> ok",
        "SELECT e.error_message FROM ops_error_logs e -- account_status has no writer\nWHERE e.id=1;",
        0,
    ),
    (
        "aliased with AS keyword -> flag",
        "SELECT x.account_status FROM ops_error_logs AS x WHERE x.id=1;",
        1,
    ),
    (
        "unrelated table with same column name -> ok",
        "SELECT a.account_status FROM accounts a WHERE a.id=1;",
        0,
    ),
    (
        "second statement in same file is independent -> flag only the bad one",
        "SELECT e.error_message FROM ops_error_logs e WHERE e.id=1;\nSELECT l.account_status FROM ops_error_logs l WHERE l.id=2;",
        1,
    ),
    (
        "`;` inside a marker comment must not split the statement -> ok",
        "SELECT\n  e.account_status, -- ops-allow-unwritten-column: account_status inert for now; restored later.\n  e.network_error_type, -- ops-allow-unwritten-column: network_error_type\n  e.model\n  FROM ops_error_logs e WHERE e.id=1;",
        0,
    ),
    (
        "`;` inside a plain comment must not split the statement -> still flag the tail",
        "SELECT\n  e.error_message, -- note: see docs; nothing to see here\n  e.account_status\n  FROM ops_error_logs e WHERE e.id=1;",
        1,
    ),
]

_TRUTH_SELFTEST_DDL = """
CREATE TABLE IF NOT EXISTS ops_error_logs (
    id           BIGSERIAL PRIMARY KEY,
    request_id   VARCHAR(64),
    retry_count  INTEGER,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS account_status VARCHAR(32),
    ADD COLUMN IF NOT EXISTS api_key_prefix VARCHAR(32);
ALTER TABLE ops_error_logs DROP COLUMN IF EXISTS retry_count;
"""

_TRUTH_SELFTEST_GO = """
const insertOpsErrorLogSQL = `
INSERT INTO ops_error_logs (
  request_id,
  api_key_prefix
) VALUES ($1,$2)`

const updateSQL = `
UPDATE ops_error_logs
SET resolved = $2, resolved_at = NOW()
WHERE id = $1`
"""


_GO_SELFTEST = [
    (
        "alias bound to ops_error_logs -> flag",
        "q := `SELECT l.account_status FROM ops_error_logs l WHERE l.id=$1`",
        1,
    ),
    (
        "alias bound to another table -> ok (duration_ms also lives on usage_logs)",
        "q := `SELECT SUM(ul.duration_ms) FROM usage_logs ul JOIN ops_error_logs o ON o.id=ul.id`",
        0,
    ),
    (
        "Go // comment naming a column is not a read -> ok",
        "// account_status has no writer; see docs/preflight-debt.md\nq := `SELECT l.model FROM ops_error_logs l`",
        0,
    ),
    (
        "SQL -- comment naming a column is not a read -> ok",
        "q := `SELECT o.model -- o.account_status would be empty\n FROM ops_error_logs o`",
        0,
    ),
    (
        "unqualified read is NOT judged in Go (table cannot be attributed) -> ok",
        "q := `SELECT MAX(duration_ms) FROM ops_error_logs`",
        0,
    ),
    (
        "per-column marker on the line -> ok",
        "q := `SELECT l.account_status FROM ops_error_logs l` // ops-allow-unwritten-column: account_status",
        0,
    ),
]

_GO_DEAD = {"account_status", "duration_ms"}


def _selftest_go():
    failures = 0
    for name, text, want in _GO_SELFTEST:
        got = len(scan_go_text(text, _GO_DEAD))
        ok = got == want
        print(f"  {'PASS' if ok else 'FAIL'} {name} (flagged={got} want={want})")
        if not ok:
            failures += 1
    return failures, len(_GO_SELFTEST)


def _selftest_contract():
    """The contract check must fire on both directions of drift."""
    checks = []

    v = contract_violations({"id", "created_at", "model", "ghost"}, {"model"})
    checks.append(("declared-but-unwritten is reported", v["unwritten"], ["ghost"]))
    checks.append(("db-managed columns are not reported", v["undeclared"], []))

    v = contract_violations({"id", "created_at", "model"}, {"model", "typo_col"})
    checks.append(("written-but-undeclared is reported", v["undeclared"], ["typo_col"]))

    v = contract_violations({"id", "created_at", "model"}, {"model"})
    checks.append(("exact contract reports nothing", v["unwritten"] + v["undeclared"], []))

    failures = 0
    for name, got, want in checks:
        ok = got == want
        print(f"  {'PASS' if ok else 'FAIL'} {name} (got={got} want={want})")
        if not ok:
            failures += 1
    return failures, len(checks)


def _selftest_truth(tmpdir):
    """DDL parsing must honour DROP COLUMN; writer parsing must union INSERT+UPDATE."""
    mig = os.path.join(tmpdir, "migrations")
    os.makedirs(mig, exist_ok=True)
    with open(os.path.join(mig, "001_x.sql"), "w", encoding="utf-8") as fh:
        fh.write(_TRUTH_SELFTEST_DDL)
    declared = declared_columns(mig)
    writers = _writer_columns_in_text(_TRUTH_SELFTEST_GO)
    dead = dead_columns(declared, writers)

    checks = [
        ("declared picks up CREATE body", "request_id" in declared, True),
        ("declared picks up ADD COLUMN", "account_status" in declared, True),
        ("DROP COLUMN removes the column", "retry_count" in declared, False),
        ("PRIMARY/constraint lines are not columns", "primary" in declared, False),
        ("writers include INSERT columns", "api_key_prefix" in writers, True),
        ("writers include UPDATE SET columns", "resolved" in writers, True),
        ("writers include UPDATE NOW() column", "resolved_at" in writers, True),
        ("id/created_at are db-managed, not dead", bool({"id", "created_at"} & dead), False),
        ("unwritten column is dead", "account_status" in dead, True),
        ("written column is not dead", "api_key_prefix" in dead, False),
    ]
    failures = 0
    for name, got, want in checks:
        ok = got == want
        print(f"  {'PASS' if ok else 'FAIL'} {name} (got={got} want={want})")
        if not ok:
            failures += 1
    return failures, len(checks)


def selftest():
    import tempfile

    failures = 0
    total = 0
    print("read-detection fixtures:")
    for name, text, want in _SELFTEST:
        got = len(scan_text(text, _DEAD))
        ok = got == want
        print(f"  {'PASS' if ok else 'FAIL'} {name} (flagged={got} want={want})")
        total += 1
        if not ok:
            failures += 1

    print("go read-detection fixtures:")
    gf, gt = _selftest_go()
    failures += gf
    total += gt

    print("column-contract fixtures:")
    cf, ct = _selftest_contract()
    failures += cf
    total += ct

    print("truth-extraction fixtures:")
    with tempfile.TemporaryDirectory() as tmp:
        tf, tt = _selftest_truth(tmp)
    failures += tf
    total += tt

    if failures:
        print(f"ops-error-log-column-writers selftest: FAIL ({failures} case(s))")
        return 1
    print(f"ops-error-log-column-writers selftest: PASS ({total}/{total})")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--quiet", action="store_true", help="print only on failure")
    ap.add_argument("--list", action="store_true", help="print declared/writer/unwritten sets")
    ap.add_argument("--selftest", action="store_true", help="run embedded fixtures and exit")
    args = ap.parse_args()
    if args.selftest:
        sys.exit(selftest())
    if args.list:
        sys.exit(report_sets())
    sys.exit(run(args.quiet))


if __name__ == "__main__":
    main()
