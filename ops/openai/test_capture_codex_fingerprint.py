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


_PATHS = {
    "version_source": eng.SETTING_GO,
    "ua_default": eng.SETTING_GO,
    "gateway_version": eng.SETTING_GO,
    "probe_version": eng.SETTING_GO,
}


def _pin(key, kind, version, raw, derived=True, found=True):
    return eng.Pin(key=key, path=_PATHS[key], kind=kind, raw=raw,
                   version=version, derivation_complete=derived, found=found)


def _baseline(versions):
    """Synthesise a Baseline with the service pins set to the given versions dict."""
    bl = eng.Baseline(originator_pinned=True, beta_pinned=True)
    bl.pins = [
        _pin("version_source", "bare", versions["version_source"], versions["version_source"]),
        _pin("ua_default", "alias", versions["ua_default"], "DefaultOpenAICodexVersion"),
        _pin("gateway_version", "alias", versions["gateway_version"], "DefaultOpenAICodexVersion"),
        _pin("probe_version", "alias", versions["probe_version"], "DefaultOpenAICodexVersion"),
    ]
    return bl


def _aligned(v="0.142.2"):
    return _baseline({k: v for k in
                      ("version_source", "ua_default", "gateway_version", "probe_version")})


class ParseVersionTests(unittest.TestCase):
    def test_parses_codex_cli_version_string(self):
        self.assertEqual(eng.parse_codex_version("codex-cli 0.142.2"), "0.142.2")
        self.assertEqual(eng.parse_codex_version("codex-cli 1.0.0-rc.1\n"), "1.0.0-rc.1")

    def test_unparseable_returns_empty(self):
        self.assertEqual(eng.parse_codex_version("no version here"), "")
        self.assertEqual(eng.parse_codex_version(""), "")


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

    def test_incomplete_derivation_is_drift_even_when_installed_matches(self):
        bl = _aligned("0.142.2")
        bl.pins[1].derivation_complete = False
        rows = eng.diff_pins(bl, "0.142.2")
        self.assertTrue(eng.has_drift(rows))


class ConsistencyTests(unittest.TestCase):
    def test_consensus_when_all_agree(self):
        self.assertEqual(_aligned("0.142.2").consensus(), "0.142.2")

    def test_no_consensus_when_one_pin_lags(self):
        bl = _aligned("0.142.2")
        bl.pins[2].version = "0.142.0"      # gateway alias/source drift
        self.assertEqual(bl.consensus(), "")
        rows = eng.consistency_rows(bl)
        self.assertTrue(any(r.status == "mismatch" for r in rows))

    def test_same_value_literal_alias_still_breaks_derivation_contract(self):
        pin = eng._alias_pin(
            "gateway_version",
            eng.SETTING_GO,
            "codexCLIVersion",
            'const codexCLIVersion = "0.142.2"',
            "0.142.2",
        )
        self.assertTrue(pin.found)
        self.assertFalse(pin.derivation_complete)

        bl = _aligned("0.142.2")
        bl.pins[2] = pin
        rows = eng.consistency_rows(bl)
        self.assertEqual(rows[2].status, "mismatch")

    def test_consistency_ignores_installed_cli(self):
        # The consistency gate only validates owner/alias derivation, so an old
        # owner must never break CI merely because upstream released a new Codex.
        rows = eng.consistency_rows(_aligned("0.1.0"))
        self.assertTrue(all(r.status == "match" for r in rows))


class EmitEditsTests(unittest.TestCase):
    def test_emits_only_version_owner_edit(self):
        bl = _aligned("0.142.2")
        edits = eng.emit_edits(bl, "0.143.0")
        self.assertEqual(edits, [{
            "file": "backend/internal/service/setting_gateway_runtime.go",
            "old": "0.142.2",
            "new": "0.143.0",
        }])

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
        # All service pins must be found...
        self.assertEqual(len(bl.pins), 4)
        for p in bl.pins:
            self.assertTrue(p.found, f"{p.key} not found via regex in {p.rel}")
            self.assertTrue(p.derivation_complete, f"{p.key} no longer derives from the owner")
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
