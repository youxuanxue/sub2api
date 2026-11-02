#!/usr/bin/env python3
"""Refresh cn-prod pgBackRest identity without exposing credentials.

The AWS Hybrid identity remains repo1.  A root-only COS credential file is an
explicit opt-in for repo2; credentials are written only to the tmpfs runtime
configuration and never to receipts or stdout.
"""
from __future__ import annotations

import configparser
import datetime as dt
import hashlib
import hmac
import json
import os
from pathlib import Path
import socket
import stat
import subprocess
import tempfile
import urllib.request
import xml.etree.ElementTree as ET

EXPECTED_ACCOUNT = "682751977094"
ROLE_ID = "AROAZ55ZQM2DC6BZQWQ4B"
TARGET_NAME = "prod-target.json"
TARGET_CANDIDATES = (
    Path("/opt/tokenkey") / TARGET_NAME,
    Path(__file__).resolve().parent / TARGET_NAME,
)
AWS_BUCKET = "tokenkey-cn-prod-physical-backup-682751977094-ap-northeast-1"
AWS_REGION = "ap-northeast-1"
CONFIG = Path("/run/tokenkey-cn-prod-backup/config/pgbackrest.conf")
COS_CREDENTIALS = Path("/etc/tokenkey/cos-backup.env")
COS_BUCKET = "tokenkey-cnprod-backup-bj-1320330536"
COS_REGION = "ap-beijing"
COS_ENDPOINT = "cos.ap-beijing.myqcloud.com"
COS_PATH = "/cn-prod/postgres"


def load_target(candidates: tuple[Path, ...] = TARGET_CANDIDATES) -> dict[str, str]:
    """Load the domestic target manifest; never guess a managed-instance id."""
    explicit = os.environ.get("TK_CN_PROD_TARGET", "").strip()
    paths = (Path(explicit),) if explicit else candidates
    for path in paths:
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
            target = {key: data[key] for key in ("instance_name", "ssm_instance_id", "ssm_region")}
        except (OSError, ValueError, KeyError, TypeError):
            continue
        if (not target["ssm_instance_id"].startswith("mi-")
                or not target["instance_name"] or not target["ssm_region"]):
            raise ValueError("invalid cn-prod target manifest")
        return target
    raise ValueError("cn-prod target manifest not found")


def read_aws_credentials(path: Path = Path("/root/.aws/credentials")) -> dict[str, str]:
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0
                or stat.S_IMODE(info.st_mode) not in (0o400, 0o600)
                or not 0 < info.st_size <= 16384):
            raise ValueError("unsafe AWS credential file")
        parser = configparser.ConfigParser(interpolation=None)
        parser.read_string(stream.read())
    values = parser["default"]
    result = {key: values[key] for key in
              ("aws_access_key_id", "aws_secret_access_key", "aws_session_token")}
    if any(not value or any(ch.isspace() for ch in value) for value in result.values()):
        raise ValueError("invalid AWS credential fields")
    return result


def read_cos_credentials(path: Path = COS_CREDENTIALS, uid: int = 0) -> dict[str, str]:
    """Read exactly the two root-only COS fields without exposing values."""
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != uid
                or stat.S_IMODE(info.st_mode) not in (0o400, 0o600)
                or not 0 < info.st_size <= 4096):
            raise ValueError("unsafe COS credential file")
        values: dict[str, str] = {}
        for raw in stream:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            key, sep, value = line.partition("=")
            if not sep or key not in ("tencent_SecretId", "tencent_SecretKey"):
                raise ValueError("invalid COS credential file")
            if not value or any(ch.isspace() for ch in value) or value[0] in "'\"":
                raise ValueError("invalid COS credential value")
            values[key] = value
    if set(values) != {"tencent_SecretId", "tencent_SecretKey"}:
        raise ValueError("incomplete COS credential file")
    return {"key": values["tencent_SecretId"], "secret": values["tencent_SecretKey"]}


def caller_identity(credentials: dict[str, str], ssm_region: str) -> dict[str, str]:
    service = "sts"
    host = f"sts.{ssm_region}.amazonaws.com"
    body = b"Action=GetCallerIdentity&Version=2011-06-15"
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    day = stamp[:8]
    headers = {
        "content-type": "application/x-www-form-urlencoded; charset=utf-8",
        "host": host,
        "x-amz-date": stamp,
        "x-amz-security-token": credentials["aws_session_token"],
    }
    signed = ";".join(sorted(headers))
    canonical = ("POST\n/\n\n"
                 + "".join(key + ":" + headers[key] + "\n" for key in sorted(headers))
                 + "\n" + signed + "\n" + hashlib.sha256(body).hexdigest())
    scope = "/".join((day, ssm_region, service, "aws4_request"))
    message = ("AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n"
               + hashlib.sha256(canonical.encode()).hexdigest())
    key = ("AWS4" + credentials["aws_secret_access_key"]).encode()
    for part in (day, ssm_region, service, "aws4_request"):
        key = hmac.new(key, part.encode(), hashlib.sha256).digest()
    headers["Authorization"] = (
        "AWS4-HMAC-SHA256 Credential=" + credentials["aws_access_key_id"] + "/" + scope
        + ", SignedHeaders=" + signed + ", Signature="
        + hmac.new(key, message.encode(), hashlib.sha256).hexdigest())
    request = urllib.request.Request("https://" + host + "/", data=body, headers=headers)
    with urllib.request.urlopen(request, timeout=20) as response:
        root = ET.fromstring(response.read())
    return {node.tag.split("}")[-1]: node.text for node in root.iter()
            if node.tag.split("}")[-1] in ("Arn", "UserId", "Account")}


