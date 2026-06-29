#!/usr/bin/env python3
"""Tests for edge_native_anthropic_smoke.sh model parsing."""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "edge_native_anthropic_smoke.sh"


class EdgeNativeAnthropicSmokeTest(unittest.TestCase):
    def _run_smoke(self, models: str) -> tuple[subprocess.CompletedProcess[str], list[str]]:
        with tempfile.TemporaryDirectory(prefix="edge-native-smoke-test-") as td:
            tmpdir = pathlib.Path(td)
            bin_dir = tmpdir / "bin"
            bin_dir.mkdir()
            model_log = tmpdir / "models.txt"

            sudo = bin_dir / "sudo"
            sudo.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    printf '%s\n' "${FAKE_ACCOUNT_IDS:-66}"
                    """
                )
            )
            sudo.chmod(0o755)

            jq = bin_dir / "jq"
            jq.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    case "$*" in
                      *".verdict"*) printf 'servable\n' ;;
                      *".http_code"*) printf '200\n' ;;
                      *) exit 0 ;;
                    esac
                    """
                )
            )
            jq.chmod(0o755)

            probe = tmpdir / "probe_account_model.sh"
            probe.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    printf '%s\n' "${MODEL:-}" >> "${PROBE_MODEL_LOG}"
                    printf '{"verdict":"servable","http_code":200}\n'
                    """
                )
            )

            realistic = tmpdir / "smoke_anthropic_realistic.py"
            realistic.write_text("")

            env = {
                **os.environ,
                "PATH": f"{bin_dir}{os.pathsep}{os.environ.get('PATH', '')}",
                "ANTHROPIC_MODELS": models,
                "PROBE_ACCOUNT_MODEL_SH": str(probe),
                "SMOKE_ANTHROPIC_REALISTIC_PY": str(realistic),
                "PROBE_MODEL_LOG": str(model_log),
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            logged_models = model_log.read_text().splitlines() if model_log.exists() else []
            return proc, logged_models

    def test_single_model_without_trailing_newline_is_used(self) -> None:
        proc, logged_models = self._run_smoke("claude-sonnet-4-6")

        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(logged_models, ["claude-sonnet-4-6"])
        self.assertIn("OK served=1", proc.stdout)
        self.assertNotIn("no models configured", proc.stderr)

    def test_comma_and_whitespace_models_are_split(self) -> None:
        proc, logged_models = self._run_smoke("claude-a, claude-b")

        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(logged_models, ["claude-a", "claude-b"])
        self.assertIn("OK served=2", proc.stdout)


if __name__ == "__main__":
    unittest.main()
