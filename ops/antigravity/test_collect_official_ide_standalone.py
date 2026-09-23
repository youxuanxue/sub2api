import struct
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import collect_official_ide_standalone as collector


def ext(kind: int, body: bytes) -> bytes:
    return struct.pack(">HH", kind, len(body)) + body


def vector(width: int, body: bytes) -> bytes:
    return len(body).to_bytes(width, "big") + body


def hello(sni: bytes = b"cloudcode-pa.googleapis.com") -> bytes:
    body = b"".join((
        struct.pack(">H", 771), b"x" * 32, vector(1, b""),
        vector(2, struct.pack(">HH", 49195, 4865)), vector(1, b"\0"),
        vector(2, b"".join((
            ext(0, vector(2, b"\0" + vector(2, sni))),
            ext(10, vector(2, struct.pack(">H", 29))),
            ext(11, vector(1, b"\0")),
            ext(13, vector(2, struct.pack(">H", 1027))),
            ext(16, vector(2, vector(1, b"h2"))),
            ext(43, vector(1, struct.pack(">H", 772))),
            ext(51, vector(2, struct.pack(">H", 29) + vector(2, b"key-share-payload"))),
        ))),
    ))
    handshake = b"\x01" + len(body).to_bytes(3, "big") + body
    return b"\x16\x03\x01" + len(handshake).to_bytes(2, "big") + handshake


class CollectorTests(unittest.TestCase):
    def test_parser_redacts_random_and_key_share(self):
        sample = collector.parse_records(hello())[0]
        self.assertEqual(sample["server_name"], "cloudcode-pa.googleapis.com")
        self.assertEqual(sample["alpn_protocols"], ["h2"])
        self.assertNotIn("random", repr(sample))
        self.assertNotIn("key-share-payload", repr(sample))

    def test_parser_rejects_truncation(self):
        with self.assertRaises(collector.ParseError):
            collector.parse_records(hello()[:-1])

    def test_http_sink_body_summary_has_no_secret_value(self):
        # Exercise the redaction contract without starting a server.
        raw = b'{"accessToken":"secret","metadata":{"ideVersion":"2.15.1"}}'
        headers = {"Authorization": "Bearer secret", "User-Agent": "antigravity/hub/2.15.1"}
        result = collector.summarize_http_request(headers, raw, "/v1internal:loadCodeAssist")
        self.assertTrue(result["authorization_present"])
        self.assertNotIn("secret", repr(result))

    def test_report_tls_rejects_malformed_record(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            (out / "clienthello-0001.bin").write_bytes(hello()[:-1])
            with self.assertRaises(collector.ParseError):
                collector.report_tls(out)


if __name__ == "__main__":
    unittest.main()
