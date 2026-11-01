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


def atomic_private(contents: str) -> None:
    if CONFIG.is_symlink() or CONFIG.parent.is_symlink():
        raise ValueError("symlink configuration refused")
    CONFIG.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    os.chmod(CONFIG.parent.parent, 0o711)
    os.chmod(CONFIG.parent, 0o755)
    fd, temporary = tempfile.mkstemp(prefix=".cfg-", dir=CONFIG.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            os.fchmod(stream.fileno(), 0o600)
            os.fchown(stream.fileno(), 70, 70)
            stream.write(contents)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, CONFIG)
        os.chmod(CONFIG, 0o600)
        os.chown(CONFIG, 70, 70)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def refresh() -> dict[str, object]:
    target = load_target()
    if os.geteuid() != 0 or socket.gethostname() != target["instance_name"]:
        raise ValueError("wrong target host or user")
    if subprocess.check_output(["findmnt", "-n", "-o", "FSTYPE", "--target", "/run"], text=True).strip() != "tmpfs":
        raise ValueError("runtime directory is not tmpfs")
    aws = read_aws_credentials()
    validate_identity(caller_identity(aws, target["ssm_region"]), target)
    cos = read_cos_credentials() if COS_CREDENTIALS.exists() else None
    atomic_private(render_config(aws, cos))
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
