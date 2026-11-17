#!/usr/bin/env python3
"""Upload a host-config / one-shot offsite package to the COS ops-archive bucket.

Plan by default. APPLY requires --apply and writes under prod/host-config/{label}/.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

import cos_archive_identity as cos_archive


def _sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--file", required=True, help="local file to archive")
    parser.add_argument("--label", required=True, help="prod/host-config/<label>/…")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument(
        "--credentials",
        default=os.environ.get("TK_COS_ARCHIVE_CREDENTIALS", ""),
        help="path to COS secretId/secretKey file (default /etc/tokenkey/cos-archive.env)",
    )
    args = parser.parse_args(argv)
    src = pathlib.Path(args.file).expanduser().resolve()
    if not src.is_file():
        raise SystemExit(f"file not found: {src}")
    if ".." in args.label or "/" in args.label or not args.label:
        raise SystemExit("label must be a single path segment")
    settings = cos_archive.load_settings()
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    key = f"{settings['prefixes']['host_config']}/{args.label}/{stamp}-{src.name}"
    uri = f"s3://{settings['bucket']}/{key}"
    cos_archive.refuse_aws_destination(uri, settings=settings)
    plan = {
        "operation": "apply" if args.apply else "plan",
        "uri": uri,
        "bytes": src.stat().st_size,
        "sha256": _sha256(src),
        "endpoint": settings["endpoint"],
        "expire_days": settings["expire_days"]["host_config"],
    }
    if not args.apply:
        print(json.dumps(plan, ensure_ascii=False, indent=2))
        return 0
    cred_path = (
        pathlib.Path(args.credentials)
        if args.credentials
        else cos_archive.DEFAULT_CREDENTIALS
    )
    env = cos_archive.aws_cli_env(cred_path)
    endpoint = cos_archive.endpoint_url(settings)
    subprocess.check_call(
        [
            "aws",
            "s3",
            "cp",
            str(src),
            uri,
            "--endpoint-url",
            endpoint,
            "--region",
            settings["region"],
            "--sse",
            "AES256",
            "--metadata",
            f"sha256={plan['sha256']}",
            "--only-show-errors",
        ],
        env=env,
    )
    head = json.loads(
        subprocess.check_output(
            [
                "aws",
                "s3api",
                "head-object",
                "--bucket",
                settings["bucket"],
                "--key",
                key,
                "--endpoint-url",
                endpoint,
                "--region",
                settings["region"],
                "--output",
                "json",
            ],
            env=env,
            text=True,
        )
    )
    if head.get("ContentLength") != plan["bytes"]:
        raise SystemExit("COS head content-length mismatch")
    receipt = {
        **plan,
        "etag": head.get("ETag"),
        "server_side_encryption": head.get("ServerSideEncryption"),
    }
    print(json.dumps(receipt, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except cos_archive.CosArchiveError as exc:
        print(json.dumps({"ok": False, "reason": str(exc)}, ensure_ascii=False))
        raise SystemExit(2) from exc
