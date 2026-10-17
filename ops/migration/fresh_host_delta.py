#!/usr/bin/env python3
"""Snapshot-consistent content delta for an isolated fresh AWS candidate.

Source operations only read PostgreSQL. No freeze, DNS or egress changes.
Rehearsal exports may run live; a final export requires the separate write freeze.
The target must remain stopped and isolated until final reconciliation/cutover.
"""
from __future__ import annotations

import argparse
import fcntl
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys

DEDUP = "usage_billing_dedup"
WIDTH = 100_000
# Live prod candidate hostname (Tokyo 2026-10-17). Keep the prior AWS name so
# older receipts/guards still resolve during standby windows.
CANDIDATES = {
    "tokenkey-prod-tokyo-20261017": "tokenkey-prod-aws-20261015",
    "tokenkey-prod-aws-20261015": "tokenkey-prod-hz-cax21",
}
# Historical edge identities remain only so archived receipts and replay tests
# can verify their source/candidate pairing; the active CLI has no edge target.
CANDIDATES.update({f"tokenkey-edge-{edge}-aws-20261015": f"tokenkey-edge-{edge}-hz-cax21"
                   for edge in ("uk1", "uk2", "us4", "us5")})


def ident(value):
    if not re.fullmatch(r"[a-zA-Z_][a-zA-Z_0-9]*", value):
        raise ValueError("unsupported SQL identifier")
    return '"' + value + '"'


def table(name):
    return "public." + ident(name)


class DB:
    def __init__(self, database="tokenkey", scope="all"):
        self.database = database
        if scope not in {"all", "oltp", "precious", "logs"}:
            raise ValueError("unsupported scope")
        self.scope = scope
        self.prefix = ["docker", "exec", "-i", "tokenkey-postgres"]

    def psql(self):
        return self.prefix + ["psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-U", "tokenkey", "-d", self.database]

    def run(self, sql):
        result = subprocess.run(self.psql(), input=sql, text=True, capture_output=True)
        if result.returncode:
            # COPY errors may include secret-bearing row contents.
            raise RuntimeError("PostgreSQL operation failed (row details withheld)")
        return result.stdout.strip()

    def schema(self, snapshot=None):
        cmd = self.prefix + ["pg_dump", "-U", "tokenkey", "-d", self.database,
                             "--schema-only", "--no-owner", "--no-acl"]
        if snapshot:
            cmd += ["--snapshot=" + snapshot]
        if self.scope != "all":
            # Only the small OLTP surface. pg_dump includes types, defaults,
            # constraints and indexes, unlike an information_schema column hash.
            cmd += ["--table=" + table(n) for n in schema_names(self)]
        r = subprocess.run(cmd, capture_output=True, check=True)
        normalized = b"\n".join(line for line in r.stdout.splitlines()
                                 if not line.startswith((b"--", b"\\restrict", b"\\unrestrict")))
        return hashlib.sha256(normalized).hexdigest()


class Snapshot:
    def __init__(self, db):
        self.db = db

    def __enter__(self):
        self.proc = subprocess.Popen(self.db.psql(), stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=subprocess.DEVNULL, text=True)
        self.proc.stdin.write("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET LOCAL idle_in_transaction_session_timeout='300s'; SET LOCAL transaction_timeout='300s'; SELECT pg_export_snapshot();\n")
        self.proc.stdin.flush()
        self.id = self.proc.stdout.readline().strip()
        if not re.fullmatch(r"[0-9A-Fa-f-]+", self.id):
            self.__exit__(None, None, None)
            raise RuntimeError("snapshot export failed")
        return self

    def sql(self, sql):
        return ("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET TRANSACTION SNAPSHOT '" + self.id +
                "'; SET LOCAL transaction_timeout='300s'; SET LOCAL statement_timeout='120s'; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL timezone='UTC'; SET LOCAL datestyle='ISO, YMD'; " + sql + "; COMMIT;")

    def run(self, sql):
        return self.db.run(self.sql(sql))

    def __exit__(self, *args):
        self.proc.communicate("ROLLBACK;\n")


