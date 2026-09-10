#!/usr/bin/env python3
"""Bounded, integrity-checked stdout transport through SSM's inline output."""
from __future__ import annotations

import argparse
import base64
import binascii
import gzip
import hashlib
import io
import subprocess
import sys
import tempfile


PREFIX = "TK_PROBE_GZIP_V1"
MAX_INLINE_BYTES = 23_000
MAX_OUTPUT_BYTES = 16 * 1024 * 1024
TRUNCATED = "--output truncated--"


class TransportError(ValueError):
    pass


def encode(data: bytes) -> str:
    if len(data) > MAX_OUTPUT_BYTES:
        raise TransportError("probe output exceeds decoded byte limit")
    compressed = base64.b64encode(gzip.compress(data, mtime=0)).decode("ascii")
    frame = f"{PREFIX} {len(data)} {hashlib.sha256(data).hexdigest()} {compressed}"
    if len(frame) + 1 > MAX_INLINE_BYTES:
        raise TransportError("compressed probe output exceeds SSM inline limit")
    return frame


def decode(frame: str) -> bytes:
    if TRUNCATED in frame:
        raise TransportError("SSM truncated probe output")
    if len(frame) > MAX_INLINE_BYTES:
        raise TransportError("probe frame exceeds inline byte limit")
    parts = frame.strip().split()
    if len(parts) != 4 or parts[0] != PREFIX:
        raise TransportError("missing or malformed probe transport frame")
    try:
        length = int(parts[1])
    except ValueError as exc:
        raise TransportError("invalid probe output length") from exc
    if not 0 <= length <= MAX_OUTPUT_BYTES:
        raise TransportError("probe output exceeds decoded byte limit")
    try:
        packed = base64.b64decode(parts[3], validate=True)
        with gzip.GzipFile(fileobj=io.BytesIO(packed)) as source:
            data = source.read(length + 1)
    except (binascii.Error, OSError, EOFError) as exc:
        raise TransportError("corrupt compressed probe output") from exc
    if len(data) != length or hashlib.sha256(data).hexdigest() != parts[2]:
        raise TransportError("probe output length or checksum mismatch")
    return data


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("encode", "decode"))
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    try:
        if args.mode == "decode":
            frame = sys.stdin.buffer.read(MAX_INLINE_BYTES + 1).decode("ascii")
            sys.stdout.buffer.write(decode(frame))
            return 0
        command = args.command
        if command and command[0] == "--":
            command = command[1:]
        if not command:
            parser.error("encode requires a command")
        # Spool stdout rather than holding an unbounded child response in RAM.
        # stderr and the exit code retain the probe's original failure semantics.
        with tempfile.TemporaryFile() as output:
            result = subprocess.run(command, stdout=output, check=False)
            if result.returncode:
                return result.returncode if result.returncode > 0 else 128 - result.returncode
            output.seek(0)
            print(encode(output.read(MAX_OUTPUT_BYTES + 1)))
        return 0
    except (TransportError, OSError, UnicodeError) as exc:
        print(f"probe-output-transport: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
