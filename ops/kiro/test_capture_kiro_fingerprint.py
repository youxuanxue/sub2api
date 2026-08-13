#!/usr/bin/env python3
"""Tests for the real Kiro CLI evidence engine."""
from __future__ import annotations

import hashlib
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import capture_kiro_fingerprint as eng  # noqa: E402


BASE_TLS = {
    "server_name": "codewhisperer.us-east-1.amazonaws.com",
    "version": 771,
    "cipher_suites": [4866, 4865, 4867, 49196, 49195, 52393, 49200, 49199, 52392, 255],
    "extensions": [45, 13, 10, 43, 11, 51, 5, 35, 0, 23],
    "curves": [29, 23, 24],
    "point_formats": [0],
    "signature_algorithms": [1283, 1027, 2055, 2054, 2053, 2052, 1537, 1281, 1025],
    "alpn_protocols": [],
    "supported_versions": [772, 771],
    "key_share_groups": [29],
    "psk_modes": [1],
}
HTTP = {
    "host": "codewhisperer.us-east-1.amazonaws.com",
    "path": "/",
    "method": "POST",
    "headers": {
        "user-agent": "aws-sdk-rust/1.3.10 ua/2.1 api/codewhispererruntime/0.1.10231 os/macos lang/rust/1.92.0 md/appVersion-2.18.0 app/AmazonQ-For-CLI",
        "x-amz-user-agent": "aws-sdk-rust/1.3.10 ua/2.1 api/codewhispererruntime/0.1.10231 os/macos lang/rust/1.92.0 m/F app/AmazonQ-For-CLI",
        "x-amz-target": "AmazonCodeWhispererService.GenerateCompletions",
        "content-type": "application/x-amz-json-1.0",
    },
    "body_keys": ["fileContext", "maxResults"],
    "origin": "",
    "model_id": "",
    "chat_trigger_type": "",
    "status_code": 200,
    "success": True,
}


def write_jsonl(path: Path, records: list[dict]) -> None:
    path.write_text("".join(json.dumps(record) + "\n" for record in records), encoding="utf-8")


def tls_samples() -> list[dict]:
    orders = [
        [45, 13, 10, 43, 11, 51, 5, 35, 0, 23],
        [51, 35, 5, 23, 0, 10, 43, 45, 13, 11],
        [13, 45, 35, 10, 0, 11, 43, 23, 51, 5],
    ]
    return [{**BASE_TLS, "extensions": order} for order in orders]


def auth_files(root: Path, *, method: str = "IdC", provider: str = "Enterprise", account_type: str = "BuilderId") -> tuple[Path, Path]:
    cache = root / "auth.json"
    cache.write_text(json.dumps({"authMethod": method, "provider": provider, "region": "us-east-1", "accessToken": "must-not-be-read"}), encoding="utf-8")
    whoami_file = root / "whoami.txt"
    whoami_file.write_text(json.dumps({"account_type": account_type, "region": "us-east-1"}) + "\n", encoding="utf-8")
    return cache, whoami_file


def complete_bundle(root: Path) -> dict:
    tls_path, http_path = root / "tls.jsonl", root / "http.jsonl"
    write_jsonl(tls_path, tls_samples())
    write_jsonl(http_path, [HTTP])
    cache, whoami = auth_files(root)
    http = eng.build_http_lane(http_path)
    return eng.assemble_evidence_bundle(
        [eng.build_tls_lane(tls_path), http, eng.build_protocol_lane(http), eng.build_auth_lane(cache, whoami)],
        kiro_cli_version="2.18.0",
    )


class JA3Tests(unittest.TestCase):
    def test_strips_grease_but_preserves_observed_order(self):
        raw, digest = eng.compute_ja3(771, [0x1A1A, 4865], [0x0A0A, 23, 0], [0x2A2A, 29], [0])
        self.assertEqual(raw, "771,4865,23-0,29,0")
        self.assertEqual(digest, hashlib.md5(raw.encode("ascii")).hexdigest())


