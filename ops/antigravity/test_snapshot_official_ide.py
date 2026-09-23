import json
import struct
import tempfile
import unittest
from pathlib import Path

import snapshot_official_ide as snapshot
from test_official_ls_clienthello import _hello


class SnapshotTests(unittest.TestCase):
    def test_reads_member_at_asar_header_offset(self):
        content = b"const args = ['--subclient_type', 'hub'];"
        header = json.dumps({"files": {"dist": {"files": {
            "languageServer.js": {"offset": "0", "size": len(content)}
        }}}}).encode()
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "app.asar"
            path.write_bytes(struct.pack("<4I", 4, len(header) + 8, len(header) + 4, len(header)) + header + content)
            self.assertEqual(snapshot.read_asar_member(path, "dist/languageServer.js"), content)
            path.write_bytes(path.read_bytes()[:-1])
            with self.assertRaisesRegex(ValueError, "truncated ASAR member"):
                snapshot.read_asar_member(path, "dist/languageServer.js")

    def test_groups_captures_without_claiming_control_plane_is_cloudcode(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            (path / "clienthello-0001.bin").write_bytes(_hello())
            (path / "clienthello-0002.bin").write_bytes(_hello(b"daily-cloudcode-pa.googleapis.com"))
            report = snapshot.summarize_captures(path)
            for sni, group in report["by_sni"].items():
                self.assertEqual(group["samples"][0]["server_name"], sni)
                self.assertEqual(group["filtered_out_sample_count"], 1)
            self.assertNotIn("cloudcode-pa.googleapis.com", report["by_sni"])

    def test_rejects_bad_record_instead_of_issuing_partial_success(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            (path / "clienthello-0001.bin").write_bytes(_hello())
            (path / "clienthello-0002.bin").write_bytes(b"bad")
            with self.assertRaises(snapshot.tls.ParseError):
                snapshot.summarize_captures(path)


if __name__ == "__main__":
    unittest.main()
