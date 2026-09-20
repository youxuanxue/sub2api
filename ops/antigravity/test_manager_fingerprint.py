import json
import tempfile
import unittest
from pathlib import Path

import manager_fingerprint as mf


class ManagerFingerprintTest(unittest.TestCase):
    def test_user_agent_and_headers_use_one_version(self):
        ua = mf.manager_user_agent("2.14.0")
        self.assertIn("Antigravity/2.14.0", ua)
        self.assertIn("Chrome/132.0.6834.160", ua)

    def test_tshark_profile_has_ja3_and_ready_status(self):
        tsv = "\t".join(["771", "4865,4866,4867", "0,11,16,43", "29,23", "0", "cloudcode-pa.googleapis.com"])
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "hello.tsv"
            output = root / "profile.json"
            source.write_text(tsv + "\n", encoding="utf-8")
            args = type("Args", (), {"tshark_tsv": str(source), "out": str(output), "manager_version": "2.14.0"})
            self.assertEqual(mf.cmd_profile_from_tsv(args), 0)
            profile = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(profile["observed"]["capture_status"], "real-clienthello-captured")
            self.assertTrue(profile["observed"]["ja3_hash"])
            self.assertEqual(mf.cmd_check(type("Args", (), {"profile": str(output)})()), 0)

    def test_pending_profile_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "pending.json"
            path.write_text(json.dumps({
                "name": "tk_canonical_antigravity_manager_chrome123",
                "cipher_suites": [],
                "extensions": [],
                "observed": {"capture_status": "pending-real-manager-clienthello"},
            }), encoding="utf-8")
            self.assertEqual(mf.cmd_check(type("Args", (), {"profile": str(path)})()), 1)

    def test_empty_tshark_capture_fails(self):
        with self.assertRaises(ValueError):
            mf.parse_tshark_tsv("")

    def test_openssl_samples_detect_randomized_order(self):
        log = """
<<< TLS 1.3 Handshake, ClientHello
TLS server extension \"unknown\" (id=2570), len=0
TLS server extension \"server name\" (id=0), len=4
TLS server extension \"supported versions\" (id=43), len=7
>>> TLS 1.3 Handshake
<<< TLS 1.3 Handshake, ClientHello
TLS server extension \"unknown\" (id=27242), len=0
TLS server extension \"supported versions\" (id=43), len=7
TLS server extension \"server name\" (id=0), len=4
>>> TLS 1.3 Handshake
"""
        summary = mf.summarize_openssl_capture(log, "4.3.0")
        self.assertEqual(summary["sample_count"], 2)
        self.assertFalse(summary["extension_order_stable"])
        self.assertFalse(summary["extension_set_stable"])
        self.assertTrue(summary["extension_set_without_grease_stable"])


if __name__ == "__main__":
    unittest.main()
