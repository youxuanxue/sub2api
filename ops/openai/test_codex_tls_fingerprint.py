#!/usr/bin/env python3
"""Pure unit tests for the Codex TLS fingerprint engine (no codex, no network).

Fixture bundles are synthesized from the committed canonical profile so the
"aligned" fixture tracks the profile and the drift fixtures differ by exactly
one field. Raw capture bundles are never committed.
"""
from __future__ import annotations

import copy
import io
import json
import random
import socket
import ssl
import sys
import tempfile
import threading
import unittest
from contextlib import redirect_stdout
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import codex_tls_fingerprint as tls  # noqa: E402
import hpack_lite  # noqa: E402

PROFILE = json.loads(tls.CODEX_TLS_PROFILE_JSON.read_text(encoding="utf-8"))


def _record(lane: str, order: list[int], profile: dict = PROFILE, **overrides) -> dict:
    extensions = list(order)
    alpn = list(profile["alpn_protocols"])
    if lane == "wss":
        extensions = [ext for ext in extensions if ext != 16]
        alpn = []
    record = {
        "server_name": "chatgpt.com",
        "version": 771,
        "cipher_suites": list(profile["cipher_suites"]),
        "extensions": extensions,
        "curves": list(profile["curves"]),
        "point_formats": list(profile["point_formats"]),
        "signature_algorithms": list(profile["signature_algorithms"]),
        "alpn_protocols": alpn,
        "supported_versions": list(profile["supported_versions"]),
        "key_share_groups": list(profile["key_share_groups"]),
        "psk_modes": list(profile["psk_modes"]),
        "session_id_length": 32,
        "tls_version": "TLSv1.3",
    }
    if lane == "https":
        record["negotiated_alpn"] = "h2"
        record["http2"] = {
            "settings": [[2, 0], [4, 2097152], [5, 16384], [6, 16384]],
            "connection_window_update": 5177345,
            "requests": [{
                "method": "POST", "path": "/backend-api/codex/responses", "pseudo_order": "m,s,a,p",
                "header_names": ["authorization", "user-agent", "originator"],
                "header_values": {"user-agent": "codex_exec/0.167.0", "originator": "codex_exec"},
            }],
        }
    else:
        record["negotiated_alpn"] = ""
        record["http1"] = {
            "method": "GET", "path": "/backend-api/codex/responses", "http_version": "HTTP/1.1",
            "upgrade": "websocket", "header_names": ["host", "upgrade"], "header_values": {},
        }
    record.update(overrides)
    return record


def fixture_bundle(version: str = "0.167.0", profile: dict = PROFILE, mutate=None) -> dict:
    rng = random.Random(7)
    records = []
    for lane, count in (("https", 4), ("wss", 3)):
        for _ in range(count):
            order = list(profile["extensions"])
            rng.shuffle(order)
            records.append(_record(lane, order, profile))
    if mutate:
        for record in records:
            mutate(record)
    return tls.assemble_bundle(records, codex_cli_version=version, captured_at="2026-11-08T00:00:00Z")


