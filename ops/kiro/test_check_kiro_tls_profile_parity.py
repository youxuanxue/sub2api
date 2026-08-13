#!/usr/bin/env python3
from __future__ import annotations

import copy
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import capture_kiro_fingerprint as capture  # noqa: E402
import check_kiro_tls_profile_parity as parity  # noqa: E402


class KiroTLSProfileParityTest(unittest.TestCase):
    def setUp(self) -> None:
        self.canonical = capture.load_committed_profile()
        assert self.canonical is not None

    def row(self) -> dict:
        return {"name": capture.KIRO_PROFILE_NAME, **capture.runtime_profile_projection(self.canonical)}

    def test_current_canonical_digest_is_stable(self) -> None:
        first = capture.runtime_profile_digest(self.canonical)
        second = capture.runtime_profile_digest(json.loads(json.dumps(self.canonical)))
        self.assertEqual(first, second)
        self.assertEqual(len(first), 64)

    def test_matching_snapshot_is_healthy(self) -> None:
        result = parity.compare_profiles(self.canonical, self.row())
        self.assertEqual(result["status"], "healthy")
        self.assertEqual(result["evidence_level"], "production_configured")
        self.assertEqual(result["canonical_digest"], result["database_digest"])

    def test_shuffled_extension_order_is_semantically_equal(self) -> None:
        row = self.row()
        row["extensions"] = list(reversed(row["extensions"]))
        result = parity.compare_profiles(self.canonical, row)
        self.assertEqual(result["status"], "healthy")

    def test_non_extension_array_order_change_is_drift(self) -> None:
        row = self.row()
        row["cipher_suites"] = list(reversed(row["cipher_suites"]))
        result = parity.compare_profiles(self.canonical, row)
        self.assertEqual(result["status"], "drift")
        self.assertIn("cipher_suites", result["field_diffs"])

    def test_any_field_change_is_drift(self) -> None:
        for field in capture.RUNTIME_PROFILE_FIELDS:
            with self.subTest(field=field):
                row = copy.deepcopy(self.row())
                if field in {"enable_grease", "shuffle_extensions"}:
                    row[field] = not row[field]
                elif row[field]:
                    row[field][0] = "h2" if field == "alpn_protocols" else int(row[field][0]) + 1
                else:
                    row[field] = ["h2"] if field == "alpn_protocols" else [1]
                self.assertEqual(parity.compare_profiles(self.canonical, row)["status"], "drift")

    def test_missing_row_is_explicit(self) -> None:
        result = parity.compare_profiles(self.canonical, None)
        self.assertEqual(result["status"], "missing")
        self.assertIsNone(result["database_digest"])

    def test_malformed_snapshot_is_observer_failure(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            bad = Path(td) / "bad.jsonl"
            bad.write_text("not-json\n", encoding="utf-8")
            proc = subprocess.run(
                [
                    sys.executable,
                    str(Path(parity.__file__)),
                    "--snapshot",
                    str(bad),
                ],
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(proc.returncode, 2)
        self.assertEqual(json.loads(proc.stdout)["status"], "observer_failed")


if __name__ == "__main__":
    unittest.main()
