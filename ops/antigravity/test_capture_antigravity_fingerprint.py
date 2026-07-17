#!/usr/bin/env python3
"""Unit tests for capture_antigravity_fingerprint.py (the deterministic antigravity
fingerprint diff engine). stdlib unittest only — run with:

  python3 ops/antigravity/test_capture_antigravity_fingerprint.py
  # or: python3 -m unittest discover -s ops/antigravity -p 'test_*.py' -t ops/antigravity
"""
from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import capture_antigravity_fingerprint as eng  # noqa: E402


def _synth_baseline() -> dict:
    return {
        "ua_version": "1.23.2",
        "ua_format": "antigravity/hub/%s windows/amd64",
        "client_id": "client-id",
        "redirect_uri": "http://localhost:8085/callback",
        "scopes": ["https://www.googleapis.com/auth/cloud-platform"],
        "body_user_agent": "antigravity",
        "ide_type": "ANTIGRAVITY",
        "ide_name": "antigravity",
        "platform": "PLATFORM_UNSPECIFIED",
        "plugin_type": "GEMINI",
        # #756 removed X-Goog-Api-Client(gl-node); absence is the aligned state.
        "x_goog_api_client": "",
    }


def _full_http(ua_version="1.23.2", os_arch="windows/amd64") -> dict:
    # Real IDE UA carries the /hub/ subclient segment and sends NO gl-node header.
    return {
        "user_agent": f"antigravity/hub/{ua_version} {os_arch}",
        "body_user_agent": "antigravity",
        "ide_type": "ANTIGRAVITY",
        "ide_name": "antigravity",
        "platform": "PLATFORM_UNSPECIFIED",
        "plugin_type": "GEMINI",
    }


class BaselineLoadTests(unittest.TestCase):
    def test_live_constants_load(self):
        """Locks the engine against the real repo Go files (drift gate)."""
        b = eng.load_antigravity_baseline()
        self.assertRegex(b["ua_version"], r"^\d+\.\d+\.\d+$")
        self.assertEqual(b["body_user_agent"], "antigravity")
        self.assertEqual(b["ide_type"], "ANTIGRAVITY")
        self.assertEqual(b["ide_name"], "antigravity")
        self.assertEqual(b["plugin_type"], "GEMINI")
        # #756 removed X-Goog-Api-Client(gl-node) from the privacy endpoints; the
        # aligned state is absence. Guards against a silent gl-node re-introduction.
        self.assertEqual(b["x_goog_api_client"], "")
        self.assertIn("/hub/", b["ua_format"])  # #756 UA subclient segment must persist
        self.assertIn("https://www.googleapis.com/auth/cclog", b["scopes"])
        self.assertIn("https://www.googleapis.com/auth/experimentsandconfigs", b["scopes"])
        self.assertEqual(len(b["scopes"]), 5)

    def test_expected_user_agent_renders_windows_amd64(self):
        b = _synth_baseline()
        self.assertEqual(eng.expected_user_agent(b), "antigravity/hub/1.23.2 windows/amd64")


class ParseUaTests(unittest.TestCase):
    def test_parses_version_and_os_arch(self):
        # the /hub/ subclient segment is optional — accept both shapes
        self.assertEqual(eng.parse_ua("antigravity/hub/2.0.11 windows/amd64"), ("2.0.11", "windows/amd64"))
        self.assertEqual(eng.parse_ua("antigravity/hub/1.104.0 darwin/arm64"), ("1.104.0", "darwin/arm64"))
        self.assertEqual(eng.parse_ua("antigravity/1.23.2 windows/amd64"), ("1.23.2", "windows/amd64"))

    def test_unparseable_returns_empty(self):
        self.assertEqual(eng.parse_ua("Mozilla/5.0"), ("", ""))
        self.assertEqual(eng.parse_ua(""), ("", ""))


