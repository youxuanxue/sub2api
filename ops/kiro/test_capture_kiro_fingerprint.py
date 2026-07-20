#!/usr/bin/env python3
"""Unit tests for capture_kiro_fingerprint.py (the deterministic kiro fingerprint
diff engine). stdlib unittest only — run with:

  python3 ops/kiro/test_capture_kiro_fingerprint.py
  # or: python3 -m pytest ops/kiro/test_capture_kiro_fingerprint.py
"""
from __future__ import annotations

import hashlib
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import capture_kiro_fingerprint as eng  # noqa: E402

GREASE_CIPHER = 0x1A1A
GREASE_EXT = 0x0A0A
GREASE_CURVE = 0x2A2A


class JA3Tests(unittest.TestCase):
    def test_strips_grease_and_formats(self):
        ja3_raw, ja3_md5 = eng.compute_ja3(
            version=771,
            ciphers=[GREASE_CIPHER, 4865, 4866],
            extensions=[GREASE_EXT, 0, 23],
            curves=[GREASE_CURVE, 29, 23],
            point_formats=[0],
        )
        self.assertEqual(ja3_raw, "771,4865-4866,0-23,29-23,0")
        # GREASE decimal values must not survive.
        self.assertNotIn(str(GREASE_CIPHER), ja3_raw)
        self.assertNotIn(str(GREASE_EXT), ja3_raw)
        self.assertEqual(ja3_md5, hashlib.md5(ja3_raw.encode("ascii")).hexdigest())

    def test_empty_lists_render_empty_fields(self):
        ja3_raw, _ = eng.compute_ja3(771, [], [], [], [])
        self.assertEqual(ja3_raw, "771,,,,")


class UserAgentTests(unittest.TestCase):
    SYNTH = {
        "streaming_sdk_version": "1.0.34",
        "runtime_sdk_version": "1.0.0",
        "kiro_ide_version": "0.11.107",
        "system_version": "darwin#24.0.0",
        "node_version": "22.22.0",
    }

    def test_expected_user_agent_matches_go_builder_shape(self):
        # Mirrors kiro.BuildUserAgent(apiName=codewhispererstreaming, mode=m/E).
        self.assertEqual(
            eng.expected_user_agent(self.SYNTH),
            "aws-sdk-js/1.0.34 ua/2.1 os/darwin#24.0.0 lang/js md/nodejs#22.22.0 "
            "api/codewhispererstreaming#1.0.34 m/E KiroIDE-0.11.107",
        )

    def test_expected_amz_user_agent_matches_go_builder_shape(self):
        self.assertEqual(
            eng.expected_amz_user_agent(self.SYNTH),
            "aws-sdk-js/1.0.34 KiroIDE-0.11.107",
        )

    def test_live_constants_load_and_render(self):
        # Locks the engine against the real repo constants file (drift gate): the
        # rebuilt UA must contain each constant the Go builder weaves in.
        consts = eng.load_kiro_constants()
        ua = eng.expected_user_agent(consts)
        for key in ("streaming_sdk_version", "system_version", "node_version", "kiro_ide_version"):
            self.assertIn(consts[key], ua, f"{key} missing from rebuilt UA")
        self.assertTrue(ua.startswith("aws-sdk-js/"))
        self.assertIn("api/codewhispererstreaming#", ua)

    def test_committed_expected_http_matches_live_constants(self):
        profile = eng.load_committed_profile()
        self.assertIsNotNone(profile)
        self.assertIsNone(eng.validate_profile_provenance(profile))

        consts = eng.load_kiro_constants()
        expected_http = profile["expected_http"]
        expected = {
            "kiro_ide_version": consts["kiro_ide_version"],
            "streaming_sdk_version": consts["streaming_sdk_version"],
            "node_version": consts["node_version"],
            "system_version": consts["system_version"],
            "user_agent": eng.expected_user_agent(consts),
            "x_amz_user_agent": eng.expected_amz_user_agent(consts),
            "source": "repo-constants",
        }
        self.assertEqual(expected_http, expected)


class TsharkParseTests(unittest.TestCase):
    def test_parses_hex_and_decimal_aggregated_cells(self):
        header = "\t".join(eng.TSHARK_FIELDS)
        row = "\t".join(
            [
                "0x0303",            # version
                "0x1301,0x1302",     # ciphers
                "0,23,65281",        # extensions
                "0x001d,0x0017",     # supported_group
                "0",                 # ec_point_format
                "0x0403,0x0804",     # sig_hash_alg
                "h2,http/1.1",       # alpn_str
                "0x0304,0x0303",     # supported_version
                "0x001d",            # key_share_group
                "1",                 # psk_ke_modes
                "runtime.us-east-1.kiro.dev",
            ]
        )
        fields = eng.parse_tshark_tsv(header + "\n" + row + "\n")
        self.assertEqual(fields["version"], 771)
        self.assertEqual(fields["ciphers"], [0x1301, 0x1302])
        self.assertEqual(fields["extensions"], [0, 23, 65281])
        self.assertEqual(fields["curves"], [0x1D, 0x17])
        self.assertEqual(fields["alpn_protocols"], ["h2", "http/1.1"])
        self.assertEqual(fields["supported_versions"], [0x0304, 0x0303])
        self.assertEqual(fields["server_name"], "runtime.us-east-1.kiro.dev")

    def test_raises_without_data_row(self):
        with self.assertRaises(ValueError):
            eng.parse_tshark_tsv("\t".join(eng.TSHARK_FIELDS) + "\n")


