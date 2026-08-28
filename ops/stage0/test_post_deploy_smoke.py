#!/usr/bin/env python3
"""Behavioral tests for post_deploy_smoke.sh suite routing and Responses probe."""

from __future__ import annotations

import json
import os
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path


_STAGE0_DIR = Path(__file__).resolve().parent
_SCRIPT = _STAGE0_DIR / "post_deploy_smoke.sh"


_FAKE_CURL = r"""#!/usr/bin/env python3
import json
import os
import pathlib
import sys

args = sys.argv[1:]
output = None
payload = ""
url = ""
for index, arg in enumerate(args):
    if arg == "-o":
        output = args[index + 1]
    elif arg == "-d":
        payload = args[index + 1]
    elif arg.startswith("http://") or arg.startswith("https://"):
        url = arg

with open(os.environ["FAKE_CURL_LOG"], "a", encoding="utf-8") as log:
    log.write(url + "\n")

if url.endswith("/api/v1/settings/public"):
    response = {"code": 0}
elif url.endswith("/v1/models"):
    response = {
        "object": "list",
        "data": [
            {"id": "claude-sonnet-4-6"},
            {"id": "gemini-3.1-pro-preview"},
            {"id": "gpt-5.4"},
        ],
    }
elif url.endswith("/v1/messages"):
    response = {
        "type": "message",
        "role": "assistant",
        "content": [{"type": "text", "text": "ok"}],
        "stop_reason": "end_turn",
        "usage": {"input_tokens": 1, "output_tokens": 1},
    }
elif url.endswith("/v1/chat/completions"):
    marker = "E2E-OPENAI-OAUTH-OK" if "E2E-OPENAI-OAUTH-OK" in payload else "E2E-OPENAI-OK"
    response = {
        "object": "chat.completion",
        "choices": [{
            "finish_reason": "stop",
            "message": {"role": "assistant", "content": marker},
        }],
        "usage": {
            "prompt_tokens": 2,
            "completion_tokens": 3,
            "total_tokens": 5,
            "completion_tokens_details": {"reasoning_tokens": 1},
        },
    }
elif url.endswith("/v1/responses"):
    configured = os.environ.get("FAKE_RESPONSES_BODY")
    response = json.loads(configured) if configured else {
        "id": "resp_smoke",
        "object": "response",
        "output": [{
            "type": "message",
            "content": [{"type": "output_text", "text": "ok"}],
        }],
        "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
    }
else:
    response = {"error": {"message": "unexpected fake curl URL: " + url}}

if output:
    pathlib.Path(output).write_text(json.dumps(response), encoding="utf-8")
http = os.environ.get("FAKE_RESPONSES_HTTP", "200") if url.endswith("/v1/responses") else "200"
sys.stdout.write(http)
"""


class PostDeploySmokeTest(unittest.TestCase):
    def _run(
        self,
        suite: str,
        *,
        responses_body: dict | None = None,
        responses_http: int = 200,
    ) -> tuple[subprocess.CompletedProcess[str], list[str]]:
        with tempfile.TemporaryDirectory() as tmp:
            tmpdir = Path(tmp)
            fakebin = tmpdir / "bin"
            fakebin.mkdir()
            fake_curl = fakebin / "curl"
            fake_curl.write_text(textwrap.dedent(_FAKE_CURL), encoding="utf-8")
            fake_curl.chmod(0o755)
            curl_log = tmpdir / "curl.log"

            env = os.environ.copy()
            env.update({
                "PATH": f"{fakebin}:{env['PATH']}",
                "TOKENKEY_BASE_URL": "https://api.example.test",
                "TOKENKEY_SITE_BASE_URL": "https://example.test",
                "TK_SMOKE_API_KEY": "sk-test",
                "TK_SMOKE_SKIP_FRONTEND": "1",
                "TK_SMOKE_ANTHROPIC_MODELS": "claude-sonnet-4-6",
                "TK_SMOKE_GEMINI_MODELS": "gemini-3.1-pro-preview",
                "TK_SMOKE_OPENAI_OAUTH_MODELS": "gpt-5.4",
                "GATEWAY_SMOKE_SUITE": suite,
                "FAKE_CURL_LOG": str(curl_log),
                "FAKE_RESPONSES_HTTP": str(responses_http),
            })
            if responses_body is not None:
                env["FAKE_RESPONSES_BODY"] = json.dumps(responses_body)

            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                cwd=_STAGE0_DIR.parents[1],
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )
            calls = curl_log.read_text(encoding="utf-8").splitlines()
            return proc, calls

    def test_full_suite_probes_responses(self) -> None:
        proc, calls = self._run("full")
        self.assertEqual(proc.returncode, 0, msg=proc.stdout + proc.stderr)
        self.assertEqual(calls.count("https://api.example.test/v1/responses"), 1)

    def test_non_full_suites_skip_responses(self) -> None:
        for suite in ("quick", "main-via-edge"):
            with self.subTest(suite=suite):
                proc, calls = self._run(suite)
                self.assertEqual(proc.returncode, 0, msg=proc.stdout + proc.stderr)
                self.assertNotIn("https://api.example.test/v1/responses", calls)

    def test_malformed_responses_shape_fails(self) -> None:
        proc, _ = self._run(
            "full",
            responses_body={"object": "response", "output": [], "usage": {}},
        )
        self.assertNotEqual(proc.returncode, 0, msg=proc.stdout + proc.stderr)
        self.assertIn("/v1/responses shape invalid", proc.stderr)

    def test_responses_auth_failure_is_hard(self) -> None:
        proc, _ = self._run(
            "full",
            responses_body={"error": {"message": "unauthorized"}},
            responses_http=401,
        )
        self.assertNotEqual(proc.returncode, 0, msg=proc.stdout + proc.stderr)
        self.assertIn("/v1/responses returned HTTP 401", proc.stderr)

    def test_responses_resource_failure_soft_degrades(self) -> None:
        proc, _ = self._run(
            "full",
            responses_body={"error": {"message": "rate limit"}},
            responses_http=429,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stdout + proc.stderr)
        self.assertIn("/v1/responses section soft-skipped", proc.stdout)


if __name__ == "__main__":
    unittest.main()
