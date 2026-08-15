#!/usr/bin/env python3
"""Remote, fail-closed primitives for a zero-loss Lightsail-to-EC2 migration.

The public controller sends this file to one host through SSM.  Commands are
plan-only unless ``--execute`` is explicit.  Presigned URLs stay in the
``TK_MIGRATION_TRANSFER_URL`` environment variable and are never rendered.
"""

from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import os
import pathlib
import re
import shlex
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
from typing import Any, Iterable


class InventoryError(RuntimeError):
    pass


SECRET_NAMES = {".env", ".env.secret"}
KNOWN_FILES = {
    "active-color": "runtime",
    "docker-compose.yml": "config",
    "qa-boundary-last-run.json": "runtime",
    "qa-export-orphan-cleanup-activated.json": "runtime",
    "qa-maintenance-last-run.json": "runtime",
    "qa-stale-first-plan.json": "runtime",
}
KNOWN_DIRECTORIES = {
    "app": "application",
    "caddy": "caddy",
    "logs": "logs",
    "pgdump": "database_backup",
    "postgres": "postgresql_physical",
    "redis": "redis",
}
SEMANTIC_DATA_PLANES = {"postgresql_physical", "redis"}
NON_TRANSFERRED_ROOTS = {"postgres", "redis", "migration", "lost+found"}
REMOTE_ACTIONS = {
    "prepare-source",
    "freeze-source",
    "restore-target",
    "enable-target",
    "proxy-source",
    "proxy-target",
    "freeze-target",
    "restore-source",
    "resume-source",
    "release-target-candidate",
    "verify-target",
    "verify-source-proxy",
}
ACTION_ROOT = pathlib.Path("/var/lib/tokenkey")
ACTION_WORK_PARENT = ACTION_ROOT / "migration"
ACTION_WORK_NAME_RE = re.compile(r"^[a-z0-9][a-z0-9-]{1,63}$")
BUNDLE_MEMBERS = {
    "artifacts.sha256",
    "database-report.json",
    "database.dump",
    "files.tar",
    "globals.sql",
    "inventory.json",
    "redis-report.json",
    "redis.tar",
}


def _sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _classify(relative: pathlib.PurePosixPath) -> str:
    top = relative.parts[0]
    if top in SECRET_NAMES or top.startswith(".env.before-"):
        return "secret"
    if top.startswith("docker-compose.yml."):
        return "config_backup"
    if top in KNOWN_FILES:
        return KNOWN_FILES[top]
    if top in KNOWN_DIRECTORIES:
        return KNOWN_DIRECTORIES[top]
    raise InventoryError(f"unclassified persistent path: {relative.as_posix()}")


def _entry(root: pathlib.Path, path: pathlib.Path) -> dict[str, Any]:
    relative = pathlib.PurePosixPath(path.relative_to(root).as_posix())
    info = path.lstat()
    mode = info.st_mode
    if stat.S_ISREG(mode):
        kind = "file"
    elif stat.S_ISDIR(mode):
        kind = "directory"
    elif stat.S_ISLNK(mode):
        kind = "symlink"
    else:
        raise InventoryError(f"unsupported file type at {relative.as_posix()}")
    classification = _classify(relative)
    item: dict[str, Any] = {
        "path": relative.as_posix(),
        "type": kind,
        "classification": classification,
        "transferable": classification not in SEMANTIC_DATA_PLANES,
        "mode": f"{stat.S_IMODE(mode):04o}",
        "uid": info.st_uid,
        "gid": info.st_gid,
        "size": info.st_size if kind == "file" else 0,
    }
    if kind == "file":
        item["sha256"] = _sha256_file(path)
    elif kind == "symlink":
        target = os.readlink(path)
        if os.path.isabs(target):
            raise InventoryError(f"symlink escapes persistent root: {relative.as_posix()}")
        resolved = pathlib.PurePosixPath(os.path.normpath(relative.parent / target))
        if resolved == pathlib.PurePosixPath("..") or resolved.parts[:1] == ("..",):
            raise InventoryError(f"symlink escapes persistent root: {relative.as_posix()}")
        item["link_target"] = target
        item["sha256"] = hashlib.sha256(os.fsencode(target)).hexdigest()
    return item


def build_inventory(root: pathlib.Path) -> dict[str, Any]:
    """Return a stable, content-redacted inventory of known persistent paths."""
    base = pathlib.Path(root)
    if not base.is_dir():
        raise InventoryError(f"inventory root is not a directory: {base}")
    paths: list[pathlib.Path] = []
    for current, directories, files in os.walk(base, topdown=True, followlinks=False):
        current_path = pathlib.Path(current)
        directories.sort()
        files.sort()
        if current_path == base:
            if "migration" in directories:
                directories.remove("migration")
            if "lost+found" in directories:
                lost_found = base / "lost+found"
                if any(lost_found.iterdir()):
                    raise InventoryError("lost+found contains recovered data")
                directories.remove("lost+found")
        for name in directories:
            paths.append(current_path / name)
        for name in files:
            paths.append(current_path / name)
    entries = [_entry(base, path) for path in paths]
    entries.sort(key=lambda item: item["path"])
    payload: dict[str, Any] = {"inventory_version": 1, "entries": entries}
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    payload["digest"] = hashlib.sha256(canonical).hexdigest()
    return payload


