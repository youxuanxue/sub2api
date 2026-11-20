#!/usr/bin/env python3
"""Exit-contract tests for the combined fingerprint orchestrator."""
from __future__ import annotations

import json
import os
import shlex
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "ops/fingerprint/capture-all-fingerprints.sh"


class CaptureAllExitContractTest(unittest.TestCase):
    def _run(
        self, codes: tuple[int, int, int, int, int, int], *args: str,
        registry: dict | None = None, evidence: str = "valid", real_gemini: bool = False,
    ) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as tmp:
            fixture = Path(tmp) / "fixture.py"
            fixture.write_text(
                "import sys\nsys.path.insert(0, " + repr(str(REPO_ROOT / "scripts/fingerprint")) + ")\n"
                + "from client_release_watch import PIN_READERS\n"
                + """import json, os, time
from pathlib import Path
from datetime import datetime, timezone
index = int(sys.argv[1])
identity = ('claude-code', 'kiro-cli', 'antigravity', 'codex', 'gemini-cli', 'grok-cli')[index]
version = PIN_READERS[identity]()
print(f"engine-{index}: {' '.join(sys.argv[2:])} installed=1.2.3 evidence=fixture-{index}")
mode = os.environ['CAPTURE_ALL_TEST_EVIDENCE'] if identity == 'gemini-cli' else 'valid'
if mode == 'missing':
    raise SystemExit(0)
if mode == 'old-version':
    version = '0.0.0'
if identity == 'codex' and sys.argv[2] == 'check':
    print(f'Codex fingerprint diff (installed={version}):')
else:
    bundle = {
        'schema_version': 3 if identity == 'kiro-cli' else 1,
        'captured_at': datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ'),
        'cc_version': version, 'kiro_cli_version': version,
        'codex_cli_version': version, 'installed_version': version,
        'http': {'user_agent': f'antigravity/cli/{version} darwin/arm64'},
    }
    if mode == 'old-time':
        bundle['captured_at'] = '2000-01-01T00:00:00Z'
    path = Path(__file__).with_name(identity + '-bundle.json')
    path.write_text('invalid json' if mode == 'invalid' else json.dumps(bundle))
    if mode == 'old-file':
        os.utime(path, (1, 1))
    print(f'bundle={path}')
""", encoding="utf-8",
            )
            paths: list[Path] = []
            for index, code in enumerate(codes):
                path = Path(tmp) / f"engine-{index}.sh"
                path.write_text(
                    f"#!/usr/bin/env bash\npython3 {shlex.quote(str(fixture))} {index} \"$@\"\nexit {code}\n",
                    encoding="utf-8",
                )
                paths.append(path)
            env = {
                **os.environ,
                "CAPTURE_ALL_TEST_EVIDENCE": evidence,
                "TOKENKEY_CAPTURE_ALL_CC": str(paths[0]),
                "TOKENKEY_CAPTURE_ALL_KIRO": str(paths[1]),
                "TOKENKEY_CAPTURE_ALL_ANTIGRAVITY": str(paths[2]),
                "TOKENKEY_CAPTURE_ALL_CODEX": str(paths[3]),
                "TOKENKEY_CAPTURE_ALL_GEMINI": str(paths[4]),
                "TOKENKEY_CAPTURE_ALL_GROK": str(paths[5]),
            }
            if real_gemini:
                env["TOKENKEY_CAPTURE_ALL_GEMINI"] = str(REPO_ROOT / "ops/geminicli/capture-gemini-fingerprint.sh")
            if registry is not None:
                registry_path = Path(tmp) / "registry.json"
                registry_path.write_text(json.dumps(registry), encoding="utf-8")
                env["TOKENKEY_CAPTURE_ALL_REGISTRY"] = str(registry_path)
            return subprocess.run(
                ["bash", str(SCRIPT), *args],
                capture_output=True,
                text=True,
                check=False,
                env=env,
            )

    def test_all_observed_and_aligned_exits_zero(self) -> None:
        result = self._run((0, 0, 0, 0, 0, 0))
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn("identity evidence coverage", result.stdout)
        self.assertIn("grok-cli", result.stdout)
        self.assertIn("gemini-cli", result.stdout)
        self.assertIn("advisory", result.stdout)
        self.assertIn("kiro-cli", result.stdout)
        registry = (REPO_ROOT / "scripts/fingerprint/client_identity_registry.json").read_text()
        self.assertNotIn('"id": "kiro"', registry)
        self.assertIn('"id": "kiro-cli"', registry)
        self.assertIn('"evidence_mode": "wire_tls_http"', registry)

    def test_drift_exits_one(self) -> None:
        result = self._run((1, 0, 0, 0, 0, 0))
        self.assertEqual(1, result.returncode)

    def test_error_takes_precedence(self) -> None:
        result = self._run((1, 2, 0, 0, 0, 0))
        self.assertEqual(2, result.returncode)

    def test_incomplete_or_skipped_exits_three(self) -> None:
        self.assertEqual(3, self._run((3, 0, 0, 0, 0, 0)).returncode)
        self.assertEqual(3, self._run((0, 0, 0, 0, 0, 0), "--skip-cc").returncode)

    def test_gemini_and_grok_execute_and_preserve_provenance(self) -> None:
        result = self._run((0, 0, 0, 0, 0, 0))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for index in (4, 5):
            self.assertIn(f"engine-{index}: capture installed=1.2.3 evidence=fixture-{index}", result.stdout)
        for lane, index in (("gemini", 4), ("grok", 5)):
            for code in (1, 2, 3):
                with self.subTest(lane=lane, code=code):
                    codes = [0] * 6
                    codes[index] = code
                    self.assertEqual(self._run(tuple(codes)).returncode, code)
            self.assertEqual(self._run((0, 0, 0, 0, 0, 0), f"--skip-{lane}").returncode, 3)

    def test_zero_exit_without_current_evidence_cannot_align(self) -> None:
        for mode, expected in (("missing", 3), ("old-file", 3), ("old-time", 3), ("invalid", 2), ("old-version", 1)):
            with self.subTest(mode=mode):
                result = self._run((0, 0, 0, 0, 0, 0), evidence=mode)
                self.assertEqual(expected, result.returncode, result.stdout + result.stderr)
                self.assertNotIn("all required engine evidence is observed and aligned", result.stdout)

    def test_real_gemini_help_is_not_capture_evidence(self) -> None:
        result = self._run((0, 0, 0, 0, 0, 0), "--gemini-arg", "--help", real_gemini=True)
        self.assertEqual(3, result.returncode, result.stdout + result.stderr)
        self.assertIn("capture bundle NOT_OBSERVED", result.stdout)
        self.assertNotIn("all required engine evidence is observed and aligned", result.stdout)

    def test_registry_required_identity_without_engine_is_incomplete(self) -> None:
        registry = json.loads((REPO_ROOT / "scripts/fingerprint/client_identity_registry.json").read_text())
        # A newly watched identity with valid pin owners must not silently disappear.
        row = dict(next(row for row in registry["identities"] if row["id"] == "gemini-cli"))
        row.update(id="new-client", capture_tool=None)
        registry["identities"].append(row)
        result = self._run((0, 0, 0, 0, 0, 0), registry=registry)
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertIn("new-client: incomplete (required)", result.stdout)
        self.assertNotIn("all required engine evidence is observed and aligned", result.stdout)

    def test_codex_lane_runs_tls_capture_and_reports_it(self) -> None:
        result = self._run((0, 0, 0, 0, 0, 0))
        self.assertIn("codex-tls", result.stdout)
        self.assertIn("Codex CLI TLS", result.stdout)
        skipped = self._run((0, 0, 0, 0, 0, 0), "--skip-codex-tls")
        self.assertEqual(3, skipped.returncode)
        self.assertRegex(skipped.stdout, r"codex-tls\s+skipped")


if __name__ == "__main__":
    unittest.main()
