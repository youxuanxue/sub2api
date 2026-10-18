#!/usr/bin/env python3
"""Tests for ops/stage0/assert-live-host-state.sh transport glue.

The live-host verdict itself is tested in live_host_state_verdict.py. These tests
stub the AWS CLI so the shell wrapper can be exercised without network access.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

from first_output_timeout_ssot import EXPECTED_ENV

_SCRIPT = pathlib.Path(__file__).resolve().parent / "assert-live-host-state.sh"


class AssertLiveHostStateTransportTest(unittest.TestCase):
    def _run_with_fake_aws(
        self, *, region: str | None = None, expected_tag: str | None = "1.8.40"
    ) -> subprocess.CompletedProcess[str]:
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="assert-live-host-state-"))
        calls = tmp / "aws-calls.txt"
        fake_bin = tmp / "bin"
        fake_bin.mkdir()
        timeout_lines = "\n                ".join(
            "ENV " + json.dumps({"key": key, "value": value})
            for key, value in EXPECTED_ENV.items()
        )
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
                {timeout_lines}
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
        cmd = ["bash", str(_SCRIPT), "i-test"]
        if expected_tag is not None:
            cmd.append(expected_tag)
        proc = subprocess.run(
            cmd,
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

    def test_no_expected_tag_runs_daily_audit_mode_without_unbound_array(self) -> None:
        proc = self._run_with_fake_aws(expected_tag=None)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("OK: live host matches intended state", proc.stdout)
        self.assertNotIn("unbound variable", proc.stderr)

    def test_region_is_passed_before_ssm_subcommand(self) -> None:
        proc = self._run_with_fake_aws(region="us-east-1")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("OK: live host matches intended state", proc.stdout)
        self.assertTrue(proc.calls)  # type: ignore[attr-defined]
        self.assertTrue(all(call.startswith("--region us-east-1 ssm ") for call in proc.calls))  # type: ignore[attr-defined]


class LiveCollectorTest(unittest.TestCase):
    def collect(self, changes):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            values = {**EXPECTED_ENV, 'SERVER_FRONTEND_URL': 'https://api.tokenkey.dev',
                      'PRIVATE_SECRET': 'must-not-be-collected'}
            values.update(changes)
            (root / 'values.json').write_text(json.dumps(values))
            (root / 'docker').write_text('''#!/usr/bin/env python3
import json,os,sys
if sys.argv[1] == 'inspect':
 print('ghcr.io/youxuanxue/sub2api:1.8.40')
else:
 for k,v in json.load(open(os.environ['FIXTURE_ROOT']+'/values.json')).items():
  if v is not None: print(k+'='+v)
''')
            (root / 'aws').write_text('''#!/usr/bin/env python3
import json,os,subprocess,sys,pathlib
args=sys.argv[1:]; out=pathlib.Path(os.environ['FIXTURE_ROOT'])/'facts'
if 'send-command' in args:
 commands=json.loads(args[args.index('--parameters')+1].split('=',1)[1])
 result=subprocess.run(['bash','-c',commands[0]],capture_output=True,text=True,check=True)
 out.write_text(result.stdout); print('cmd-123')
elif args[args.index('--query')+1]=='Status': print('Success')
else: print(out.read_text())
''')
            for name in ('aws','docker'):
                (root / name).chmod(0o755)
            env = {**os.environ, 'PATH': f'{root}:'+os.environ['PATH'],
                   'FIXTURE_ROOT': str(root), 'APP_CONTAINER': 'tokenkey-blue'}
            for key in ('REQUIRE_ENV','EXPECT_ENV'):
                env.pop(key, None)
            result = subprocess.run(['bash',str(_SCRIPT),'i-test','1.8.40'],
                                    env=env,capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            facts=(root/'facts').read_text()
            self.assertNotIn('must-not-be-collected',facts)
            return result.stdout

    def test_actual_remote_collector_matches_verdict_and_detects_kiro_drift(self):
        self.assertIn('OK: live host matches intended state',self.collect({}))
        key='GATEWAY_KIRO_FIRST_OUTPUT_TIMEOUT_SECONDS'
        self.assertIn('missing required env on host: '+key,self.collect({key:None}))
        self.assertIn('required env value differs on host: '+key,self.collect({key:'1'}))

if __name__ == "__main__":
    unittest.main()
