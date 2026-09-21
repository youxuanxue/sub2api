#!/usr/bin/env python3
"""Parse raw ClientHello bytes captured from the official Antigravity LS.

The local CONNECT capture used for this tool intentionally does not forward any
bytes upstream.  This parser accepts a concatenation of TLS records, extracts
ClientHello metadata, and emits a redacted JSON report.  Client randoms, key
share values, and other payload bytes are never written to the report.

This is an evidence collector, not a TLS impersonation profile generator.  A
Go language-server ClientHello is kept separate from the Electron UI version
and from the Antigravity-Manager Chrome emulation route.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import struct
import sys
from pathlib import Path
from typing import Any

SCHEMA_VERSION = 1
TLS_HANDSHAKE = 22
CLIENT_HELLO = 1
GREASE = frozenset(0x0A0A + 0x1010 * n for n in range(16))

EXTENSION_NAMES = {
    0: "server_name",
    10: "supported_groups",
    11: "ec_point_formats",
    13: "signature_algorithms",
    16: "alpn",
    21: "padding",
    43: "supported_versions",
    45: "psk_key_exchange_modes",
    51: "key_share",
}


class ParseError(ValueError):
    """The capture is not a complete, parseable TLS ClientHello stream."""


def _need(data: bytes, offset: int, size: int, what: str) -> None:
    if offset + size > len(data):
        raise ParseError(f"truncated {what}: need {size} bytes at offset {offset}")


def _u8(data: bytes, offset: int, what: str) -> tuple[int, int]:
    _need(data, offset, 1, what)
    return data[offset], offset + 1


def _u16(data: bytes, offset: int, what: str) -> tuple[int, int]:
    _need(data, offset, 2, what)
    return struct.unpack_from(">H", data, offset)[0], offset + 2


def _vector(data: bytes, offset: int, length_bytes: int, what: str) -> tuple[bytes, int]:
    length = 0
    for _ in range(length_bytes):
        value, offset = _u8(data, offset, what + " length")
        length = (length << 8) | value
    _need(data, offset, length, what)
    return data[offset : offset + length], offset + length


def _u16_list(data: bytes, what: str) -> list[int]:
    if len(data) % 2:
        raise ParseError(f"{what} has odd byte length")
    return [struct.unpack_from(">H", data, i)[0] for i in range(0, len(data), 2)]


def _strip_grease(values: list[int]) -> list[int]:
    return [value for value in values if value not in GREASE]


def _is_grease(value: int) -> bool:
    return value in GREASE


def compute_ja3(
    legacy_version: int,
    cipher_suites: list[int],
    extensions: list[int],
    supported_groups: list[int],
    point_formats: list[int],
) -> tuple[str, str]:
    raw = ",".join(
        (
            str(legacy_version),
            "-".join(str(value) for value in _strip_grease(cipher_suites)),
            "-".join(str(value) for value in _strip_grease(extensions)),
            "-".join(str(value) for value in _strip_grease(supported_groups)),
            "-".join(str(value) for value in point_formats),
        )
    )
    return raw, hashlib.md5(raw.encode("ascii")).hexdigest()


def _parse_extension(extension_type: int, body: bytes) -> dict[str, Any]:
    result: dict[str, Any] = {}
    if extension_type == 0:  # server_name
        names, offset = _vector(body, 0, 2, "server_name list")
        if offset != len(body):
            raise ParseError("trailing bytes in server_name extension")
        if names:
            name_type, offset = _u8(names, 0, "server name type")
            name, offset = _vector(names, offset, 2, "server name")
            if name_type == 0:
                # SNI is ASCII in the observed capture.  Avoid retaining raw
                # bytes and keep malformed test input printable.
                result["server_name"] = name.decode("ascii", "replace")
    elif extension_type == 10:  # supported_groups
        values, offset = _vector(body, 0, 2, "supported_groups")
        if offset != len(body):
            raise ParseError("trailing bytes in supported_groups extension")
        result["supported_groups"] = _u16_list(values, "supported_groups")
    elif extension_type == 11:  # ec_point_formats
        values, offset = _vector(body, 0, 1, "ec_point_formats")
        if offset != len(body):
            raise ParseError("trailing bytes in ec_point_formats extension")
        result["point_formats"] = list(values)
    elif extension_type == 13:  # signature_algorithms
        values, offset = _vector(body, 0, 2, "signature_algorithms")
        if offset != len(body):
            raise ParseError("trailing bytes in signature_algorithms extension")
        result["signature_algorithms"] = _u16_list(values, "signature_algorithms")
    elif extension_type == 16:  # ALPN
        values, offset = _vector(body, 0, 2, "ALPN list")
        if offset != len(body):
            raise ParseError("trailing bytes in ALPN extension")
        protocols: list[str] = []
        cursor = 0
        while cursor < len(values):
            item, cursor = _vector(values, cursor, 1, "ALPN protocol")
            protocols.append(item.decode("ascii", "replace"))
        result["alpn_protocols"] = protocols
    elif extension_type == 43:  # supported_versions
        values, offset = _vector(body, 0, 1, "supported_versions")
        if offset != len(body):
            raise ParseError("trailing bytes in supported_versions extension")
        result["supported_versions"] = _u16_list(values, "supported_versions")
    elif extension_type == 45:  # psk_key_exchange_modes
        values, offset = _vector(body, 0, 1, "psk_key_exchange_modes")
        if offset != len(body):
            raise ParseError("trailing bytes in psk_key_exchange_modes extension")
        result["psk_key_exchange_modes"] = list(values)
    elif extension_type == 51:  # key_share
        shares, offset = _vector(body, 0, 2, "key_share list")
        if offset != len(body):
            raise ParseError("trailing bytes in key_share extension")
        groups: list[int] = []
        cursor = 0
        while cursor < len(shares):
            group, cursor = _u16(shares, cursor, "key_share group")
            key, cursor = _vector(shares, cursor, 2, "key_share key")
            del key  # payload is intentionally not retained
            groups.append(group)
        result["key_share_groups"] = groups
    return result


def parse_client_hello(body: bytes, record_version: int) -> dict[str, Any]:
    offset = 0
    legacy_version, offset = _u16(body, offset, "legacy version")
    _need(body, offset, 32, "ClientHello random")
    offset += 32  # Never retain the random; it is per-connection and not a profile field.
    session_id, offset = _vector(body, offset, 1, "session id")
    del session_id
    cipher_bytes, offset = _vector(body, offset, 2, "cipher suites")
    cipher_suites = _u16_list(cipher_bytes, "cipher suites")
    compression_methods, offset = _vector(body, offset, 1, "compression methods")
    del compression_methods
    extensions_bytes, offset = _vector(body, offset, 2, "extensions")
    if offset != len(body):
        raise ParseError("trailing bytes after ClientHello")

    extensions: list[int] = []
    extension_payload_lengths: dict[str, int] = {}
    decoded: dict[str, Any] = {}
    cursor = 0
    while cursor < len(extensions_bytes):
        extension_type, cursor = _u16(extensions_bytes, cursor, "extension type")
        extension_body, cursor = _vector(extensions_bytes, cursor, 2, "extension body")
        extensions.append(extension_type)
        extension_payload_lengths[str(extension_type)] = len(extension_body)
        decoded.update(_parse_extension(extension_type, extension_body))

    supported_groups = decoded.get("supported_groups", [])
    point_formats = decoded.get("point_formats", [])
    ja3_raw, ja3_hash = compute_ja3(
        legacy_version, cipher_suites, extensions, supported_groups, point_formats
    )
    result: dict[str, Any] = {
        "record_version": record_version,
        "legacy_version": legacy_version,
        "cipher_suites": cipher_suites,
        "extensions": extensions,
        "extension_names": [EXTENSION_NAMES.get(value, f"unknown_{value}") for value in extensions],
        "extension_payload_lengths": extension_payload_lengths,
        "ja3_raw": ja3_raw,
        "ja3_hash": ja3_hash,
    }
    result.update(decoded)
    return result


def parse_tls_records(data: bytes) -> list[dict[str, Any]]:
    """Parse every ClientHello handshake in a concatenated TLS record stream."""
    samples: list[dict[str, Any]] = []
    offset = 0
    while offset < len(data):
        _need(data, offset, 5, "TLS record header")
        content_type, record_version, record_length = struct.unpack_from(">BHH", data, offset)
        offset += 5
        _need(data, offset, record_length, "TLS record payload")
        payload = data[offset : offset + record_length]
        offset += record_length
        if content_type != TLS_HANDSHAKE:
            continue
        cursor = 0
        while cursor < len(payload):
            message_type, cursor = _u8(payload, cursor, "handshake type")
            message_body, cursor = _vector(payload, cursor, 3, "handshake body")
            if message_type == CLIENT_HELLO:
                samples.append(parse_client_hello(message_body, record_version))
    if not samples:
        raise ParseError("capture contains no ClientHello handshake")
    return samples


def _stable(samples: list[dict[str, Any]], field: str) -> bool:
    return len({json.dumps(sample.get(field), sort_keys=True) for sample in samples}) == 1


def build_report(data: bytes, source: str, server_name: str | None = None) -> dict[str, Any]:
    samples = parse_tls_records(data)
    total_samples = len(samples)
    if server_name:
        wanted = server_name.strip().lower()
        samples = [sample for sample in samples if str(sample.get("server_name", "")).lower() == wanted]
        if not samples:
            raise ParseError(f"no ClientHello matched SNI {server_name!r}")
    report = {
        "schema_version": SCHEMA_VERSION,
        "source": source,
        "capture_status": "real-clienthello-captured",
        "sample_count": len(samples),
        "samples": samples,
        "stability": {
            "cipher_suite_order_stable": _stable(samples, "cipher_suites"),
            "extension_order_stable": _stable(samples, "extensions"),
            "extension_set_stable": len({frozenset(s["extensions"]) for s in samples}) == 1,
            "extension_set_without_grease_stable": len(
                {frozenset(_strip_grease(s["extensions"])) for s in samples}
            )
            == 1,
            "alpn_stable": _stable(samples, "alpn_protocols"),
            "supported_versions_stable": _stable(samples, "supported_versions"),
            "key_share_groups_stable": _stable(samples, "key_share_groups"),
            "ja3_stable": _stable(samples, "ja3_hash"),
        },
        "grease": {
            "cipher_suites": sorted({v for s in samples for v in s["cipher_suites"] if _is_grease(v)}),
            "extensions": sorted({v for s in samples for v in s["extensions"] if _is_grease(v)}),
            "supported_groups": sorted(
                {v for s in samples for v in s.get("supported_groups", []) if _is_grease(v)}
            ),
        },
        "note": "Randoms and key-share payloads are intentionally omitted; this report is for comparison, not replay.",
    }
    if server_name:
        report["sni_filter"] = server_name
        report["filtered_out_sample_count"] = total_samples - len(samples)
    return report


def compare_report_to_profile(report: dict[str, Any], profile: dict[str, Any]) -> list[str]:
    """Return evidence mismatches against a TokenKey TLS profile JSON."""
    errors: list[str] = []
    if report.get("capture_status") != "real-clienthello-captured":
        errors.append("capture_status is not real-clienthello-captured")
    if not report.get("samples"):
        errors.append("report has no samples")
        return errors
    stability = report.get("stability", {})
    for field in ("cipher_suite_order_stable", "extension_order_stable", "ja3_stable"):
        if stability.get(field) is not True:
            errors.append(f"{field} is not true")
    sample = report["samples"][0]
    mappings = {
        "cipher_suites": "cipher_suites",
        "supported_groups": "curves",
        "point_formats": "point_formats",
        "signature_algorithms": "signature_algorithms",
        "alpn_protocols": "alpn_protocols",
        "supported_versions": "supported_versions",
        "key_share_groups": "key_share_groups",
        "extensions": "extensions",
    }
    for sample_field, profile_field in mappings.items():
        if sample.get(sample_field, []) != profile.get(profile_field, []):
            errors.append(
                f"{profile_field} differs: capture={sample.get(sample_field, [])!r} "
                f"profile={profile.get(profile_field, [])!r}"
            )
    observed = profile.get("observed", {})
    if sample.get("ja3_hash") != observed.get("ja3_hash"):
        errors.append(
            f"ja3_hash differs: capture={sample.get('ja3_hash')!r} profile={observed.get('ja3_hash')!r}"
        )
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("capture", type=Path, help="concatenated raw TLS records")
    parser.add_argument("--out", type=Path, help="write JSON report instead of stdout")
    parser.add_argument("--source", default="official-antigravity-language-server-local-connect")
    parser.add_argument("--sni", help="only include ClientHello samples for this exact SNI")
    parser.add_argument("--check-profile", type=Path, help="compare the report with a TokenKey profile JSON")
    args = parser.parse_args(argv)
    try:
        report = build_report(args.capture.read_bytes(), args.source, args.sni)
    except (OSError, ParseError, ValueError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    if args.check_profile:
        try:
            profile = json.loads(args.check_profile.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            print(f"ERROR: cannot read profile: {exc}", file=sys.stderr)
            return 2
        mismatches = compare_report_to_profile(report, profile)
        if mismatches:
            print("DRIFT")
            for mismatch in mismatches:
                print(f"- {mismatch}")
            return 1
        print("MATCH: official LS ClientHello agrees with profile")
        return 0
    encoded = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.out:
        args.out.write_text(encoded, encoding="utf-8")
        print(f"report={args.out}")
    else:
        print(encoded, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
