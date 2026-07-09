#!/usr/bin/env python3
"""Unit tests for capture_codex_fingerprint.py (the deterministic Codex
fingerprint diff/consistency engine). stdlib unittest only — run with:

  python3 ops/openai/test_capture_codex_fingerprint.py
  # or: python3 -m unittest discover -s ops/openai -p 'test_*.py' -t ops/openai
"""
from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import capture_codex_fingerprint as eng  # noqa: E402

UA_TMPL = "codex-tui/{v} (Mac OS 26.3.1; arm64) iTerm.app/3.6.11 (codex-tui; {v})"


_PATHS = {
    "ua_default": eng.SETTING_GO,
    "gateway_version": eng.GATEWAY_GO,
    "probe_version": eng.USAGE_GO,
    "placeholder_en": eng.EN_TS,
    "placeholder_zh": eng.ZH_TS,
}


def _pin(key, kind, version, raw, consistent=True, found=True):
    return eng.Pin(key=key, path=_PATHS[key], kind=kind, raw=raw,
                   version=version, consistent_internal=consistent, found=found)


def _baseline(versions):
    """Synthesise a Baseline with the 5 pins set to the given versions dict."""
    bl = eng.Baseline(originator_pinned=True, beta_pinned=True)
    bl.pins = [
        _pin("ua_default", "ua", versions["ua_default"], UA_TMPL.format(v=versions["ua_default"])),
        _pin("gateway_version", "bare", versions["gateway_version"], versions["gateway_version"]),
        _pin("probe_version", "bare", versions["probe_version"], versions["probe_version"]),
        _pin("placeholder_en", "ua", versions["placeholder_en"], UA_TMPL.format(v=versions["placeholder_en"])),
        _pin("placeholder_zh", "ua", versions["placeholder_zh"], UA_TMPL.format(v=versions["placeholder_zh"])),
    ]
    return bl


def _aligned(v="0.142.2"):
    return _baseline({k: v for k in
                      ("ua_default", "gateway_version", "probe_version",
                       "placeholder_en", "placeholder_zh")})


class ParseVersionTests(unittest.TestCase):
    def test_parses_codex_cli_version_string(self):
        self.assertEqual(eng.parse_codex_version("codex-cli 0.142.2"), "0.142.2")
        self.assertEqual(eng.parse_codex_version("codex-cli 1.0.0-rc.1\n"), "1.0.0-rc.1")

    def test_unparseable_returns_empty(self):
        self.assertEqual(eng.parse_codex_version("no version here"), "")
        self.assertEqual(eng.parse_codex_version(""), "")


class ExtractUaVersionTests(unittest.TestCase):
    def test_extracts_version_not_os_or_terminal(self):
        # OS 26.3.1 and iTerm 3.6.11 must NOT be mistaken for the codex version.
        v, ok = eng.extract_ua_version(UA_TMPL.format(v="0.142.2"))
        self.assertEqual(v, "0.142.2")
        self.assertTrue(ok)

    def test_detects_internal_prefix_suffix_disagreement(self):
        ua = "codex-tui/0.142.2 (Mac OS 26.3.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.142.0)"
        v, ok = eng.extract_ua_version(ua)
        self.assertEqual(v, "0.142.2")  # prefix wins as the reported version
        self.assertFalse(ok)            # but flagged inconsistent

    def test_unparseable_ua(self):
        self.assertEqual(eng.extract_ua_version("Mozilla/5.0"), ("", False))


class BumpUaLiteralTests(unittest.TestCase):
    def test_swaps_only_version_keeps_os_and_terminal(self):
        out = eng.bump_ua_literal(UA_TMPL.format(v="0.142.2"), "0.143.0")
        self.assertEqual(out, UA_TMPL.format(v="0.143.0"))
        # OS / terminal segment preserved verbatim.
        self.assertIn("Mac OS 26.3.1; arm64", out)
        self.assertIn("iTerm.app/3.6.11", out)


class DiffTests(unittest.TestCase):
    def test_all_match_when_installed_equals_pins(self):
        rows = eng.diff_pins(_aligned("0.142.2"), "0.142.2")
        self.assertFalse(eng.has_drift(rows))
        self.assertTrue(all(r.status == "match" for r in rows))

    def test_version_drift_is_actionable(self):
        rows = eng.diff_pins(_aligned("0.142.2"), "0.143.0")
        self.assertTrue(eng.has_drift(rows))
        self.assertTrue(all(r.status == "mismatch" for r in rows))

    def test_no_installed_cli_is_info_not_drift(self):
        rows = eng.diff_pins(_aligned("0.142.2"), "")
        self.assertFalse(eng.has_drift(rows))
        self.assertTrue(all(r.status == "info" for r in rows))

    def test_internal_ua_disagreement_is_drift_even_when_installed_matches(self):
        bl = _aligned("0.142.2")
        bl.pins[0].consistent_internal = False  # ua_default half-edited
        rows = eng.diff_pins(bl, "0.142.2")
        self.assertTrue(eng.has_drift(rows))