def compare_inventories(expected: dict[str, Any], actual: dict[str, Any]) -> list[str]:
    """Compare ordinary files; DB and Redis use separate semantic reports."""
    expected_entries = {
        item.get("path"): item
        for item in expected.get("entries", [])
        if isinstance(item, dict) and item.get("classification") not in SEMANTIC_DATA_PLANES
    }
    actual_entries = {
        item.get("path"): item
        for item in actual.get("entries", [])
        if isinstance(item, dict) and item.get("classification") not in SEMANTIC_DATA_PLANES
    }
    differences: list[str] = []
    for path in sorted(set(expected_entries) | set(actual_entries)):
        before = expected_entries.get(path)
        after = actual_entries.get(path)
        if before is None:
            differences.append(f"{path}: unexpected path")
            continue
        if after is None:
            differences.append(f"{path}: missing path")
            continue
        fields = ["type", "classification", "mode", "uid", "gid", "size", "link_target", "sha256"]
        for field in fields:
            if before.get(field) != after.get(field):
                differences.append(f"{path}: {field} mismatch")
    return differences


def compare_redis_reports(
    expected: dict[str, Any],
    actual: dict[str, Any],
    *,
    now_ms: int | None = None,
) -> list[str]:
    current_ms = int(time.time() * 1000) if now_ms is None else now_ms
    expected_keys = {item.get("key_sha256"): item for item in expected.get("keys", [])}
    actual_keys = {item.get("key_sha256"): item for item in actual.get("keys", [])}
    differences: list[str] = []
    for key_digest in sorted(set(expected_keys) | set(actual_keys)):
        before = expected_keys.get(key_digest)
        after = actual_keys.get(key_digest)
        if before is None:
            differences.append(f"{key_digest}: unexpected key")
            continue
        if after is None:
            expire_at = before.get("expire_at_ms")
            if not isinstance(expire_at, int) or expire_at < 0 or expire_at > current_ms:
                differences.append(f"{key_digest}: missing before absolute expiry")
            continue
        if (
            before.get("value_sha256") != after.get("value_sha256")
            or before.get("expire_at_ms") != after.get("expire_at_ms")
        ):
            differences.append(f"{key_digest}: value or expiry mismatch")
    return differences


def _safe_archive_path(name: str, *, label: str) -> pathlib.PurePosixPath:
    path = pathlib.PurePosixPath(name)
    if path.is_absolute() or any(part == ".." for part in path.parts):
        raise InventoryError(f"unsafe {label} member: {name}")
    return path


def validate_bundle_archive(path: pathlib.Path) -> None:
    with tarfile.open(path, "r:gz") as archive:
        members = archive.getmembers()
    names: list[str] = []
    for member in members:
        normalized = _safe_archive_path(member.name, label="bundle").as_posix()
        if normalized in {"", "."} or not member.isfile():
            raise InventoryError(f"unsafe bundle member: {member.name}")
        names.append(normalized)
    if len(names) != len(set(names)):
        raise InventoryError("duplicate bundle members")
    actual = set(names)
    if actual != BUNDLE_MEMBERS:
        unexpected = sorted(actual - BUNDLE_MEMBERS)
        missing = sorted(BUNDLE_MEMBERS - actual)
        raise InventoryError(
            f"unexpected bundle members: unexpected={unexpected} missing={missing}"
        )


def _validate_archive_link(
    member: tarfile.TarInfo,
    member_path: pathlib.PurePosixPath,
) -> pathlib.PurePosixPath:
    target = pathlib.PurePosixPath(member.linkname)
    if target.is_absolute():
        raise InventoryError(f"unsafe archive link: {member.name} -> {member.linkname}")
    base = pathlib.PurePosixPath() if member.islnk() else member_path.parent
    resolved = pathlib.PurePosixPath(os.path.normpath((base / target).as_posix()))
    if resolved == pathlib.PurePosixPath("..") or resolved.parts[:1] == ("..",):
        raise InventoryError(f"unsafe archive link: {member.name} -> {member.linkname}")
    return resolved


def validate_data_archive(path: pathlib.Path, kind: str) -> None:
    if kind not in {"files", "redis"}:
        raise InventoryError(f"unsupported data archive kind: {kind}")
    with tarfile.open(path, "r:") as archive:
        members = archive.getmembers()
    seen: set[str] = set()
    for member in members:
        member_path = _safe_archive_path(member.name, label="archive")
        normalized = member_path.as_posix()
        if normalized in seen:
            raise InventoryError(f"duplicate archive member: {member.name}")
        seen.add(normalized)
        if normalized in {"", "."}:
            if not member.isdir():
                raise InventoryError(f"unsupported archive member type: {member.name}")
            continue
        if kind == "redis":
            if member_path.parts[0] != "redis":
                raise InventoryError(f"unexpected redis archive member: {member.name}")
        elif member_path.parts[0] in NON_TRANSFERRED_ROOTS:
            raise InventoryError(f"unexpected files archive member: {member.name}")
        else:
            _classify(member_path)
        if member.issym() or member.islnk():
            resolved = _validate_archive_link(member, member_path)
            if kind == "files" and resolved.parts and resolved.parts[0] in NON_TRANSFERRED_ROOTS:
                raise InventoryError(
                    f"archive link targets excluded data plane: {member.name} -> {member.linkname}"
                )
        elif not (member.isfile() or member.isdir()):
            raise InventoryError(f"unsupported archive member type: {member.name}")


