#!/usr/bin/env python3
"""Publish or audit the protected-main TokenKey pricing registry snapshot.

The historical setting key remains ``tk_pricing_overlay_runtime`` for binary
compatibility, but its value is now a transport envelope for one complete
registry artifact. Runtime never merges this value with another pricing source.

``check`` is read-only. It compares the live envelope with the exact registry
bytes committed at the locally fetched ``origin/main``.

``sync-runtime`` is intentionally stricter: HEAD must equal ``origin/main``, the
working-tree registry must be byte-identical to that commit, and the registry
gate must pass before any production I/O. This makes an unmerged branch or an
arbitrary local file ineligible for global publication.
"""
from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import io
import json
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import NoReturn

REPO_ROOT = Path(__file__).resolve().parents[2]
REGISTRY_RELATIVE_PATH = "backend/internal/service/tk_pricing_overlay.json"
REGISTRY_PATH = REPO_ROOT / REGISTRY_RELATIVE_PATH
REGISTRY_GATE = REPO_ROOT / "scripts" / "checks" / "pricing-overlay.py"
SETTING_KEY = "tk_pricing_overlay_runtime"
SCHEMA_VERSION = 1
MAX_REGISTRY_BYTES = 8 << 20

PSQL = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1"
REDISCLI = "env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli"

_ssm_spec = __import__("importlib.util").util.spec_from_file_location(
    "tk_ssm_execution", REPO_ROOT / "ops" / "stage0" / "ssm_execution.py"
)
_SSM = __import__("importlib.util").util.module_from_spec(_ssm_spec)
assert _ssm_spec and _ssm_spec.loader
_ssm_spec.loader.exec_module(_SSM)

_FULL_GIT_OBJECT = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})\Z")
_SHA256 = re.compile(r"[0-9a-f]{64}\Z")


def fail(message: str) -> NoReturn:
    print(f"ERROR: {message}", file=sys.stderr)
    raise SystemExit(2)


@dataclass(frozen=True)
class RegistryArtifact:
    source_commit: str
    registry_bytes: bytes

    @property
    def registry_sha256(self) -> str:
        return hashlib.sha256(self.registry_bytes).hexdigest()


@dataclass(frozen=True)
class RuntimeInspection:
    state: str
    source_commit: str = ""
    registry_sha256: str = ""
    registry_bytes: bytes = b""
    error: str = ""

    def is_current(self, expected: RegistryArtifact) -> bool:
        return (
            self.state == "valid"
            and self.source_commit == expected.source_commit
            and self.registry_sha256 == expected.registry_sha256
            and self.registry_bytes == expected.registry_bytes
        )


def _validate_source_commit(source_commit: str) -> None:
    if not isinstance(source_commit, str) or not _FULL_GIT_OBJECT.fullmatch(source_commit):
        raise ValueError("source_commit must be a full lowercase Git object id")


def build_runtime_envelope(registry_bytes: bytes, source_commit: str) -> dict:
    _validate_source_commit(source_commit)
    if not registry_bytes:
        raise ValueError("registry artifact is empty")
    if len(registry_bytes) > MAX_REGISTRY_BYTES:
        raise ValueError(f"registry artifact exceeds {MAX_REGISTRY_BYTES} bytes")
    json.loads(registry_bytes)
    return {
        "_snapshot": {
            "schema_version": SCHEMA_VERSION,
            "source_commit": source_commit,
            "registry_sha256": hashlib.sha256(registry_bytes).hexdigest(),
        },
        "_registry_gzip_base64": base64.b64encode(
            gzip.compress(registry_bytes, mtime=0)
        ).decode("ascii"),
    }


def encode_runtime_envelope(registry_bytes: bytes, source_commit: str) -> bytes:
    envelope = build_runtime_envelope(registry_bytes, source_commit)
    return json.dumps(
        envelope, sort_keys=True, separators=(",", ":"), ensure_ascii=True
    ).encode("utf-8")


def _decompress_registry(encoded: str) -> bytes:
    try:
        compressed = base64.b64decode(encoded, validate=True)
        with gzip.GzipFile(fileobj=io.BytesIO(compressed), mode="rb") as stream:
            registry_bytes = stream.read(MAX_REGISTRY_BYTES + 1)
    except (OSError, ValueError) as exc:
        raise ValueError(f"invalid registry gzip/base64: {exc}") from exc
    if len(registry_bytes) > MAX_REGISTRY_BYTES:
        raise ValueError(f"decompressed registry exceeds {MAX_REGISTRY_BYTES} bytes")
    return registry_bytes


