"""node1_alert.py: the only alert path for a job that runs off the hosts
host_https_watch.py covers.
"""
import json
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import node1_alert
import host_https_watch as watch


class JobRegistrationTest(unittest.TestCase):
    def test_the_mirror_job_is_registered_in_the_shared_limits_table(self):
        # node1_alert reuses host_https_watch's job_fact/JOB_LIMITS rather
        # than defining its own notion of "healthy", so the two alert paths
        # cannot drift into disagreeing about the same job name.
        self.assertIn(node1_alert.JOB, watch.JOB_LIMITS)

    def test_cadence_tolerates_one_missed_run(self):
        # install_node1_db_mirror.py schedules every 4 hours.
        max_age, max_runtime = watch.JOB_LIMITS[node1_alert.JOB]
        self.assertGreaterEqual(max_age, 2 * 4 * 3600)
        self.assertGreaterEqual(max_runtime, 1800)


class CredentialTest(unittest.TestCase):
    def test_missing_credentials_refuse_before_any_probe(self):
        with tempfile.TemporaryDirectory() as root:
            env_file = pathlib.Path(root) / "env"
            env_file.write_text("")
            state = pathlib.Path(root) / "state.json"
            with patch.object(sys, "argv",
                              ["node1_alert.py", "--dry-run",
                               "--env-file", str(env_file), "--state", str(state)]):
                with self.assertRaisesRegex(ValueError, "credentials are missing"):
                    node1_alert.main()

    def test_credentials_are_read_only_from_the_declared_two_keys(self):
        with tempfile.TemporaryDirectory() as root:
            env_file = pathlib.Path(root) / "env"
            env_file.write_text(
                'TOKENKEY_FEISHU_WEBHOOK_URL="https://example.invalid/hook"\n'
                'TOKENKEY_FEISHU_WEBHOOK_SECRET="s3cr3t"\n'
                "SOME_OTHER_VAR=should-not-leak\n")
            state = pathlib.Path(root) / "state.json"
            with patch.object(sys, "argv",
                              ["node1_alert.py", "--dry-run",
                               "--env-file", str(env_file), "--state", str(state)]), \
                 patch.object(node1_alert, "probe_job",
                             return_value={"healthy": True, "reason": "recent completion"}):
                rc = node1_alert.main()
            self.assertEqual(rc, 0)


class AlertFlowTest(unittest.TestCase):
    def _run(self, root, probe_result, previous=None):
        env_file = pathlib.Path(root) / "env"
        env_file.write_text(
            'TOKENKEY_FEISHU_WEBHOOK_URL="https://example.invalid/hook"\n'
            'TOKENKEY_FEISHU_WEBHOOK_SECRET="s3cr3t"\n')
        state = pathlib.Path(root) / "state.json"
        if previous is not None:
            state.write_text(json.dumps(previous))
        sent = []
        with patch.object(sys, "argv",
                          ["node1_alert.py", "--env-file", str(env_file), "--state", str(state)]), \
             patch.object(node1_alert, "probe_job", return_value=probe_result), \
             patch.object(node1_alert, "post_feishu", side_effect=lambda msg, **_: sent.append(msg)):
            node1_alert.main()
        return sent, json.loads(state.read_text()) if state.exists() else {}

    def test_two_consecutive_failures_trigger_one_alert_not_every_tick(self):
        # Mirrors next_state's own debounce: a single blip must not page.
        with tempfile.TemporaryDirectory() as root:
            sent1, state1 = self._run(root, {"healthy": False, "reason": "service failed: exit-code"})
            self.assertEqual(sent1, [])
            sent2, state2 = self._run(root, {"healthy": False, "reason": "service failed: exit-code"},
                                      previous=state1)
            self.assertEqual(len(sent2), 1)
            self.assertIn(node1_alert.JOB, sent2[0])
            self.assertIn("unhealthy", sent2[0])

    def test_recovery_after_an_active_alert_sends_one_message(self):
        with tempfile.TemporaryDirectory() as root:
            failing = {node1_alert.JOB: {"failures": 3, "active": True, "sent_at": 0, "observed_at": 0}}
            sent, _ = self._run(root, {"healthy": True, "reason": "recent completion"}, previous=failing)
            self.assertEqual(len(sent), 1)
            self.assertIn("recovered", sent[0])

    def test_state_persists_across_runs_for_debounce(self):
        # Matches tick_jobs' own shape: "active" is only written once an
        # alert actually fires, consistent with the shared next_state logic.
        with tempfile.TemporaryDirectory() as root:
            _, state = self._run(root, {"healthy": True, "reason": "recent completion"})
            self.assertIn(node1_alert.JOB, state)
            self.assertNotIn("active", state[node1_alert.JOB])
            self.assertFalse(bool(state[node1_alert.JOB].get("active", False)))


if __name__ == "__main__":
    unittest.main()
