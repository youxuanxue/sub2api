#!/usr/bin/env python3
"""Behavior tests for deploy/aws/stage0/legal-page production assets."""

from __future__ import annotations

import pathlib
import re
import unittest

_DIR = pathlib.Path(__file__).resolve().parent / "legal-page"


class LegalPageProductionTest(unittest.TestCase):
    def test_privacy_and_terms_are_production_not_draft(self) -> None:
        privacy = (_DIR / "privacy.html").read_text()
        terms = (_DIR / "terms.html").read_text()
        for body, name in ((privacy, "privacy"), (terms, "terms")):
            with self.subTest(page=name):
                self.assertIn("Production", body)
                self.assertNotRegex(body, r"(?i)\bdraft\b")
                self.assertIn("ORBIT LOGIC PTE. LTD.", body)
                self.assertIn("contact@orbitlogic.dev", body)
                self.assertIn("/legal-assets/shared.css", body)
                self.assertIn('href="/privacy"', body)
                self.assertIn('href="/terms"', body)
                self.assertIn("2026-09-07", body)

    def test_privacy_states_controller_retention_and_response_aim(self) -> None:
        privacy = (_DIR / "privacy.html").read_text()
        self.assertIn("data controller", privacy)
        self.assertIn("90 days", privacy)
        self.assertIn("30 days", privacy)
        self.assertIn("24 months", privacy)
        self.assertRegex(privacy, re.compile(r"respond within.*30 days", re.I))
        self.assertIn("do not", privacy.lower())
        self.assertIn("train", privacy.lower())

    def test_terms_include_indemnity_and_general_provisions(self) -> None:
        terms = (_DIR / "terms.html").read_text()
        self.assertIn("indemnify", terms)
        self.assertIn("Republic of Singapore", terms)
        self.assertIn("Entire agreement", terms)
        self.assertIn("Severability", terms)
        self.assertNotIn("export control", terms.lower())
        self.assertNotIn("sanctions", terms.lower())


if __name__ == "__main__":
    unittest.main()
