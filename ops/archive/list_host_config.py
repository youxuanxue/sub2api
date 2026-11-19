#!/usr/bin/env python3
"""Read-only list of COS prod/host-config/ objects (no download of bodies).

Uses the same COS identity as upload_host_config.py. Prints key / size /
LastModified only — never object content.
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import subprocess
import sys

_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import cos_archive_identity as cos_archive  # noqa: E402


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--prefix",
        default="",
        help="optional extra prefix under prod/host-config/ (e.g. prod-entry)",
    )
    parser.add_argument(
        "--credentials",
        default=os.environ.get("TK_COS_ARCHIVE_CREDENTIALS", ""),
        help="path to COS secretId/secretKey file",
    )
    args = parser.parse_args(argv)
    settings = cos_archive.load_settings()
    base = settings["prefixes"]["host_config"].rstrip("/") + "/"
    if args.prefix:
        if "/" in args.prefix or ".." in args.prefix:
            raise SystemExit("prefix must be a single path segment")
        base = f"{base}{args.prefix.strip('/')}/"
    uri = f"s3://{settings['bucket']}/{base}"
    cos_archive.refuse_aws_destination(uri, settings=settings)
    cred_path = (
        pathlib.Path(args.credentials)
        if args.credentials
        else cos_archive.DEFAULT_CREDENTIALS
    )
    env = cos_archive.aws_cli_env(cred_path)
    endpoint = cos_archive.endpoint_url(settings)
    raw = subprocess.check_output(
        [
            "aws",
            "s3api",
            "list-objects-v2",
            "--bucket",
            settings["bucket"],
            "--prefix",
            base,
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
    payload = json.loads(raw or "{}")
    objects = []
    for item in payload.get("Contents") or []:
        objects.append(
            {
                "key": item.get("Key"),
                "bytes": item.get("Size"),
                "last_modified": item.get("LastModified"),
            }
        )
    print(
        json.dumps(
            {
                "ok": True,
                "bucket": settings["bucket"],
                "prefix": base,
                "count": len(objects),
                "objects": objects,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except cos_archive.CosArchiveError as exc:
        print(json.dumps({"ok": False, "reason": str(exc)}, ensure_ascii=False))
        raise SystemExit(2) from exc
    except FileNotFoundError as exc:
        print(json.dumps({"ok": False, "reason": f"credentials missing: {exc}"}, ensure_ascii=False))
        raise SystemExit(2) from exc
