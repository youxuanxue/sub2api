#!/usr/bin/env python3
"""Behavior tests for deploy/domestic/rolling.py (design §4/§5, 验收).

A fake Executor parses each agent invocation and simulates the node; no
remote module is imported and nothing touches the network.
"""
from __future__ import annotations

import contextlib
import datetime as dt
import io
import json
import shlex
import tempfile
import unittest
from collections import namedtuple
from pathlib import Path

from deploy.domestic import rolling

Node = namedtuple("Node", "name role lighthouse_instance_id private_ip ssm_instance_id")
Result = namedtuple("Result", "channel status exit_code stdout_tail stderr_tail")

TAG = "1.8.380"
REV = "a" * 40
OLD_TAG = "1.8.377"
OLD_REV = "b" * 40
NEW_ID = "sha256:" + "1" * 64
OLD_ID = "sha256:" + "0" * 64
NEW_REF = rolling.local_tag(TAG, REV)
OLD_REF = rolling.local_tag(OLD_TAG, OLD_REV)

APP_B = Node("app-b", "app", "lhins-b", "10.2.4.13", None)
APP_A = Node("app-a", "app", "lhins-a", "10.2.4.11", "mi-a")
ENTRY = Node("entry", "entry", "lhins-e", "10.2.4.7", None)
CONFIRM = f"rolling-cn-prod-{TAG}"


class FakeHost:
    def __init__(self, node, *, image_id=OLD_ID, health=200, target_version=TAG):
        self.node = node
        self.image_id, self.health, self.running = image_id, health, True
        self.pin = OLD_REF if image_id == OLD_ID else NEW_REF
        self.backup = None
        self.marker = False
        self.in_flight = 0
        self.drain_ok = True
        self.drain_ok_after_up = True
        self.cpu, self.mem, self.mem_limit = 10.0, 5.0, 3 << 30
        self.containers = {"tokenkey": "running"}
        self.socat = False
        self.compose_node = True
        self.cred_drift: list = []
        self.images = {OLD_REF: {"image_id": OLD_ID, "architecture": "amd64", "version": OLD_TAG, "revision": OLD_REV},
                       NEW_REF: {"image_id": NEW_ID, "architecture": "amd64", "version": target_version,
                                 "revision": REV}}
        self.receipt_id = NEW_ID
        self.smoke_fail_for: set = set()
        self.new_ignores_marker = False
        self.up_fail = False
        self.traffic = True
        self.ops: list = []

    def image_by_id(self, image_id):
        return next((v for v in self.images.values() if v["image_id"] == image_id), None)

    def probe(self, args):
        cur = self.image_by_id(self.image_id)
        facts = {
            "compose_files": {n: (n != "compose.node.yml" or self.compose_node) for n in rolling.COMPOSE_FILES},
            "env": {"exists": True, "mode": "600", "pins": [self.pin]},
            "marker": self.marker,
            "containers": dict(self.containers),
            "container": {
                "image_ref": self.pin, "image_id": self.image_id, "running": self.running, "socat": self.socat,
                "project": "tokenkey", "config_files": "", "data_mount": True, "memory_limit": self.mem_limit,
                "ports": sorted([f"{self.node.private_ip}:8080", "127.0.0.1:18080"]),
            },
            "current_image": dict(cur) if cur else None,
            "pin_image_id": self.images.get(self.pin, {}).get("image_id"),
            "health": self.health if self.running else 0, "running": self.running,
            "cpu_percent": self.cpu, "mem_percent": self.mem, "memory_limit": self.mem_limit,
            "inflight": {"in_flight": self.in_flight, "draining": self.health == 503},
            "target": None, "backup": None, "credential_drift": list(self.cred_drift), "compose_error": None,
        }
        if args.get("--tag"):
            t = self.images.get(NEW_REF)
            if t:
                facts["target"] = {**t, "local_tag": NEW_REF, "receipt_image_id": self.receipt_id,
                                   "receipt_local_tag": NEW_REF}
            if self.backup:
                b = self.images[self.backup]
                facts["backup"] = {"pin": self.backup, "image_id": b["image_id"], "revision": b["revision"],
                                   "version": b["version"]}
        return facts

    def handle(self, op, argv):
        a = {}
        i = 0
        while i < len(argv):
            if argv[i].startswith("--"):
                if i + 1 < len(argv) and not argv[i + 1].startswith("--"):
                    a[argv[i]] = argv[i + 1]
                    i += 2
                    continue
                a[argv[i]] = True
            i += 1
        self.ops.append(op if op != "signal" else f"signal:{a['--signal']}")
        if op == "probe":
            return self.probe(a)
        if op == "vitals":
            return {"health": self.health if self.running else 0, "running": self.running,
                    "cpu_percent": self.cpu, "mem_percent": self.mem, "memory_limit": self.mem_limit}
        if op == "signal":
            if a["--signal"] == "USR1":
                self.health = 503
            return {"signal": a["--signal"]}
        if op == "drain":
            self.health = 503
            ok = self.drain_ok if self.image_id == OLD_ID or not self.ops.count("up") else self.drain_ok_after_up
            return {"drained": ok, "health": 503, "in_flight": 0 if ok else 4, "tries": 3}
        if op == "repin":
            if self.pin != NEW_REF:
                self.backup = self.pin
            self.pin = NEW_REF
            return {"backup": "x", "old_ref": self.backup, "new_ref": NEW_REF}
        if op == "restore":
            assert a["--expect-ref"] == self.backup, (a, self.backup)
            self.pin = self.backup
            return {"restored": "x", "ref": self.pin}
        if op == "marker":
            self.marker = not a.get("--remove")
            return {"marker": self.marker}
        if op == "up":
            if self.up_fail and self.pin == NEW_REF:
                self.running = False
                return {"ok": False, "running": False, "health": 0, "image_id": None}
            self.image_id = self.images[self.pin]["image_id"]
            self.running = True
            ignores = self.new_ignores_marker and self.image_id == NEW_ID
            self.health = 503 if self.marker and not ignores else 200
            return {"ok": True, "running": True, "health": self.health, "image_id": self.image_id}
        if op == "smoke":
            passed = self.image_id not in self.smoke_fail_for
            return {"passed": passed, "failed": [] if passed else ["tokenkey /api/v1/settings/public"],
                    "report": "r"}
        if op == "release":
            self.health, self.marker = 200, False
            return {"health": 200, "at": 1_800_000_000}
        if op == "traffic":
            return {"seen": self.traffic, "count": 5 if self.traffic else 0}
        raise AssertionError(op)


