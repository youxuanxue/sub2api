#!/usr/bin/env python3
"""Tests for resolve-prod-running-tag-via-ssm.sh transport and parsing."""
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
        env_image: str = "",
        resolve_instance: str = "mi-test0123456789ab",
        resolve_region: str = "eu-west-2",
        transport_rc: int = 0,
    ) -> subprocess.CompletedProcess[str]:
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="resolve-prod-running-tag-"))
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
        fake_exec = tmp / "execute_remote_params.py"
        fake_exec.write_text(
            textwrap.dedent(
                f"""\
                #!/usr/bin/env python3
                import sys
                args = sys.argv[1:]
                stdout_file = None
                for idx, item in enumerate(args):
                    if item == "--stdout-file" and idx + 1 < len(args):
                        stdout_file = args[idx + 1]
                body = '''ACTIVE_COLOR {{"value":"green"}}
                APPCONTAINER {{"name":"tokenkey-green","requested":"auto","fallback_used":false}}
                RUNIMAGE {{"image":"{image}"}}
                ENVIMAGE {{"image":"{env_image}"}}
                '''
                if stdout_file:
                    open(stdout_file, "w", encoding="utf-8").write(body)
                raise SystemExit({transport_rc})
                """
            ),
            encoding="utf-8",
        )
        fake_exec.chmod(0o755)
        env = {
            **os.environ,
            "RESOLVE_PROD_SSM_TARGET": str(fake_resolve),
            "STAGE0_EXECUTE_REMOTE_PARAMS": str(fake_exec),
            "STAGE0_SSM_OUTPUT_DIR": "",
        }
        proc = subprocess.run(
            ["bash", str(_SCRIPT), "--timeout-seconds", "5", *args],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        return proc

    def test_resolves_via_cutover_helper_and_prints_bare_tag(self) -> None:
        proc = self._run()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(proc.stdout.strip(), "1.8.91")

    def test_instance_id_skips_resolver_and_json_includes_runtime_facts(self) -> None:
        proc = self._run("--instance-id", "i-direct", "--json")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        data = json.loads(proc.stdout)
        self.assertEqual(data["instance_id"], "i-direct")
        self.assertEqual(data["container"], "tokenkey-green")
        self.assertEqual(data["image"], "ghcr.io/youxuanxue/sub2api:1.8.91")
        self.assertEqual(data["tag"], "1.8.91")

    def test_rejects_mutable_latest_tag(self) -> None:
        proc = self._run(image="ghcr.io/youxuanxue/sub2api:latest")
        self.assertNotEqual(proc.returncode, 0)

    def test_digest_pinned_runtime_falls_back_to_env_image(self) -> None:
        proc = self._run(
            image="ghcr.io/youxuanxue/sub2api@sha256:deadbeef",
            env_image="ghcr.io/youxuanxue/sub2api:1.8.92",
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(proc.stdout.strip(), "1.8.92")

    def test_digest_without_env_fallback_fails_closed(self) -> None:
        digest = "ghcr.io/youxuanxue/sub2api@sha256:deadbeef"
        proc = self._run(image=digest, env_image="")
        self.assertNotEqual(proc.returncode, 0)


if __name__ == "__main__":
    unittest.main()