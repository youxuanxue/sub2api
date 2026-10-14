#!/usr/bin/env python3
"""Exercise old-host identity gates, exact timer mutations and rollback."""
import contextlib
import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import retire_aws_standby_timers as retire

INSTANCE = "i-0123456789abcdef0"


class RetirementTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.states = {name: {"ActiveState": "active", "UnitFileState": "enabled"} for name in retire.TIMERS}
        self.original = copy.deepcopy(self.states)
        self.calls = []
        self.running = "tokenkey-postgres\ntokenkey-redis"
        self.busy = False
        self.fail_timer = None
        self.identities = [INSTANCE]
        for patcher in [
            mock.patch.object(retire, "BACKUP_ROOT", self.root),
            mock.patch.object(retire, "instance_identity", side_effect=lambda: self.identities.pop(0) if len(self.identities) > 1 else self.identities[0]),
            mock.patch.object(retire, "command", side_effect=self.command),
            mock.patch.object(retire, "require_root"),
        ]:
            patcher.start()
            self.addCleanup(patcher.stop)

    def command(self, *args, **kwargs):
        self.calls.append(args)
        if args[0] == "docker":
            return self.running
        verb = args[1]
        if verb == "show":
            name = args[2]
            if name.endswith(".service"):
                return "active" if self.busy else "failed"
            return "\n".join(f"{key}={value}" for key, value in self.states[name].items())
        name = args[-1]
        self.assertIn(name, retire.TIMERS)
        if name == self.fail_timer and verb == "disable":
            self.fail_timer = None
            raise RuntimeError("synthetic disable failure")
        if verb in {"enable", "disable"}:
            self.states[name]["UnitFileState"] = "enabled" if verb == "enable" else "disabled"
        if verb in {"start", "stop"} or "--now" in args:
            self.states[name]["ActiveState"] = "active" if verb == "start" else "inactive"
        return ""

    def run_operation(self, operation, *extra):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = retire.main(["--expected-instance", INSTANCE, "--operation", operation, *extra])
        self.assertEqual(code, 0)
        return out.getvalue()

    def mutations(self):
        return [args for args in self.calls if args[0] == "systemctl" and args[1] != "show"]

    def test_plan_is_read_only_and_apply_changes_only_allowlisted_timers(self):
        self.run_operation("plan")
        self.assertEqual(self.mutations(), [])
        self.assertEqual(list(self.root.iterdir()), [])
        output = self.run_operation("apply")
        backup = Path(json.loads(output.splitlines()[0])["backup"])
        self.assertEqual(json.loads(backup.read_text())["timers"], self.original)
        self.assertEqual(backup.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.mutations(), [("systemctl", "disable", "--now", timer) for timer in retire.TIMERS])
        self.assertTrue(all(state == {"ActiveState": "inactive", "UnitFileState": "disabled"} for state in self.states.values()))
        self.calls.clear()
        self.run_operation("apply")
        self.assertEqual(self.mutations(), [])
        self.run_operation("rollback", "--backup", str(backup))
        self.assertEqual(self.states, self.original)

    def test_hcloud_wrong_identity_running_gateway_and_busy_jobs_never_mutate(self):
        for change in ["identity", "gateway", "busy"]:
            with self.subTest(change=change):
                self.identities = ["mi-0123456789abcdef0" if change == "identity" else INSTANCE]
                self.running = "tokenkey-blue" if change == "gateway" else "tokenkey-postgres"
                self.busy = change == "busy"
                with self.assertRaises(RuntimeError):
                    self.run_operation("apply")
                self.assertEqual(self.mutations(), [])
                self.assertEqual(self.states, self.original)

    def test_changed_identity_at_apply_gate_does_not_reactivate_timers(self):
        self.identities = [INSTANCE, "i-11111111111111111"]
        with self.assertRaises(RuntimeError):
            self.run_operation("apply")
        self.assertEqual(self.mutations(), [])

    def test_partial_failure_restores_original_timer_states(self):
        self.fail_timer = retire.TIMERS[1]
        with self.assertRaises(RuntimeError):
            self.run_operation("apply")
        self.assertEqual(self.states, self.original)
        self.assertNotIn(("systemctl", "stop", "tokenkey-postgres"), self.calls)

    def test_rollback_rejects_other_host_or_unapproved_unit(self):
        path = self.root / "bad.json"
        for payload in [
            {"instance_id": "i-11111111111111111", "timers": self.original},
            {"instance_id": INSTANCE, "timers": {"tokenkey.service": self.original[retire.TIMERS[0]]}},
        ]:
            path.write_text(json.dumps(payload))
            path.chmod(0o600)
            with self.assertRaises(RuntimeError):
                self.run_operation("rollback", "--backup", str(path))
            self.assertEqual(self.mutations(), [])


if __name__ == "__main__":
    unittest.main()
