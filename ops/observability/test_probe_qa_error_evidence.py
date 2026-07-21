import os
import pathlib
import stat
import subprocess
import tempfile
import textwrap
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops" / "observability" / "probe-qa-error-evidence.sh"


class QAErrorEvidenceProbeTest(unittest.TestCase):
    def run_probe(self, docker_script: str, **env_overrides: str) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as tmp:
            fake_bin = pathlib.Path(tmp)
            docker = fake_bin / "docker"
            docker.write_text(textwrap.dedent(docker_script), encoding="utf-8")
            docker.chmod(docker.stat().st_mode | stat.S_IXUSR)
            env = os.environ.copy()
            env.update(env_overrides)
            env["PATH"] = f"{fake_bin}:{env['PATH']}"
            return subprocess.run(
                ["bash", str(SCRIPT)],
                cwd=ROOT,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

    def test_reports_metadata_replay_and_batched_blob_presence(self) -> None:
        proc = self.run_probe(
            r"""
            #!/bin/sh
            if [ "$1" = "inspect" ]; then
              [ "$2" = "tokenkey-blue" ] && exit 0
              exit 1
            fi
            if [ "$1" = "exec" ] && [ "$2" = "tokenkey-postgres" ]; then
              case "$*" in
                *to_regclass*) echo t ;;
                *distinct_error_request_hashes*) echo '{"distinct_error_request_hashes":2,"hashes_with_success":0}' ;;
                *distinct_request_hashes*) echo '{"error_requests":3,"qa_records":3,"distinct_request_hashes":2}' ;;
                *LATERAL*COALESCE\(capture_status*) echo '{"capture_status":"captured","rows":3}' ;;
                *COALESCE\(q.capture_status*) echo '{"capture_status":"stale-query","rows":99}' ;;
                *request_blob_uri*response_blob_uri*stream_blob_uri*"SELECT DISTINCT refs.request_id, refs.blob_uri"*)
                  printf 'r1\tfile:///app/data/qa_blobs/r1-request.zst\n'
                  printf 'r1\tfile:///app/data/qa_blobs/r1-response.zst\n'
                  printf 'r2\thttps://s3.example/r2.zst\n'
                  ;;
              esac
              exit 0
            fi
            if [ "$1" = "exec" ] && [ "$2" = "-i" ] && [ "$3" = "tokenkey-blue" ]; then
              cat >/dev/null
              printf '1\t1\n'
              exit 0
            fi
            exit 1
            """,
            MODEL="claude-sonnet-4-5",
            REQUEST_PATH="/v1/chat/completions",
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn('"error_requests":3', proc.stdout)
        self.assertIn('"hashes_with_success":0', proc.stdout)
        self.assertIn('"capture_status":"captured"', proc.stdout)
        self.assertIn(
            '{"local_refs":2,"local_present":1,"local_missing":1,"remote_refs":1}',
            proc.stdout,
        )
        self.assertNotIn("qa_blobs/r1", proc.stdout)

    def test_reports_missing_qa_table(self) -> None:
        proc = self.run_probe(
            r"""
            #!/bin/sh
            if [ "$1" = "exec" ] && [ "$2" = "tokenkey-postgres" ]; then
              echo f
              exit 0
            fi
            exit 1
            """
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn('"qa_records_available":false', proc.stdout)

    def test_rejects_invalid_numeric_filter_before_docker(self) -> None:
        proc = self.run_probe("#!/bin/sh\nexit 99\n", WINDOW_MINUTES="1 hour")
        self.assertEqual(proc.returncode, 2)
        self.assertIn("WINDOW_MINUTES must be a non-negative integer", proc.stderr)


if __name__ == "__main__":
    unittest.main()