class ProfileAndDiffTests(unittest.TestCase):
    def _fields(self):
        return {
            "version": 771,
            "ciphers": [GREASE_CIPHER, 4865, 4866, 4867],
            "extensions": [GREASE_EXT, 0, 23, 10],
            "curves": [GREASE_CURVE, 29, 23],
            "point_formats": [0],
            "signature_algorithms": [0x0403, 0x0804],
            "alpn_protocols": ["http/1.1"],
            "supported_versions": [0x0304, 0x0303],
            "key_share_groups": [29],
            "psk_modes": [1],
        }

    def test_build_profile_strips_grease_and_records_flag(self):
        expected_http = {
            "kiro_ide_version": "0.11.107",
            "streaming_sdk_version": "1.0.34",
            "node_version": "22.22.0",
            "system_version": "darwin#24.0.0",
            "user_agent": "synthetic-ua",
            "x_amz_user_agent": "synthetic-amz-ua",
        }
        prof = eng.build_canonical_profile(self._fields(), expected_http, "passive-pcap:test")
        self.assertEqual(prof["name"], eng.KIRO_PROFILE_NAME)
        self.assertTrue(prof["enable_grease"])  # GREASE was present in capture
        self.assertNotIn(GREASE_CIPHER, prof["cipher_suites"])
        self.assertNotIn(GREASE_EXT, prof["extensions"])
        self.assertEqual(prof["cipher_suites"], [4865, 4866, 4867])
        self.assertIn("ja3_hash", prof["observed"])
        self.assertEqual(prof["observed"]["source"], "passive-pcap:test")
        self.assertNotIn("user_agent", prof["observed"])
        self.assertEqual(prof["expected_http"]["user_agent"], "synthetic-ua")
        self.assertEqual(prof["expected_http"]["source"], "repo-constants")
        self.assertIsNone(eng.validate_profile_provenance(prof))

    def test_rejects_legacy_mixed_profile_provenance(self):
        legacy = {
            "observed": {
                "ja3_raw": "771,,,,",
                "ja3_hash": "a" * 32,
                "server_name": "runtime.us-east-1.kiro.dev",
                "user_agent": "constant-derived-but-mislabeled",
                "source": "passive-pcap",
            },
            "expected_http": {
                "kiro_ide_version": "1.0.165",
                "streaming_sdk_version": "1.0.34",
                "node_version": "22.22.0",
                "system_version": "darwin#24.0.0",
                "user_agent": "expected-ua",
                "x_amz_user_agent": "expected-amz-ua",
                "source": "repo-constants",
            },
        }
        error = eng.validate_profile_provenance(legacy)
        self.assertIsNotNone(error)
        self.assertIn("non-pcap HTTP fields", error)

    def test_diff_first_capture_is_non_actionable(self):
        prof = eng.build_canonical_profile(self._fields(), {}, "passive-pcap:test")
        bundle = {"tls": {"ja3_hash": prof["observed"]["ja3_hash"]}}
        rows = eng.diff_bundle(bundle, committed=None)
        ja3_row = next(r for r in rows if r.field == "tls.ja3_hash")
        self.assertEqual(ja3_row.status, "missing_tokenkey")
        self.assertFalse(eng.has_actionable_mismatch(rows))

    def test_diff_detects_ja3_drift_against_committed(self):
        committed = {"observed": {"ja3_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
        bundle = {"tls": {"ja3_hash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
        rows = eng.diff_bundle(bundle, committed)
        self.assertTrue(eng.has_actionable_mismatch(rows))

    def test_diff_matches_ja3(self):
        committed = {"observed": {"ja3_hash": "cccccccccccccccccccccccccccccccc"}}
        bundle = {"tls": {"ja3_hash": "cccccccccccccccccccccccccccccccc"}}
        rows = eng.diff_bundle(bundle, committed)
        self.assertFalse(eng.has_actionable_mismatch(rows))
        statuses = {r.field: r.status for r in rows}
        self.assertEqual(statuses["tls.ja3_hash"], "match")


if __name__ == "__main__":
    unittest.main()
