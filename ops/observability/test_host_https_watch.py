import pathlib
import sys
import unittest
from unittest.mock import patch
from types import SimpleNamespace

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import host_https_watch as watch


class HostHTTPSWatchTest(unittest.TestCase):
    def test_two_failures_then_recovery_and_cooldown(self):
        sent = []
        targets = {"prod": "https://api.tokenkey.dev/health"}
        bad = lambda url: {"healthy": False, "http_code": 503}
        good = lambda url: {"healthy": True, "http_code": 200}
        state = watch.tick(targets, {}, 1000, bad, sent.append)
        self.assertEqual(sent, [])
        state = watch.tick(targets, state, 1120, bad, sent.append)
        self.assertTrue(state["prod"]["active"])
        self.assertEqual(len(sent), 1)
        state = watch.tick(targets, state, 1240, bad, sent.append)
        self.assertEqual(len(sent), 1)
        state = watch.tick(targets, state, 1360, good, sent.append)
        self.assertFalse(state["prod"]["active"])
        self.assertIn("recovered", sent[-1])
        state = watch.tick(targets, state, 1480, good, sent.append)
        self.assertEqual(len(sent), 2)

    def test_delivery_failure_keeps_previous_state_for_retry(self):
        previous = {"prod": {"failures": 1, "observed_at": 1000}}
        original = {k: dict(v) for k, v in previous.items()}
        def fail(message):
            raise RuntimeError("delivery unavailable")
        with self.assertRaisesRegex(RuntimeError, "delivery unavailable"):
            watch.tick({"prod": "https://api.tokenkey.dev/health"}, previous, 1120,
                       lambda url: {"healthy": False, "http_code": 0}, fail)
        self.assertEqual(previous, original)

    def test_stale_failure_is_not_a_consecutive_outage(self):
        result, action = watch.next_state({"failures": 1, "observed_at": 1000}, False, 2000)
        self.assertEqual(result["failures"], 1)
        self.assertEqual(action, "")

    def test_healthy_initial_tick_never_sends(self):
        sent = []
        result = watch.tick({"prod": "https://api.tokenkey.dev/health"}, {}, 1000,
                            lambda url: {"healthy": True, "http_code": 200}, sent.append)
        self.assertEqual(result["prod"]["failures"], 0)
        self.assertEqual(sent, [])


class HostJobWatchTest(unittest.TestCase):
    def test_job_failure_alert_and_recovery_are_deduplicated(self):
        name = "tokenkey-pgdump"
        sent = []
        bad = lambda _: {"healthy": False, "reason": "service failed: exit-code"}
        good = lambda _: {"healthy": True, "reason": "recent completion"}
        state = watch.tick_jobs([name], {}, 1000, bad, sent.append)
        self.assertEqual(sent, [])
        state = watch.tick_jobs([name], state, 1120, bad, sent.append)
        self.assertEqual(len(sent), 1)
        state = watch.tick_jobs([name], state, 1240, bad, sent.append)
        self.assertEqual(len(sent), 1)
        state = watch.tick_jobs([name], state, 1360, good, sent.append)
        self.assertIn("recovered", sent[-1])
        self.assertFalse(state["job:" + name]["active"])

    def test_probe_failure_alert_names_node_and_does_not_hide_other_jobs(self):
        names = ["tokenkey-pgdump", "tokenkey-disk-metrics"]
        def probe(name):
            if name == names[0]:
                raise OSError("synthetic private detail")
            return {"healthy": True, "reason": "recent completion"}
        sent = []
        state = watch.tick_jobs(names, {}, 1000, probe, sent.append, "uk1")
        state = watch.tick_jobs(names, state, 1120, probe, sent.append, "uk1")
        self.assertEqual(len(sent), 1)
        self.assertIn("uk1/tokenkey-pgdump", sent[0])
        self.assertIn("OSError", sent[0])
        self.assertNotIn("private detail", sent[0])
        self.assertEqual(state["job:" + names[1]]["failures"], 0)

    def test_job_starting_during_sampling_is_not_overdue(self):
        # A job starts after probing begins, within one /proc/uptime tick.
        # The clock must be read after systemd and retain sub-centisecond precision.
        clock = [1000.0]
        def show(command, **kwargs):
            clock[0] = 1000.002
            if command[2].endswith(".service"):
                return SimpleNamespace(stdout="ActiveState=activating\nResult=success\nExecMainStartTimestampMonotonic=1000001000\n")
            return SimpleNamespace(stdout="ActiveState=active\nUnitFileState=enabled\n")
        with patch.object(watch.subprocess, "run", side_effect=show), \
             patch.object(watch.time, "monotonic", side_effect=lambda: clock[0]), \
             patch.object(watch.Path, "read_text", return_value="1000.00 0.00"):
            self.assertEqual(watch.probe_job("tokenkey-disk-metrics"),
                             {"healthy": True, "reason": "running"})

    def test_probe_still_rejects_real_execution_timeout(self):
        replies = [SimpleNamespace(stdout="ActiveState=activating\nResult=success\nExecMainStartTimestampMonotonic=1000000000\n"),
                   SimpleNamespace(stdout="ActiveState=active\nUnitFileState=enabled\n")]
        with patch.object(watch.subprocess, "run", side_effect=replies), \
             patch.object(watch.time, "monotonic", return_value=1061.0):
            self.assertEqual(watch.probe_job("tokenkey-disk-metrics"),
                             {"healthy": False, "reason": "execution overdue"})

    def test_failed_stale_disabled_and_running_jobs(self):
        timer = {"ActiveState": "active", "UnitFileState": "enabled"}
        service = {"ActiveState": "inactive", "Result": "success", "ExecMainExitTimestampMonotonic": "1000000000"}
        self.assertTrue(watch.job_fact(service, timer, 1100, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact(service, timer, 1500, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact(service, {**timer, "UnitFileState": "disabled"}, 1100, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact({**service, "Result": "timeout"}, timer, 1100, (300, 60))["healthy"])
        running = {**service, "ActiveState": "activating", "ExecMainStartTimestampMonotonic": "1080000000"}
        self.assertTrue(watch.job_fact(running, timer, 1100, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact(running, timer, 1200, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact({"ActiveState": "inactive", "Result": "success"}, timer, 1100, (300, 60))["healthy"])


if __name__ == "__main__":
    unittest.main()
