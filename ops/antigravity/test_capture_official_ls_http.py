from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import capture_official_ls_http as sink  # noqa: E402


class OfficialLSHTTPSinkTests(unittest.TestCase):
    def test_content_length_is_bounded_and_non_negative(self) -> None:
        self.assertEqual(sink.bounded_content_length("-1"), 0)
        self.assertEqual(sink.bounded_content_length("bogus"), 0)
        self.assertEqual(sink.bounded_content_length(str(3 * 1024 * 1024)), 2 * 1024 * 1024)

    def test_summarize_body_handles_empty_input(self) -> None:
        self.assertEqual(sink.summarize_body(b""), {"bytes": 0, "json": False})

    def test_summarize_body_keeps_ide_metadata_only(self) -> None:
        raw = json.dumps(
            {
                "metadata": {"ideType": "ANTIGRAVITY", "ideVersion": "2.14.0"},
                "accessToken": "must-not-be-retained",
            }
        ).encode()
        result = sink.summarize_body(raw)
        self.assertEqual(result["ideType"], "ANTIGRAVITY")
        self.assertEqual(result["ideVersion"], "2.14.0")
        self.assertNotIn("must-not-be-retained", repr(result))


if __name__ == "__main__":
    unittest.main()
