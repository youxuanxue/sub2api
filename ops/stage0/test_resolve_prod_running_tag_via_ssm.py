#!/usr/bin/env python3
"""Tests for resolve-prod-running-tag-via-ssm.sh transport and parsing.

The script is an AWS/SSM wrapper, so these tests stub aws + resolve_prod_ssm_target
and return deterministic probe output. No network or live AWS state is used.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "resolve-prod-running-tag-via-ssm.sh"


class ResolveProdRunningTagViaSsmTest(unittest.TestCase):
    def _run(
        self,
        *args: str,
        image: str = "ghcr.io/youxuanxue/sub2api:1.8.91",
        resolve_instance: str = "mi-test0123456789ab",
        resolve_region: str = "eu-west-2",
    ) -> subprocess.CompletedProcess[str]:
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="resolve-prod-running-tag-"))
        calls = tmp / "aws-calls.txt"
        fake_bin = tmp / "bin"
        fake_bin.mkdir()
        fake_resolve = tmp / "resolve_prod_ssm_target.py"
        fake_resolve.write_text(
            textwrap.dedent(
                f"""\
                #!/usr/bin/env python3
                import json
                print(json.dumps({{
                    "instance_id": {resolve_instance!r},
                    "ssm_region": {resolve_region!r},
                    "target": "hetzner",
                }}))
                """
            ),
            encoding="utf-8",
        )
        fake_resolve.chmod(0o755)
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
                  'ssm get-command-invocation --command-id cmd-123 --instance-id {resolve_instance} --query Status --output text'|\
                  'ssm get-command-invocation --command-id cmd-123 --instance-id i-direct --query Status --output text')
                    printf 'Success\\n'
                    ;;
                  'ssm get-command-invocation --command-id cmd-123 --instance-id {resolve_instance} --query StandardOutputContent --output text'|\
                  'ssm get-command-invocation --command-id cmd-123 --instance-id i-direct --query StandardOutputContent --output text')
                    cat <<'OUT'
                ACTIVE_COLOR {{"value":"green"}}
                APPCONTAINER {{"name":"tokenkey-green","requested":"auto","fallback_used":false}}
                RUNIMAGE {{"image":"{image}"}}
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
            "RESOLVE_PROD_SSM_TARGET": str(fake_resolve),
        }
        proc = subprocess.run(
            ["bash", str(_SCRIPT), "--timeout-seconds", "5", *args],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        proc.calls = calls.read_text(encoding="utf-8").splitlines()  # type: ignore[attr-defined]
        return proc

    def test_resolves_via_cutover_helper_and_prints_bare_tag(self) -> None:
        proc = self._run()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(proc.stdout.strip(), "1.8.91")
        self.assertFalse(any("cloudformation describe-stacks" in c for c in proc.calls))  # type: ignore[attr-defined]
        self.assertTrue(any("ssm send-command" in c for c in proc.calls))  # type: ignore[attr-defined]
        self.assertTrue(any("mi-test0123456789ab" in c for c in proc.calls))  # type: ignore[attr-defined]

    def test_instance_id_skips_resolver_and_json_includes_runtime_facts(self) -> None:
        proc = self._run("--instance-id", "i-direct", "--json")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        data = json.loads(proc.stdout)
        self.assertEqual(data["instance_id"], "i-direct")
        self.assertEqual(data["container"], "tokenkey-green")
        self.assertEqual(data["image"], "ghcr.io/youxuanxue/sub2api:1.8.91")
        self.assertEqual(data["tag"], "1.8.91")
        self.assertFalse(any("cloudformation describe-stacks" in c for c in proc.calls))  # type: ignore[attr-defined]

    def test_rejects_mutable_latest_tag(self) -> None:
        proc = self._run(image="ghcr.io/youxuanxue/sub2api:latest")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not a Stage0 release tag", proc.stderr)


if __name__ == "__main__":
    unittest.main()
