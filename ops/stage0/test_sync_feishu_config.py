"""Exercise production alert sync through a local SSM fixture."""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


class SyncFeishuConfigTest(unittest.TestCase):
    def run_sync(self, status="Success", receipt=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ops = root / "ops" / "stage0"
            ops.mkdir(parents=True)
            shutil.copyfile(Path(__file__).with_name("sync-feishu-config.sh"), ops / "sync.sh")
            (root / "ops" / "lib").mkdir(parents=True)
            shutil.copyfile(Path(__file__).resolve().parents[1] / "lib" / "resolve_app_container.py",
                            root / "ops" / "lib" / "resolve_app_container.py")
            (ops / "resolve_prod_ssm_target.py").write_text(
                'import json\nprint(json.dumps({"instance_id":"i-prod-fixture",'
                '"ssm_region":"ap-northeast-1","target":"aws-fresh"}))\n'
            )
            aws = root / "aws"
            aws.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
with open(os.environ["CALLS"], "a") as handle:
    handle.write(json.dumps(args) + "\\n")
if "send-command" in args:
    params = args[args.index("--parameters") + 1]
    commands = json.loads(pathlib.Path(params.removeprefix("file://")).read_text())["commands"]
    assert len(commands) == 1 and commands[0].startswith("bash -lc ")
    print("cmd-fixture")
elif "wait" in args:
    sys.exit(0 if os.environ["REMOTE_STATUS"] == "Success" else 1)
elif "--query" in args:
    query = args[args.index("--query") + 1]
    if query == "Status": print(os.environ["REMOTE_STATUS"])
    elif query == "StandardOutputContent" and os.environ["RECEIPT"] == "yes":
        print("FEISHU_ENABLED=true FEISHU_WEBHOOK_PRESENT=t FEISHU_SECRET_PRESENT=t")
        print("FEISHU_SYNC_OK=1")
        print("ENV_FEISHU_SYNC_OK=1")
''')
            aws.chmod(0o700)
            calls = root / "calls.jsonl"
            env = {
                **os.environ,
                "PATH": str(root) + os.pathsep + os.environ["PATH"],
                "TK_FEISHU_WEBHOOK_URL": "https://example.test/private-hook",
                "TK_FEISHU_SIGNING_SECRET": "fixture-private-secret",
                "STAGE0_SSM_OUTPUT_DIR": str(root),
                "CALLS": str(calls),
                "REMOTE_STATUS": status,
                "RECEIPT": "yes" if receipt else "no",
            }
            result = subprocess.run(["bash", str(ops / "sync.sh"), "prod"], env=env,
                                    capture_output=True, text=True, check=False)
            requests = [json.loads(line) for line in calls.read_text().splitlines()]
            self.assertNotIn(env["TK_FEISHU_WEBHOOK_URL"], result.stdout + result.stderr)
            self.assertNotIn(env["TK_FEISHU_SIGNING_SECRET"], result.stdout + result.stderr)
            for request in requests:
                self.assertEqual(request[request.index("--region") + 1], "ap-northeast-1")
                flag = "--instance-ids" if "send-command" in request else "--instance-id"
                self.assertEqual(request[request.index(flag) + 1], "i-prod-fixture")
            self.assertTrue(any("wait" in request for request in requests))
            return result

    def test_verified_remote_success(self):
        result = self.run_sync()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("feishu config sync complete", result.stdout)

    def test_remote_script_resolves_layout_exception_postgres(self):
        """Tencent USW keeps tk-archive-standby; do not hard-code tokenkey-postgres only."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ops = root / "ops" / "stage0"
            ops.mkdir(parents=True)
            shutil.copyfile(Path(__file__).with_name("sync-feishu-config.sh"), ops / "sync.sh")
            (root / "ops" / "lib").mkdir(parents=True)
            shutil.copyfile(Path(__file__).resolve().parents[1] / "lib" / "resolve_app_container.py",
                            root / "ops" / "lib" / "resolve_app_container.py")
            (ops / "resolve_prod_ssm_target.py").write_text(
                'import json\nprint(json.dumps({"instance_id":"i-prod-fixture",'
                '"ssm_region":"ap-northeast-1","target":"aws-fresh"}))\n'
            )
            aws = root / "aws"
            aws.write_text(
                "#!/usr/bin/env python3\n"
                "import os, pathlib, sys\n"
                "args = sys.argv[1:]\n"
                "if 'send-command' in args:\n"
                "    print('cmd-fixture')\n"
                "elif 'wait' in args:\n"
                "    raise SystemExit(0)\n"
                "elif '--query' in args:\n"
                "    q = args[args.index('--query') + 1]\n"
                "    if q == 'Status': print('Success')\n"
                "    elif q == 'StandardOutputContent':\n"
                "        print('FEISHU_ENABLED=true FEISHU_WEBHOOK_PRESENT=t "
                "FEISHU_SECRET_PRESENT=t\\nFEISHU_SYNC_OK=1\\nENV_FEISHU_SYNC_OK=1')\n"
            )
            aws.chmod(0o700)
            env = {
                **os.environ,
                "PATH": str(root) + os.pathsep + os.environ["PATH"],
                "TK_FEISHU_WEBHOOK_URL": "https://example.test/private-hook",
                "TK_FEISHU_SIGNING_SECRET": "fixture-private-secret",
                "STAGE0_SSM_OUTPUT_DIR": str(root),
            }
            proc = subprocess.run(
                ["bash", str(ops / "sync.sh"), "prod"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            body = (root / "ssm-params.json").read_text()
            self.assertIn("tk-archive-standby", body)
            self.assertIn("tokenkey-postgres tk-archive-standby", body)
            self.assertNotIn("grep -qx tokenkey-postgres", body)
            self.assertIn("POSTGRES_CONTAINER=", body)
            self.assertIn("$POSTGRES_CONTAINER", body)
            self.assertIn('docker exec \\"$POSTGRES_CONTAINER\\"', body)

    def test_remote_failure_is_not_submission_success(self):
        result = self.run_sync(status="Failed")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Feishu config sync failed", result.stderr)

    def test_missing_verification_receipt_fails(self):
        result = self.run_sync(receipt=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Feishu config sync failed", result.stderr)


if __name__ == "__main__":
    unittest.main()
