#!/usr/bin/env python3
"""Capture and validate the Antigravity-Manager wire identity.

The Manager community route declares rquest Chrome123 emulation. TokenKey can
temporarily run a Chrome120 uTLS compatibility preset, but this tool will only
mark a profile production-ready after a passive capture contains a real
ClientHello. HTTP MITM logs and passive TLS captures are intentionally separate:
the former proves UA/headers, while the latter proves the originating TLS.

stdlib-only; no network access.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any

PROFILE_PREFIX = "tk_canonical_antigravity_manager_chrome"
SCHEMA_VERSION = 1
GREASE = {0x0A0A + 0x1010 * n for n in range(16)}
TSV_FIELDS = (
    "tls.handshake.version",
    "tls.handshake.ciphersuite",
    "tls.handshake.extension.type",
    "tls.handshake.extensions_supported_group",
    "tls.handshake.extensions_ec_point_format",
    "tls.handshake.extensions_server_name",
)


def parse_ints(value: str) -> list[int]:
    result: list[int] = []
    for token in value.replace(",", " ").split():
        token = token.strip()
        if not token:
            continue
        try:
            result.append(int(token, 0))
        except ValueError as exc:
            raise ValueError(f"invalid TLS integer {token!r}") from exc
    return result


def parse_tshark_tsv(text: str) -> dict[str, Any]:
    """Parse one tshark -T fields row; fail on missing ClientHello fields."""
    rows = [line for line in text.splitlines() if line.strip()]
    if not rows:
        raise ValueError("TLS TSV is empty; capture a real ClientHello first")
    values = rows[0].split("\t")
    if len(values) < len(TSV_FIELDS):
        raise ValueError(f"TLS TSV needs {len(TSV_FIELDS)} tab-separated fields")
    version = parse_ints(values[0])[0]
    ciphers = parse_ints(values[1])
    extensions = parse_ints(values[2])
    curves = parse_ints(values[3])
    point_formats = parse_ints(values[4])
    if not ciphers or not extensions:
        raise ValueError("TLS TSV lacks cipher suites or extensions")
    return {
        "version": version,
        "cipher_suites": ciphers,
        "extensions": extensions,
        "curves": curves,
        "point_formats": point_formats,
        "server_name": values[5].strip(),
    }


def compute_ja3(tls: dict[str, Any]) -> tuple[str, str]:
    def clean(values: list[int]) -> str:
        return "-".join(str(value) for value in values if value not in GREASE)

    raw = ",".join(
        [
            str(tls["version"]),
            clean(tls["cipher_suites"]),
            clean(tls["extensions"]),
            clean(tls["curves"]),
            "-".join(str(value) for value in tls["point_formats"]),
        ]
    )
    return raw, hashlib.md5(raw.encode("ascii")).hexdigest()


def is_grease(value: int) -> bool:
    return value in GREASE or (value & 0x0F0F) == 0x0A0A and (value >> 8) == (value & 0xFF)


def parse_openssl_extension_samples(text: str) -> list[list[int]]:
    """Extract extension order from `openssl s_server -tlsextdebug` output.

    OpenSSL prints the ClientHello extensions after parsing them. This avoids
    BPF permissions while retaining the client-originated order and GREASE
    values. It does not expose the full extension payload, so callers must
    treat the result as a family summary rather than a complete profile.
    """
    samples: list[list[int]] = []
    for block in text.split("<<< TLS 1.3 Handshake"):
        if "ClientHello" not in block:
            continue
        block = block.split(">>> TLS 1.3 Handshake", 1)[0]
        ids = [int(value) for value in re.findall(r"TLS server extension .*?id=(\d+)", block)]
        if ids:
            samples.append(ids)
    return samples


def summarize_openssl_capture(text: str, manager_version: str) -> dict[str, Any]:
    samples = parse_openssl_extension_samples(text)
    if not samples:
        raise ValueError("no ClientHello extension samples found in OpenSSL log")
    normalized = [tuple(value for value in sample if not is_grease(value)) for sample in samples]
    common = sorted(set(samples[0]).intersection(*map(set, samples)))
    return {
        "schema_version": SCHEMA_VERSION,
        "source": "local-rquest-openssl-tlsextdebug",
        "manager_version": manager_version,
        "manager_emulation": "Chrome123",
        "capture_status": "partial-clienthello-captured",
        "sample_count": len(samples),
        "extension_order_stable": len(set(normalized)) == 1,
        "extension_set_stable": all(set(sample) == set(samples[0]) for sample in samples),
        "extension_set_without_grease_stable": len({frozenset(sample) for sample in normalized}) == 1,
        "extension_set_without_grease": [value for value in common if not is_grease(value)],
        "grease_extension_values": sorted({value for sample in samples for value in sample if is_grease(value)}),
        "extension_orders": samples,
        "note": "ClientHello observed by a local TLS server; extension payloads and raw packet capture are not retained.",
    }


def manager_user_agent(version: str) -> str:
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:[-.][0-9A-Za-z]+)*", version):
        raise ValueError(f"invalid Manager version: {version!r}")
    return f"Antigravity/{version} (Macintosh; Intel Mac OS X 10_15_7) Chrome/132.0.6834.160 Electron/39.2.3"


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"cannot read JSON {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return value


def cmd_emit_headers(args: argparse.Namespace) -> int:
    print(json.dumps({
        "user_agent": manager_user_agent(args.version),
        "x-client-name": "antigravity",
        "x-client-version": args.version,
        "identity": {
            "x-machine-id": "stable per-account 16-byte hex SHA-256 prefix",
            "x-vscode-sessionid": "stable per-account-and-session SHA-256 material rendered as RFC4122 v4 UUID",
        },
    }, indent=2))
    return 0


def cmd_profile_from_tsv(args: argparse.Namespace) -> int:
    tls = parse_tshark_tsv(Path(args.tshark_tsv).read_text(encoding="utf-8"))
    ja3_raw, ja3_hash = compute_ja3(tls)
    profile = {
        "name": f"{PROFILE_PREFIX}{args.manager_version}",
        "description": "Captured Antigravity-Manager ClientHello; pair only with Manager UA and x-client headers.",
        "enable_grease": False,
        "shuffle_extensions": False,
        "cipher_suites": tls["cipher_suites"],
        "curves": tls["curves"],
        "point_formats": tls["point_formats"],
        "signature_algorithms": [],
        "alpn_protocols": ["h2", "http/1.1"],
        "supported_versions": [],
        "key_share_groups": [],
        "psk_modes": [],
        "extensions": tls["extensions"],
        "observed": {
            "source": "passive-manager-clienthello",
            "manager_emulation": "Chrome123",
            "manager_version": args.manager_version,
            "capture_status": "real-clienthello-captured",
            "ja3_raw": ja3_raw,
            "ja3_hash": ja3_hash,
            "server_name": tls["server_name"],
        },
    }
    Path(args.out).write_text(json.dumps(profile, indent=2) + "\n", encoding="utf-8")
    print(f"profile={args.out}\nja3_hash={ja3_hash}\ncapture_status=real-clienthello-captured")
    return 0


def cmd_check(args: argparse.Namespace) -> int:
    profile = load_json(Path(args.profile))
    errors: list[str] = []
    name = str(profile.get("name", ""))
    observed = profile.get("observed")
    if not name.startswith(PROFILE_PREFIX):
        errors.append(f"name must start with {PROFILE_PREFIX}")
    if not isinstance(observed, dict):
        errors.append("observed metadata is required")
    else:
        if observed.get("capture_status") != "real-clienthello-captured":
            errors.append("capture_status is not real-clienthello-captured")
        if not observed.get("ja3_hash"):
            errors.append("ja3_hash is required for a captured profile")
    for field in ("cipher_suites", "extensions"):
        if not profile.get(field):
            errors.append(f"{field} must contain captured values")
    if errors:
        print("NOT READY")
        for error in errors:
            print(f"- {error}")
        return 1
    print("READY: real Manager ClientHello profile")
    return 0


def cmd_analyze_openssl(args: argparse.Namespace) -> int:
    summary = summarize_openssl_capture(Path(args.log).read_text(encoding="utf-8"), args.manager_version)
    Path(args.out).write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(f"summary={args.out}")
    print(f"samples={summary['sample_count']} extension_order_stable={summary['extension_order_stable']} extension_set_stable={summary['extension_set_stable']}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    emit = sub.add_parser("emit-headers")
    emit.add_argument("--version", required=True)
    emit.set_defaults(func=cmd_emit_headers)
    make = sub.add_parser("profile-from-tsv")
    make.add_argument("--tshark-tsv", required=True)
    make.add_argument("--out", required=True)
    make.add_argument("--manager-version", required=True)
    make.set_defaults(func=cmd_profile_from_tsv)
    check = sub.add_parser("check")
    check.add_argument("--profile", required=True)
    check.set_defaults(func=cmd_check)
    analyze = sub.add_parser("analyze-openssl")
    analyze.add_argument("--log", required=True)
    analyze.add_argument("--out", required=True)
    analyze.add_argument("--manager-version", required=True)
    analyze.set_defaults(func=cmd_analyze_openssl)
    return parser


def main(argv: list[str] | None = None) -> int:
    try:
        args = build_parser().parse_args(argv)
        return args.func(args)
    except (OSError, ValueError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