def write_receipt_atomic(path: pathlib.Path, payload: dict[str, Any]) -> None:
    destination = pathlib.Path(path)
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    content = json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    temporary: pathlib.Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            "w",
            encoding="utf-8",
            dir=destination.parent,
            prefix=f".{destination.name}.",
            delete=False,
        ) as handle:
            temporary = pathlib.Path(handle.name)
            os.chmod(handle.name, 0o600)
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, destination)
        temporary = None
        directory_fd = os.open(destination.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def _q(path: pathlib.Path | str) -> str:
    return shlex.quote(str(path))


def _helper_command(helper: pathlib.Path, command: str, root: pathlib.Path, output: pathlib.Path) -> str:
    return f"python3 {_q(helper)} {command} --root {_q(root)} --output {_q(output)}"


def _validate_action_paths(root: pathlib.Path, work_dir: pathlib.Path) -> None:
    root = pathlib.Path(root)
    work_dir = pathlib.Path(work_dir)
    if root != ACTION_ROOT or root.resolve(strict=False) != ACTION_ROOT.resolve(strict=False):
        raise ValueError("remote actions require fixed tokenkey migration paths")
    expected_resolved = ACTION_WORK_PARENT.resolve(strict=False) / work_dir.name
    if (
        work_dir.parent != ACTION_WORK_PARENT
        or work_dir.resolve(strict=False) != expected_resolved
        or not ACTION_WORK_NAME_RE.fullmatch(work_dir.name)
    ):
        raise ValueError("remote actions require fixed tokenkey migration paths")


def build_remote_plan(
    action: str,
    *,
    root: pathlib.Path,
    work_dir: pathlib.Path,
    transfer_url: str = "",
    target_eip: str = "",
    helper_path: pathlib.Path | None = None,
    expected_bundle_sha256: str = "",
    rehearsal: bool = False,
) -> list[str]:
    """Build shell steps without embedding the presigned transfer URL."""
    if action not in REMOTE_ACTIONS:
        raise ValueError(f"unsupported remote action: {action}")
    _validate_action_paths(root, work_dir)
    if rehearsal and action != "restore-target":
        raise ValueError("rehearsal is supported only for restore-target")
    helper = helper_path or pathlib.Path(__file__)
    compose = f"sudo docker compose -f {_q(root / 'docker-compose.yml')} --env-file {_q(root / '.env')}"
    setup = [f"install -d -m 0700 {_q(work_dir)}"]
    inventory = _helper_command(helper, "inventory", root, work_dir / "inventory.json")
    database_report = _helper_command(helper, "database-report", root, work_dir / "database-report.json")
    redis_report = _helper_command(helper, "redis-report", root, work_dir / "redis-report.json")
    stop_pgdump = (
        "if sudo systemctl list-unit-files tokenkey-pgdump.timer --no-legend 2>/dev/null "
        "| grep -q '^tokenkey-pgdump[.]timer'; then sudo systemctl stop "
        "tokenkey-pgdump.timer tokenkey-pgdump.service; fi"
    )

    if action == "prepare-source":
        return setup + [
            database_report,
            f"sudo docker exec tokenkey-postgres pg_dump --format=custom --create -U tokenkey -d tokenkey > {_q(work_dir / 'database.dump')}",
            f"sudo docker exec tokenkey-postgres pg_dumpall --globals-only -U tokenkey > {_q(work_dir / 'globals.sql')}",
            "sudo docker exec tokenkey-redis redis-cli SAVE",
            redis_report,
            inventory,
            f"sudo tar -C {_q(root)} --exclude=postgres --exclude=redis --exclude=migration --exclude=lost+found -cpf {_q(work_dir / 'files.tar')} .",
            f"sudo tar -C {_q(root)} -cpf {_q(work_dir / 'redis.tar')} redis",
            f"sha256sum {_q(work_dir / 'database.dump')} {_q(work_dir / 'globals.sql')} {_q(work_dir / 'files.tar')} {_q(work_dir / 'redis.tar')} {_q(work_dir / 'inventory.json')} {_q(work_dir / 'database-report.json')} {_q(work_dir / 'redis-report.json')} > {_q(work_dir / 'artifacts.sha256')}",
            f"tar -C {_q(work_dir)} -czf {_q(work_dir / 'bundle.tar.gz')} database.dump globals.sql files.tar redis.tar inventory.json database-report.json redis-report.json artifacts.sha256",
            f"sha256sum {_q(work_dir / 'bundle.tar.gz')} | awk '{{print $1}}' > {_q(work_dir / 'bundle.sha256')}",
            f"curl --fail --silent --show-error --max-time 600 -X PUT --upload-file {_q(work_dir / 'bundle.tar.gz')} \"$TK_MIGRATION_TRANSFER_URL\"",
            f"printf '%s\\n' PREPARE_SOURCE_OK bundle_sha256=$(cat {_q(work_dir / 'bundle.sha256')}) manifest_digest=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[\"digest\"])' {_q(work_dir / 'inventory.json')})",
        ]

    def freeze_bundle(ok_marker: str, *, app_may_be_stopped: bool = False) -> list[str]:
        if app_may_be_stopped:
            drain = [
                "TARGET_RUNNING=$(sudo docker inspect -f '{{.State.Running}}' tokenkey 2>/dev/null || echo false); "
                "if [ \"$TARGET_RUNNING\" = true ]; then "
                "sudo docker kill -s SIGUSR1 tokenkey; "
                "for attempt in $(seq 1 60); do body=$(sudo docker exec tokenkey wget -q -T 2 -O - http://localhost:8080/health/inflight); "
                "printf '%s' \"$body\" | grep -q '\"draining\":true' && printf '%s' \"$body\" | grep -q '\"in_flight\":0' && break; "
                "[ \"$attempt\" -lt 60 ] || exit 42; sleep 1; done; "
                f"{compose} stop tokenkey caddy; "
                "else echo 'target app already stopped; continue reverse snapshot'; fi",
                f"{compose} stop caddy",
            ]
        else:
            drain = [
                f"sudo rm -f {_q(work_dir / 'Caddyfile.source.next')} {_q(work_dir / 'Caddyfile.source')}; "
                f"sudo cp -a {_q(root / 'caddy/Caddyfile')} {_q(work_dir / 'Caddyfile.source.next')}; "
                f"sudo mv {_q(work_dir / 'Caddyfile.source.next')} {_q(work_dir / 'Caddyfile.source')}",
                "sudo docker kill -s SIGUSR1 tokenkey",
                "for attempt in $(seq 1 60); do body=$(sudo docker exec tokenkey wget -q -T 2 -O - http://localhost:8080/health/inflight); printf '%s' \"$body\" | grep -q '\"draining\":true' && printf '%s' \"$body\" | grep -q '\"in_flight\":0' && break; [ \"$attempt\" -lt 60 ] || exit 42; sleep 1; done",
                f"{compose} stop tokenkey caddy",
            ]
        data_services = []
        if app_may_be_stopped:
            data_services = [
                f"{compose} up -d --no-deps postgres redis",
                "for attempt in $(seq 1 60); do sudo docker exec tokenkey-postgres pg_isready -U tokenkey -d tokenkey >/dev/null && sudo docker exec tokenkey-redis redis-cli ping | grep -qx PONG && break; [ \"$attempt\" -lt 60 ] || exit 47; sleep 1; done",
            ]
        return setup + drain + [
            "sudo systemctl disable tokenkey.service tokenkey-pgdump.timer",
            stop_pgdump,
        ] + data_services + [
            database_report,
            f"sudo docker exec tokenkey-postgres pg_dump --format=custom --create -U tokenkey -d tokenkey > {_q(work_dir / 'database.dump')}",
            f"sudo docker exec tokenkey-postgres pg_dumpall --globals-only -U tokenkey > {_q(work_dir / 'globals.sql')}",
            "sudo docker exec tokenkey-redis redis-cli SAVE",
            redis_report,
            f"{compose} stop redis postgres",
            inventory,
            f"sudo tar -C {_q(root)} --exclude=postgres --exclude=redis --exclude=migration --exclude=lost+found -cpf {_q(work_dir / 'files.tar')} .",
            f"sudo tar -C {_q(root)} -cpf {_q(work_dir / 'redis.tar')} redis",
            f"sha256sum {_q(work_dir / 'database.dump')} {_q(work_dir / 'globals.sql')} {_q(work_dir / 'files.tar')} {_q(work_dir / 'redis.tar')} {_q(work_dir / 'inventory.json')} {_q(work_dir / 'database-report.json')} {_q(work_dir / 'redis-report.json')} > {_q(work_dir / 'artifacts.sha256')}",
            f"tar -C {_q(work_dir)} -czf {_q(work_dir / 'bundle.tar.gz')} database.dump globals.sql files.tar redis.tar inventory.json database-report.json redis-report.json artifacts.sha256",
            f"sha256sum {_q(work_dir / 'bundle.tar.gz')} | awk '{{print $1}}' > {_q(work_dir / 'bundle.sha256')}",
            f"curl --fail --silent --show-error --max-time 600 -X PUT --upload-file {_q(work_dir / 'bundle.tar.gz')} \"$TK_MIGRATION_TRANSFER_URL\"",
            f"printf '%s\\n' {ok_marker} bundle_sha256=$(cat {_q(work_dir / 'bundle.sha256')}) manifest_digest=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[\"digest\"])' {_q(work_dir / 'inventory.json')})",
        ]

    if action == "freeze-source":
        return setup + [
            f"touch {_q(ACTION_WORK_PARENT / '.write-owner-locked')}",
        ] + freeze_bundle("FREEZE_SOURCE_OK")[len(setup):]

    if action in {"restore-target", "restore-source"}:
        if not expected_bundle_sha256:
            raise ValueError(f"{action} requires expected_bundle_sha256")
        if len(expected_bundle_sha256) != 64 or any(ch not in "0123456789abcdef" for ch in expected_bundle_sha256.lower()):
            raise ValueError("expected_bundle_sha256 must be a 64-character hexadecimal digest")
        expected = shlex.quote(expected_bundle_sha256)
        reports = [
            _helper_command(helper, "database-report", root, work_dir / "database-report.actual.json"),
            _helper_command(helper, "redis-report", root, work_dir / "redis-report.actual.json"),
            _helper_command(helper, "inventory", root, work_dir / "inventory.actual.json"),
        ]
        comparison = [] if rehearsal else [
            f"python3 {_q(helper)} compare-json --expected {_q(work_dir / 'database-report.json')} --actual {_q(work_dir / 'database-report.actual.json')} --label database",
            f"python3 {_q(helper)} compare-redis --expected {_q(work_dir / 'redis-report.json')} --actual {_q(work_dir / 'redis-report.actual.json')}",
            f"python3 {_q(helper)} compare-inventory --expected {_q(work_dir / 'inventory.json')} --actual {_q(work_dir / 'inventory.actual.json')}",
        ]
        restore_guard = (
            [f"touch {_q(ACTION_WORK_PARENT / '.write-owner-locked')}"]
            if action == "restore-target" and not rehearsal
            else []
        )
        rehearsal_checks = [] if not rehearsal else [
            f"{compose} config --quiet",
            f"{compose} pull tokenkey",
            f"{compose} create --no-deps tokenkey",
            "test \"$(sudo docker inspect -f '{{.State.Running}}' tokenkey)\" = false",
        ]
        return setup + restore_guard + [
            f"curl --fail --silent --show-error --max-time 600 \"$TK_MIGRATION_TRANSFER_URL\" -o {_q(work_dir / 'bundle.tar.gz')}",
            f"test \"$(sha256sum {_q(work_dir / 'bundle.tar.gz')} | awk '{{print $1}}')\" = {expected}",
            f"python3 {_q(helper)} validate-bundle --archive {_q(work_dir / 'bundle.tar.gz')}",
            f"tar -C {_q(work_dir)} -xzf {_q(work_dir / 'bundle.tar.gz')}",
            f"cd {_q(work_dir)} && sha256sum -c artifacts.sha256",
            f"python3 {_q(helper)} validate-tar --archive {_q(work_dir / 'files.tar')} --kind files",
            f"python3 {_q(helper)} validate-tar --archive {_q(work_dir / 'redis.tar')} --kind redis",
            f"{compose} stop caddy tokenkey redis postgres",
            f"sudo find {_q(root / 'app')} {_q(root / 'caddy')} {_q(root / 'logs')} {_q(root / 'pgdump')} -mindepth 1 -delete",
            f"sudo find {_q(root)} -maxdepth 1 -type f ! -name .env ! -name .env.secret ! -name docker-compose.yml -delete",
            f"sudo tar -C {_q(root)} -xpf {_q(work_dir / 'files.tar')}",
            f"sudo find {_q(root / 'postgres')} -mindepth 1 -delete",
            f"sudo find {_q(root / 'redis')} -mindepth 1 -delete",
            f"sudo tar -C {_q(root)} -xpf {_q(work_dir / 'redis.tar')}",
            f"{compose} up -d --no-deps postgres redis",
            "for attempt in $(seq 1 60); do sudo docker exec tokenkey-postgres pg_isready -U tokenkey -d postgres >/dev/null && break; [ \"$attempt\" -lt 60 ] || exit 43; sleep 1; done",
            f"POSTGRES_USER=$(sed -n 's/^POSTGRES_USER=//p' {_q(root / '.env')} | head -1); POSTGRES_USER=${{POSTGRES_USER:-tokenkey}}; sudo docker exec tokenkey-postgres psql -v ON_ERROR_STOP=1 -q -U \"$POSTGRES_USER\" -d postgres -c \"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='tokenkey' AND pid <> pg_backend_pid();\" -c 'DROP DATABASE IF EXISTS tokenkey;'",
            f"POSTGRES_USER=$(sed -n 's/^POSTGRES_USER=//p' {_q(root / '.env')} | head -1); POSTGRES_USER=${{POSTGRES_USER:-tokenkey}}; sudo docker exec tokenkey-postgres psql -At -v ON_ERROR_STOP=1 -U \"$POSTGRES_USER\" -d postgres -c \"SELECT format('DROP ROLE %I;', rolname) FROM pg_roles WHERE rolname !~ '^pg_' AND rolname <> current_user\" | sudo docker exec -i tokenkey-postgres psql -v ON_ERROR_STOP=1 -q -U \"$POSTGRES_USER\" -d postgres",
            f"POSTGRES_USER=$(sed -n 's/^POSTGRES_USER=//p' {_q(root / '.env')} | head -1); POSTGRES_USER=${{POSTGRES_USER:-tokenkey}}; sed \"/^CREATE ROLE $POSTGRES_USER;$/d\" {_q(work_dir / 'globals.sql')} | sudo docker exec -i tokenkey-postgres psql -v ON_ERROR_STOP=1 -q -U \"$POSTGRES_USER\" -d postgres; printf '%s\\n' restore-globals-ok",
            f"POSTGRES_USER=$(sed -n 's/^POSTGRES_USER=//p' {_q(root / '.env')} | head -1); POSTGRES_USER=${{POSTGRES_USER:-tokenkey}}; sudo docker exec -i tokenkey-postgres pg_restore --create -U \"$POSTGRES_USER\" -d postgres < {_q(work_dir / 'database.dump')}",
        ] + reports + comparison + rehearsal_checks + [
            f"printf '%s\\n' {'RESTORE_TARGET_OK' if action == 'restore-target' else 'RESTORE_SOURCE_OK'}",
        ]

    if action == "enable-target":
        return setup + [
            f"rm -f {_q(ACTION_WORK_PARENT / '.target-proxy-retained')}",
            f"touch {_q(ACTION_WORK_PARENT / '.target-write-owner-active')}",
            f"date -u +%FT%TZ > {_q(work_dir / 'target_accepts_writes_at')}",
            "sudo systemctl enable tokenkey.service tokenkey-pgdump.timer",
            f"{compose} up -d --no-deps tokenkey caddy",
            "for attempt in $(seq 1 60); do sudo docker exec tokenkey wget -q -T 2 -O /dev/null http://localhost:8080/health/live && break; [ \"$attempt\" -lt 60 ] || exit 44; sleep 1; done",
            f"API_DOMAIN=$(sed -n 's/^API_DOMAIN=//p' {_q(root / '.env')} | head -1); test -n \"$API_DOMAIN\"; for attempt in $(seq 1 60); do curl -fsS --max-time 3 --resolve \"$API_DOMAIN:443:127.0.0.1\" \"https://$API_DOMAIN/health/live\" >/dev/null && break; [ \"$attempt\" -lt 60 ] || exit 46; sleep 1; done",
            "sudo systemctl start tokenkey-pgdump.timer",
            f"rm -f {_q(ACTION_WORK_PARENT / '.write-owner-locked')}",
            "printf '%s\\n' ENABLE_TARGET_OK",
        ]

    if action in {"verify-target", "verify-source-proxy"}:
        resolved_ip = "127.0.0.1"
        if action == "verify-source-proxy":
            if not target_eip:
                raise ValueError("verify-source-proxy requires target_eip")
            resolved_ip = str(ipaddress.ip_address(target_eip))
        checks = []
        if action == "verify-target":
            checks.append(
                "sudo docker exec tokenkey wget -q -T 3 -O /dev/null "
                "http://localhost:8080/health/live"
            )
        checks.extend([
            f"API_DOMAIN=$(sed -n 's/^API_DOMAIN=//p' {_q(root / '.env')} | head -1); test -n \"$API_DOMAIN\"; "
            f"curl -fsS --max-time 5 --resolve \"$API_DOMAIN:443:{resolved_ip}\" "
            "\"https://$API_DOMAIN/health/live\" >/dev/null",
            f"printf '%s\\n' {'VERIFY_TARGET_OK' if action == 'verify-target' else 'VERIFY_SOURCE_PROXY_OK'}",
        ])
        return setup + checks

    if action in {"proxy-source", "proxy-target"}:
        if not target_eip:
            raise ValueError(f"{action} requires target_eip")
        ipaddress.ip_address(target_eip)
        proxy_file = work_dir / "Caddyfile.proxy"
        ownership = []
        if action == "proxy-target":
            ownership = [
                f"touch {_q(ACTION_WORK_PARENT / '.target-proxy-retained')}",
                f"rm -f {_q(ACTION_WORK_PARENT / '.target-write-owner-active')} {_q(ACTION_WORK_PARENT / '.write-owner-locked')}",
            ]
        source_caddy = (
            f"test -f {_q(work_dir / 'Caddyfile.source')}"
            if action == "proxy-source"
            else f"test -f {_q(work_dir / 'Caddyfile.source')} || sudo cp -a {_q(root / 'caddy/Caddyfile')} {_q(work_dir / 'Caddyfile.source')}"
        )
        return setup + ownership + [
            "sudo systemctl disable tokenkey.service tokenkey-pgdump.timer",
            stop_pgdump,
            source_caddy,
            f'''API_DOMAIN=$(sed -n 's/^API_DOMAIN=//p' {_q(root / '.env')} | head -1); test -n "$API_DOMAIN"; printf '%s\\n' "$API_DOMAIN {{ reverse_proxy https://{target_eip} {{ transport http {{ tls_server_name $API_DOMAIN }} header_up Host $API_DOMAIN }} }}" > {_q(proxy_file)}''',
            f"sudo docker run --rm -v {_q(proxy_file)}:/etc/caddy/Caddyfile:ro caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile",
            f"sudo install -m 0644 {_q(proxy_file)} {_q(root / 'caddy/Caddyfile')}",
            f"{compose} up -d --no-deps --force-recreate caddy",
            f"API_DOMAIN=$(sed -n 's/^API_DOMAIN=//p' {_q(root / '.env')} | head -1); test -n \"$API_DOMAIN\"; "
            "for attempt in $(seq 1 20); do curl -fsS --max-time 5 "
            "--resolve \"$API_DOMAIN:443:127.0.0.1\" \"https://$API_DOMAIN/health/live\" >/dev/null && break; "
            "[ \"$attempt\" -lt 20 ] || exit 48; sleep 1; done",
            f"printf '%s\\n' {'PROXY_SOURCE_OK' if action == 'proxy-source' else 'PROXY_TARGET_OK'}",
        ]

    if action == "freeze-target":
        return setup + [
            f"touch {_q(ACTION_WORK_PARENT / '.write-owner-locked')}",
        ] + freeze_bundle("FREEZE_TARGET_OK", app_may_be_stopped=True)[len(setup):]

    if action == "resume-source":
        return setup + [
            f"if [ -f {_q(work_dir / 'Caddyfile.source')} ]; then sudo install -m 0644 {_q(work_dir / 'Caddyfile.source')} {_q(root / 'caddy/Caddyfile')}; fi",
            "sudo systemctl enable tokenkey.service tokenkey-pgdump.timer",
            f"{compose} up -d postgres redis",
            f"{compose} up -d tokenkey caddy",
            "for attempt in $(seq 1 60); do sudo docker exec tokenkey wget -q -T 2 -O /dev/null http://localhost:8080/health/live && break; [ \"$attempt\" -lt 60 ] || exit 49; sleep 1; done",
            f"API_DOMAIN=$(sed -n 's/^API_DOMAIN=//p' {_q(root / '.env')} | head -1); test -n \"$API_DOMAIN\"; curl -fsS --max-time 5 --resolve \"$API_DOMAIN:443:127.0.0.1\" \"https://$API_DOMAIN/health/live\" >/dev/null",
            "sudo systemctl start tokenkey-pgdump.timer",
            f"rm -f {_q(ACTION_WORK_PARENT / '.write-owner-locked')}",
            "printf '%s\\n' RESUME_SOURCE_OK",
        ]

    if action == "release-target-candidate":
        return setup + [
            f"{compose} stop tokenkey caddy",
            "sudo systemctl disable --now tokenkey.service tokenkey-pgdump.timer",
            f"{compose} up -d --no-deps postgres redis",
            "for attempt in $(seq 1 60); do test \"$(sudo docker inspect -f '{{.State.Health.Status}}' tokenkey-postgres)\" = healthy && test \"$(sudo docker inspect -f '{{.State.Health.Status}}' tokenkey-redis)\" = healthy && sudo docker exec tokenkey-postgres pg_isready -U tokenkey -d tokenkey >/dev/null && sudo docker exec tokenkey-redis redis-cli ping | grep -qx PONG && break; [ \"$attempt\" -lt 60 ] || exit 50; sleep 1; done",
            "test \"$(sudo docker inspect -f '{{.State.Running}}' tokenkey 2>/dev/null || echo false)\" = false",
            "test \"$(sudo docker inspect -f '{{.State.Running}}' tokenkey-caddy 2>/dev/null || echo false)\" = false",
            f"rm -f {_q(ACTION_WORK_PARENT / '.write-owner-locked')} {_q(ACTION_WORK_PARENT / '.target-write-owner-active')} {_q(ACTION_WORK_PARENT / '.target-proxy-retained')}",
            "printf '%s\\n' RELEASE_TARGET_CANDIDATE_OK",
        ]

    raise AssertionError(action)


def _run_plan(plan: Iterable[str], *, transfer_url: str) -> None:
    env = dict(os.environ)
    if transfer_url:
        env["TK_MIGRATION_TRANSFER_URL"] = transfer_url
    for command in plan:
        subprocess.run(["bash", "-euo", "pipefail", "-c", command], check=True, env=env)


def _write_json(path: pathlib.Path, payload: Any) -> None:
    if not isinstance(payload, dict):
        raise InventoryError("receipt payload must be a JSON object")
    write_receipt_atomic(path, payload)


def _load_json(path: pathlib.Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise InventoryError(f"JSON object required: {path}")
    return payload


def _database_report(output: pathlib.Path) -> None:
    list_query = (
        "SELECT table_schema || E'\\t' || table_name FROM information_schema.tables "
        "WHERE table_type='BASE TABLE' AND table_schema NOT IN ('pg_catalog','information_schema') "
        "ORDER BY table_schema,table_name"
    )
    result = subprocess.run(
        ["sudo", "docker", "exec", "tokenkey-postgres", "psql", "-U", "tokenkey", "-d", "tokenkey", "-At", "-c", list_query],
        check=True,
        capture_output=True,
        text=True,
    )
    tables: list[dict[str, Any]] = []
    for line in result.stdout.splitlines():
        schema, table = line.split("\t", 1)
        quoted = '"' + schema.replace('"', '""') + '"."' + table.replace('"', '""') + '"'
        query = f"COPY (SELECT row_to_json(t)::text FROM {quoted} t ORDER BY row_to_json(t)::text COLLATE \"C\") TO STDOUT"
        proc = subprocess.Popen(
            ["sudo", "docker", "exec", "tokenkey-postgres", "psql", "-U", "tokenkey", "-d", "tokenkey", "-qAt", "-c", query],
            stdout=subprocess.PIPE,
        )
        assert proc.stdout is not None
        digest = hashlib.sha256()
        count = 0
        for row in proc.stdout:
            digest.update(row)
            count += 1
        if proc.wait() != 0:
            raise subprocess.CalledProcessError(proc.returncode, proc.args)
        tables.append({"schema": schema, "table": table, "rows": count, "sha256": digest.hexdigest()})
    sequence_query = (
        "SELECT schemaname || E'\\t' || sequencename || E'\\t' || COALESCE(last_value::text,'NULL') "
        "FROM pg_sequences WHERE schemaname NOT IN ('pg_catalog','information_schema') "
        "ORDER BY schemaname,sequencename"
    )
    sequence_result = subprocess.run(
        ["sudo", "docker", "exec", "tokenkey-postgres", "psql", "-U", "tokenkey", "-d", "tokenkey", "-At", "-c", sequence_query],
        check=True,
        capture_output=True,
        text=True,
    )
    sequences = []
    for line in sequence_result.stdout.splitlines():
        schema, sequence, last_value = line.split("\t", 2)
        sequences.append({"schema": schema, "sequence": sequence, "last_value": last_value})

    credential_query = (
        "COPY (SELECT json_build_object('id',id,'credentials',credentials)::text "
        "FROM accounts WHERE deleted_at IS NULL ORDER BY id) TO STDOUT"
    )
    credential_result = subprocess.run(
        ["sudo", "docker", "exec", "tokenkey-postgres", "psql", "-U", "tokenkey", "-d", "tokenkey", "-qAt", "-c", credential_query],
        check=True,
        capture_output=True,
        text=True,
    )
    account_credentials = []
    for line in credential_result.stdout.splitlines():
        row = json.loads(line)
        account_id = row.get("id")
        canonical = json.dumps(row.get("credentials"), sort_keys=True, separators=(",", ":")).encode()
        account_credentials.append({"account_id": account_id, "credentials_sha256": hashlib.sha256(canonical).hexdigest()})

    _write_json(
        output,
        {
            "report_version": 1,
            "kind": "postgresql_logical",
            "tables": tables,
            "sequences": sequences,
            "account_credentials": account_credentials,
        },
    )


def _redis_report(output: pathlib.Path) -> None:
    script = (
        "local function hex(value) return (value:gsub('.', function(char) "
        "return string.format('%02x', string.byte(char)) end)) end; "
        "local out = {}; local cursor = '0'; repeat "
        "local page = redis.call('SCAN', cursor); cursor = page[1]; "
        "for _, key in ipairs(page[2]) do "
        "table.insert(out, {hex(key), hex(redis.call('DUMP', key)), "
        "redis.call('PEXPIRETIME', key)}) end until cursor == '0'; return {cursor, out}"
    )
    raw = subprocess.run(
        [
            "sudo", "docker", "exec", "tokenkey-redis", "redis-cli", "--json",
            "EVAL", script, "0",
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    payload = json.loads(raw.stdout)
    if not isinstance(payload, list) or len(payload) != 2 or payload[0] != "0":
        raise InventoryError("invalid Redis binary report response")
    keys: list[dict[str, Any]] = []
    rows = payload[1]
    if not isinstance(rows, list):
        raise InventoryError("invalid Redis binary report rows")
    for row in rows:
        if not isinstance(row, list) or len(row) != 3:
            raise InventoryError("invalid Redis binary report row")
        try:
            key = bytes.fromhex(str(row[0]))
            dumped = bytes.fromhex(str(row[1]))
            expire_at = int(row[2])
        except (TypeError, ValueError) as exc:
            raise InventoryError("invalid Redis binary report encoding") from exc
        if expire_at == -2:
            continue
        keys.append({
            "key_sha256": hashlib.sha256(key).hexdigest(),
            "value_sha256": hashlib.sha256(dumped).hexdigest(),
            "expire_at_ms": expire_at,
        })
    keys.sort(key=lambda item: item["key_sha256"])
    _write_json(output, {"report_version": 1, "kind": "redis_logical", "keys": keys})


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    inventory = sub.add_parser("inventory")
    inventory.add_argument("--root", type=pathlib.Path, required=True)
    inventory.add_argument("--output", type=pathlib.Path, required=True)
    for name in ("database-report", "redis-report"):
        report = sub.add_parser(name)
        report.add_argument("--root", type=pathlib.Path, required=True)
        report.add_argument("--output", type=pathlib.Path, required=True)
    compare_inventory = sub.add_parser("compare-inventory")
    compare_inventory.add_argument("--expected", type=pathlib.Path, required=True)
    compare_inventory.add_argument("--actual", type=pathlib.Path, required=True)
    compare_json = sub.add_parser("compare-json")
    compare_json.add_argument("--expected", type=pathlib.Path, required=True)
    compare_json.add_argument("--actual", type=pathlib.Path, required=True)
    compare_json.add_argument("--label", required=True)
    compare_redis = sub.add_parser("compare-redis")
    compare_redis.add_argument("--expected", type=pathlib.Path, required=True)
    compare_redis.add_argument("--actual", type=pathlib.Path, required=True)
    validate_bundle = sub.add_parser("validate-bundle")
    validate_bundle.add_argument("--archive", type=pathlib.Path, required=True)
    validate_tar = sub.add_parser("validate-tar")
    validate_tar.add_argument("--archive", type=pathlib.Path, required=True)
    validate_tar.add_argument("--kind", choices=("files", "redis"), required=True)
    action = sub.add_parser("action")
    action.add_argument("action", choices=sorted(REMOTE_ACTIONS))
    action.add_argument("--root", type=pathlib.Path, default=pathlib.Path("/var/lib/tokenkey"))
    action.add_argument("--work-dir", type=pathlib.Path, required=True)
    action.add_argument("--transfer-url", default="")
    action.add_argument("--target-eip", default="")
    action.add_argument("--expected-bundle-sha256", default="")
    action.add_argument("--rehearsal", action="store_true")
    action.add_argument("--execute", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.command == "inventory":
        _write_json(args.output, build_inventory(args.root))
        return 0
    if args.command == "database-report":
        _database_report(args.output)
        return 0
    if args.command == "redis-report":
        _redis_report(args.output)
        return 0
    if args.command == "compare-inventory":
        differences = compare_inventories(_load_json(args.expected), _load_json(args.actual))
        if differences:
            raise InventoryError("inventory verification failed: " + "; ".join(differences))
        return 0
    if args.command == "compare-json":
        if _load_json(args.expected) != _load_json(args.actual):
            raise InventoryError(f"{args.label} semantic verification failed")
        return 0
    if args.command == "compare-redis":
        differences = compare_redis_reports(
            _load_json(args.expected),
            _load_json(args.actual),
        )
        if differences:
            raise InventoryError("redis semantic verification failed: " + "; ".join(differences))
        return 0
    if args.command == "validate-bundle":
        validate_bundle_archive(args.archive)
        return 0
    if args.command == "validate-tar":
        validate_data_archive(args.archive, args.kind)
        return 0
    if args.command == "action":
        plan = build_remote_plan(
            args.action,
            root=args.root,
            work_dir=args.work_dir,
            transfer_url=args.transfer_url,
            target_eip=args.target_eip,
            helper_path=pathlib.Path(__file__),
            expected_bundle_sha256=args.expected_bundle_sha256,
            rehearsal=args.rehearsal,
        )
        if not args.execute:
            print(json.dumps({"mode": "plan", "action": args.action, "commands": plan}, indent=2))
            return 0
        _run_plan(plan, transfer_url=args.transfer_url)
        return 0
    raise AssertionError(args.command)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (InventoryError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"edge_ec2_remote: {exc}", file=sys.stderr)
        raise SystemExit(1)
