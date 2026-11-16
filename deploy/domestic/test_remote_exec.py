#!/usr/bin/env python3
"""Contract tests for deploy/domestic/remote_exec.py (fake runner, no network)."""
from __future__ import annotations

import base64
import contextlib
import io
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import remote_exec as rx  # noqa: E402

MANIFEST = {
    "schema_version": 1,
    "instance_id": "lhins-6qmb3fd6",
    "ssm_region": "eu-west-2",
    "ssm_instance_id": "mi-06568963b4b3c3239",
    "data_lighthouse_instance_id": "lhins-odyx5eis",
    "data_private_ip": "10.2.4.5",
    "data_ssm_region": "eu-west-2",
    "data_ssm_instance_id": "mi-084a456b3dee71bb4",
    "tat_region": "ap-beijing",
    "entry": {"name": "entry", "lighthouse_instance_id": "lhins-j89o28j0", "private_ip": "10.2.4.7"},
    "apps": [
        {"name": "app-b", "lighthouse_instance_id": "lhins-0kxx7cco", "private_ip": "10.2.4.13", "weight": 1},
        {"name": "app-a", "lighthouse_instance_id": "lhins-6qmb3fd6", "private_ip": "10.2.4.11", "weight": 3,
         "ssm_instance_id": "mi-06568963b4b3c3239"},
    ],
}

APP_B = rx.Node("app-b", "app", "lhins-0kxx7cco", "10.2.4.13", None)
APP_A = rx.Node("app-a", "app", "lhins-6qmb3fd6", "10.2.4.11", "mi-06568963b4b3c3239", ssm_region="eu-west-2")


def _b64(text: str | bytes) -> str:
    raw = text.encode() if isinstance(text, str) else text
    return base64.b64encode(raw).decode()


class FakeRunner:
    """Records argv; answers tccli/aws calls from scripted responses."""

    def __init__(self, *, tat_tasks=None, ssm_invocations=None, fail_on=None):
        self.calls: list[tuple[list[str], str | None]] = []
        self.tat_tasks = list(tat_tasks or [])
        self.ssm_invocations = list(ssm_invocations or [])
        self.fail_on = fail_on

    def __call__(self, argv, *, input=None):
        self.calls.append((list(argv), input))
        if self.fail_on and self.fail_on in argv:
            raise subprocess.CalledProcessError(255, argv[:3], output="", stderr="AuthFailure")
        if argv[:3] == ["tccli", "tat", "RunCommand"]:
            return json.dumps({"InvocationId": "inv-abc123", "RequestId": "r"})
        if argv[:3] == ["tccli", "tat", "DescribeInvocationTasks"]:
            task = self.tat_tasks.pop(0) if len(self.tat_tasks) > 1 else self.tat_tasks[0]
            return json.dumps({"InvocationTaskSet": [task] if task else [], "TotalCount": 1})
        if argv[:3] == ["aws", "ssm", "send-command"]:
            return json.dumps({"Command": {"CommandId": "cmd-123"}})
        if argv[:3] == ["aws", "ssm", "get-command-invocation"]:
            inv = self.ssm_invocations.pop(0) if len(self.ssm_invocations) > 1 else self.ssm_invocations[0]
            return json.dumps(inv)
        raise AssertionError(f"unexpected argv {argv[:3]}")

    def channels(self) -> set[str]:
        return {argv[0] for argv, _ in self.calls}

    def tat_content(self) -> str:
        argv = next(a for a, _ in self.calls if a[:3] == ["tccli", "tat", "RunCommand"])
        return base64.b64decode(argv[argv.index("--Content") + 1]).decode()


def tat_task(status: str, exit_code=None, output: str | bytes = "") -> dict:
    result = {"Output": _b64(output) if output else ""}
    if exit_code is not None:
        result["ExitCode"] = exit_code
    return {"InvocationTaskId": "invt-1", "TaskStatus": status, "TaskResult": result}


def no_sleep(_):
    pass


class ManifestTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)

    def write(self, data) -> pathlib.Path:
        p = pathlib.Path(self._tmp.name) / "prod-target.json"
        p.write_text(json.dumps(data))
        return p

    def test_loads_entry_apps_data(self):
        nodes = rx.load_nodes(self.write(MANIFEST))
        self.assertEqual(list(nodes), ["entry", "app-b", "app-a", "data"])
        self.assertEqual(nodes["entry"].role, "entry")
        self.assertEqual(nodes["app-a"].ssm_instance_id, "mi-06568963b4b3c3239")
        self.assertIsNone(nodes["app-b"].ssm_instance_id)
        self.assertEqual(nodes["data"].private_ip, "10.2.4.5")
        self.assertEqual(nodes["data"].ssm_instance_id, "mi-084a456b3dee71bb4")
        self.assertEqual({n.tat_region for n in nodes.values()}, {"ap-beijing"})

    def test_app_nodes_keep_manifest_order(self):
        self.assertEqual([n.name for n in rx.app_nodes(self.write(MANIFEST))], ["app-b", "app-a"])

    def test_missing_apps_is_explicit(self):
        data = {k: v for k, v in MANIFEST.items() if k != "apps"}
        with self.assertRaisesRegex(rx.RemoteExecError, "'apps' list missing"):
            rx.load_nodes(self.write(data))

    def test_empty_apps_is_explicit(self):
        with self.assertRaisesRegex(rx.RemoteExecError, "'apps' list missing or empty"):
            rx.load_nodes(self.write({**MANIFEST, "apps": []}))

    def test_missing_entry_is_explicit(self):
        data = {k: v for k, v in MANIFEST.items() if k != "entry"}
        with self.assertRaisesRegex(rx.RemoteExecError, "'entry' object missing"):
            rx.load_nodes(self.write(data))

    def test_missing_tat_region(self):
        data = {k: v for k, v in MANIFEST.items() if k != "tat_region"}
        with self.assertRaisesRegex(rx.RemoteExecError, "tat_region"):
            rx.load_nodes(self.write(data))

    def test_prod_data_machine_nested_flattens(self):
        data = {
            "schema_version": 1,
            "tat_region": "na-siliconvalley",
            "ssm_region": "eu-west-2",
            "entry": {"name": "entry", "lighthouse_instance_id": "lhins-entry",
                      "private_ip": "10.4.0.8", "ssm_instance_id": "mi-" + "a" * 17},
            "apps": [
                {"name": "app-cand", "lighthouse_instance_id": "lhins-cand",
                 "private_ip": "10.4.0.9", "ssm_instance_id": "mi-" + "b" * 17},
                {"name": "app-old", "lighthouse_instance_id": "lhins-old",
                 "private_ip": "10.4.0.16", "ssm_instance_id": "mi-" + "c" * 17},
            ],
            "data_machine": {
                "lighthouse_instance_id": "lhins-data",
                "private_ip": "10.4.0.3",
                "expected_ssm_instance_id": "mi-" + "d" * 17,
                "ssm_region": "eu-west-2",
            },
        }
        nodes = rx.load_nodes(self.write(data))
        self.assertEqual(nodes["data"].private_ip, "10.4.0.3")
        self.assertEqual(nodes["data"].ssm_instance_id, "mi-" + "d" * 17)
        self.assertEqual(nodes["app-cand"].tat_region, "na-siliconvalley")
        self.assertEqual([n.name for n in rx.app_nodes(self.write(data))],
                         ["app-cand", "app-old"])

    def test_rejects_duplicate_ip_and_bad_ip(self):
        apps = [dict(MANIFEST["apps"][0]), dict(MANIFEST["apps"][1], private_ip="10.2.4.13")]
        with self.assertRaisesRegex(rx.RemoteExecError, "unique"):
            rx.load_nodes(self.write({**MANIFEST, "apps": apps}))
        apps = [dict(MANIFEST["apps"][0], private_ip="10.2.4.13; reboot")]
        with self.assertRaisesRegex(rx.RemoteExecError, "not an IP"):
            rx.load_nodes(self.write({**MANIFEST, "apps": apps}))

    def test_repo_manifest_parses_once_fields_land(self):
        data = json.loads(rx.DEFAULT_MANIFEST.read_text())
        if "apps" not in data or "entry" not in data:
            self.skipTest("prod-target.json has no entry/apps yet")
        nodes = rx.load_nodes()
        self.assertIn("entry", nodes)
        self.assertIn("data", nodes)