class TLSLaneTests(unittest.TestCase):
    def test_requires_three_semantically_stable_permuted_samples(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tls.jsonl"
            write_jsonl(path, tls_samples())
            lane = eng.build_tls_lane(path)
        self.assertTrue(lane.valid)
        self.assertEqual(lane.source, "real-cli-mitm-clienthello")
        self.assertTrue(lane.observed["shuffle_extensions"])
        self.assertEqual(lane.observed["extensions"], sorted(BASE_TLS["extensions"]))
        self.assertEqual(len({sample["ja3_hash"] for sample in lane.observed["samples"]}), 3)

    def test_fixed_order_is_incomplete_not_false_cli_baseline(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tls.jsonl"
            write_jsonl(path, [BASE_TLS, BASE_TLS, BASE_TLS])
            lane = eng.build_tls_lane(path)
        self.assertTrue(lane.valid)
        self.assertIn("permutation", lane.error)

    def test_semantic_variance_is_invalid(self):
        records = tls_samples()
        records[2] = {**records[2], "cipher_suites": [4865]}
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tls.jsonl"
            write_jsonl(path, records)
            lane = eng.build_tls_lane(path)
        self.assertFalse(lane.valid)
        self.assertIn("vary", lane.error)

    def test_missing_is_not_observed(self):
        self.assertEqual(eng.build_tls_lane(None).source, eng.NOT_OBSERVED)


class HTTPLaneTests(unittest.TestCase):
    def test_requires_a_successful_real_response(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "http.jsonl"
            write_jsonl(path, [{**HTTP, "status_code": 403, "success": False}])
            lane = eng.build_http_lane(path)
        self.assertTrue(lane.valid)
        self.assertIn("no successful response", lane.error)

    def test_rejects_secret_fields_or_values(self):
        for record in ({**HTTP, "authorization": "Bearer secret"}, {**HTTP, "path": "arn:aws:secret"}):
            with self.subTest(record=record):
                with tempfile.TemporaryDirectory() as tmp:
                    path = Path(tmp) / "http.jsonl"
                    write_jsonl(path, [record])
                    lane = eng.build_http_lane(path)
                self.assertFalse(lane.valid)

    def test_protocol_is_derived_only_from_http_success(self):
        lane = eng.EvidenceLane("http", {"successful_count": 1, "records": [HTTP]}, "real-cli-mitm-http", True)
        protocol = eng.build_protocol_lane(lane)
        self.assertEqual(protocol.source, "real-cli-mitm-http")
        self.assertEqual(protocol.observed["targets"], ["AmazonCodeWhispererService.GenerateCompletions"])
        self.assertEqual(protocol.observed["body_keys"], ["fileContext", "maxResults"])


class AuthLaneTests(unittest.TestCase):
    def test_combines_matching_whoami_and_cache_metadata(self):
        with tempfile.TemporaryDirectory() as tmp:
            cache, whoami = auth_files(Path(tmp))
            lane = eng.build_auth_lane(cache, whoami)
        self.assertTrue(lane.valid)
        self.assertEqual(lane.observed, {"cohort": "builder_id", "region": "us-east-1"})
        self.assertNotIn("token", json.dumps(lane.observed).lower())

    def test_unknown_source_combination_is_invalid(self):
        with tempfile.TemporaryDirectory() as tmp:
            cache, whoami = auth_files(Path(tmp), method="Social", provider="BuilderID")
            lane = eng.build_auth_lane(cache, whoami)
        self.assertFalse(lane.valid)
        self.assertIn("unknown", lane.error)

    def test_caller_cannot_supply_a_cohort_label(self):
        parser = eng.build_parser()
        with self.assertRaises(SystemExit):
            parser.parse_args(["bundle", "--auth-cohort", "builder_id", "--kiro-cli-version", "2.18.0", "--out", "/tmp/x"])


class BundleAndProfileTests(unittest.TestCase):
    def test_all_four_real_lanes_and_version_are_required(self):
        with tempfile.TemporaryDirectory() as tmp:
            bundle = complete_bundle(Path(tmp))
        self.assertTrue(eng.evidence_complete(bundle))
        self.assertEqual(eng.compute_bundle_exit_code(bundle, None), eng.EXIT_COMPLETE)
        bundle["evidence_lanes"]["auth"]["source"] = eng.NOT_OBSERVED
        self.assertEqual(eng.compute_bundle_exit_code(bundle, None), eng.EXIT_INCOMPLETE)

    def test_candidate_is_cli_only_and_semantic_projection_ignores_permutation_order(self):
        with tempfile.TemporaryDirectory() as tmp:
            profile = eng.build_canonical_profile(complete_bundle(Path(tmp)))
        encoded = json.dumps(profile)
        self.assertEqual(profile["name"], "tk_canonical_kiro_cli")
        self.assertTrue(profile["shuffle_extensions"])
        self.assertIn("aws-sdk-rust", profile["observed"]["user_agent"])
        for retired in ("KiroIDE", "aws-sdk-js", "kiro_ide", "expected_http"):
            self.assertNotIn(retired, encoded)
        reordered = dict(profile)
        reordered["extensions"] = list(reversed(profile["extensions"]))
        self.assertEqual(eng.runtime_profile_digest(profile), eng.runtime_profile_digest(reordered))
        changed = dict(profile)
        changed["cipher_suites"] = list(reversed(profile["cipher_suites"]))
        self.assertNotEqual(eng.runtime_profile_digest(profile), eng.runtime_profile_digest(changed))

    def test_semantic_drift_not_sample_ja3_drift(self):
        with tempfile.TemporaryDirectory() as tmp:
            bundle = complete_bundle(Path(tmp))
            committed = eng.build_canonical_profile(bundle)
        committed["observed"]["samples"] = [{"ja3_hash": "different-observation"}]
        self.assertEqual(eng.compute_bundle_exit_code(bundle, committed), eng.EXIT_COMPLETE)
        committed["curves"] = [29]
        self.assertEqual(eng.compute_bundle_exit_code(bundle, committed), eng.EXIT_DRIFT)

    def test_emit_profile_rejects_incomplete_bundle(self):
        bundle = eng.assemble_evidence_bundle([], kiro_cli_version="2.18.0")
        with self.assertRaisesRegex(ValueError, "complete real CLI evidence"):
            eng.build_canonical_profile(bundle)

    def test_replay_lane_has_distinct_runtime_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tls.jsonl"
            write_jsonl(path, tls_samples())
            lane = eng.build_tls_lane(
                path,
                observed_source="tokenkey-utls-mitm-clienthello",
            )
        self.assertTrue(lane.valid)
        self.assertEqual(lane.source, "tokenkey-utls-mitm-clienthello")


if __name__ == "__main__":
    unittest.main()
