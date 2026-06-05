#!/usr/bin/env python3
"""Normalize a Stage0 embed file for platform-independent drift detection.

Why this exists
---------------
build-cfn.sh / render-bootstrap.sh embed source files as `gzip|base64` blobs in
the CFN templates and the Lightsail launch script. The historical `--check`
drift gate *re-compressed* the sources and byte-compared against the committed
blob. That is NOT a stable comparison: the gzip DEFLATE stream is an
implementation/version artifact, not a canonical encoding. Measured on this
repo:

  * Apple gzip 475 and GNU gzip agree on small inputs (docker-compose.yml) but
    diverge on the larger bootstrap script -> build-cfn --check false-reds on
    macOS while CI (ubuntu) is green.
  * Even python's gzip is not stable: linux py3.12 vs py3.14 produce different
    bytes for the same input (different bundled zlib), same length.

So any "re-compress and compare bytes" gate is fragile by construction and
turns every macOS developer's pre-commit hook red, which erodes the
deterministic-gate discipline (people reach for --no-verify).

What this does
--------------
Replaces every base64 blob with `sha256` of its DECODED content (gunzip when the
payload carries the gzip magic, otherwise the raw bytes). Decompression is
universal and version-independent, so the normalized view is identical on every
platform. The drift gate then compares the *decoded payload* (the real
invariant: "does the committed blob decode to the current source?") instead of
the compressor's byte representation. Structural (non-blob) lines pass through
verbatim, so template-shape drift is still caught.

Handles both embed forms:
  * CFN:  `      Value: '<base64>'` inside `>>> NAME START/END` marker regions.
          A blob split across `NAME_PART1` / `NAME_PART2` regions (the SSM
          Standard-tier 2-slot bootstrap) is joined before decoding.
  * bash: `VAR_GZB64='<base64>'` / `VAR_B64='<base64>'` assignments (no markers,
          no split) in the generated launch script.

Decode failures are fatal (exit 2) — a blob that does not decode is drift we
must surface, never silently pass.
"""
from __future__ import annotations

import base64
import binascii
import gzip
import hashlib
import re
import sys

_START = re.compile(r">>>\s+(\S+)\s+START")
_END = re.compile(r">>>\s+(\S+)\s+END")
# CFN blob: a single-quoted pure-base64 Value (excludes `Value: !Sub '...'` etc.
# because those do not start with a quote+base64 run).
_CFN_VALUE = re.compile(r"^(\s*)Value:\s*'([A-Za-z0-9+/=]*)'\s*$")
# bash blob: NAME_GZB64='...' / NAME_B64='...' with a long pure-base64 payload.
_BASH_VALUE = re.compile(r"^([A-Za-z_][A-Za-z0-9_]*_(?:GZB64|B64))='([A-Za-z0-9+/=]{16,})'\s*$")


def _decoded_sha(b64: str, label: str) -> str:
    try:
        raw = base64.b64decode(b64, validate=True)
    except binascii.Error as exc:  # pragma: no cover - defensive
        sys.stderr.write(f"normalize-embeds: {label}: invalid base64: {exc}\n")
        sys.exit(2)
    if raw[:2] == b"\x1f\x8b":
        try:
            raw = gzip.decompress(raw)
        except (OSError, EOFError) as exc:
            sys.stderr.write(f"normalize-embeds: {label}: gunzip failed: {exc}\n")
            sys.exit(2)
    return hashlib.sha256(raw).hexdigest()


def _base_name(region: str) -> str:
    """Strip a trailing _PARTn so split blobs join under one logical name."""
    return re.sub(r"_PART\d+$", "", region)


def main() -> int:
    region: str | None = None
    parts: dict[str, list[str]] = {}
    out: list[str] = []

    for line in sys.stdin.read().splitlines():
        m = _START.search(line)
        if m:
            region = m.group(1)
            out.append(line)
            continue
        m = _END.search(line)
        if m:
            out.append(line)
            region = None
            continue

        if region is not None:
            mv = _CFN_VALUE.match(line)
            if mv:
                base = _base_name(region)
                parts.setdefault(base, []).append(mv.group(2))
                if region.endswith("_PART1"):
                    # First slot of a split blob: buffer, emit a stable marker so
                    # the line sequence stays identical between the two files.
                    out.append(f"{mv.group(1)}# embed[{region}]=buffered")
                else:
                    joined = "".join(parts.pop(base))
                    out.append(f"{mv.group(1)}# embed[{base}]=sha256:{_decoded_sha(joined, base)}")
                continue
            out.append(line)
            continue

        mb = _BASH_VALUE.match(line)
        if mb:
            out.append(f"{mb.group(1)}=# embed sha256:{_decoded_sha(mb.group(2), mb.group(1))}")
            continue
        out.append(line)

    if parts:
        sys.stderr.write(
            f"normalize-embeds: dangling split blob(s) with no _PART2: {sorted(parts)}\n"
        )
        return 2

    sys.stdout.write("\n".join(out) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
