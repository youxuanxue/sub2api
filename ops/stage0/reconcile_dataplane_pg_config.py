#!/usr/bin/env python3
"""Render restored file overrides from the data-plane container command.

Approved scope: docs/approved/design-data-plane-split.md. Default is read-only.
No server restart, ALTER SYSTEM, or change to effective values is performed.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import sys
import subprocess
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "lib"))
import resolve_app_container

PG = None


def run(args):
    return subprocess.check_output(args, text=True, timeout=30)


def sql(statement):
    return json.loads(run(["docker", "exec", "--user", "postgres", PG,
                           "psql", "-U", "tokenkey", "-d", "tokenkey", "-XAt",
                           "-v", "ON_ERROR_STOP=1", "-c", statement]))


def command_settings(command):
    return dict(command[i + 1].split("=", 1) for i, arg in enumerate(command[:-1])
            if arg == "-c" and "=" in command[i + 1])


def reconcile_overrides(contents, settings):
    lines, removed = [], []
    for line in contents.splitlines(keepends=True):
        match = re.match(r"\s*([a-z_][a-z_0-9]*)\s*=", line)
        if match and match.group(1) in settings:
            name = match.group(1)
            value = settings[name].replace("\\", "\\\\").replace("'", "''")
            rendered = f"{name} = '{value}' # generated from container command\n"
            if line != rendered:
                removed.append(name)
            lines.append(rendered)
        else:
            lines.append(line)
    return "".join(lines), sorted(set(removed))


def atomic_write(path, content, stat):
    fd, temporary = tempfile.mkstemp(prefix=".pg-config-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
            os.fchmod(stream.fileno(), stat.st_mode & 0o777)
            os.fchown(stream.fileno(), stat.st_uid, stat.st_gid)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expected-host", required=True)
    parser.add_argument("--timeline", required=True, type=int)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if socket.gethostname() != args.expected_host:
        raise RuntimeError("host mismatch")
    global PG
    PG = resolve_app_container.resolve_postgres_server()
    if not PG:
        raise RuntimeError('unique running PostgreSQL server not resolved')
    container = json.loads(run(["docker", "inspect", PG]))[0]
    if not container["State"]["Running"]:
        raise RuntimeError("primary container not running")
    names = command_settings(container["Config"]["Cmd"])
    if not {"shared_buffers", "max_connections", "archive_mode"} <= names.keys():
        raise RuntimeError("container command does not own the data-plane settings")
    identity = sql("SELECT json_build_object('timeline',timeline_id,'recovery',pg_is_in_recovery(),"
                   "'started',pg_postmaster_start_time(),'config',current_setting('config_file')) "
                   "FROM pg_control_checkpoint()")
    if identity["recovery"] or identity["timeline"] != args.timeline:
        raise RuntimeError("live primary timeline mismatch")
    mounts = [m for m in container["Mounts"]
              if identity["config"] == m["Destination"] + "/postgresql.conf"]
    if len(mounts) != 1:
        raise RuntimeError("config mount cannot be uniquely resolved")
    path = Path(mounts[0]["Source"]) / "postgresql.conf"
    if path.is_symlink():
        raise RuntimeError("config symlink refused")
    before, stat = path.read_text(), path.stat()
    after, removed = reconcile_overrides(before, names)
    effective = sql("SELECT json_object_agg(name,setting) FROM pg_settings")
    result = {"host": args.expected_host, "timeline": args.timeline, "path": str(path),
              "reconciled_overrides": removed, "before_sha256": hashlib.sha256(before.encode()).hexdigest(),
              "status": "planned"}
    if not args.apply:
        print(json.dumps(result))
        return
    backup = Path("/var/lib/tokenkey/postgres-backup") / ("postgresql.conf.pre-closeout-" + result["before_sha256"][:12])
    if removed:
        if not backup.exists():
            backup.write_text(before)
            os.chmod(backup, 0o600)
        if path.read_text() != before:
            raise RuntimeError("config changed after plan")
        atomic_write(path, after, stat)
    try:
        sql("SELECT to_json(pg_reload_conf())")
        time.sleep(1)
        if sql("SELECT json_object_agg(name,setting) FROM pg_settings") != effective:
            raise RuntimeError("effective PostgreSQL settings changed")
        if sql("SELECT to_json(pg_postmaster_start_time())") != identity["started"]:
            raise RuntimeError("primary restarted during reconciliation")
        problems = sql("SELECT COALESCE(json_agg(name), '[]'::json) FROM pg_settings WHERE pending_restart")
        if problems:
            raise RuntimeError("pending restart remains: " + ",".join(problems))
    except Exception:
        if removed:
            atomic_write(path, before, stat)
            sql("SELECT to_json(pg_reload_conf())")
        raise
    result.update(status="ok", backup=str(backup) if removed else None,
                  effective_settings_unchanged=True, restarted=False)
    receipt = Path("/var/lib/tokenkey/postgres-backup/config-reconciliation.json")
    result["completed_at"] = time.time()
    receipt.write_text(json.dumps(result, indent=2) + "\n")
    os.chmod(receipt, 0o640)
    print(json.dumps(result))


if __name__ == "__main__":
    main()
