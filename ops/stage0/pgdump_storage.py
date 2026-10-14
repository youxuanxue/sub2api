#!/usr/bin/env python3
"""Host-only pgdump S3 identity shared by uploads and restore canaries.

Explicit Hetzner configuration never falls back to the AWS instance identity.
Secrets are file references, never command arguments or gateway environment.
"""
from __future__ import annotations

import argparse
import configparser
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import urllib.parse

CONFIG_PATH = Path("/etc/tokenkey/pgdump-storage.json")
TARGET_RE = re.compile(r"(?:prod|edge:[a-z][a-z0-9]{1,15})")
OBJECT_RE = re.compile(r"tokenkey-\d{8}T\d{6}Z\.sql\.gz")


class StorageError(RuntimeError):
    pass


def private_file(path: Path) -> bytes:
    try:
        info = path.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid()
                or stat.S_IMODE(info.st_mode) & 0o077):
            raise StorageError("storage file must be an owner-only regular file")
        return path.read_bytes()
    except OSError as exc:
        raise StorageError("cannot read private storage file") from exc


def location(raw: str, target: str | None = None) -> tuple[str, str]:
    if raw.strip() != raw or any(char in raw for char in "'\"`$\\"):
        raise StorageError("invalid pgdump S3 URI")
    parsed = urllib.parse.urlparse(raw)
    prefix = parsed.path.strip("/")
    if (parsed.scheme != "s3" or not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", parsed.netloc)
            or parsed.params or parsed.query or parsed.fragment):
        raise StorageError("pgdump URI must be s3://bucket/prefix")
    if not re.fullmatch(r"(?:prod|edge/[a-z][a-z0-9]{1,15})/pgdump", prefix):
        raise StorageError("invalid pgdump prefix")
    if target is not None:
        if TARGET_RE.fullmatch(target) is None:
            raise StorageError("invalid pgdump target")
        expected = "prod/pgdump" if target == "prod" else f"edge/{target.split(':', 1)[1]}/pgdump"
        if prefix != expected:
            raise StorageError("pgdump prefix does not match target")
    return parsed.netloc, prefix


class Storage:
    def __init__(self, *, target: str | None = None,
                 config_path: Path = CONFIG_PATH,
                 env_path: Path = Path("/var/lib/tokenkey/.env")):
        self.endpoint = None
        self.region = None
        self.credentials = None
        self.sse_key = None
        # lexists rejects broken symlinks rather than treating them as absent.
        if os.path.lexists(config_path):
            try:
                config = json.loads(private_file(config_path))
                if set(config) != {"schema_version", "target", "s3_uri", "endpoint", "region", "credentials_file", "sse_c_key_file"}:
                    raise StorageError("unsupported pgdump storage configuration")
                if type(config["schema_version"]) is not int or config["schema_version"] != 1 or TARGET_RE.fullmatch(config["target"]) is None:
                    raise StorageError("invalid pgdump storage identity")
                if target is not None and config["target"] != target:
                    raise StorageError("storage configuration belongs to another target")
                self.bucket, self.prefix = location(config["s3_uri"], config["target"])
                region = config["region"]
                if region not in {"fsn1", "nbg1", "hel1"} or config["endpoint"] != f"https://{region}.your-objectstorage.com":
                    raise StorageError("unsupported Hetzner endpoint")
                self.endpoint, self.region = config["endpoint"], region
                self.credentials = Path(config["credentials_file"])
                self.sse_key = Path(config["sse_c_key_file"])
                if not self.credentials.is_absolute() or not self.sse_key.is_absolute():
                    raise StorageError("absolute private storage paths required")
                credentials = configparser.ConfigParser(interpolation=None)
                credentials.read_string(private_file(self.credentials).decode())
                if credentials.sections() != ["pgdump"] or set(credentials["pgdump"]) != {"aws_access_key_id", "aws_secret_access_key"}:
                    raise StorageError("invalid pgdump credential profile")
                if not all(credentials["pgdump"].values()) or len(private_file(self.sse_key)) != 32:
                    raise StorageError("invalid storage credentials or encryption key")
            except (ValueError, TypeError, KeyError, configparser.Error) as exc:
                raise StorageError("invalid private pgdump storage configuration") from exc
        else:
            try:
                values = [line.partition("=")[2] for line in env_path.read_text().splitlines()
                          if line.startswith("TOKENKEY_PGDUMP_S3_URI=")]
            except OSError as exc:
                raise StorageError("cannot read host backup configuration") from exc
            if len(values) != 1 or not values[0]:
                raise StorageError("TOKENKEY_PGDUMP_S3_URI must appear exactly once and be non-empty")
            self.bucket, self.prefix = location(values[0], target)

    def command(self, args: list[str], *, encrypted: bool = False) -> list[str]:
        if self.endpoint is None:
            return ["aws", *args]
        # Remove ambient AWS identity, role, endpoint and CLI configuration.
        # /usr/bin/env receives only file paths; no secret appears in ps or SSM.
        unset = [item for key in os.environ if key.startswith("AWS_") for item in ("-u", key)]
        command = ["env", *unset,
                   f"AWS_SHARED_CREDENTIALS_FILE={self.credentials}",
                   "AWS_CONFIG_FILE=/dev/null", "AWS_PROFILE=pgdump",
                   "AWS_EC2_METADATA_DISABLED=true", "AWS_MAX_ATTEMPTS=2",
                   "AWS_REQUEST_CHECKSUM_CALCULATION=WHEN_REQUIRED",
                   "AWS_RESPONSE_CHECKSUM_VALIDATION=WHEN_REQUIRED",
                   "aws", *args, "--endpoint-url", self.endpoint, "--region", self.region]
        if encrypted:
            command += ["--sse-c", "AES256", "--sse-c-key", f"fileb://{self.sse_key}"]
        return command

    def upload(self, source: Path) -> str:
        if OBJECT_RE.fullmatch(source.name) is None or not source.is_file():
            raise StorageError("invalid local pgdump artifact")
        uri = f"s3://{self.bucket}/{self.prefix}/{source.name}"
        result = subprocess.run(self.command(["s3", "cp", "--only-show-errors", str(source), uri],
                                             encrypted=True), capture_output=True, timeout=900)
        if result.returncode:
            raise StorageError("off-box upload failed (local dump retained)")
        return uri


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=CONFIG_PATH)
    parser.add_argument("--target")
    parser.add_argument("--upload", type=Path)
    args = parser.parse_args()
    try:
        storage = Storage(target=args.target, config_path=args.config)
        if args.upload:
            print(f"pgdump: off-boxed -> {storage.upload(args.upload)}")
        else:
            print(json.dumps({"s3_uri": f"s3://{storage.bucket}/{storage.prefix}",
                              "endpoint": storage.endpoint, "encrypted": storage.sse_key is not None}))
    except (StorageError, OSError, subprocess.TimeoutExpired):
        print("pgdump: storage validation/upload failed; no AWS fallback; local dump retained", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
