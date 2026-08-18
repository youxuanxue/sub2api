import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest


SCRIPT = pathlib.Path(__file__).with_name("probe-edge-health.sh")


class ProbeEdgeHealthTest(unittest.TestCase):
    def test_terminal_only_skips_account_and_docker_log_work(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = pathlib.Path(tmpdir)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            marker = tmp / "non-terminal-docker-call"
            (fakebin / "docker").write_text(
                textwrap.dedent(
                    f"""\
                    #!/usr/bin/env bash
                    if [ "$1" = exec ]; then
                      printf '%s\n' \\
                        'TERMINAL_META {{"schema_version":1,"watermark":"2026-08-18T12:06:00Z"}}' \\
                        'TERMINAL_WINDOW {{"bucket_start":"2026-08-18T12:00:00Z","heartbeat_minutes":5,"producer_epochs":1,"all_complete":true}}'
                      exit 0
                    fi
                    touch '{marker}'
                    exit 2
                    """
                ),
                encoding="utf-8",
            )
            (fakebin / "docker").chmod(0o755)
            result = subprocess.run(
                ["bash", str(SCRIPT)],
                env={**os.environ, "PATH": f"{fakebin}:{os.environ.get('PATH', '')}", "TERMINAL_ONLY": "1"},
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertIn("TERMINAL_META", result.stdout)
            self.assertNotIn("TRAFFIC ", result.stdout)
            self.assertNotIn("ACCT ", result.stdout)
            self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
