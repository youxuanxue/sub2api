#!/usr/bin/env python3
"""Fetch one COS prefix into a staging directory over the S3 API.

Why this exists alongside the rclone path: neither production host nor node1
has rclone, but node1 already carries aws-cli 2.38 and boto3 1.34, and Tencent
COS is S3-compatible (the pgBackRest repo is configured as repo2-type=s3). So
the dependency-free way to mirror a backup is the S3 API itself.

Why node1 pulls instead of the host pushing, measured 2026-11-03 on the live
hosts: prod streaming to node1 over the jump host sustained 0.8 MiB/s in the
afternoon, while node1 pulling from the public internet sustained 2.9 MiB/s.
Pulling is both faster and keeps the copy off the production host's CPU and
egress entirely. It is the opposite conclusion from the QA archive, because the
shapes are opposite: QA raw is 200k files averaging 37 KiB where per-file
round-trips dominate, while the pgBackRest repository runs with repo2-bundle=y
and repo2-block=y, so its objects are already packed and the transfer is bound
by bandwidth alone.

This module only ever reads: list_objects_v2 and get_object. It never calls
put, copy, or delete, so a credential that happens to carry write permission
cannot be used by this path to modify the repository it is mirroring.
"""
from __future__ import annotations

import os
import re
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Any, Iterator

# Credentials come from the environment the caller prepared; nothing is read
# from this repository. The host keeps them in /etc/tokenkey/cos-backup.env,
# which spells them tencent_SecretId / tencent_SecretKey, so those names are
# tried first and the generic ones accepted as a fallback.
ENV_KEY = "tencent_SecretId"
ENV_SECRET = "tencent_SecretKey"
ENV_KEY_ALIASES = (ENV_KEY, "COS_SECRET_ID", "AWS_ACCESS_KEY_ID")
ENV_SECRET_ALIASES = (ENV_SECRET, "COS_SECRET_KEY", "AWS_SECRET_ACCESS_KEY")
# A COS bucket name ends in -<appid>; the endpoint is per-region.
BUCKET_RE = re.compile(r"^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]-\d{5,}$")
# Measured on node1 2026-11-03 against the live prod repository (207 WAL
# objects, 300 MiB): 8 workers sustained 19.9 MiB/s, 16 sustained 29.7 MiB/s.
# A single stream managed 2.9 MiB/s, so concurrency is what makes this viable --
# 53% of the objects are WAL segments under 1 MiB where latency dominates.
MAX_WORKERS = 16
CHUNK = 8 * 1024 * 1024


class FetchError(RuntimeError):
    pass


def _from_env(names: tuple[str, ...]) -> str:
    for name in names:
        value = os.environ.get(name, "").strip()
        if value:
            return value
    return ""


def _client(endpoint: str, region: str):
    try:
        import boto3
        from botocore.config import Config
    except ImportError as exc:  # pragma: no cover - environment dependent
        raise FetchError("boto3 is required to fetch from COS over S3") from exc
    key = _from_env(ENV_KEY_ALIASES)
    secret = _from_env(ENV_SECRET_ALIASES)
    if not key or not secret:
        raise FetchError(f"{ENV_KEY} and {ENV_SECRET} must be set in the environment")
    if not endpoint.startswith("https://"):
        endpoint = "https://" + endpoint
    return boto3.client(
        "s3",
        endpoint_url=endpoint,
        region_name=region,
        aws_access_key_id=key,
        aws_secret_access_key=secret,
        # COS uses virtual-host addressing, matching repo2-s3-uri-style=host.
        config=Config(s3={"addressing_style": "virtual"},
                      retries={"max_attempts": 5, "mode": "standard"},
                      connect_timeout=20, read_timeout=120),
    )


def validate_bucket(bucket: str) -> str:
    if not BUCKET_RE.match(bucket):
        raise FetchError(f"not a COS bucket name: {bucket!r}")
    return bucket


def normalise_prefix(prefix: str) -> str:
    """A prefix must stay inside the repository and cannot escape upward."""
    cleaned = prefix.strip().strip("/")
    if not cleaned:
        raise FetchError("prefix must not be empty")
    parts = [p for p in cleaned.split("/") if p]
    if any(p in (".", "..") for p in parts):
        raise FetchError(f"prefix must not contain relative segments: {prefix!r}")
    return "/".join(parts)