def validate_identity(identity: dict[str, str], target: dict[str, str]) -> None:
    # UserId binds both the role id and managed-instance session.  Do not bind
    # the human-readable role name, which may change without changing scope.
    expected_user = ROLE_ID + ":" + target["ssm_instance_id"]
    if identity.get("Account") != EXPECTED_ACCOUNT or identity.get("UserId") != expected_user:
        raise ValueError("unexpected SSM runtime identity")


def render_config(aws: dict[str, str], cos: dict[str, str] | None = None) -> str:
    lines = [
        "[global]",
        "repo1-type=s3", "repo1-path=/cn-prod/postgres",
        f"repo1-s3-bucket={AWS_BUCKET}", f"repo1-s3-region={AWS_REGION}",
        f"repo1-s3-endpoint=s3.{AWS_REGION}.amazonaws.com",
        "repo1-s3-key-type=shared", f"repo1-s3-key={aws['aws_access_key_id']}",
        f"repo1-s3-key-secret={aws['aws_secret_access_key']}",
        f"repo1-s3-token={aws['aws_session_token']}",
        "repo1-retention-full-type=time", "repo1-retention-full=7",
        "repo1-bundle=y", "repo1-block=y", "compress-type=zst", "process-max=2",
        "start-fast=y", "archive-async=n", "archive-timeout=120",
        "log-level-console=info", "log-level-file=off",
    ]
    if cos is not None:
        lines.extend([
            "repo2-type=s3", f"repo2-path={COS_PATH}",
            f"repo2-s3-bucket={COS_BUCKET}", f"repo2-s3-region={COS_REGION}",
            f"repo2-s3-endpoint={COS_ENDPOINT}", "repo2-s3-uri-style=host",
            "repo2-s3-key-type=shared", f"repo2-s3-key={cos['key']}",
            f"repo2-s3-key-secret={cos['secret']}",
            "repo2-retention-full-type=time", "repo2-retention-full=7",
            "repo2-bundle=y", "repo2-block=y",
        ])
    lines.extend(["", "[tokenkey]", "pg1-path=/var/lib/postgresql/data",
                  "pg1-socket-path=/var/run/postgresql", "pg1-user=tokenkey", ""])
    return "\n".join(lines)


def atomic_private(contents: str, destination: Path | None = None) -> None:
    """Write a 0600 postgres-owned config atomically inside the mounted dir.

    ``destination`` is parameterised so the repo2 enablement preflight can stage
    a sibling file in the same bind-mounted directory (the container sees the
    whole directory, so a sibling is reachable via `pgbackrest --config`) without
    touching the live pgbackrest.conf that archive_command depends on. It
    resolves CONFIG at call time rather than as a default argument, so the live
    path stays overridable.
    """
    destination = CONFIG if destination is None else destination
    if destination.is_symlink() or destination.parent.is_symlink():
        raise ValueError("symlink configuration refused")
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    os.chmod(destination.parent.parent, 0o711)
    os.chmod(destination.parent, 0o755)
    fd, temporary = tempfile.mkstemp(prefix=".cfg-", dir=destination.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            os.fchmod(stream.fileno(), 0o600)
            os.fchown(stream.fileno(), 70, 70)
            stream.write(contents)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, destination)
        os.chmod(destination, 0o600)
        os.chown(destination, 70, 70)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


# Staged config lives beside the live one: the container bind-mounts the whole
# directory, so `pgbackrest --config` can reach a sibling while archive_command
# keeps using pgbackrest.conf.
STAGING = CONFIG.with_name(".pgbackrest-staging.conf")
STAGING_IN_CONTAINER = "/etc/pgbackrest/" + STAGING.name
POSTGRES_CANDIDATES = ("tokenkey-postgres", "tk-archive-standby")


def _container() -> str:
    """Resolve the single running database container, failing closed.

    Mirrors deploy/domestic/cn_pgbackrest.py:_container(): a literal target dies
    with "No such container" on the Tencent USW layout, which renamed exactly
    this container, while still looking like a healthy probe.
    """
    running = [name for name in POSTGRES_CANDIDATES if subprocess.run(
        ["docker", "inspect", "-f", "{{.State.Running}}", name],
        capture_output=True, text=True, check=False).stdout.strip() == "true"]
    if len(running) != 1:
        raise ValueError(f"postgres container unresolved (candidates: {', '.join(POSTGRES_CANDIDATES)})")
    return running[0]


def repo2_live() -> bool:
    """True when the live config already carries repo2 (steady state)."""
    try:
        return any(line.startswith("repo2-type=")
                   for line in CONFIG.read_text(encoding="utf-8").splitlines())
    except OSError:
        return False


