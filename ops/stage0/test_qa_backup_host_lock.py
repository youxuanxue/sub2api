#!/usr/bin/env python3
"""Run the host scripts against real kernel flock locks, without a database."""
from __future__ import annotations

import fcntl
import fnmatch
import gzip
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops/qa"))
import test_qa_maintenance_phase2_runtime as runner_fixture  # noqa: E402


class HostLockTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env, self.receipt, self.docker_log, _, _ = runner_fixture.QAPhase2RunnerTest()._sandbox(self.root)
        self.env = {**os.environ, **self.env}
        self.env["PATH"] = str(self.root / "bin") + os.pathsep + os.environ["PATH"]
        self.lock = Path(self.env["QA_LIFECYCLE_LOCK_FILE"])
        self.attempt = self.root / "lock-attempt"
        real_flock = shutil.which("flock")
        self.assertIsNotNone(real_flock)
        # Observe acquisition attempts, then delegate to the real OS utility.
        (self.root / "bin/flock").write_text(
            "#!/bin/bash\n"
            f"touch '{self.attempt}'\n"
            'args=("$@")\n'
            'if [ "${TEST_LOCK_IMMEDIATE:-0}" = 1 ]; then\n'
            '  for ((i=0;i<${#args[@]};i++)); do\n'
            '    if [ "${args[$i]}" = -w ]; then args[$((i+1))]=0; fi\n'
            '  done\nfi\n'
            f'exec "{real_flock}" "${{args[@]}}"\n'
        )
        (self.root / "bin/flock").chmod(0o755)

    def blocked_run(self, script, *, shared, immediate=False):
        with self.lock.open("w") as lease:
            fcntl.flock(lease, fcntl.LOCK_SH if shared else fcntl.LOCK_EX)
            process = subprocess.Popen(["bash", str(script), "--trigger=timer"] if "qa-" in script.name else ["bash", str(script)],
                                       env={**self.env, "TEST_LOCK_IMMEDIATE": "1" if immediate else "0"},
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                deadline = time.monotonic() + 5
                while not self.attempt.exists() and process.poll() is None and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertTrue(self.attempt.exists(), "script never attempted the host lock")
                if immediate:
                    stdout, stderr = process.communicate(timeout=5)
                    self.assertNotEqual(process.returncode, 0, (stdout, stderr))
                else:
                    self.assertIsNone(process.poll(), "script passed a conflicting kernel lock")
                self.assertFalse(self.docker_log.exists(), "database/container work began before acquiring the lock")
                fcntl.flock(lease, fcntl.LOCK_UN)
                if not immediate:
                    stdout, stderr = process.communicate(timeout=10)
                    self.assertEqual(process.returncode, 0, (stdout, stderr))
            finally:
                if process.poll() is None:
                    process.kill()
                    process.communicate()

    def test_archive_waits_for_backup_and_preserves_success_receipt(self):
        self.blocked_run(ROOT / "deploy/aws/stage0/tokenkey-qa-maintenance.sh", shared=True)
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt["runner_exit_code"], 0)
        self.assertEqual(receipt["normal"]["state"], "committed")

    def test_archive_lock_timeout_records_failure_without_container_work(self):
        self.blocked_run(ROOT / "deploy/aws/stage0/tokenkey-qa-maintenance.sh", shared=True, immediate=True)
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt["runner_exit_code"], 75)
        self.assertEqual(receipt["error_code"], "lifecycle_lock_busy")

    def boundary(self):
        self.env.update({
            "QA_BOUNDARY_RUNTIME_DIR": self.env["QA_MAINTENANCE_RUNTIME_DIR"],
            "QA_BOUNDARY_RESOLVER": self.env["QA_MAINTENANCE_RESOLVER"],
            "QA_BOUNDARY_HOST_DATA_ROOT": self.env["QA_MAINTENANCE_HOST_DATA_ROOT"],
            "QA_BOUNDARY_RECEIPT": str(self.receipt),
        })
        child = json.loads(self.env["TEST_CHILD_JSON"])
        child.update(receipt_version=1, job_name="qa-boundary", boundary={"provision": {"ranges_covered": 72}})
        self.env["TEST_CHILD_JSON"] = json.dumps(child)
        return ROOT / "deploy/aws/stage0/tokenkey-qa-boundary.sh"

    def test_boundary_waits_for_backup_before_starting_child(self):
        self.blocked_run(self.boundary(), shared=True)
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt["runner_exit_code"], 0)
        self.assertEqual(receipt["boundary"]["provision"]["ranges_covered"], 72)

    def test_boundary_lock_timeout_records_failure_without_container_work(self):
        self.blocked_run(self.boundary(), shared=True, immediate=True)
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt["runner_exit_code"], 75)
        self.assertEqual(receipt["error_code"], "lifecycle_lock_busy")

    def dump_script(self):
        dumps = self.root / "pgdump"
        dumps.mkdir()
        # Redirect fixed host paths only; execute the real dump and lease code.
        body = (ROOT / "deploy/aws/stage0/tokenkey-pgdump.sh").read_text()
        body = body.replace("DUMP_DIR=/var/lib/tokenkey/pgdump", f"DUMP_DIR={dumps}")
        body = body.replace("/var/lib/tokenkey/.env", str(self.root / "absent.env"))
        body = body.replace("/etc/tokenkey/pgdump-storage.json", str(self.root / "absent-storage.json"))
        script = self.root / "pgdump.sh"
        script.write_text(body)
        docker = self.root / "bin/docker"
        docker.write_text('#!/bin/bash\nprintf "%s\\0" "$@" > "$TEST_DOCKER_LOG"\nhead -c 50000 /dev/urandom\n')
        docker.chmod(0o755)
        return script, dumps

    def test_dump_waits_for_qa_then_writes_valid_recovery_artifact(self):
        script, dumps = self.dump_script()
        self.blocked_run(script, shared=False)
        artifacts = list(dumps.glob("*.sql.gz"))
        self.assertEqual(len(artifacts), 1)
        self.assertEqual(len(gzip.decompress(artifacts[0].read_bytes())), 50000)
        with self.lock.open("w") as lease:
            fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_dump_releases_lease_before_slow_object_upload(self):
        script, dumps = self.dump_script()
        config = self.root / "absent-storage.json"
        config.write_text("{}")
        helper = self.root / "upload.py"
        uploaded = self.root / "uploaded"
        helper.write_text(
            "import fcntl, gzip, os, pathlib, sys\n"
            "with open(os.environ['QA_LIFECYCLE_LOCK_FILE'], 'w') as lease:\n"
            "    fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)\n"
            "    assert len(gzip.decompress(pathlib.Path(sys.argv[2]).read_bytes())) == 50000\n"
            f"    pathlib.Path({str(uploaded)!r}).write_text('upload under exclusive QA lease')\n"
        )
        script.write_text(script.read_text().replace("/usr/local/lib/tokenkey/pgdump_storage.py", str(helper)))
        result = subprocess.run(["bash", str(script)], env=self.env, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(uploaded.read_text(), "upload under exclusive QA lease")
        self.assertEqual(len(list(dumps.glob("*.sql.gz"))), 1)

    def test_dump_includes_reconciliation_detail_and_excludes_operational_rows(self):
        script, _ = self.dump_script()
        result = subprocess.run(["bash", str(script)], env=self.env, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        args = self.docker_log.read_bytes().decode().rstrip("\0").split("\0")
        # Inspect the effective pg_dump invocation, including argument quoting.
        dump_args = args[args.index("-U"):]
        excluded = [arg.split("=", 1)[1] for arg in dump_args if arg.startswith("--exclude-table-data=")]
        self.assertEqual(dump_args, ["-U", "tokenkey", "-d", "tokenkey", "--format=plain", "--no-owner",
                                     *[f"--exclude-table-data={pattern}" for pattern in excluded]])
        cases = {
            "usage_billing_dedup": True,
            "usage_logs": True,
            "usage_logs_20261017": True,
            "usage_logs_default": True,
            "ops_system_logs": False,
            "ops_error_logs_20261017": False,
            "qa_records_default": False,
        }
        for table, included in cases.items():
            with self.subTest(table=table):
                self.assertEqual(not any(fnmatch.fnmatchcase(table, glob) for glob in excluded), included)

    def test_dump_lock_timeout_does_not_prune_existing_backup(self):
        script, dumps = self.dump_script()
        old = dumps / "tokenkey-existing.sql.gz"
        old.write_bytes(b"precious recovery point")
        self.blocked_run(script, shared=False, immediate=True)
        self.assertEqual(old.read_bytes(), b"precious recovery point")
        self.assertEqual(list(dumps.iterdir()), [old])


def load_tests(loader, tests, pattern):
    if shutil.which("flock") is not None:
        return tests

    def linux_tests():
        # macOS has no flock CLI. Exercise the same suite in Linux for both
        # standalone and unittest discovery; never silently skip lock coverage.
        result = subprocess.run([
            "docker", "run", "--rm", "--pull=never", "--network=none",
            "-e", "PYTHONDONTWRITEBYTECODE=1", "-v", f"{ROOT}:/repo:ro", "-w", "/repo",
            "python:3.13-slim", "python3", "ops/stage0/test_qa_backup_host_lock.py",
        ], capture_output=True, text=True, timeout=90)
        if result.returncode:
            raise AssertionError(result.stdout + result.stderr)

    return unittest.TestSuite([unittest.FunctionTestCase(linux_tests)])


if __name__ == "__main__":
    unittest.main()
