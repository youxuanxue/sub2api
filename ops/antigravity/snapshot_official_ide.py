#!/usr/bin/env python3
"""Snapshot the installed macOS App and locally captured TLS evidence.

Does not launch the App, read its account state, or contact Google. HTTP identity
is deliberately not inferred from the package version or from TLS captures.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import plistlib
import struct
import subprocess
from datetime import datetime, timezone
from pathlib import Path

import official_ls_clienthello as tls


def read_asar_member(path: Path, member: str) -> bytes:
    with path.open("rb") as stream:
        prefix = stream.read(16)
        if len(prefix) != 16:
            raise ValueError("truncated ASAR header")
        _, header_size, _, json_size = struct.unpack("<4I", prefix)
        if not 0 < json_size <= header_size <= 32 * 1024 * 1024:
            raise ValueError("invalid ASAR header size")
        entry = json.loads(stream.read(json_size))
        for part in member.split("/"):
            entry = entry["files"][part]
        stream.seek(8 + header_size + int(entry["offset"]))
        data = stream.read(entry["size"])
        if len(data) != entry["size"]:
            raise ValueError("truncated ASAR member")
        return data


def summarize_captures(directory: Path) -> dict:
    records = sorted(directory.glob("clienthello-*.bin"))
    if not records:
        raise ValueError("no ClientHello records in capture directory")
    raw = b"".join(path.read_bytes() for path in records)
    samples = tls.parse_tls_records(raw)  # Fail closed on malformed input.
    return {
        "raw_records": [
            {"file": p.name, "sha256": hashlib.sha256(p.read_bytes()).hexdigest()}
            for p in records
        ],
        "by_sni": {
            sni: tls.build_report(raw, "official-ls-local-connect", sni)
            for sni in sorted({s["server_name"] for s in samples})
        },
    }


def snapshot(app: Path, capture_dir: Path | None) -> dict:
    contents = app / "Contents"
    info = plistlib.loads((contents / "Info.plist").read_bytes())
    electron = plistlib.loads(
        (contents / "Frameworks/Electron Framework.framework/Resources/Info.plist").read_bytes()
    )
    binary = contents / "Resources/bin/language_server"
    source = read_asar_member(contents / "Resources/app.asar", "dist/languageServer.js")
    result = {
        "schema_version": 1,
        "collected_at": datetime.now(timezone.utc).isoformat(),
        "package": {
            "app_version": info["CFBundleShortVersionString"],
            "bundle_id": info["CFBundleIdentifier"],
            "electron_version": electron["CFBundleVersion"],
            "ls_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
            "ls_build_info": subprocess.check_output(
                ["go", "version", "-m", str(binary)], text=True, timeout=15
            ).split(": ", 1)[-1].strip(),
            "ls_stamp": subprocess.check_output(
                [str(binary), "--stamp"], text=True, timeout=15
            ).strip(),
            "launch_source": "app.asar/dist/languageServer.js",
            "launch_source_sha256": hashlib.sha256(source).hexdigest(),
            "launch_identity_excerpt": source.decode().split("const args = [", 1)[1].split("]", 1)[0].strip(),
        },
        "http_identity": {"status": "not-captured"},
        "authenticated_image_validation": "not-performed",
    }
    if capture_dir:
        result["tls"] = summarize_captures(capture_dir)
        result["tls"]["provenance_note"] = (
            "Caller-supplied records. Bind their process/version and capture conditions "
            "in the experiment record; the snapshot does not attest their origin."
        )
    return result


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--app", type=Path, default=Path("/Applications/Antigravity.app"))
    ap.add_argument("--capture-dir", type=Path)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    try:
        report = snapshot(args.app, args.capture_dir)
        args.out.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")
    except (OSError, ValueError, KeyError, IndexError, subprocess.SubprocessError) as exc:
        ap.exit(2, f"snapshot failed: {exc}\n")
    print(f"snapshot={args.out} app={report['package']['app_version']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