def inspect_runtime_document(runtime: object) -> RuntimeInspection:
    if runtime == {} or runtime is None:
        return RuntimeInspection(state="absent")
    if not isinstance(runtime, dict):
        return RuntimeInspection(state="invalid", error="runtime value must be an object")
    envelope_keys = {"_snapshot", "_registry_gzip_base64"}
    if not envelope_keys.intersection(runtime):
        return RuntimeInspection(state="legacy", error="raw overlay is not a snapshot envelope")
    if set(runtime) != envelope_keys:
        return RuntimeInspection(state="invalid", error="runtime envelope has missing or unknown fields")

    snapshot = runtime.get("_snapshot")
    if not isinstance(snapshot, dict) or set(snapshot) != {
        "schema_version", "source_commit", "registry_sha256"
    }:
        return RuntimeInspection(state="invalid", error="snapshot metadata has missing or unknown fields")
    if snapshot.get("schema_version") != SCHEMA_VERSION:
        return RuntimeInspection(state="invalid", error="unsupported schema_version")

    source_commit = snapshot.get("source_commit")
    digest = snapshot.get("registry_sha256")
    try:
        _validate_source_commit(source_commit)
    except ValueError as exc:
        return RuntimeInspection(state="invalid", error=str(exc))
    if not isinstance(digest, str) or not _SHA256.fullmatch(digest):
        return RuntimeInspection(state="invalid", error="registry_sha256 must be lowercase SHA-256 hex")
    encoded = runtime.get("_registry_gzip_base64")
    if not isinstance(encoded, str) or not encoded:
        return RuntimeInspection(state="invalid", error="registry gzip/base64 must be a non-empty string")
    try:
        registry_bytes = _decompress_registry(encoded)
        json.loads(registry_bytes)
    except (ValueError, json.JSONDecodeError) as exc:
        return RuntimeInspection(state="invalid", error=str(exc))
    actual_digest = hashlib.sha256(registry_bytes).hexdigest()
    if actual_digest != digest:
        return RuntimeInspection(state="invalid", error="registry digest mismatch")
    return RuntimeInspection(
        state="valid",
        source_commit=source_commit,
        registry_sha256=digest,
        registry_bytes=registry_bytes,
    )


