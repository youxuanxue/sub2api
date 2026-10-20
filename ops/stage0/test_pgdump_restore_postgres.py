"""Real PostgreSQL 18 dump/restore, reconciliation and failure cleanup.

Only S3 delivery uses a fixture. Docker, pg_dump, psql and the canary all run;
the source and recovered databases contain synthetic data and have no network.
"""
from __future__ import annotations

import datetime as dt
import gzip
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import time
import unittest
from unittest import mock

import pgdump_restore_canary as canary


@unittest.skipUnless(shutil.which("docker"), "Docker is required for PostgreSQL recovery")
class PostgresRestoreTest(unittest.TestCase):
    def test_real_restore_cost_mismatch_and_timeout_cleanup(self):
        source = f"tk-pgdump-fixture-{os.getpid()}"
        recovered = f"tokenkey-pgdump-canary-{os.getpid()}"

        def command(args, **kwargs):
            return subprocess.run(args, check=True, capture_output=True, text=True,
                                  timeout=90, **kwargs).stdout

        def sql(container, text):
            return command(["docker", "exec", "-i", container, "psql", "-XAt",
                            "-U", "tokenkey", "-d", "tokenkey", "-v", "ON_ERROR_STOP=1"],
                           input=text)

        def remove_source():
            command(["docker", "rm", "-f", source])
            result = subprocess.run(["docker", "inspect", source], capture_output=True,
                                    text=True, timeout=30)
            self.assertTrue(canary._container_missing(result), result.stderr)

        self.addCleanup(remove_source)
        command(["docker", "run", "--detach", "--name", source, "--network=none",
                 "--memory=512m", "--tmpfs", "/var/lib/postgresql:rw,size=268435456",
                 "--env", "POSTGRES_USER=tokenkey", "--env", "POSTGRES_DB=tokenkey",
                 "--env", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:18-alpine"])
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            ready = subprocess.run(["docker", "exec", source, "pg_isready", "-h", "127.0.0.1",
                                    "-U", "tokenkey", "-d", "tokenkey"], capture_output=True, timeout=10)
            if ready.returncode == 0:
                break
            time.sleep(0.2)
        else:
            self.fail("fixture PostgreSQL did not become ready")
        sql(source, "\n".join(f"CREATE TABLE {name}(id bigint); INSERT INTO {name} VALUES (1);"
                              for name in canary.PRECIOUS_TABLES) + """
CREATE TABLE usage_logs(id bigint, created_at timestamptz, total_cost numeric(20,10),
  actual_cost numeric(20,10), note text) PARTITION BY RANGE(created_at);
CREATE TABLE usage_logs_20260818 PARTITION OF usage_logs
  FOR VALUES FROM ('2026-08-18') TO ('2026-08-19');
CREATE TABLE usage_logs_default PARTITION OF usage_logs DEFAULT;
INSERT INTO usage_logs VALUES
  (1,'2026-08-18 07:00:00.1+00',0.1000000001,0.0100000001,E'tab\tand\nnewline'),
  (2,'2026-08-18 16:00:00.123456+09',0.2000000002,0.0200000002,'second');
""")
        dump = command(["docker", "exec", source, "pg_dump", "-U", "tokenkey",
                        "-d", "tokenkey", "--no-owner"])
        archive = gzip.compress(dump.encode())
        current = dt.datetime.now(dt.timezone.utc)
        mode = "success"

        def run(args, **kwargs):
            if args[:3] == ["aws", "s3api", "list-objects-v2"]:
                return subprocess.CompletedProcess(args, 0, json.dumps({"Contents": [{
                    "Key": "prod/pgdump/tokenkey-20260818T070000Z.sql.gz",
                    "LastModified": current.isoformat(), "Size": len(archive)}]}), "")
            if args[:3] == ["aws", "s3", "cp"]:
                pathlib.Path(args[4]).write_bytes(archive)
                return subprocess.CompletedProcess(args, 0, "", "")
            if args[:3] == ["docker", "rm", "-f"] and len(args) > 3 and args[3] == recovered:
                # Production SSM is root; an unprivileged Linux test runner
                # needs ownership of fixture files before host-side cleanup.
                # Only touch a still-running canary; if it already exited, docker
                # rm -f still cleans up and a hard chown failure must not abort
                # the suite (local Docker flake seen 2026-10-20).
                inspect = subprocess.run(
                    ["docker", "inspect", "-f", "{{.State.Running}}", recovered],
                    capture_output=True, text=True, timeout=30,
                )
                if inspect.returncode == 0 and inspect.stdout.strip() == "true":
                    try:
                        command(["docker", "exec", "--user", "0", recovered, "chown", "-R",
                                 f"{os.getuid()}:{os.getgid()}", "/var/lib/postgresql"])
                    except subprocess.CalledProcessError:
                        pass
            if args[:2] == ["bash", "-o"] and mode == "timeout":
                kwargs["timeout"] = 0.5
            if "canary_usage_evidence" in args[-1] and mode == "mismatch":
                self.assertEqual(args[2], recovered)
                sql(recovered, "UPDATE usage_logs SET actual_cost=actual_cost+0.0000000001 WHERE id=1;")
            return canary._default_run(args, **kwargs)

        with tempfile.TemporaryDirectory(prefix="tk-pgdump-test-") as directory:
            root = pathlib.Path(directory)
            env = root / "host.env"
            env.write_text("TOKENKEY_PGDUMP_S3_URI=s3://fixture-bucket/prod/pgdump\n")
            receipt_root = root / "receipts"
            with mock.patch.object(canary, "LIVE_POSTGRES", source):
                def restore():
                    return canary.run_canary("prod", env_path=env, storage_config=root / "no-config",
                                             receipt_root=receipt_root, run=run, now=lambda: current)

                receipt = restore()
                self.assertEqual(receipt["restored_counts"], {name: 1 for name in canary.PRECIOUS_TABLES})
                expected = {
                    "usage_logs_20260818": {"rows": 2, "total_cost": "0.3000000003",
                        "actual_cost": "0.0300000003", "min_created_at": "2026-08-18T07:00:00.100000Z",
                        "max_created_at": "2026-08-18T07:00:00.123456Z"},
                    "usage_logs_default": {"rows": 0, "total_cost": "0", "actual_cost": "0",
                        "min_created_at": None, "max_created_at": None},
                }
                self.assertEqual(receipt["usage_verification"], {"source": expected, "restored": expected})
                previous = (receipt_root / "latest.json").read_bytes()
                for mode, error in (("mismatch", "differ from S3 dump"), ("timeout", "command could not run")):
                    with self.subTest(mode=mode):
                        if mode == "timeout":
                            archive = gzip.compress((dump + "\nSELECT pg_sleep(30);\n").encode())
                        started = time.monotonic()
                        with self.assertRaisesRegex(canary.CanaryError, error):
                            restore()
                        self.assertLess(time.monotonic() - started, 25)
                        self.assertEqual((receipt_root / "latest.json").read_bytes(), previous)
                        self.assertEqual(list(receipt_root.glob(".restore-*")), [])
                        result = subprocess.run(["docker", "inspect", recovered], capture_output=True,
                                                text=True, timeout=30)
                        self.assertTrue(canary._container_missing(result), result.stderr)


if __name__ == "__main__":
    unittest.main()