class DiffTests(unittest.TestCase):
    def test_all_match_when_capture_matches_baseline(self):
        bundle = {"http": _full_http(), "tls": {}}
        rows = eng.diff_bundle(bundle, _synth_baseline())
        self.assertFalse(eng.has_actionable_mismatch(rows))
        statuses = {r.field: r.status for r in rows}
        self.assertEqual(statuses["http.ua_version"], "match")
        self.assertEqual(statuses["http.body_user_agent"], "match")
        self.assertEqual(statuses["http.ide_type"], "match")
        self.assertEqual(statuses["http.x_goog_api_client"], "match")

    def test_ua_version_drift_is_actionable(self):
        bundle = {"http": _full_http(ua_version="1.24.0"), "tls": {}}
        rows = eng.diff_bundle(bundle, _synth_baseline())
        self.assertTrue(eng.has_actionable_mismatch(rows))
        row = next(r for r in rows if r.field == "http.ua_version")
        self.assertEqual(row.status, "mismatch")
        self.assertEqual(row.captured, "1.24.0")
        self.assertEqual(row.tokenkey, "1.23.2")

    def test_os_arch_difference_is_info_not_drift(self):
        # Captured on a Mac: darwin/arm64 vs TokenKey's pinned windows/amd64 — NOT drift.
        bundle = {"http": _full_http(os_arch="darwin/arm64"), "tls": {}}
        rows = eng.diff_bundle(bundle, _synth_baseline())
        self.assertFalse(eng.has_actionable_mismatch(rows))
        row = next(r for r in rows if r.field == "http.ua_os_arch")
        self.assertEqual(row.status, "info")
        self.assertEqual(row.captured, "darwin/arm64")

    def test_gl_node_regression_is_actionable(self):
        # #756 removed gl-node; a captured gl-node means the real IDE sends it again
        # while TokenKey does not — actionable drift (re-evaluate the removal).
        http = _full_http()
        http["x_goog_api_client"] = "gl-node/24.0.0"
        rows = eng.diff_bundle({"http": http, "tls": {}}, _synth_baseline())
        self.assertTrue(eng.has_actionable_mismatch(rows))
        row = next(r for r in rows if r.field == "http.x_goog_api_client")
        self.assertEqual(row.status, "mismatch")

    def test_gl_node_absence_is_aligned(self):
        # capture present but no gl-node header → aligned with the #756 removal.
        rows = eng.diff_bundle({"http": _full_http(), "tls": {}}, _synth_baseline())
        row = next(r for r in rows if r.field == "http.x_goog_api_client")
        self.assertEqual(row.status, "match")
        self.assertFalse(eng.has_actionable_mismatch(rows))

    def test_body_user_agent_drift_is_actionable(self):
        http = _full_http()
        http["body_user_agent"] = "windsurf"
        rows = eng.diff_bundle({"http": http, "tls": {}}, _synth_baseline())
        self.assertTrue(eng.has_actionable_mismatch(rows))

    def test_no_http_capture_is_non_actionable(self):
        rows = eng.diff_bundle({"http": {}, "tls": {}}, _synth_baseline())
        self.assertFalse(eng.has_actionable_mismatch(rows))
        self.assertTrue(all(r.status in ("missing_capture", "info") for r in rows))

    def test_client_metadata_on_serving_path_is_info(self):
        http = _full_http()
        http["client_metadata"] = '{"ideType":"ANTIGRAVITY","platform":"MACOS","pluginType":"GEMINI"}'
        rows = eng.diff_bundle({"http": http, "tls": {}}, _synth_baseline())
        self.assertFalse(eng.has_actionable_mismatch(rows))
        row = next(r for r in rows if r.field == "http.client_metadata")
        self.assertEqual(row.status, "info")

    def test_ja3_is_recorded_but_non_actionable(self):
        bundle = {"http": _full_http(), "tls": {"ja3_hash": "deadbeef"}}
        rows = eng.diff_bundle(bundle, _synth_baseline())
        self.assertFalse(eng.has_actionable_mismatch(rows))
        row = next(r for r in rows if r.field == "tls.ja3_hash")
        self.assertEqual(row.status, "info")
        self.assertFalse(row.critical)


class HttpLogMergeTests(unittest.TestCase):
    def test_merges_fields_across_endpoint_lines(self):
        # streamGenerateContent carries UA+body, loadCodeAssist carries ideType, etc.
        lines = [
            {"path": "/v1internal:streamGenerateContent", "user_agent": "antigravity/1.23.2 windows/amd64",
             "body_user_agent": "antigravity", "project": "proj-x", "model": "claude-sonnet-4-6"},
            {"path": "/v1internal:loadCodeAssist", "ide_type": "ANTIGRAVITY", "ide_name": "antigravity"},
            {"path": "/v1internal:setUserSettings", "x_goog_api_client": "gl-node/22.21.1"},
        ]
        with tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False) as fh:
            for ln in lines:
                fh.write(json.dumps(ln) + "\n")
            path = Path(fh.name)
        merged = eng.parse_http_log(path)
        path.unlink()
        self.assertEqual(merged["user_agent"], "antigravity/1.23.2 windows/amd64")
        self.assertEqual(merged["ide_type"], "ANTIGRAVITY")
        self.assertEqual(merged["x_goog_api_client"], "gl-node/22.21.1")
        self.assertEqual(merged["project"], "proj-x")
        self.assertEqual(len(merged["seen_paths"]), 3)