def fingerprint_sql(name, where="true"):
    # Count + four independent 64-bit sums of each SHA-256 row digest. Numeric
    # sums don't overflow; no string_agg over millions of rows or secret output.
    # Schema hash fixes column order/types; UTC fixes timestamptz representation.
    bucket = f"floor(id::numeric/{WIDTH})::bigint" if name == DEDUP else "0::bigint"
    sums = ",".join(f"coalesce(sum(('x'||substr(h,{start},16))::bit(64)::bigint),0)::text"
                    for start in (1, 17, 33, 49))
    return ("SELECT coalesce(json_agg(x ORDER BY bucket),'[]'::json) FROM ("
            f"SELECT bucket, count(*) AS rows, ARRAY[{sums}] AS digest FROM ("
            f"SELECT {bucket} AS bucket, encode(sha256(convert_to(row_to_json(t)::text,'UTF8')),'hex') AS h "
            # Keep the row digest below aggregation. Without this projection
            # barrier PostgreSQL can expand SHA/JSON four times and sort whole
            # source rows instead of compact digests (measured on prod dedup).
            f"FROM ONLY {table(name)} t WHERE {where} OFFSET 0) h GROUP BY bucket) x")


SELF_CHECK_EXEMPT = {}


def scoped_names(db):
    try:
        from .standard_freeze_delta import list_oltp_tables_sql, APPEND_ONLY_TABLE, ARCHIVE_TABLE
    except ImportError:
        from standard_freeze_delta import list_oltp_tables_sql, APPEND_ONLY_TABLE, ARCHIVE_TABLE
    if db.scope == 'logs':
        names = db.run("SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace "
            "WHERE n.nspname='public' AND c.relkind='r' AND c.relname ~ '^(usage_logs|ops_error_logs|ops_system_logs)' ORDER BY c.relname").splitlines()
    else:
        names = db.run(list_oltp_tables_sql()).splitlines()
    if db.scope == 'precious':
        names += [APPEND_ONLY_TABLE, ARCHIVE_TABLE]
    return sorted(names)


def schema_names(db):
    names = scoped_names(db)
    for name in names:
        ident(name)
    parents = db.run("SELECT DISTINCT p.relname FROM pg_inherits i JOIN pg_class p ON p.oid=i.inhparent "
        "JOIN pg_class c ON c.oid=i.inhrelid WHERE p.relnamespace='public'::regnamespace AND c.relnamespace='public'::regnamespace "
        "AND c.relname IN (" + ','.join("'" + n + "'" for n in names) + ") ORDER BY p.relname").splitlines()
    return sorted(set(names + parents))


def iter_self_check_sql():
    return [("fingerprint_sql", fingerprint_sql("users")),
            ("fingerprint_sql:dedup", fingerprint_sql(DEDUP))]


def manifest(db, snap):
    if snap.run("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace "
                "WHERE c.relkind IN ('r','S') AND n.nspname <> 'public' "
                "AND n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema'") != "0":
        raise ValueError("non-public application relations are unsupported")
    names = json.loads(snap.run("SELECT coalesce(json_agg(c.relname ORDER BY c.relname),'[]'::json) "
        "FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace "
        "WHERE n.nspname='public' AND c.relkind='r'"))
    if db.scope != "all":
        names = scoped_names(db)
        # Refuse accidental expansion into a large-table fingerprint migration.
        size = int(snap.run("SELECT coalesce(sum(pg_total_relation_size(c.oid)),0) FROM pg_class c "
            "JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname IN (" +
            ",".join("'" + n + "'" for n in names) + ")"))
        limit = (1 if db.scope == "oltp" else 8) * 1024**3
        if not names or size > limit:
            raise ValueError("rehearsal scope exceeds size bound; inspect before proceeding")
    data = {}
    for name in names:
        data[name] = json.loads(snap.run(fingerprint_sql(name)))
    sequences = {}
    seq_sql = "SELECT coalesce(json_agg(sequencename ORDER BY sequencename),'[]'::json) FROM pg_sequences WHERE schemaname='public'"
    if db.scope != "all":
        seq_sql += " AND sequencename IN (SELECT s.relname FROM pg_class s JOIN pg_depend d ON d.objid=s.oid AND d.classid='pg_class'::regclass JOIN pg_class t ON t.oid=d.refobjid WHERE d.deptype IN ('a','i') AND t.relnamespace='public'::regnamespace AND t.relname IN (" + ",".join("'" + n + "'" for n in schema_names(db)) + "))"
    for name in json.loads(snap.run(seq_sql)):
        sequences[name] = json.loads(snap.run(f"SELECT row_to_json(s) FROM (SELECT last_value,is_called FROM {table(name)}) s"))
    return {"version": 1, "scope": db.scope, "width": WIDTH, "schema": db.schema(snap.id), "tables": data,
            "sequences": sequences, "hostname": socket.gethostname()}


