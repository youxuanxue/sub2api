#!/usr/bin/env python3
"""Tests for ops/stage0/assert-live-host-state.sh transport glue.

The live-host verdict itself is tested in live_host_state_verdict.py. These tests
stub the AWS CLI so the shell wrapper can be exercised without network access.
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "assert-live-host-state.sh"


class AssertLiveHostStateTransportTest(unittest.TestCase):
    def _run_with_fake_aws(self, *, region: str | None = None) -> subprocess.CompletedProcess[str]:
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="assert-live-host-state-"))
        calls = tmp / "aws-calls.txt"
        fake_bin = tmp / "bin"
        fake_bin.mkdir()
        (fake_bin / "aws").write_text(
            textwrap.dedent(
                f"""\
                #!/usr/bin/env bash
                set -euo pipefail
                printf '%s\\n' "$*" >> {str(calls)!r}
                if [[ "${{1:-}}" == "--region" ]]; then
                  shift 2
                fi
                case "$*" in
                  'ssm send-command '*)
                    printf 'cmd-123\\n'
                    ;;
                  'ssm get-command-invocation --command-id cmd-123 --instance-id i-test --query Status --output text')
                    printf 'Success\\n'
                    ;;
                  'ssm get-command-invocation --command-id cmd-123 --instance-id i-test --query StandardOutputContent --output text')
                    cat <<'OUT'
                APPCONTAINER {{"name":"tokenkey-blue"}}
                RUNIMAGE {{"image":"ghcr.io/youxuanxue/sub2api:1.8.40"}}
                ENV {{"key":"SERVER_FRONTEND_URL","value":"https://api.tokenkey.dev"}}
                ENV {{"key":"QA_CAPTURE_EXPORT_STORAGE_DRIVER","value":"s3"}}
                ENV {{"key":"QA_CAPTURE_EXPORT_STORAGE_REGION","value":"us-east-1"}}
                ENV {{"key":"QA_CAPTURE_EXPORT_STORAGE_BUCKET","value":"tokenkey-prod-qa-exports-682751977094"}}
                ENV {{"key":"QA_CAPTURE_EXPORT_STORAGE_PREFIX","value":"traj-exports"}}
                RETENTION {{"value":"1.5"}}
                OUT
                    ;;
                  *)
                    printf 'unexpected aws call: %s\\n' "$*" >&2
                    exit 9
                    ;;
                esac
                """
            ),
            encoding="utf-8",
        )
        (fake_bin / "aws").chmod(0o755)
        env = {
            **os.environ,
            "PATH": f"{fake_bin}:{os.environ['PATH']}",
            "SSM_TIMEOUT_SECONDS": "5",
        }
        env.pop("AWS_REGION", None)
        env.pop("AWS_DEFAULT_REGION", None)
        if region is not None:
            env["AWS_REGION"] = region
        proc = subprocess.run(
            ["bash", str(_SCRIPT), "i-test", "1.8.40"],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        proc.calls = calls.read_text(encoding="utf-8").splitlines()  # type: ignore[attr-defined]
        return proc

    def test_no_region_uses_aws_default_chain_without_unbound_array(self) -> None:
        proc = self._run_with_fake_aws()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("OK: live host matches intended state", proc.stdout)
        self.assertTrue(proc.calls)  # type: ignore[attr-defined]
        self.assertTrue(all(not call.startswith("--region ") for call in proc.calls))  # type: ignore[attr-defined]
        self.assertNotIn("unbound variable", proc.stderr)

    def test_region_is_passed_before_ssm_subcommand(self) -> None:
        proc = self._run_with_fake_aws(region="us-east-1")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("OK: live host matches intended state", proc.stdout)
        self.assertTrue(proc.calls)  # type: ignore[attr-defined]
        self.assertTrue(all(call.startswith("--region us-east-1 ssm ") for call in proc.calls))  # type: ignore[attr-defined]


if __name__ == "__main__":
    unittest.main()
