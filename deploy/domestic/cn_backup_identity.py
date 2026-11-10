#!/usr/bin/env python3
"""Render cn-prod's COS-only pgBackRest runtime config without exposing credentials.

2026-11-08 user decision: stop writing AWS repo1 (Tokyo); Beijing COS, kept at
index repo2, is the only repository
(docs/approved/design-tencent-cos-backup-node1-qa.md). This no longer reads the
SSM Agent's AWS session, calls STS, or renders any repo1/AWS option. The
root-only COS credential is required: without it the refresh fails and the
current config stays in place, never falling back to AWS.

The repo2 staging preflight (stanza-create through a sibling config before
going live) was removed with repo1: it protected an AWS-only live
archive_command from a stanza-less COS repository. COS has been live with a
valid stanza since 2026-11-02, so removing repo1 only shrinks the archive-push
fan-out and needs no staging (verified with pgBackRest 2.58, see the design).

After the data-plane split (docs/approved/design-data-plane-split.md) the live
database runs on the data machine (`data_instance_name` in prod-target.json).
There the refresh reads the data machine's own root-only COS credential and
rewrites the on-disk config the database container mounts read-only, so a key
rotation is one file on one host.
"""
from __future__ import annotations

import json
import os
from pathlib import Path
import socket
import stat
import subprocess
import tempfile

TARGET_NAME = "prod-target.json"
TARGET_CANDIDATES = (
    Path("/opt/tokenkey") / TARGET_NAME,
    Path(__file__).resolve().parent / TARGET_NAME,
)
CONFIG = Path("/run/tokenkey-cn-prod-backup/config/pgbackrest.conf")
# Data machine: the database container mounts this directory read-only at
# /etc/pgbackrest. root:postgres 0640 is what the container reads today.
DATA_CONFIG = Path("/etc/tokenkey/pgbackrest-cos/pgbackrest.conf")
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
        data_name = data.get("data_instance_name")
        target["data_instance_name"] = data_name if isinstance(data_name, str) and data_name else ""
        return target
    raise ValueError("cn-prod target manifest not found")


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


def render_config(cos: dict[str, str]) -> str:
    """COS-only config at index repo2; nothing for repo1 or any AWS endpoint."""
    lines = [
        "[global]",
        "repo2-type=s3", f"repo2-path={COS_PATH}",
        f"repo2-s3-bucket={COS_BUCKET}", f"repo2-s3-region={COS_REGION}",
        f"repo2-s3-endpoint={COS_ENDPOINT}", "repo2-s3-uri-style=host",
        "repo2-s3-key-type=shared", f"repo2-s3-key={cos['key']}",
        f"repo2-s3-key-secret={cos['secret']}",
        "repo2-retention-full-type=time", "repo2-retention-full=7",
        "repo2-bundle=y", "repo2-block=y",
        "compress-type=zst", "process-max=2",
        "start-fast=y", "archive-async=n", "archive-timeout=120",
        "log-level-console=info", "log-level-file=off",
        "", "[tokenkey]", "pg1-path=/var/lib/postgresql/data",
        "pg1-socket-path=/var/run/postgresql", "pg1-user=tokenkey", "",
    ]
    return "\n".join(lines)


def atomic_private(contents: str, destination: Path | None = None, *,
                   owner: tuple[int, int] = (70, 70), mode: int = 0o600,
                   prepare_parent: bool = True) -> None:
    """Write a postgres-readable config atomically inside the mounted dir."""
    destination = CONFIG if destination is None else destination
    if destination.is_symlink() or destination.parent.is_symlink():
        raise ValueError("symlink configuration refused")
    if prepare_parent:
        destination.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
        os.chmod(destination.parent.parent, 0o711)
        os.chmod(destination.parent, 0o755)
    fd, temporary = tempfile.mkstemp(prefix=".cfg-", dir=destination.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            os.fchmod(stream.fileno(), mode)
            os.fchown(stream.fileno(), *owner)
            stream.write(contents)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, destination)
        os.chmod(destination, mode)
        os.chown(destination, *owner)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def refresh_data_machine() -> dict[str, object]:
    """Rewrite the data machine's mounted config from its own credential file."""
    if DATA_CONFIG.parent.is_symlink() or not DATA_CONFIG.parent.is_dir():
        raise ValueError("data-machine pgBackRest config directory missing")
    # Read first: a missing or malformed credential keeps the current config.
    contents = render_config(read_cos_credentials())
    atomic_private(contents, DATA_CONFIG, owner=(0, 70), mode=0o640, prepare_parent=False)
    return {"status": "refreshed", "host_role": "data-machine", "repositories": ["cos-repo2"],
            "aws_repo1": "retired", "credentials_withheld": True, "cutover_authorized": False}


def refresh() -> dict[str, object]:
    target = load_target()
    if os.geteuid() != 0:
        raise ValueError("refresh requires root")
    hostname = socket.gethostname()
    if target["data_instance_name"] and hostname == target["data_instance_name"]:
        return refresh_data_machine()
    if hostname != target["instance_name"]:
        raise ValueError("wrong target host; refresh runs on the app host or the data machine")
    if subprocess.check_output(["findmnt", "-n", "-o", "FSTYPE", "--target", "/run"], text=True).strip() != "tmpfs":
        raise ValueError("runtime directory is not tmpfs")
    atomic_private(render_config(read_cos_credentials()))
    return {"status": "refreshed", "repositories": ["cos-repo2"], "aws_repo1": "retired",
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