class ConsistencyTests(unittest.TestCase):
    def test_consensus_when_all_agree(self):
        self.assertEqual(_aligned("0.142.2").consensus(), "0.142.2")

    def test_no_consensus_when_one_pin_lags(self):
        bl = _aligned("0.142.2")
        bl.pins[3].version = "0.142.0"      # en placeholder forgotten
        bl.pins[3].raw = UA_TMPL.format(v="0.142.0")
        self.assertEqual(bl.consensus(), "")
        rows = eng.consistency_rows(bl)
        self.assertTrue(any(r.status == "mismatch" for r in rows))

    def test_consistency_ignores_installed_cli(self):
        # The consistency gate must pass even on an old pinned version, as long as
        # all five agree — it must never break CI on a fresh upstream codex release.
        rows = eng.consistency_rows(_aligned("0.1.0"))
        self.assertTrue(all(r.status == "match" for r in rows))


class EmitEditsTests(unittest.TestCase):
    def test_emits_one_edit_per_lagging_pin(self):
        bl = _aligned("0.142.2")
        edits = eng.emit_edits(bl, "0.143.0")
        self.assertEqual(len(edits), 5)
        # bare pin: whole value replaced
        gw = next(e for e in edits if "openai_gateway_service.go" in e["file"])
        self.assertEqual(gw["new"], "0.143.0")
        # ua pin: only version tokens swapped, OS/terminal kept
        ua = next(e for e in edits if "setting_gateway_runtime.go" in e["file"])
        self.assertEqual(ua["new"], UA_TMPL.format(v="0.143.0"))
        self.assertIn("iTerm.app/3.6.11", ua["new"])

    def test_no_edits_when_already_aligned(self):
        self.assertEqual(eng.emit_edits(_aligned("0.142.2"), "0.142.2"), [])


class LocateBinaryTests(unittest.TestCase):
    """locate_codex_binary must be bounded — never an open-ended disk glob."""

    def test_returns_none_when_not_installed(self):
        with mock.patch.object(eng.shutil, "which", return_value=None):
            self.assertIsNone(eng.locate_codex_binary())

    def test_finds_native_binary_in_npm_layout(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            wrapper = root / "lib/node_modules/@openai/codex/bin/codex.js"
            native = (root / "lib/node_modules/@openai/codex/node_modules/@openai/"
                      "codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex")
            wrapper.parent.mkdir(parents=True)
            native.parent.mkdir(parents=True)
            wrapper.write_text("// wrapper", encoding="utf-8")
            native.write_bytes(b"native")
            with mock.patch.object(eng.shutil, "which", return_value=str(wrapper)):
                # resolve() the expected path too — the engine resolves symlinks
                # (macOS /var -> /private/var) so compare canonicalised paths.
                self.assertEqual(eng.locate_codex_binary(), native.resolve())

    def test_standalone_native_target_returns_itself(self):
        with tempfile.TemporaryDirectory() as d:
            native = Path(d) / "bin/codex"
            native.parent.mkdir(parents=True)
            native.write_bytes(b"native")
            with mock.patch.object(eng.shutil, "which", return_value=str(native)):
                self.assertEqual(eng.locate_codex_binary(), native.resolve())

    def test_missing_native_under_pkg_root_returns_none_without_root_glob(self):
        # Wrapper present but no native pkg: must give up at the @openai/codex root,
        # NOT keep ascending to '/' and glob the whole disk.
        with tempfile.TemporaryDirectory() as d:
            wrapper = Path(d) / "node_modules/@openai/codex/bin/codex.js"
            wrapper.parent.mkdir(parents=True)
            wrapper.write_text("// wrapper", encoding="utf-8")
            with mock.patch.object(eng.shutil, "which", return_value=str(wrapper)):
                self.assertIsNone(eng.locate_codex_binary())


class LiveRepoTests(unittest.TestCase):
    """Lock the engine against the real repo files (drift gate for the regexes)."""

    def test_live_baseline_loads_and_is_consistent(self):
        bl = eng.load_baseline()
        # All five pins must be found...
        self.assertEqual(len(bl.pins), 5)
        for p in bl.pins:
            self.assertTrue(p.found, f"{p.key} not found via regex in {p.rel}")
            self.assertRegex(p.version, r"^\d+\.\d+\.\d+")
        # ...mutually consistent (this is exactly what the preflight gate asserts)...
        self.assertNotEqual(bl.consensus(), "", "live codex version pins disagree")
        # ...and the non-version pins are present.
        self.assertTrue(bl.originator_pinned, "originator pin missing from source")
        self.assertTrue(bl.beta_pinned, "OpenAI-Beta pin missing from source")

    def test_live_check_consistency_passes(self):
        self.assertEqual(eng.main(["check-consistency"]), 0)


if __name__ == "__main__":
    unittest.main()