class RepositoryBusy(RuntimeError):
    """stanza-create lost the lock race with a running backup; retry later."""


def _staged(container: str, *args: str, timeout: int) -> str:
    result = subprocess.run(
        ["docker", "exec", "--user", "postgres", container, "pgbackrest",
         f"--config={STAGING_IN_CONTAINER}", "--stanza=tokenkey", *args],
        capture_output=True, text=True, check=False, timeout=timeout + 15)
    if result.returncode:
        output = (result.stderr or result.stdout or "")[-2000:]
        # [050] is a lock held by the 2-hourly backup, not a repo2 problem.
        # Treating it as failure would turn a timing collision into a stuck
        # enablement; the next 15-minute refresh retries cleanly.
        if "[050]" in output:
            raise RepositoryBusy(" ".join(args))
        raise ValueError(f"staged pgbackrest {' '.join(args)} failed: {output}")
    return result.stdout


def prepare_repo2(contents: str) -> None:
    """Create and verify repo2 BEFORE it reaches the live configuration.

    Why this exists: archive_command is `pgbackrest --stanza=tokenkey
    archive-push %p` with no --repo, so pgBackRest fans WAL out to EVERY
    configured repository. The moment repo2 lands in the live config, archiving
    depends on it -- and a brand-new repository has no stanza, so every push
    fails until archive-timeout expires. On 2026-11-02 that stalled cn-prod WAL
    archiving for ~3 minutes and failed the 14:30 backup ([082] WAL segment not
    archived), with repo1 itself perfectly healthy.

    So the repo2 config is staged beside the live one and exercised through
    --config first, exactly as the approved rollout order requires
    (docs/ops/stage0/pgbackrest-cos-parallel-repository.md step 2: "put the repo2
    configuration in a temporary config file, run stanza-create, check and a
    read-only info; confirm repo1 is still readable, THEN enable dual writes").
    Any failure leaves the live config AWS-only, so the archive path never
    depends on an unverified repository.
    """
    container = _container()
    atomic_private(contents, STAGING)
    try:
        # Order is load-bearing: create the stanza, prove both repositories
        # accept a WAL round-trip, then prove the primary chain still reads.
        _staged(container, "stanza-create", timeout=300)
        _staged(container, "check", timeout=180)
        primary = json.loads(_staged(container, "--repo=1", "--output=json", "info", timeout=120))
        if len(primary) != 1 or primary[0].get("name") != "tokenkey":
            raise ValueError("staged repo1 info did not describe the tokenkey stanza")
        if (primary[0].get("status") or {}).get("code") != 0:
            raise ValueError("staged repo1 info reported a degraded primary chain")
    finally:
        if STAGING.exists():
            STAGING.unlink()


def refresh() -> dict[str, object]:
    target = load_target()
    if os.geteuid() != 0 or socket.gethostname() != target["instance_name"]:
        raise ValueError("wrong target host or user")
    if subprocess.check_output(["findmnt", "-n", "-o", "FSTYPE", "--target", "/run"], text=True).strip() != "tmpfs":
        raise ValueError("runtime directory is not tmpfs")
    aws = read_aws_credentials()
    validate_identity(caller_identity(aws, target["ssm_region"]), target)
    cos = read_cos_credentials() if COS_CREDENTIALS.exists() else None
    contents = render_config(aws, cos)
    # Only the enablement transition needs the preflight. In steady state (repo2
    # already live, or no COS credentials at all) this is a plain render, so the
    # 15-minute timer costs nothing extra.
    if cos is not None and not repo2_live():
        try:
            prepare_repo2(contents)
        except RepositoryBusy as busy:
            # Deliberately not an error: the live config stays AWS-only and the
            # next refresh retries. Reporting it as failure would page someone
            # for a lock collision that resolves itself.
            return {"status": "deferred", "source_repository": "read-only",
                    "cos_repo2": "pending", "reason": f"repository busy: {busy}",
                    "credentials_withheld": True, "cutover_authorized": False}
        # Re-read repo1's identity instead of installing the config rendered
        # before the preflight. The SSM agent rotates /root/.aws/credentials on
        # its own schedule (it logs "next credential rotation will be in ~25
        # minutes"), and the preflight can burn up to ~11 minutes, so the
        # pre-rendered session token may already be superseded. Writing it would
        # leave repo1 on a stale token until the next refresh -- defeating the
        # very ordering this unit exists to guarantee.
        aws = read_aws_credentials()
        validate_identity(caller_identity(aws, target["ssm_region"]), target)
        atomic_private(render_config(aws, cos))
        return {"status": "refreshed", "source_repository": "read-only",
                "cos_repo2": "enabled", "repo2_prepared": True,
                "credentials_withheld": True, "cutover_authorized": False}
    atomic_private(contents)
    return {"status": "refreshed", "source_repository": "read-only",
            "cos_repo2": "enabled" if cos is not None else "disabled",
            "credentials_withheld": True, "cutover_authorized": False}


def main() -> int:
    try:
        print(__import__("json").dumps(refresh()))
    except Exception:
        print('{"status":"failed","credentials_withheld":true,"cutover_authorized":false}')
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