class DecisionTest(unittest.TestCase):
    def test_aligned_fixture_matches_committed_profile(self) -> None:
        result = tls.decide(fixture_bundle(), PROFILE)
        self.assertEqual("aligned", result["verdict"], result)
        self.assertEqual(0, result["exit_code"])
        self.assertEqual([], result["runtime_diffs"])
        self.assertEqual([], result["wire_diffs"])
        self.assertEqual(PROFILE["observed"]["https"]["ja4"], result["observed"]["https"]["ja4"])
        self.assertEqual(PROFILE["observed"]["websocket"]["ja4"], result["observed"]["websocket"]["ja4"])
        self.assertEqual(PROFILE["observed"]["https"]["ja3_raw_sorted_extensions"],
                         result["observed"]["https"]["ja3_raw_sorted_extensions"])

    def test_version_only_keeps_digest_and_needs_no_migration(self) -> None:
        result = tls.decide(fixture_bundle(version="0.168.0"), PROFILE)
        self.assertEqual("version_only", result["verdict"])
        self.assertEqual(1, result["exit_code"])
        self.assertFalse(result["migration_required"])
        self.assertEqual(result["runtime_digest"]["committed"], result["runtime_digest"]["observed"])

    def test_alpn_removed_is_tls_drift_with_migration(self) -> None:
        def drop_alpn(record: dict) -> None:
            record["extensions"] = [ext for ext in record["extensions"] if ext != 16]
            record["alpn_protocols"] = []
            if "http2" in record:
                record["negotiated_alpn"] = "h2"
        result = tls.decide(fixture_bundle(mutate=drop_alpn), PROFILE)
        self.assertEqual("tls_drift", result["verdict"])
        self.assertEqual(1, result["exit_code"])
        self.assertTrue(result["migration_required"])
        fields = {diff["field"] for diff in result["runtime_diffs"]}
        self.assertEqual({"alpn_protocols", "extensions"}, fields)
        self.assertNotEqual(result["runtime_digest"]["committed"], result["runtime_digest"]["observed"])
        self.assertTrue(any("forward migration" in action for action in result["actions"]))

    def test_cipher_added_is_tls_drift(self) -> None:
        def add_cipher(record: dict) -> None:
            record["cipher_suites"] = record["cipher_suites"] + [49172]
        result = tls.decide(fixture_bundle(mutate=add_cipher), PROFILE)
        self.assertEqual("tls_drift", result["verdict"])
        self.assertEqual(["cipher_suites"], [diff["field"] for diff in result["runtime_diffs"]])

    def test_http2_settings_change_is_wire_drift_without_migration(self) -> None:
        def bump_window(record: dict) -> None:
            if "http2" in record:
                record["http2"]["connection_window_update"] = 15663105
        result = tls.decide(fixture_bundle(mutate=bump_window), PROFILE)
        self.assertEqual("tls_drift", result["verdict"])
        self.assertFalse(result["migration_required"])
        self.assertEqual(["https.http2_akamai"], [diff["field"] for diff in result["wire_diffs"]])

    def test_wss_gaining_alpn_is_wire_drift(self) -> None:
        def wss_alpn(record: dict) -> None:
            if "http1" in record:
                record["extensions"] = record["extensions"] + [16]
                record["alpn_protocols"] = ["http/1.1"]
        result = tls.decide(fixture_bundle(mutate=wss_alpn), PROFILE)
        self.assertEqual("tls_drift", result["verdict"])
        fields = {diff["field"] for diff in result["wire_diffs"]}
        self.assertIn("websocket.alpn_extension", fields)
        self.assertIn("websocket.ja4", fields)

    def test_grease_appearing_is_runtime_drift(self) -> None:
        def grease(record: dict) -> None:
            record["cipher_suites"] = [0x0A0A] + record["cipher_suites"]
        result = tls.decide(fixture_bundle(mutate=grease), PROFILE)
        self.assertEqual("tls_drift", result["verdict"])
        self.assertEqual(["enable_grease"], [diff["field"] for diff in result["runtime_diffs"]])

    def test_invalid_bundles_exit_two(self) -> None:
        too_few = fixture_bundle()
        too_few["records"] = [r for r in too_few["records"] if r["lane"] == "https"]
        self.assertEqual("invalid", tls.decide(too_few, PROFILE)["verdict"])
        mixed = fixture_bundle()
        mixed["records"][0]["cipher_suites"] = [4865]
        result = tls.decide(mixed, PROFILE)
        self.assertEqual(2, result["exit_code"])
        self.assertIn("varies", result["error"])
        self.assertEqual(2, tls.decide(fixture_bundle(), None)["exit_code"])
        wrong_sni = fixture_bundle(mutate=lambda r: r.update(server_name="example.com"))
        self.assertEqual("invalid", tls.decide(wrong_sni, PROFILE)["verdict"])

    def test_secret_bearing_records_are_rejected(self) -> None:
        def leak(record: dict) -> None:
            if "http2" in record:
                record["http2"]["requests"][0]["header_values"]["authorization"] = "Bearer abc"
        with self.assertRaises(tls.InvalidEvidence):
            fixture_bundle(mutate=leak)