class TatRunTest(unittest.TestCase):
    def test_success_decodes_output(self):
        runner = FakeRunner(tat_tasks=[tat_task("RUNNING"), tat_task("SUCCESS", 0, "hello\n")])
        res = rx.run(APP_B, "echo hello", runner=runner, sleep=no_sleep)
        self.assertEqual(res, rx.Result("tat", "Success", 0, "hello\n", ""))
        argv = runner.calls[0][0]
        self.assertEqual(argv[argv.index("--region") + 1], "ap-beijing")
        self.assertEqual(argv[argv.index("--CommandType") + 1], "SHELL")
        self.assertEqual(json.loads(argv[argv.index("--InstanceIds") + 1]), ["lhins-0kxx7cco"])
        self.assertEqual(argv[argv.index("--Timeout") + 1], "600")
        self.assertIn("--CommandName", argv)
        describe = runner.calls[1][0]
        self.assertEqual(describe[describe.index("--HideOutput") + 1], "False")
        self.assertIn("inv-abc123", describe[describe.index("--Filters") + 1])

    def test_failed_exit_code(self):
        runner = FakeRunner(tat_tasks=[tat_task("FAILED", 3, "boom")])
        res = rx.run(APP_B, "exit 3", runner=runner, sleep=no_sleep)
        self.assertEqual((res.status, res.exit_code, res.stdout_tail), ("Failed", 3, "boom"))

    def test_remote_timeout_status(self):
        runner = FakeRunner(tat_tasks=[tat_task("TIMEOUT", -1)])
        res = rx.run(APP_B, "sleep 999", timeout=5, runner=runner, sleep=no_sleep)
        self.assertEqual(res.status, "TimedOut")

    def test_poll_timeout_never_switches_to_ssm(self):
        runner = FakeRunner(tat_tasks=[tat_task("RUNNING")])
        sleeps = []
        res = rx.run(APP_A, "sleep 999", timeout=10, runner=runner, sleep=sleeps.append)
        self.assertEqual((res.channel, res.status, res.exit_code), ("tat", "TimedOut", None))
        self.assertEqual(runner.channels(), {"tccli"})
        run_commands = [a for a, _ in runner.calls if a[:3] == ["tccli", "tat", "RunCommand"]]
        self.assertEqual(len(run_commands), 1, "must not resubmit")
        self.assertEqual(len(sleeps), rx._poll_budget(10))

    def test_dispatch_error_raises_without_leaking_script(self):
        runner = FakeRunner(fail_on="RunCommand")
        with self.assertRaises(rx.RemoteExecError) as ctx:
            rx.run(APP_B, "echo TOP-SECRET-BODY", runner=runner, sleep=no_sleep)
        self.assertNotIn("TOP-SECRET-BODY", str(ctx.exception))
        self.assertIn("AuthFailure", str(ctx.exception))

    def test_identity_mismatch_raises(self):
        runner = FakeRunner(tat_tasks=[tat_task("FAILED", 97, "remote_exec: identity mismatch")])
        with self.assertRaisesRegex(rx.RemoteExecError, "identity mismatch"):
            rx.run(APP_B, "echo hi", runner=runner, sleep=no_sleep)

    def test_preamble_and_heredoc_in_content(self):
        runner = FakeRunner(tat_tasks=[tat_task("SUCCESS", 0)])
        rx.run(APP_B, "echo payload", runner=runner, sleep=no_sleep)
        content = runner.tat_content()
        self.assertIn("hostname -I", content)
        self.assertIn("grep -qxF 10.2.4.13", content)
        self.assertIn("exit 97", content)
        self.assertIn(f"<<'{rx.HEREDOC_DELIMITER}'", content)
        self.assertLess(content.index("hostname -I"), content.index("echo payload"))

    def test_output_truncated_to_last_8kb(self):
        big = "A" * 20000 + "TAIL-MARKER"
        runner = FakeRunner(tat_tasks=[tat_task("SUCCESS", 0, big)])
        res = rx.run(APP_B, "x", runner=runner, sleep=no_sleep)
        self.assertEqual(len(res.stdout_tail.encode()), rx.OUTPUT_TAIL_BYTES)
        self.assertTrue(res.stdout_tail.endswith("TAIL-MARKER"))

    def test_content_over_limit_rejected_before_dispatch(self):
        runner = FakeRunner(tat_tasks=[tat_task("SUCCESS", 0)])
        script = "#" * (rx.TAT_MAX_CONTENT_B64 * 3 // 4 + 10)
        with self.assertRaisesRegex(rx.RemoteExecError, "over the 65536 limit"):
            rx.run(APP_B, script, runner=runner, sleep=no_sleep)
        self.assertEqual(runner.calls, [])

    def test_bad_via_and_timeout(self):
        with self.assertRaises(rx.RemoteExecError):
            rx.run(APP_B, "x", via="ssh", runner=FakeRunner())
        with self.assertRaises(rx.RemoteExecError):
            rx.run(APP_B, "x", timeout=0, runner=FakeRunner())


class SsmRunTest(unittest.TestCase):
    def test_explicit_ssm(self):
        runner = FakeRunner(ssm_invocations=[
            {"Status": "InProgress"},
            {"Status": "Success", "ResponseCode": 0, "StandardOutputContent": "ok\n", "StandardErrorContent": "warn"},
        ])
        res = rx.run(APP_A, "echo ok", via="ssm", runner=runner, sleep=no_sleep)
        self.assertEqual(res, rx.Result("ssm", "Success", 0, "ok\n", "warn"))
        self.assertEqual(runner.channels(), {"aws"})
        argv = runner.calls[0][0]
        self.assertEqual(argv[argv.index("--instance-ids") + 1], "mi-06568963b4b3c3239")
        self.assertEqual(argv[argv.index("--region") + 1], "eu-west-2")
        params = json.loads(argv[argv.index("--parameters") + 1])
        cmd = params["commands"][0]
        self.assertIn(f"<<'{rx.HEREDOC_DELIMITER}'", cmd)
        self.assertIn("grep -qxF 10.2.4.11", cmd)

    def test_ssm_failed(self):
        runner = FakeRunner(ssm_invocations=[{"Status": "Failed", "ResponseCode": 2, "StandardErrorContent": "e"}])
        res = rx.run(APP_A, "exit 2", via="ssm", runner=runner, sleep=no_sleep)
        self.assertEqual((res.status, res.exit_code), ("Failed", 2))

    def test_ssm_identity_mismatch(self):
        runner = FakeRunner(ssm_invocations=[{"Status": "Failed", "ResponseCode": 97}])
        with self.assertRaisesRegex(rx.RemoteExecError, "identity mismatch"):
            rx.run(APP_A, "x", via="ssm", runner=runner, sleep=no_sleep)

    def test_node_without_ssm_raises(self):
        runner = FakeRunner()
        with self.assertRaisesRegex(rx.RemoteExecError, "no ssm_instance_id"):
            rx.run(APP_B, "echo hi", via="ssm", runner=runner, sleep=no_sleep)
        self.assertEqual(runner.calls, [])


class PutFileTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = pathlib.Path(self._tmp.name)

    def local(self, data: bytes) -> pathlib.Path:
        p = self.dir / "payload.sh"
        p.write_bytes(data)
        return p

    def test_size_limit(self):
        p = self.local(b"x" * (rx.PUT_FILE_MAX_BYTES + 1))
        runner = FakeRunner()
        with self.assertRaisesRegex(rx.RemoteExecError, "put_file limit"):
            rx.put_file(APP_B, p, "/opt/tokenkey/x.sh", runner=runner)
        self.assertEqual(runner.calls, [])

    def test_path_roots(self):
        p = self.local(b"echo hi\n")
        for bad in ("/etc/passwd", "/opt/tokenkeyX/a", "relative/a", "/opt/tokenkey/", "/tmp/a"):
            with self.subTest(bad=bad), self.assertRaises(rx.RemoteExecError):
                rx.put_file(APP_B, p, bad, runner=FakeRunner())

    def test_dotdot_rejected(self):
        p = self.local(b"echo hi\n")
        for bad in ("/opt/tokenkey/../etc/shadow", "/var/lib/tokenkey/a..b", "/private/x/../../root"):
            with self.subTest(bad=bad), self.assertRaisesRegex(rx.RemoteExecError, r"\.\."):
                rx.put_file(APP_B, p, bad, runner=FakeRunner())

    def test_bad_mode(self):
        p = self.local(b"echo hi\n")
        with self.assertRaises(rx.RemoteExecError):
            rx.put_file(APP_B, p, "/opt/tokenkey/a", mode="777; rm -rf /", runner=FakeRunner())

    def test_dispatches_one_tat_command(self):
        p = self.local(b"echo hi\n")
        runner = FakeRunner(tat_tasks=[tat_task("SUCCESS", 0, "put_file ok")])
        res = rx.put_file(APP_B, p, "/var/lib/tokenkey/app/x.sh", mode="700", runner=runner)
        self.assertEqual(res.status, "Success")
        self.assertEqual(sum(a[:3] == ["tccli", "tat", "RunCommand"] for a, _ in runner.calls), 1)
        content = runner.tat_content()
        self.assertIn("sha256sum", content)
        self.assertIn('install -m "$MODE"', content)
        self.assertIn("DEST=/var/lib/tokenkey/app/x.sh", content)


@unittest.skipUnless(shutil.which("bash") and shutil.which("sha256sum"), "needs bash + sha256sum")
class GeneratedShellTest(unittest.TestCase):
    """Run the generated shell locally to prove its behavior, not just its text."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = pathlib.Path(self._tmp.name)
        bindir = self.dir / "bin"
        bindir.mkdir()
        fake = bindir / "hostname"
        fake.write_text('#!/bin/sh\n[ "$1" = "-I" ] && echo "$FAKE_IPS"\n')
        fake.chmod(0o755)
        self.env = {**os.environ, "PATH": f"{bindir}:{os.environ['PATH']}"}

    def sh(self, command: str, ips: str) -> subprocess.CompletedProcess:
        return subprocess.run(["/bin/sh", "-c", command], env={**self.env, "FAKE_IPS": ips},
                              capture_output=True, text=True, timeout=30)

    def test_identity_gate(self):
        cmd = rx.build_remote_command(APP_B, "echo ran-$((1+1))\nread x || echo stdin-closed\n")
        ok = self.sh(cmd, "10.2.4.13 172.17.0.1 ")
        self.assertEqual(ok.returncode, 0, ok.stderr)
        self.assertEqual(ok.stdout, "ran-2\nstdin-closed\n")
        bad = self.sh(cmd, "10.2.4.130 10.2.4.11")
        self.assertEqual(bad.returncode, 97)
        self.assertNotIn("ran-", bad.stdout)
        self.assertIn("identity mismatch", bad.stderr)

    def test_heredoc_keeps_script_verbatim(self):
        script = "X='$HOME `id` $(id)'\nprintf '%s\\n' \"$X\"\ncat <<'EOF'\n$literal\nEOF\n"
        res = self.sh(rx.build_remote_command(APP_B, script), "10.2.4.13")
        self.assertEqual(res.stdout, "$HOME `id` $(id)\n$literal\n", res.stderr)

    def test_delimiter_line_rejected(self):
        with self.assertRaises(rx.RemoteExecError):
            rx.build_remote_command(APP_B, f"echo\n{rx.HEREDOC_DELIMITER}\nrm -rf /\n")

    def test_put_file_writes_and_verifies(self):
        dest = self.dir / "out" / "a.sh"
        payload = bytes(range(256)) * 10 + "中文\n".encode()
        res = self.sh(rx.build_put_file_script(str(dest), payload, "640"), "")
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(dest.read_bytes(), payload)
        self.assertEqual(oct(dest.stat().st_mode & 0o777), "0o640")
        self.assertEqual([p.name for p in dest.parent.iterdir()], ["a.sh"], "temp files left behind")

    def test_put_file_sha_mismatch_keeps_target(self):
        dest = self.dir / "a.sh"
        dest.write_text("original\n")
        script = rx.build_put_file_script(str(dest), b"new content\n", "600")
        good = __import__("hashlib").sha256(b"new content\n").hexdigest()
        script = script.replace(f"WANT={good}", "WANT=" + "0" * 64)
        res = self.sh(script, "")
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("sha256 mismatch", res.stderr)
        self.assertEqual(dest.read_text(), "original\n")
        self.assertEqual([p.name for p in self.dir.iterdir() if p.name != "bin"], ["a.sh"])


class CliTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = pathlib.Path(self._tmp.name)
        self.manifest = self.dir / "m.json"
        self.manifest.write_text(json.dumps(MANIFEST))
        self.script = self.dir / "s.sh"
        self.script.write_text("echo hi\n")

    def cli(self, *args, runner=None):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = rx.main(["--manifest", str(self.manifest), *args], runner=runner)
        return rc, out.getvalue(), err.getvalue()

    def test_nodes_table(self):
        rc, out, _ = self.cli("nodes")
        self.assertEqual(rc, 0)
        for token in ("entry", "app-b", "app-a", "data", "10.2.4.13", "lhins-j89o28j0"):
            self.assertIn(token, out)
        self.assertNotIn("SECRET", out.upper())

    def test_run_failure_is_nonzero(self):
        rx_sleep = rx.time.sleep
        rx.time.sleep = no_sleep
        self.addCleanup(setattr, rx.time, "sleep", rx_sleep)
        rc, _, _ = self.cli("run", "--node", "app-b", "--script", str(self.script),
                            runner=FakeRunner(tat_tasks=[tat_task("FAILED", 1)]))
        self.assertEqual(rc, 1)

    def test_run_ssm_without_ssm_node_is_nonzero(self):
        rc, _, err = self.cli("run", "--node", "app-b", "--script", str(self.script), "--via", "ssm",
                              runner=FakeRunner())
        self.assertEqual(rc, 2)
        self.assertIn("no ssm_instance_id", err)

    def test_unknown_node_and_bad_remote(self):
        self.assertEqual(self.cli("run", "--node", "app-z", "--script", str(self.script))[0], 2)
        rc, _, err = self.cli("put-file", "--node", "app-b", "--local", str(self.script), "--remote", "/etc/x")
        self.assertEqual(rc, 2)
        self.assertIn("permitted roots", err)


if __name__ == "__main__":
    unittest.main()