class Ja3Tests(unittest.TestCase):
    def test_strips_grease(self):
        ja3_raw, _ = eng.compute_ja3(771, [0x1A1A, 4865], [0x0A0A, 0], [0x2A2A, 29], [0])
        self.assertEqual(ja3_raw, "771,4865,0,29,0")


class BundleRoundtripTests(unittest.TestCase):
    def test_bundle_from_artifacts_roundtrip(self):
        # Build the captured UA from the live baseline so this stays aligned across
        # version/format bumps (e.g. the #756 /hub/ change) — no hardcoded version.
        base = eng.load_antigravity_baseline()
        ua = eng.expected_user_agent(base)
        with tempfile.TemporaryDirectory() as d:
            http_log = Path(d) / "http.jsonl"
            http_log.write_text(json.dumps({
                "path": "/v1internal:streamGenerateContent",
                "user_agent": ua,
                "body_user_agent": base["body_user_agent"],
            }) + "\n", encoding="utf-8")
            out = Path(d) / "bundle.json"
            rc = eng.main(["bundle-from-artifacts", "--http-log", str(http_log), "--out", str(out)])
            self.assertEqual(rc, 0)
            bundle = json.loads(out.read_text(encoding="utf-8"))
            self.assertEqual(bundle["schema_version"], eng.SCHEMA_VERSION)
            self.assertEqual(bundle["http"]["user_agent"], ua)
            self.assertIn("antigravity_baseline", bundle)
            # check passes (aligned) on this bundle
            rc_check = eng.main(["check", "--bundle", str(out)])
            self.assertEqual(rc_check, 0)


import mitm_antigravity_http_headers as addon  # noqa: E402


class _FakeHeaders:
    """Minimal stand-in for mitmproxy Headers: ordered, duplicate-preserving items()."""

    def __init__(self, pairs):
        self._pairs = list(pairs)

    def items(self, multi=False):  # noqa: ARG002 - signature parity with mitmproxy
        return list(self._pairs)


class RedactionTest(unittest.TestCase):
    def test_redact_masks_secret_value_keeps_name(self):
        # bearer / cookie / *auth* / *token* / *key* values are masked to <redacted:Nb>
        secret = "supersecretvalue"
        for name in ("Authorization", "Cookie", "Proxy-Authorization",
                     "X-Goog-Api-Key", "X-Auth-Token", "some-secret-header"):
            out = addon._redact(name, secret)
            self.assertEqual(out, f"<redacted:{len(secret)}b>", f"{name} should be redacted")
            self.assertNotIn("supersecret", out)

    def test_redact_keeps_nonsecret_values(self):
        # the load-bearing fingerprint headers must survive verbatim
        self.assertEqual(addon._redact("User-Agent", "antigravity/2.0.11 windows/amd64"),
                         "antigravity/2.0.11 windows/amd64")
        self.assertEqual(addon._redact("X-Goog-Api-Client", "gl-node/22.21.1"), "gl-node/22.21.1")
        self.assertEqual(addon._redact("Content-Type", "application/json"), "application/json")

    def test_ordered_headers_preserves_order_and_redacts(self):
        hdrs = _FakeHeaders([
            ("Host", "daily-cloudcode-pa.googleapis.com"),
            ("User-Agent", "antigravity/2.0.11 windows/amd64"),
            ("Authorization", "Bearer aaa.bbb.ccc"),
            ("Content-Type", "application/json"),
        ])
        out = addon._ordered_headers(hdrs)
        self.assertEqual([n for n, _ in out],
                         ["Host", "User-Agent", "Authorization", "Content-Type"])
        self.assertEqual(out[1][1], "antigravity/2.0.11 windows/amd64")  # UA verbatim
        self.assertTrue(out[2][1].startswith("<redacted:"))  # bearer masked
        self.assertNotIn("Bearer", out[2][1])


if __name__ == "__main__":
    unittest.main()