class CanonicalProfileTest(unittest.TestCase):
    def test_emit_profile_round_trips_to_aligned(self) -> None:
        emitted = tls.build_canonical_profile(fixture_bundle(), PROFILE)
        for field in tls.RUNTIME_FIELDS:
            self.assertEqual(PROFILE[field], emitted[field], field)
        for label, path in tls.WIRE_FIELDS:
            self.assertEqual(tls._dig(PROFILE["observed"], path), tls._dig(emitted["observed"], path), label)
        self.assertEqual("aligned", tls.decide(fixture_bundle(), emitted)["verdict"])
        self.assertEqual(tls.kiro.runtime_profile_digest(PROFILE), emitted["observed"]["runtime_digest"])

    def test_emit_profile_refuses_unshuffled_evidence(self) -> None:
        bundle = fixture_bundle()
        for record in bundle["records"]:
            record["extensions"] = sorted(record["extensions"])
        with self.assertRaises(tls.InvalidEvidence):
            tls.build_canonical_profile(bundle, PROFILE)

    def test_cli_emit_then_check(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bundle_path = Path(tmp) / "b.json"
            bundle_path.write_text(json.dumps(fixture_bundle(version="0.168.0")), encoding="utf-8")
            out = Path(tmp) / "profile.json"
            with redirect_stdout(io.StringIO()):
                self.assertEqual(0, tls.main(["emit-profile", "--bundle", str(bundle_path), "--out", str(out)]))
                self.assertEqual(1, tls.main(["check", "--bundle", str(bundle_path)]))
                self.assertEqual(0, tls.main(["check", "--bundle", str(bundle_path), "--profile", str(out)]))
            self.assertEqual("0.168.0", json.loads(out.read_text())["observed"]["codex_cli_version"])


class MigrationParityTest(unittest.TestCase):
    def test_latest_seed_migration_matches_canonical_json_field_by_field(self) -> None:
        migration = tls.latest_seed_migration()
        self.assertIsNotNone(migration)
        row = tls.parse_seed_migration(migration.read_text(encoding="utf-8"))
        self.assertEqual(PROFILE["name"], row["name"])
        for field in tls.RUNTIME_FIELDS:
            self.assertEqual(PROFILE[field], row[field], f"{migration.name}: {field}")
        self.assertEqual(tls.kiro.runtime_profile_digest(PROFILE), tls.kiro.runtime_profile_digest(row))
        self.assertIn(PROFILE["observed"]["https"]["ja4"], row["description"])
        self.assertIn(PROFILE["observed"]["https"]["ja3_hash_sorted_extensions"], row["description"])

    def test_tk109_is_the_seed_and_matches(self) -> None:
        row = tls.parse_seed_migration((tls.MIGRATIONS_DIR / "tk_109_codex_cli_tls_profile.sql").read_text(encoding="utf-8"))
        self.assertEqual(set(tls._SQL_FIELDS), set(row))
        for field in tls.RUNTIME_FIELDS:
            self.assertEqual(PROFILE[field], row[field], field)

    def test_parser_detects_a_drifted_migration(self) -> None:
        sql = (tls.MIGRATIONS_DIR / "tk_109_codex_cli_tls_profile.sql").read_text(encoding="utf-8")
        drifted = tls.parse_seed_migration(sql.replace('\'["h2","http/1.1"]\'::jsonb', '\'["http/1.1"]\'::jsonb'))
        self.assertNotEqual(PROFILE["alpn_protocols"], drifted["alpn_protocols"])


def _client_hello_bytes(sni: str, ciphers: list[int], ext: list[tuple[int, bytes]], session_id: bytes) -> bytes:
    u16 = lambda v: v.to_bytes(2, "big")  # noqa: E731
    sni_ext = u16(len(sni) + 3) + b"\x00" + u16(len(sni)) + sni.encode()
    extensions = [(0, sni_ext)] + ext
    ext_bytes = b"".join(u16(t) + u16(len(d)) + d for t, d in extensions)
    body = (u16(0x0303) + b"\x11" * 32 + bytes([len(session_id)]) + session_id
            + u16(2 * len(ciphers)) + b"".join(u16(c) for c in ciphers) + b"\x01\x00"
            + u16(len(ext_bytes)) + ext_bytes)
    handshake = b"\x01" + len(body).to_bytes(3, "big") + body
    # Split across two records to exercise reassembly.
    half = len(handshake) // 2
    return b"".join(b"\x16\x03\x01" + u16(len(part)) + part for part in (handshake[:half], handshake[half:]))


class ClientHelloParseTest(unittest.TestCase):
    def test_parse_and_ja4(self) -> None:
        u16 = lambda v: v.to_bytes(2, "big")  # noqa: E731
        alpn = b"\x02h2\x08http/1.1"
        ext = [
            (16, u16(len(alpn)) + alpn),
            (10, u16(4) + u16(29) + u16(23)),
            (13, u16(4) + u16(1027) + u16(2052)),
            (43, b"\x04" + u16(772) + u16(771)),
            (45, b"\x01\x01"),
            (51, u16(8) + u16(29) + u16(4) + b"\xaa" * 4),
            (11, b"\x01\x00"),
        ]
        raw = _client_hello_bytes("chatgpt.com", [0x1301, 0x1302], ext, b"\x22" * 32)
        self.assertEqual(len(raw), tls.client_hello_length(raw))
        self.assertIsNone(tls.client_hello_length(raw[:12]))
        record = tls.parse_client_hello_bytes(raw)
        self.assertEqual("chatgpt.com", record["server_name"])
        self.assertEqual([4865, 4866], record["cipher_suites"])
        self.assertEqual([0, 16, 10, 13, 43, 45, 51, 11], record["extensions"])
        self.assertEqual(["h2", "http/1.1"], record["alpn_protocols"])
        self.assertEqual([29], record["key_share_groups"])
        self.assertEqual([772, 771], record["supported_versions"])
        self.assertEqual(32, record["session_id_length"])
        self.assertTrue(tls.compute_ja4(record).startswith("t13d0208h2_"))

    def test_ja4_matches_committed_hand_capture(self) -> None:
        https = _record("https", list(PROFILE["extensions"]))
        wss = _record("wss", list(PROFILE["extensions"]))
        self.assertEqual("t13d1011h2_61a7ad8aa9b6_0d308c48d2a3", tls.compute_ja4(https))
        self.assertEqual("t13d101000_61a7ad8aa9b6_0d308c48d2a3", tls.compute_ja4(wss))
        self.assertEqual("082240ccb778985f253b420029540a34", tls.ja3_sorted(https)[1])
        self.assertEqual("54294ea073f85fb8975a09107e5dd489", tls.ja3_sorted(wss)[1])


class HPACKTest(unittest.TestCase):
    def test_rfc7541_c4_huffman_requests(self) -> None:
        decoder = hpack_lite.Decoder()
        first = decoder.decode(bytes.fromhex("828684418cf1e3c2e5f23a6ba0ab90f4ff"))
        self.assertEqual([(":method", "GET"), (":scheme", "http"), (":path", "/"), (":authority", "www.example.com")], first)
        second = decoder.decode(bytes.fromhex("828684be5886a8eb10649cbf"))
        self.assertEqual(("cache-control", "no-cache"), second[-1])
        self.assertEqual((":authority", "www.example.com"), second[3])

    def test_bad_padding_rejected(self) -> None:
        with self.assertRaises(hpack_lite.HPACKError):
            hpack_lite.huffman_decode(b"\x00")

    def test_encode_response_round_trip(self) -> None:
        block = hpack_lite.encode_response(200, [("content-type", "text/event-stream"), ("x-tk", "1")])
        self.assertEqual([(":status", "200"), ("content-type", "text/event-stream"), ("x-tk", "1")],
                         hpack_lite.Decoder().decode(block))


class Http2ServerTest(unittest.TestCase):
    def test_serve_http2_records_settings_window_and_pseudo_order(self) -> None:
        server, client = socket.socketpair()
        result: dict = {}
        thread = threading.Thread(target=lambda: result.update(tls.serve_http2(server)))
        thread.start()
        frame = tls._frame
        settings = b"".join(k.to_bytes(2, "big") + v.to_bytes(4, "big") for k, v in ((2, 0), (4, 2097152)))
        block = bytes.fromhex("8387") + b"\x04" + bytes([len("/backend-api/codex/responses")]) + b"/backend-api/codex/responses" \
            + b"\x01" + bytes([len("chatgpt.com")]) + b"chatgpt.com"
        client.sendall(b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" + frame(4, 0, 0, settings)
                       + frame(8, 0, 0, (5177345).to_bytes(4, "big")) + frame(1, 0x4, 1, block)
                       + frame(0, 0x1, 1, b"{}") + frame(7, 0, 0, b"\x00" * 8))
        thread.join(5)
        client.close()
        server.close()
        self.assertEqual([[2, 0], [4, 2097152]], result["settings"])
        self.assertEqual(5177345, result["connection_window_update"])
        self.assertEqual("m,s,p,a", result["requests"][0]["pseudo_order"])
        self.assertEqual("/backend-api/codex/responses", result["requests"][0]["path"])
        self.assertEqual("2:0;4:2097152|5177345|0|m,s,p,a", tls.akamai_h2(result))


class ChildEnvTest(unittest.TestCase):
    def test_real_credentials_and_proxies_are_stripped(self) -> None:
        import os
        from unittest import mock
        env = {"OPENAI_API_KEY": "sk-real", "CODEX_HOME": "/home/u/.codex", "NO_PROXY": "*", "HOME": "/h", "PATH": "/bin"}
        with mock.patch.dict(os.environ, env, clear=True):
            child = tls.codex_child_env(Path("/tmp/x"), Path("/tmp/ca.pem"), 1234)
        self.assertNotIn("OPENAI_API_KEY", child)
        self.assertNotIn("NO_PROXY", child)
        self.assertEqual("/tmp/x", child["CODEX_HOME"])
        self.assertEqual("http://127.0.0.1:1234", child["HTTPS_PROXY"])
        self.assertTrue(child["TK_CODEX_TLS_CAPTURE_DUMMY_KEY"].startswith("sk-tk-local"))


if __name__ == "__main__":
    unittest.main()
