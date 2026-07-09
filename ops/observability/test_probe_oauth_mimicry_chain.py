#!/usr/bin/env python3
"""Validation tests for probe-oauth-mimicry-chain.sh."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-oauth-mimicry-chain.sh"


class ProbeOAuthMimicryChainTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SCRIPT)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_emits_verdict_and_fingerprint_scope(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "docker").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    if [ "$1" = inspect ]; then
                      [ "$2" = tokenkey ] && exit 0
                      exit 1
                    fi
                    if [ "$1" = exec ]; then
                      echo '{"user_agent":"OpenAI/Python 2.44.0","account_type":"oauth","platform":"anthropic","model":"claude-sonnet-4-6","tls_profile_name":"tk_canonical_cc_oauth"}'
                      exit 0
                    fi
                    if [ "$1" = logs ]; then
                      cat <<'LOGS'
                    INFO gateway.anthropic_prompt_fingerprint {"billing_prefix_present":true,"identity_anchor_id":"claude_code_cli","normalize_changes":"system_rewrite"}
                    INFO gateway.anthropic_oauth_mimic_egress {"ingress_ua_class":"openai_python_sdk","egress_user_agent":"claude-cli/2.1.205 (external, cli)","billing_prefix_present":true,"egress_stainless_package_version":"0.94.0"}
                    INFO service.gateway [ClaudeMimicDebug] mimic=true user-agent="claude-cli/2.1.205 (external, cli)" system.preview="x-anthropic-billing-header"
                    LOGS
                      exit 0
                    fi
                    exit 2
                    """
                ),
            )
            (fakebin / "docker").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "CONTAINER": "tokenkey",
                "WINDOW_MINUTES": "60",
                "LIMIT": "10",
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
            payload = json.loads(proc.stdout)
            self.assertIn("verdict", payload)
            self.assertIn("fingerprint_scope", payload["verdict"])
            self.assertIn("system", payload["verdict"]["fingerprint_scope"])
            self.assertGreaterEqual(
                payload["egress_prompt_fingerprint"].get("billing_prefix_present", 0),
                1,
            )
            self.assertGreaterEqual(
                payload["egress_oauth_mimic"].get("count", 0),
                1,
            )


if __name__ == "__main__":
    unittest.main()
