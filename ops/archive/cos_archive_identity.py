#!/usr/bin/env python3
"""COS-only object-archive identity + AWS destination fail-closed.

SSOT: docs/approved/design-cos-object-archive.md
Settings: ops/archive/cos-archive.json (bucket/endpoint/prefixes; no secrets).
"""
from __future__ import annotations

import json
import os
import re
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parent
SETTINGS_NAME = "cos-archive.json"
# Same CAM key as pgBackRest; one file on every data machine (prod + cn-prod).
DEFAULT_CREDENTIALS = Path("/etc/tokenkey/cos-backup.env")
_HOST_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]*[A-Za-z0-9]$")
_BUCKET_RE = re.compile(r"^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$")


class CosArchiveError(ValueError):
    """Fail-closed object-archive identity error."""


def settings_path() -> Path:
    explicit = os.environ.get("TK_COS_ARCHIVE_SETTINGS", "").strip()
    return Path(explicit) if explicit else ROOT / SETTINGS_NAME


def load_settings(path: Path | None = None) -> dict[str, Any]:
    target = path or settings_path()
    try:
        document = json.loads(target.read_text(encoding="utf-8"))
    except (OSError, ValueError, TypeError) as exc:
        raise CosArchiveError(f"cos archive settings unreadable: {target}") from exc
    if not isinstance(document, dict):
        raise CosArchiveError("cos archive settings must be a JSON object")
    bucket = document.get("bucket")
    region = document.get("region")
    endpoint = document.get("endpoint")
    prefixes = document.get("prefixes")
    expire_days = document.get("expire_days")
    if not isinstance(bucket, str) or not _BUCKET_RE.fullmatch(bucket):
        raise CosArchiveError("cos archive bucket is invalid")
    if not isinstance(region, str) or not region:
        raise CosArchiveError("cos archive region is invalid")
    if not isinstance(endpoint, str) or not _HOST_RE.fullmatch(endpoint):
        raise CosArchiveError("cos archive endpoint must be a bare host name")
    if "amazonaws.com" in endpoint.lower():
        raise CosArchiveError("cos archive endpoint must not be AWS")
    if not isinstance(prefixes, dict) or not isinstance(expire_days, dict):
        raise CosArchiveError("cos archive prefixes/expire_days missing")
    for key in ("ops_archive", "ops_archive_export", "host_config"):
        prefix = prefixes.get(key)
        days = expire_days.get(key)
        if not isinstance(prefix, str) or not prefix or prefix.startswith("/") or ".." in prefix:
            raise CosArchiveError(f"invalid prefix for {key}")
        if not isinstance(days, int) or isinstance(days, bool) or days < 1:
            raise CosArchiveError(f"invalid expire_days for {key}")
    forbidden = document.get("forbidden_aws_buckets") or []
    if not isinstance(forbidden, list) or not all(isinstance(item, str) for item in forbidden):
        raise CosArchiveError("forbidden_aws_buckets must be a string list")
    return document


def endpoint_url(settings: dict[str, Any] | None = None) -> str:
    """Virtual-hosted-style COS endpoint required by Tencent (path-style forbidden)."""
    cfg = settings or load_settings()
    return f"https://{cfg['bucket']}.{cfg['endpoint']}"


def archive_base_uri(settings: dict[str, Any] | None = None) -> str:
    cfg = settings or load_settings()
    return f"s3://{cfg['bucket']}/{cfg['prefixes']['ops_archive']}"


def export_base_uri(settings: dict[str, Any] | None = None) -> str:
    cfg = settings or load_settings()
    return f"s3://{cfg['bucket']}/{cfg['prefixes']['ops_archive_export']}"


def host_config_base_uri(settings: dict[str, Any] | None = None) -> str:
    cfg = settings or load_settings()
    return f"s3://{cfg['bucket']}/{cfg['prefixes']['host_config']}"


def refuse_aws_destination(uri_or_bucket: str, *, settings: dict[str, Any] | None = None) -> None:
    """Fail closed when an archive write would land on AWS S3."""
    cfg = settings or load_settings()
    text = uri_or_bucket.strip()
    bucket = text
    if text.startswith("s3://"):
        parts = urlsplit(text)
        bucket = parts.netloc
        hostish = parts.netloc.lower()
        if "amazonaws.com" in hostish:
            raise CosArchiveError("archive destination must not be AWS S3")
    bucket = bucket.split(".")[0] if bucket.endswith(".amazonaws.com") else bucket
    forbidden = {name.lower() for name in cfg.get("forbidden_aws_buckets") or []}
    if bucket.lower() in forbidden:
        raise CosArchiveError(f"archive destination bucket is forbidden AWS residual: {bucket}")
    if "amazonaws.com" in text.lower():
        raise CosArchiveError("archive destination must not be AWS S3")
    # Approved COS bucket is the only allowed write target for new archives.
    if bucket != cfg["bucket"]:
        raise CosArchiveError(
            f"archive destination bucket must be {cfg['bucket']}, got {bucket}"
        )


def aws_cli_env(credentials_path: Path | None = None) -> dict[str, str]:
    """Build env for aws CLI against COS. Never prints secrets."""
    path = credentials_path
    if path is None:
        explicit = os.environ.get("TK_COS_ARCHIVE_CREDENTIALS", "").strip()
        path = Path(explicit) if explicit else DEFAULT_CREDENTIALS
    if not path.is_file():
        raise CosArchiveError(f"cos archive credentials missing: {path}")
    raw = path.read_text(encoding="utf-8")
    secret_id = secret_key = ""
    if raw.lstrip().startswith("{"):
        payload = json.loads(raw)
        secret_id = str(payload.get("secretId") or payload.get("SecretId") or "")
        secret_key = str(payload.get("secretKey") or payload.get("SecretKey") or "")
    else:
        for line in raw.splitlines():
            if "=" not in line or line.strip().startswith("#"):
                continue
            key, value = line.split("=", 1)
            key = key.strip()
            value = value.strip().strip('"').strip("'")
            if key in {"secretId", "SecretId", "tencent_SecretId"}:
                secret_id = value
            elif key in {"secretKey", "SecretKey", "tencent_SecretKey"}:
                secret_key = value
    if not secret_id or not secret_key:
        raise CosArchiveError("cos archive credentials incomplete")
    env = os.environ.copy()
    env["AWS_ACCESS_KEY_ID"] = secret_id
    env["AWS_SECRET_ACCESS_KEY"] = secret_key
    env.pop("AWS_SESSION_TOKEN", None)
    cfg = load_settings()
    env["AWS_DEFAULT_REGION"] = str(cfg["region"])
    return env


def inject_endpoint(argv: list[str], settings: dict[str, Any] | None = None) -> list[str]:
    """Insert virtual-hosted COS --endpoint-url into an aws s3/s3api argv list."""
    cfg = settings or load_settings()
    out = list(argv)
    if len(out) >= 2 and out[0] == "aws" and out[1] in {"s3", "s3api"}:
        if "--endpoint-url" not in out:
            out[2:2] = ["--endpoint-url", endpoint_url(cfg)]
    return out


def wrap_runner(command_runner):
    """Wrap a command runner so archive writes hit COS, not AWS."""

    def run(argv: list[str]) -> str:
        return command_runner(inject_endpoint(argv))

    return run