class FakeEntry:
    def __init__(self, path: Path, now):
        self.path, self.now, self.ops = path, now, []

    def handle(self, op, argv):
        assert op == "lease", op
        action = argv[0]
        holder = argv[argv.index("--holder") + 1]
        self.ops.append(action)
        return rolling.lease_op(action, holder, rolling.LEASE_TTL, "--take-over" in argv, self.path, self.now)


class FakeExecutor:
    def __init__(self, hosts: dict):
        self.hosts = hosts
        self.scripts: list = []
        self.puts: list = []

    def run(self, node, script, *, timeout):
        self.scripts.append((node.name, script))
        lines = [ln for ln in script.splitlines() if ln.startswith("python3 ")]
        if not lines:
            return Result("tat", "Success", 0, "", "")
        argv = shlex.split(lines[-1])
        assert argv[:3] == ["python3", str(rolling.AGENT_PATH), "agent"], argv
        out = self.hosts[node.name].handle(argv[3], argv[4:])
        return Result("tat", "Success", 0, rolling.RESULT_PREFIX + json.dumps(out), "")

    def put_file(self, node, local, remote, *, mode="600"):
        self.puts.append((node.name, remote, mode))
        return Result("tat", "Success", 0, "", "")


class RollingTestBase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.clock = [dt.datetime(2026, 11, 15, 8, 0, tzinfo=dt.timezone.utc)]
        self.b, self.a = FakeHost(APP_B), FakeHost(APP_A)
        self.entry = FakeEntry(Path(self.tmp.name) / "rolling-deploy.lease", lambda: self.clock[0])
        self.executor = FakeExecutor({"app-b": self.b, "app-a": self.a, "entry": self.entry})
        self.receipt = Path(self.tmp.name) / "receipt.json"
        self.nodes = [APP_B, APP_A]

    def main(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = rolling.main(list(argv), executor=self.executor, nodes=self.nodes, entry=ENTRY)
        self.out, self.err = out.getvalue(), err.getvalue()
        return code

    def deploy(self, *extra):
        return self.main("deploy", "--tag", TAG, "--revision", REV, "--confirm", CONFIRM,
                         "--receipt-out", str(self.receipt), *extra)

    def receipt_json(self):
        return json.loads(self.receipt.read_text())


class HappyPathTest(RollingTestBase):
    def test_rolls_app_b_then_app_a_with_full_state_machine(self):
        self.assertEqual(self.deploy(), 0, self.out + self.err)
        steps = ["drain", "repin", "marker", "up", "smoke", "release", "traffic"]
        for host in (self.b, self.a):
            self.assertEqual(host.image_id, NEW_ID)
            self.assertEqual(host.health, 200)
            self.assertEqual([op for op in host.ops if op in steps], steps)
        names = [n for n, s in self.executor.scripts if "agent drain" in s]
        self.assertEqual(names, ["app-b", "app-a"])
        r = self.receipt_json()
        self.assertEqual(r["outcome"], "success")
        self.assertEqual([(n["name"], n["before_image_id"], n["after_image_id"], n["outcome"]) for n in r["nodes"]],
                         [("app-b", OLD_ID, NEW_ID, "rolled"), ("app-a", OLD_ID, NEW_ID, "rolled")])
        self.assertTrue(all("seconds" in s for n in r["nodes"] for s in n["steps"]))
        self.assertEqual(self.receipt.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.entry.ops, ["acquire", "renew", "renew", "release"])
        self.assertFalse(self.entry.path.exists())

    def test_up_runs_drained_and_smoke_expects_draining(self):
        self.deploy()
        smoke = [s for n, s in self.executor.scripts if " smoke " in s][0]
        self.assertIn("--expect-draining", smoke)
        self.assertIn("--revision " + REV, smoke)

    def test_remote_scripts_are_quoted_and_verify_agent_hash(self):
        self.deploy()
        for _, script in self.executor.scripts:
            self.assertNotIn(".env", script.replace(".env.before", ""))
            self.assertNotRegex(script, r"\becho\b|\bcat\b|\benv\b|printenv")
            if "agent" in script:
                self.assertIn("sha256sum -c", script)
        self.assertIn(("app-b", str(rolling.AGENT_PATH) + ".gz", "600"), self.executor.puts)

    def test_shipped_agent_fits_remote_exec_put_file_limit(self):
        import gzip
        for name in ("rolling.py", "candidate_smoke.py"):
            raw = (Path(rolling.__file__).parent / name).read_bytes()
            self.assertLess(len(gzip.compress(raw, mtime=0)), 48 * 1024, name)

    def test_already_on_target_and_serving_is_skipped(self):
        self.b.image_id, self.b.pin = NEW_ID, NEW_REF
        self.assertEqual(self.deploy(), 0, self.out)
        self.assertNotIn("drain", self.b.ops)
        self.assertEqual(self.receipt_json()["nodes"][0]["outcome"], "skipped")

    def test_receipt_has_no_secrets(self):
        self.deploy()
        text = self.receipt.read_text()
        self.assertNotIn("PASSWORD", text)
        self.assertNotIn("SECRET", text)


class PlanStatusTest(RollingTestBase):
    def test_plan_is_default_and_read_only(self):
        code = self.main("--tag", TAG, "--revision", REV)
        self.assertEqual(code, 0, self.out + self.err)
        self.assertIn("app-b (10.2.4.13): current=000000000000 target=111111111111", self.out)
        self.assertIn("drain -> repin -> marker -> up -> smoke -> release -> traffic", self.out)
        mutating = {"drain", "repin", "marker", "up", "signal:USR1", "signal:USR2", "release", "restore"}
        self.assertFalse(mutating & set(self.b.ops + self.a.ops))
        self.assertEqual(self.entry.ops, [])

    def test_status_reads_lease(self):
        self.assertEqual(self.main("status"), 0)
        self.assertIn("lease: None", self.out)


class ConfirmTest(RollingTestBase):
    def test_deploy_without_confirm_is_refused(self):
        code = self.main("deploy", "--tag", TAG, "--revision", REV, "--receipt-out", str(self.receipt))
        self.assertEqual(code, rolling.EXIT_USAGE)
        self.assertIn(f"--confirm rolling-cn-prod-{TAG}", self.err)
        self.assertEqual(self.executor.scripts, [])

    def test_wrong_confirm_is_refused(self):
        code = self.main("deploy", "--tag", TAG, "--revision", REV, "--confirm", "rolling-cn-prod-1.8.379",
                         "--receipt-out", str(self.receipt))
        self.assertEqual(code, rolling.EXIT_USAGE)
        self.assertEqual(self.executor.scripts, [])

    def test_v_prefix_tag_normalizes(self):
        code = self.main("deploy", "--tag", "v" + TAG, "--revision", REV, "--confirm", CONFIRM,
                         "--receipt-out", str(self.receipt))
        self.assertEqual(code, 0, self.out + self.err)


class DrainTimeoutTest(RollingTestBase):
    def test_drain_timeout_releases_old_and_stops_before_any_change(self):
        self.b.drain_ok = False
        self.assertEqual(self.deploy(), 1)
        self.assertEqual(self.b.image_id, OLD_ID)
        self.assertEqual(self.b.pin, OLD_REF)
        self.assertEqual(self.b.health, 200)
        self.assertNotIn("repin", self.b.ops)
        self.assertNotIn("drain", self.a.ops)
        r = self.receipt_json()
        self.assertEqual(r["nodes"][0]["outcome"], "drain-timeout")
        self.assertTrue(r["manual_steps"])
        self.assertIn("drain not complete", self.out)
        self.assertEqual(self.entry.ops[-1], "release")


class SmokeFailureTest(RollingTestBase):
    def test_smoke_failure_rolls_back_only_current_node_and_drains_first(self):
        self.b.smoke_fail_for = {NEW_ID}
        self.assertEqual(self.deploy(), 1)
        self.assertEqual(self.b.image_id, OLD_ID)
        self.assertEqual(self.b.pin, OLD_REF)
        self.assertEqual(self.b.health, 200)
        after_smoke = self.b.ops[self.b.ops.index("smoke"):]
        # rollback: drain new container -> restore .env -> marker -> up old -> smoke -> USR2
        self.assertEqual([op for op in after_smoke if op != "vitals"],
                         ["smoke", "drain", "restore", "marker", "up", "smoke", "release"])
        self.assertEqual(self.a.ops.count("drain"), 0)
        self.assertEqual(self.a.image_id, OLD_ID)
        node = self.receipt_json()["nodes"][0]
        self.assertEqual(node["outcome"], "rolled-back")
        self.assertEqual(node["after_image_id"], OLD_ID)

    def test_failure_on_second_node_keeps_first_on_new_image(self):
        self.a.smoke_fail_for = {NEW_ID}
        self.assertEqual(self.deploy(), 1)
        self.assertEqual(self.b.image_id, NEW_ID)
        self.assertEqual(self.a.image_id, OLD_ID)
        self.assertEqual([n["outcome"] for n in self.receipt_json()["nodes"]], ["rolled", "rolled-back"])
        self.assertIn("finished nodes stay on", self.out)

    def test_rollback_refuses_to_recreate_when_new_container_will_not_drain(self):
        self.b.smoke_fail_for = {NEW_ID}
        self.b.drain_ok_after_up = False
        self.assertEqual(self.deploy(), 1)
        self.assertNotIn("restore", self.b.ops)
        self.assertEqual(self.receipt_json()["nodes"][0]["outcome"], "rollback-failed")
        self.assertIn("ROLLBACK FAILED", self.out)

    def test_up_failure_rolls_back(self):
        self.b.up_fail = True
        self.assertEqual(self.deploy(), 1)
        self.assertEqual(self.b.image_id, OLD_ID)
        self.assertEqual(self.b.health, 200)
        self.assertNotIn("drain", self.b.ops[self.b.ops.index("up"):])  # nothing running to drain


class MarkerIgnoredTest(RollingTestBase):
    def test_health_200_after_up_drains_immediately_and_rolls_back(self):
        self.b.new_ignores_marker = True
        self.assertEqual(self.deploy(), 1)
        ops = self.b.ops
        up = ops.index("up")
        self.assertEqual(ops[up + 1], "signal:USR1")
        self.assertNotIn("smoke", ops[:ops.index("restore")])
        self.assertEqual(self.b.image_id, OLD_ID)
        self.assertEqual(self.b.health, 200)
        self.assertIn("ignores the start-draining marker", self.out)
        self.assertNotIn("drain", self.a.ops)


class LastHealthyTest(RollingTestBase):
    def test_refuses_when_other_node_is_unhealthy(self):
        self.a.health = 0
        self.a.running = False
        self.assertEqual(self.deploy(), 1)
        self.assertNotIn("drain", self.b.ops)

    def test_refuses_to_drain_last_serving_node_when_capacity_check_fails(self):
        # app-b rolls; app-a's turn finds app-b back at 503 (e.g. watchdog flapped)
        orig = self.b.handle

        def flaky(op, argv):
            out = orig(op, argv)
            if op == "vitals":
                out["health"] = 503
            return out
        self.b.handle = flaky
        self.assertEqual(self.deploy(), 1)
        self.assertEqual(self.b.image_id, NEW_ID)
        self.assertNotIn("drain", self.a.ops)
        self.assertIn("last healthy", self.out)

    def test_single_app_node_is_refused(self):
        self.nodes = [APP_B]
        self.assertEqual(self.deploy(), 1)
        self.assertIn("at least two app nodes", self.out)
        self.assertNotIn("drain", self.b.ops)

    def test_cpu_and_memory_thresholds(self):
        self.a.cpu = 75.0
        self.assertEqual(self.deploy(), 1)
        self.assertNotIn("drain", self.b.ops)
        self.assertIn("CPU 75.0%", self.out)
        self.a.cpu, self.a.mem = 10.0, 61.0
        self.entry.path.unlink(missing_ok=True)
        self.assertEqual(self.deploy(), 1)
        self.assertIn("memory 61.0%", self.out)


class ResumeTest(RollingTestBase):
    def test_stuck_draining_on_old_image_resumes_from_swap(self):
        self.b.health = 503  # a previous run died after USR1
        self.assertEqual(self.deploy(), 0, self.out)
        b_ops = [op for op in self.b.ops if op != "probe"]
        self.assertEqual(b_ops[:4], ["drain", "repin", "marker", "up"])
        # resuming a node already out of rotation skips the capacity probe of app-a
        self.assertEqual(self.a.ops[:self.a.ops.index("drain")].count("vitals"), 0)
        self.assertEqual(self.receipt_json()["nodes"][0]["mode"], "resume-swap")
        self.assertEqual((self.b.image_id, self.a.image_id), (NEW_ID, NEW_ID))

    def test_stuck_draining_on_new_image_resumes_from_smoke(self):
        self.b.image_id, self.b.pin, self.b.backup, self.b.health = NEW_ID, NEW_REF, OLD_REF, 503
        self.assertEqual(self.deploy(), 0, self.out)
        b_ops = [op for op in self.b.ops if op not in ("probe", "vitals")]
        self.assertEqual(b_ops[:3], ["smoke", "release", "traffic"])
        self.assertNotIn("repin", self.b.ops)
        self.assertEqual(self.receipt_json()["nodes"][0]["mode"], "resume-smoke")

    def test_resume_node_goes_first_even_if_later_in_order(self):
        self.a.health = 503
        self.assertEqual(self.deploy(), 0, self.out)
        self.assertEqual([n["name"] for n in self.receipt_json()["nodes"]], ["app-a", "app-b"])

    def test_resume_smoke_failure_rolls_back_to_backup(self):
        self.b.image_id, self.b.pin, self.b.backup, self.b.health = NEW_ID, NEW_REF, OLD_REF, 503
        self.b.smoke_fail_for = {NEW_ID}
        self.assertEqual(self.deploy(), 1)
        self.assertEqual((self.b.image_id, self.b.pin, self.b.health), (OLD_ID, OLD_REF, 200))

    def test_resume_smoke_without_backup_is_refused(self):
        self.b.image_id, self.b.pin, self.b.health = NEW_ID, NEW_REF, 503
        self.assertEqual(self.deploy(), 1)
        self.assertIn("rollback target unknown", self.out)
        self.assertNotIn("smoke", self.b.ops)


class LeaseTest(RollingTestBase):
    def seed(self, holder="alice@laptop#1", minutes_ago=0):
        start = self.clock[0] - dt.timedelta(minutes=minutes_ago)
        self.entry.path.write_text(json.dumps({
            "holder": holder, "started_at": rolling.iso(start),
            "expires_at": rolling.iso(start + dt.timedelta(seconds=rolling.LEASE_TTL))}))

    def test_live_lease_blocks(self):
        self.seed(minutes_ago=5)
        self.assertEqual(self.deploy(), 1)
        self.assertIn("held by alice@laptop#1", self.out)
        self.assertNotIn("drain", self.b.ops)
        self.assertTrue(self.entry.path.exists())

    def test_expired_lease_needs_take_over(self):
        self.seed(minutes_ago=31)
        self.assertEqual(self.deploy(), 1)
        self.assertIn("--take-over", self.out)
        self.assertNotIn("drain", self.b.ops)

    def test_expired_lease_take_over_prints_previous_holder(self):
        self.seed(minutes_ago=31)
        self.assertEqual(self.deploy("--take-over"), 0, self.out)
        self.assertIn("took over expired lease from alice@laptop#1", self.out)
        self.assertEqual(self.receipt_json()["lease_previous_holder"], "alice@laptop#1")
        self.assertFalse(self.entry.path.exists())

    def test_take_over_does_not_steal_live_lease(self):
        self.seed(minutes_ago=5)
        self.assertEqual(self.deploy("--take-over"), 1)
        self.assertIn("held by", self.out)

    def test_lease_op_renew_and_release_require_holder(self):
        p = Path(self.tmp.name) / "l"
        now = lambda: self.clock[0]  # noqa: E731
        self.assertTrue(rolling.lease_op("acquire", "me", 1800, False, p, now)["ok"])
        self.assertFalse(rolling.lease_op("renew", "other", 1800, False, p, now)["ok"])
        self.clock[0] += dt.timedelta(minutes=20)
        lease = rolling.lease_op("renew", "me", 1800, False, p, now)["lease"]
        self.assertEqual(lease["expires_at"], "2026-11-15T08:50:00Z")
        self.assertEqual(lease["started_at"], "2026-11-15T08:00:00Z")
        self.assertEqual(set(json.loads(p.read_text())), {"holder", "started_at", "expires_at"})
        self.assertTrue(rolling.lease_op("release", "me", 1800, False, p, now)["ok"])
        self.assertFalse(p.exists())


class PreflightRefusalTest(RollingTestBase):
    def assertRefused(self, needle):
        self.assertEqual(self.deploy(), 1)
        self.assertIn(needle, self.out)
        self.assertNotIn("drain", self.b.ops + self.a.ops)
        self.assertEqual(self.entry.ops[-1], "release")

    def test_image_below_minimum_version_is_refused(self):
        for h in (self.b, self.a):
            h.images[NEW_REF]["version"] = "1.8.376"
        self.assertRefused("below MIN_ROLLING_VERSION 1.8.377")

    def test_old_image_below_minimum_version_is_refused(self):
        for h in (self.b, self.a):
            h.images[OLD_REF]["version"] = "1.8.376"
        self.assertRefused("no SIGUSR2 handler")

    def test_not_prewarmed_is_refused(self):
        del self.a.images[NEW_REF]
        self.assertRefused("not prewarmed")

    def test_receipt_mismatch_is_refused(self):
        self.a.receipt_id = "sha256:" + "9" * 64
        self.assertRefused("differs from warm receipt")

    def test_credential_drift_names_keys_only(self):
        self.a.cred_drift = ["REDIS_PASSWORD"]
        self.assertRefused("credentials differ from the live container: REDIS_PASSWORD")
        self.assertIn("values not shown", self.out)

    def test_bluegreen_layout_is_refused_with_migration_step(self):
        self.a.containers = {"tokenkey-blue": "running", "tokenkey-green": "exited", "tokenkey": "running"}
        self.assertRefused("M4")

    def test_socat_tokenkey_is_refused(self):
        self.a.socat = True
        self.assertRefused("socat forwarder")

    def test_missing_compose_node_is_refused(self):
        self.b.compose_node = False
        self.assertRefused("compose.node.yml; complete migration step M2")


class TrafficWarnTest(RollingTestBase):
    def test_no_traffic_stops_without_rollback(self):
        self.b.traffic = False
        self.assertEqual(self.deploy(), rolling.EXIT_WARN)
        self.assertEqual((self.b.image_id, self.b.health), (NEW_ID, 200))
        self.assertNotIn("restore", self.b.ops)
        self.assertNotIn("drain", self.a.ops)
        self.assertEqual(self.receipt_json()["outcome"], "stopped-warning")


class PureHelperTest(unittest.TestCase):
    def test_version_floor(self):
        self.assertTrue(rolling.version_at_least("1.8.377"))
        self.assertTrue(rolling.version_at_least("1.9.0"))
        self.assertFalse(rolling.version_at_least("1.8.376"))
        self.assertFalse(rolling.version_at_least("1.8.377-rc.1"))
        self.assertFalse(rolling.version_at_least(""))

    def test_count_traffic_ignores_health(self):
        logs = "\n".join([
            '2026-11-15T08:00:00Z\tINFO\thttp request completed\t{"component":"http.access","path":"/health/live"}',
            '{"level":"INFO","component":"http.access","path":"/v1/messages"}',
            'SIGUSR2 received; drain released',
            '{"component":"http.access","path":"/health/inflight"}',
        ])
        self.assertEqual(rolling.count_traffic(logs), 1)

    def test_classify(self):
        f = lambda img, h, run=True: {"container": {"image_id": img, "running": run}, "health": h}  # noqa: E731
        self.assertEqual(rolling.classify(f(NEW_ID, 200), NEW_ID), "skip")
        self.assertEqual(rolling.classify(f(OLD_ID, 200), NEW_ID), "full")
        self.assertEqual(rolling.classify(f(OLD_ID, 503), NEW_ID), "resume-swap")
        self.assertEqual(rolling.classify(f(NEW_ID, 503), NEW_ID), "resume-smoke")
        self.assertEqual(rolling.classify(f(OLD_ID, 0, False), NEW_ID), "stuck")


class AgentHostTest(unittest.TestCase):
    """Host-side .env rewrite keeps 0600 and only touches TOKENKEY_IMAGE."""

    def test_repin_and_restore_keep_mode_and_bytes(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            env = root / ".env"
            env.write_text(f"A=1\nTOKENKEY_IMAGE={OLD_REF}\nDATABASE_PASSWORD=s3cr3t\n")
            env.chmod(0o600)
            orig = rolling.ROOT
            rolling.ROOT = root
            try:
                ns = type("A", (), {"tag": TAG, "revision": REV, "expect_ref": OLD_REF})
                out = rolling.agent_repin(ns)
                self.assertEqual(out["old_ref"], OLD_REF)
                self.assertNotIn("s3cr3t", json.dumps(out))
                self.assertEqual(env.read_text(), f"A=1\nTOKENKEY_IMAGE={NEW_REF}\nDATABASE_PASSWORD=s3cr3t\n")
                backup = root / rolling.backup_name(TAG, REV)
                self.assertEqual(env.stat().st_mode & 0o777, 0o600)
                self.assertEqual(backup.stat().st_mode & 0o777, 0o600)
                rolling.agent_repin(ns)  # idempotent: backup keeps the old pin
                self.assertIn(OLD_REF, backup.read_text())
                rolling.agent_restore(ns)
                self.assertEqual(env.read_text(), f"A=1\nTOKENKEY_IMAGE={OLD_REF}\nDATABASE_PASSWORD=s3cr3t\n")
                self.assertEqual(env.stat().st_mode & 0o777, 0o600)
            finally:
                rolling.ROOT = orig


if __name__ == "__main__":
    unittest.main()
