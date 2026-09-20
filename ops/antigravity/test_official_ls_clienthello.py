#!/usr/bin/env python3
"""Tests for the official language-server ClientHello evidence parser."""
from __future__ import annotations

import struct
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import official_ls_clienthello as parser  # noqa: E402


def _ext(extension_type: int, body: bytes) -> bytes:
    return struct.pack(">HH", extension_type, len(body)) + body


def _vector(length_bytes: int, body: bytes) -> bytes:
    return len(body).to_bytes(length_bytes, "big") + body


def _hello() -> bytes:
    # The values mirror the shape of the local LS capture while keeping all
    # payloads synthetic.  The parser must not retain random/key-share bytes.
    sni = _vector(2, b"\x00" + _vector(2, b"antigravity-unleash.goog"))
    groups = _vector(2, struct.pack(">HH", 4588, 29))
    points = _vector(1, b"\x00")
    sigs = _vector(2, struct.pack(">HH", 1027, 2052))
    alpn = _vector(2, _vector(1, b"h2") + _vector(1, b"http/1.1"))
    versions = _vector(1, struct.pack(">HH", 772, 771))
    key = _vector(2, struct.pack(">H", 4588) + _vector(2, b"key-share-payload"))
    extensions = b"".join(
        (
            _ext(0, sni),
            _ext(10, groups),
            _ext(11, points),
            _ext(13, sigs),
            _ext(16, alpn),
            _ext(43, versions),
            _ext(51, key),
        )
    )
    body = b"".join(
        (
            struct.pack(">H", 771),
            b"r" * 32,
            _vector(1, b""),
            _vector(2, struct.pack(">HHH", 49195, 4865, 4866)),
            _vector(1, b"\x00"),
            _vector(2, extensions),
        )
    )
    handshake = b"\x01" + len(body).to_bytes(3, "big") + body
    return b"\x16\x03\x01" + len(handshake).to_bytes(2, "big") + handshake


class OfficialLsParserTests(unittest.TestCase):
    def test_parses_concatenated_client_hellos(self):
        report = parser.build_report(_hello() + _hello(), "test")
        self.assertEqual(report["sample_count"], 2)
        sample = report["samples"][0]
        self.assertEqual(sample["server_name"], "antigravity-unleash.goog")
        self.assertEqual(sample["alpn_protocols"], ["h2", "http/1.1"])
        self.assertEqual(sample["supported_versions"], [772, 771])
        self.assertEqual(sample["key_share_groups"], [4588])
        self.assertTrue(report["stability"]["ja3_stable"])
        self.assertNotIn("random", sample)
        self.assertNotIn("key-share-payload", repr(report))

    def test_ja3_strips_grease(self):
        raw, digest = parser.compute_ja3(771, [0x0A0A, 4865], [0x1A1A, 0], [0x2A2A, 29], [0])
        self.assertEqual(raw, "771,4865,0,29,0")
        self.assertEqual(len(digest), 32)

    def test_rejects_truncated_capture(self):
        with self.assertRaises(parser.ParseError):
            parser.parse_tls_records(_hello()[:-1])

    def test_rejects_non_tls_capture(self):
        with self.assertRaises(parser.ParseError):
            parser.parse_tls_records(b"not-a-tls-record")

    def test_profile_comparison_reports_drift(self):
        report = parser.build_report(_hello(), "test")
        profile = {
            "cipher_suites": report["samples"][0]["cipher_suites"],
            "curves": report["samples"][0]["supported_groups"],
            "point_formats": report["samples"][0]["point_formats"],
            "signature_algorithms": report["samples"][0]["signature_algorithms"],
            "alpn_protocols": report["samples"][0]["alpn_protocols"],
            "supported_versions": report["samples"][0]["supported_versions"],
            "key_share_groups": report["samples"][0]["key_share_groups"],
            "extensions": report["samples"][0]["extensions"],
            "observed": {"ja3_hash": report["samples"][0]["ja3_hash"]},
        }
        self.assertEqual(parser.compare_report_to_profile(report, profile), [])
        profile["alpn_protocols"] = ["http/1.1"]
        self.assertTrue(parser.compare_report_to_profile(report, profile))


if __name__ == "__main__":
    unittest.main()