def _run_git(args: list[str]) -> bytes:
    try:
        proc = subprocess.run(
            ["git", *args], cwd=REPO_ROOT, check=True, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except subprocess.CalledProcessError as exc:
        detail = exc.stderr.decode("utf-8", errors="replace").strip()
        fail(f"git {' '.join(args)} failed: {detail}")
    return proc.stdout


def _git_is_ancestor(ancestor: str, descendant: str) -> bool:
    _validate_source_commit(ancestor)
    _validate_source_commit(descendant)
    proc = subprocess.run(
        ["git", "merge-base", "--is-ancestor", ancestor, descendant],
        cwd=REPO_ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if proc.returncode == 0:
        return True
    if proc.returncode == 1:
        return False
    detail = proc.stderr.decode("utf-8", errors="replace").strip()
    fail(f"cannot verify pricing snapshot ancestry {ancestor}..{descendant}: {detail}")


def load_origin_main_artifact(*, require_publishable_checkout: bool) -> RegistryArtifact:
    source_commit = _run_git(["rev-parse", "origin/main^{commit}"]).decode().strip()
    _validate_source_commit(source_commit)
    registry_bytes = _run_git(["show", f"{source_commit}:{REGISTRY_RELATIVE_PATH}"])
    if require_publishable_checkout:
        head = _run_git(["rev-parse", "HEAD^{commit}"]).decode().strip()
        if head != source_commit:
            fail(f"HEAD {head} is not current origin/main {source_commit}")
        try:
            local_bytes = REGISTRY_PATH.read_bytes()
        except OSError as exc:
            fail(f"cannot read working-tree registry: {exc}")
        if local_bytes != registry_bytes:
            fail("working-tree registry is not byte-identical to current origin/main")
    return RegistryArtifact(source_commit=source_commit, registry_bytes=registry_bytes)


def _decode_runtime_value(output: str) -> dict:
    output = output.strip()
    if not output:
        return {}
    raw = gzip.decompress(base64.b64decode(output)).decode("utf-8").strip()
    if not raw:
        return {}
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError("runtime settings value must be a JSON object")
    return value


def read_runtime_document(instance_id: str) -> dict:
    shell = (
        f"{PSQL} -c \"SELECT value FROM settings WHERE key='{SETTING_KEY}';\""
        " | gzip -c | base64 | tr -d '\\n'"
    )
    shell_b64 = base64.b64encode(shell.encode()).decode()
    output = _SSM.run_shell_b64(
        instance_id, shell_b64, "pricing registry: read runtime snapshot"
    )
    try:
        return _decode_runtime_value(output)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        fail(f"runtime settings blob decode failed: {exc}")


def _run_registry_gate() -> None:
    result = subprocess.run(
        [sys.executable, str(REGISTRY_GATE), "--path", str(REGISTRY_PATH)],
        cwd=REPO_ROOT,
    )
    if result.returncode != 0:
        fail("pricing registry gate failed; refusing publication")


def _print_inspection(expected: RegistryArtifact, inspection: RuntimeInspection) -> None:
    print(f"protected main: {expected.source_commit}")
    print(f"registry sha256: {expected.registry_sha256}")
    if inspection.is_current(expected):
        print("OK: runtime snapshot is the exact protected-main registry artifact.")
        return
    if inspection.state == "valid":
        print(
            "DRIFT: runtime snapshot is valid but differs from protected main "
            f"(commit={inspection.source_commit}, sha256={inspection.registry_sha256})."
        )
    else:
        suffix = f": {inspection.error}" if inspection.error else ""
        print(f"DRIFT: runtime snapshot state={inspection.state}{suffix}.")


def cmd_check(_args: argparse.Namespace) -> int:
    expected = load_origin_main_artifact(require_publishable_checkout=False)
    instance_id = _SSM.resolve_prod_instance()
    inspection = inspect_runtime_document(read_runtime_document(instance_id))
    _print_inspection(expected, inspection)
    return 0 if inspection.is_current(expected) else 1


def _write_runtime_document(instance_id: str, envelope_bytes: bytes) -> None:
    gz_b64 = base64.b64encode(gzip.compress(envelope_bytes, mtime=0)).decode("ascii")
    expected_len = len(envelope_bytes.decode("utf-8"))
    expected_md5 = hashlib.md5(envelope_bytes).hexdigest()
    raw_b64_len = len(base64.b64encode(envelope_bytes))
    shell = (
        "set -euo pipefail\n"
        f"JSON_B64=\"$(echo {gz_b64} | base64 -d | gunzip | base64 | tr -d '\\n')\"\n"
        f"test \"${{#JSON_B64}}\" -eq {raw_b64_len}\n"
        f"{PSQL} <<SQL\n"
        f"INSERT INTO settings (key, value, updated_at) VALUES "
        f"('{SETTING_KEY}', convert_from(decode('$JSON_B64','base64'),'UTF8'), NOW()) "
        "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();\n"
        f"SELECT key || '|' || length(value)::text || '|' || md5(value) "
        f"FROM settings WHERE key='{SETTING_KEY}';\n"
        "SQL\n"
        "echo UPSERT_OK\n"
        f"{REDISCLI} PUBLISH settings_updated refresh </dev/null || "
        "echo 'WARN: redis PUBLISH failed; replicas will reload on their poll interval'\n"
    )
    shell_b64 = base64.b64encode(shell.encode()).decode("ascii")
    if len(shell_b64) > 90_000:
        fail(f"encoded sync payload is {len(shell_b64)} bytes; refusing oversized SSM command")
    output = _SSM.run_shell_b64(
        instance_id, shell_b64, "pricing registry: publish protected-main snapshot"
    )
    print(output)
    expected_line = f"{SETTING_KEY}|{expected_len}|{expected_md5}"
    if "UPSERT_OK" not in output or expected_line not in output:
        fail(f"runtime UPSERT read-back mismatch; expected {expected_line!r}")


def cmd_sync_runtime(args: argparse.Namespace) -> int:
    expected = load_origin_main_artifact(require_publishable_checkout=True)
    _run_registry_gate()
    envelope_bytes = encode_runtime_envelope(expected.registry_bytes, expected.source_commit)
    if args.dry_run:
        print(
            "DRY-RUN: validated protected-main pricing registry "
            f"commit={expected.source_commit} sha256={expected.registry_sha256}; no production I/O."
        )
        return 0

    instance_id = _SSM.resolve_prod_instance()
    current = inspect_runtime_document(read_runtime_document(instance_id))
    if current.is_current(expected):
        print("runtime snapshot already equals protected main; nothing to publish.")
        return 0

    # A Git revert is a newer descendant and remains publishable. An expected
    # commit behind the active source means the local origin/main ref is stale;
    # fail closed instead of turning a workstation or delayed run into rollback.
    if current.state == "valid" and current.source_commit != expected.source_commit:
        if not _git_is_ancestor(current.source_commit, expected.source_commit):
            fail(
                "refusing pricing snapshot downgrade: active runtime source "
                f"{current.source_commit} is not an ancestor of expected "
                f"protected-main source {expected.source_commit}"
            )

    _write_runtime_document(instance_id, envelope_bytes)
    post = inspect_runtime_document(read_runtime_document(instance_id))
    if not post.is_current(expected):
        fail(
            "publish returned success but exact-byte read-back verification failed: "
            f"state={post.state} error={post.error!r}"
        )
    print(
        "published + verified protected-main pricing registry "
        f"commit={expected.source_commit} sha256={expected.registry_sha256}."
    )
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command")
    sub.add_parser("check", help="read-only protected-main versus runtime audit")
    sync = sub.add_parser("sync-runtime", help="publish the protected-main registry envelope")
    sync.add_argument("--dry-run", action="store_true", help="validate without AWS/SSM I/O")
    args = parser.parse_args(argv)
    if args.command == "check":
        return cmd_check(args)
    if args.command == "sync-runtime":
        return cmd_sync_runtime(args)
    parser.print_help()
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