def iter_objects(client, bucket: str, prefix: str) -> Iterator[dict[str, Any]]:
    """Every object under one prefix, paginated."""
    token = None
    while True:
        kwargs = {"Bucket": bucket, "Prefix": prefix + "/", "MaxKeys": 1000}
        if token:
            kwargs["ContinuationToken"] = token
        page = client.list_objects_v2(**kwargs)
        for item in page.get("Contents", []):
            key = item["Key"]
            # A directory placeholder carries no payload.
            if key.endswith("/") and item.get("Size", 0) == 0:
                continue
            yield {"key": key, "bytes": int(item.get("Size", 0)),
                   "etag": (item.get("ETag") or "").strip('"')}
        if not page.get("IsTruncated"):
            return
        token = page.get("NextContinuationToken")
        if not token:
            return


def survey(*, bucket: str, prefix: str, endpoint: str, region: str) -> dict[str, Any]:
    """Count and size a prefix without downloading it.

    The mirror needs this before copying: its capacity policy reserves space up
    front, and a snapshot that cannot fit must be refused before any bytes move.
    """
    client = _client(endpoint, region)
    objects = list(iter_objects(client, validate_bucket(bucket), normalise_prefix(prefix)))
    return {"object_count": len(objects),
            "total_bytes": sum(o["bytes"] for o in objects),
            "objects": objects}


def _target_for(staging: Path, key: str, prefix: str) -> Path:
    relative = key[len(prefix) + 1:] if key.startswith(prefix + "/") else key
    if not relative or relative.startswith("/"):
        raise FetchError(f"object key does not sit under the prefix: {key!r}")
    target = (staging / relative).resolve()
    root = staging.resolve()
    if root != target and root not in target.parents:
        raise FetchError(f"object key escapes the staging directory: {key!r}")
    return target


def _download_one(client, bucket: str, prefix: str, staging: Path, obj: dict) -> int:
    target = _target_for(staging, obj["key"], prefix)
    target.parent.mkdir(parents=True, exist_ok=True)
    partial = target.with_name(target.name + ".part")
    body = client.get_object(Bucket=bucket, Key=obj["key"])["Body"]
    written = 0
    try:
        with partial.open("wb") as handle:
            while True:
                block = body.read(CHUNK)
                if not block:
                    break
                handle.write(block)
                written += len(block)
    finally:
        body.close()
    if written != obj["bytes"]:
        partial.unlink(missing_ok=True)
        raise FetchError(f"short read for {obj['key']}: {written} of {obj['bytes']}")
    partial.replace(target)
    os.chmod(target, 0o600)
    return written


def fetch_prefix(*, bucket: str, prefix: str, endpoint: str, region: str,
                 staging: Path, objects: list[dict] | None = None,
                 workers: int = MAX_WORKERS) -> dict[str, Any]:
    """Download every object under one prefix into staging.

    Each object lands as a .part file and is renamed only once its byte count
    matches the listing, so an interrupted fetch leaves no file that a manifest
    would accept. The caller still verifies the whole tree afterwards; this is
    the cheap check that keeps a truncated object from reaching that stage.
    """
    bucket = validate_bucket(bucket)
    prefix = normalise_prefix(prefix)
    client = _client(endpoint, region)
    if objects is None:
        objects = list(iter_objects(client, bucket, prefix))
    if not objects:
        raise FetchError(f"no objects under {bucket}/{prefix}")
    staging.mkdir(parents=True, exist_ok=True)

    copied = 0
    failures: list[str] = []
    with ThreadPoolExecutor(max_workers=max(1, workers)) as pool:
        futures = {pool.submit(_download_one, client, bucket, prefix, staging, o): o
                   for o in objects}
        for future in as_completed(futures):
            try:
                copied += future.result()
            except Exception as exc:
                failures.append(f"{futures[future]['key']}: {exc}")
    if failures:
        raise FetchError("objects failed to download: " + "; ".join(failures[:5]))
    expected = sum(o["bytes"] for o in objects)
    if copied != expected:
        raise FetchError(f"copied {copied} bytes, listing declared {expected}")
    return {"object_count": len(objects), "total_bytes": copied}