def changes(base, source):
    if base.get("scope", "all") != source.get("scope", "all"):
        raise ValueError("delta scope mismatch")
    if base["version"] != 1 or source["version"] != 1 or base["width"] != WIDTH or source["width"] != WIDTH:
        raise ValueError("delta format mismatch")
    if base["schema"] != source["schema"] or set(base["tables"]) != set(source["tables"]):
        raise ValueError("schema changed: refresh the candidate baseline before continuing")
    result = []
    for name in sorted(source["tables"]):
        old, new = base["tables"][name], source["tables"][name]
        if old == new:
            continue
        if name == DEDUP:
            old = {r["bucket"]: r for r in old}
            new = {r["bucket"]: r for r in new}
            for bucket in sorted(old.keys() | new.keys()):
                if old.get(bucket) != new.get(bucket):
                    result.append({"table": name, "bucket": bucket})
        else:
            result.append({"table": name, "bucket": None})
    return result


def predicate(change):
    bucket = change["bucket"]
    if bucket is None:
        return "true"
    if change["table"] != DEDUP or type(bucket) is not int:
        raise ValueError("invalid dedup range")
    return f"id >= {bucket * WIDTH} AND id < {(bucket + 1) * WIDTH}"


def export_bundle(db, base, out):
    out.mkdir(mode=0o700)  # Never reuse/overwrite a previous bundle.
    with Snapshot(db) as snap:
        source = manifest(db, snap)
        diff = changes(base, source)
        for index, change in enumerate(diff):
            filename = f"data-{index:04d}.copy.gz"
            sql = f"COPY (SELECT * FROM ONLY {table(change['table'])} WHERE {predicate(change)}) TO STDOUT"
            proc = subprocess.Popen(db.psql(), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            proc.stdin.write(snap.sql(sql).encode()); proc.stdin.close()
            with gzip.open(out / filename, "wb", compresslevel=1) as target:
                while block := proc.stdout.read(1024 * 1024):
                    target.write(block)
            if proc.wait() != 0:
                raise RuntimeError("COPY export failed")
            change["file"] = filename
            change["sha256"] = hashlib.sha256((out / filename).read_bytes()).hexdigest()
        result = {"base": base, "source": source, "changes": diff,
                  "final_freeze_verified": False}
        (out / "bundle.json").write_text(json.dumps(result, sort_keys=True))
    return {"changed_units": len(diff), "compressed_bytes": sum(f.stat().st_size for f in out.glob("*.gz"))}


def candidate_guard(expected):
    if expected not in CANDIDATES or socket.gethostname() != expected:
        raise RuntimeError("apply is limited to the exact fresh AWS candidate")
    def docker(*args):
        return subprocess.check_output(["docker", *args], text=True).strip()
    if docker("network", "inspect", "tokenkey_tokenkey-network", "--format", "{{.Internal}}") != "true":
        raise RuntimeError("candidate application network must be internal")
    running = docker("ps", "--format", "{{.Names}}").splitlines()
    if any(name not in {"tokenkey-postgres", "tokenkey-redis", "tokenkey-caddy"} for name in running):
        raise RuntimeError("stop all candidate gateway/worker writers before applying")


def apply_bundle(db, root, expected):
    candidate_guard(expected)
    # Lock before reading the baseline, not just before writing tables. Otherwise
    # two bundles can validate the same base and subsequently overwrite each other.
    with Path('/var/lib/tokenkey/migration/apply.lock').open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError('another candidate refresh is running') from None
        return _apply_bundle(db, root, expected)


def _apply_bundle(db, root, expected):
    candidate_guard(expected)
    if db.scope != "all":
        if not Path('/var/lib/tokenkey/migration/DATA_SYNC_HOLD').is_file():
            raise RuntimeError('persistent candidate hold is required')
        running = subprocess.check_output(['docker', 'ps', '--format', '{{.Names}}'], text=True).strip()
        if running != 'tokenkey-postgres':
            raise RuntimeError('OLTP refresh requires only PostgreSQL running')
    bundle = json.loads((root / "bundle.json").read_text())
    if db.scope != bundle["source"].get("scope", "all"):
        raise ValueError("apply scope mismatch")
    if bundle["base"]["hostname"] != expected or bundle["source"]["hostname"] != CANDIDATES[expected]:
        raise ValueError("bundle source/candidate identity mismatch")
    diff = changes(bundle["base"], bundle["source"])
    if [{"table": c["table"], "bucket": c["bucket"]} for c in bundle["changes"]] != diff:
        raise ValueError("bundle change list mismatch")
    with Snapshot(db) as snap:
        current = manifest(db, snap)
    for key in ("schema", "tables", "sequences"):
        if current[key] != bundle["base"][key]:
            raise RuntimeError(f"candidate changed since baseline: {key}")
    for index, change in enumerate(bundle["changes"]):
        if change["file"] != f"data-{index:04d}.copy.gz":
            raise ValueError("unsafe bundle filename")
        path = root / change["file"]
        if hashlib.sha256(path.read_bytes()).hexdigest() != change["sha256"]:
            raise ValueError("bundle checksum mismatch")
    # One target transaction. DELETE respects excluded FK children with replica
    # triggers disabled; TRUNCATE would reject those children even in replica mode.
    script = root / "apply.sql"
    candidate_guard(expected)
    with script.open("wb") as out:
        out.write(b"BEGIN; SET LOCAL transaction_timeout='300s'; SET LOCAL statement_timeout='120s'; SET LOCAL lock_timeout='5s'; SET LOCAL idle_in_transaction_session_timeout='120s'; SET LOCAL timezone='UTC'; SET LOCAL datestyle='ISO, YMD'; SET LOCAL session_replication_role=replica;\n")
        if db.scope != "all":
            out.write(("LOCK TABLE " + ",".join(table(n) for n in sorted(bundle["source"]["tables"])) + " IN EXCLUSIVE MODE;\n").encode())
            try:
                from .oltp_foreign_keys import foreign_key_guard_sql
            except ImportError:
                from oltp_foreign_keys import foreign_key_guard_sql
            before_fk, after_fk = foreign_key_guard_sql(db, set(bundle["source"]["tables"]))
            out.write(before_fk.encode())
        for change in bundle["changes"]:
            name = table(change["table"])
            out.write(f"DELETE FROM ONLY {name} WHERE {predicate(change)};\nCOPY {name} FROM STDIN;\n".encode())
            with gzip.open(root / change["file"], "rb") as data:
                while block := data.read(1024 * 1024): out.write(block)
            out.write(b"\\.\n")
        # Verify the actual COPY result before COMMIT; no row values in errors.
        for name in sorted({c["table"] for c in bundle["changes"]}):
            expected_json = json.dumps(bundle["source"]["tables"][name], separators=(",", ":"))
            check = fingerprint_sql(name)
            out.write(("DO $verify$ DECLARE actual jsonb; BEGIN SELECT q.v::jsonb INTO actual FROM (" +
                       check + ") q(v); IF actual IS DISTINCT FROM '" + expected_json +
                       "'::jsonb THEN RAISE EXCEPTION 'content mismatch: " + name +
                       "'; END IF; END $verify$;\n").encode())
        if db.scope != "all":
            out.write(after_fk.encode())
        for name, value in bundle["source"]["sequences"].items():
            # ALTER RESTART is transactional, unlike setval. Verify the positive
            # unit increment before using this migration's supported sequence path.
            increment = int(db.run("SELECT seqincrement FROM pg_sequence WHERE seqrelid='" + table(name) + "'::regclass"))
            if increment != 1:
                raise ValueError("unsupported sequence increment")
            next_value = int(value['last_value']) + int(bool(value['is_called']))
            out.write(f"ALTER SEQUENCE {table(name)} RESTART WITH {next_value};\n".encode())
        out.write(b"COMMIT;\n")
    with script.open("rb") as source:
        result = subprocess.run(db.psql(), stdin=source, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if result.returncode:
        raise RuntimeError("delta apply failed; keep candidate offline (row details withheld)")
    return {"applied_units": len(diff), "content_verified_before_commit": True,
            "source_hostname": bundle["source"]["hostname"], "final_freeze_verified": False}


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("manifest", "export", "apply"))
    parser.add_argument("--database", default="tokenkey")
    parser.add_argument("--scope", choices=("all", "oltp", "precious", "logs"), default="precious")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--base", type=Path)
    parser.add_argument("--bundle", type=Path)
    parser.add_argument("--expected-candidate")
    args = parser.parse_args()
    if args.scope == 'all' and args.operation != 'manifest':
        parser.error('all-table scope is only available for an explicit read-only manifest audit')
    db = DB(args.database, args.scope)
    if args.operation == "manifest":
        if not args.output: parser.error("--output required")
        with Snapshot(db) as snap: result = manifest(db, snap)
        args.output.write_text(json.dumps(result, sort_keys=True))
        print(json.dumps({"manifest": str(args.output), "tables": len(result["tables"])}))
    elif args.operation == "export":
        if not args.base or not args.output: parser.error("--base and --output required")
        print(json.dumps(export_bundle(db, json.loads(args.base.read_text()), args.output)))
    else:
        if not args.bundle or not args.expected_candidate: parser.error("--bundle and --expected-candidate required")
        print(json.dumps(apply_bundle(db, args.bundle, args.expected_candidate)))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, ValueError, subprocess.SubprocessError) as exc:
        print(f"fresh-host delta failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
