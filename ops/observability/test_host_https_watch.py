import pathlib
import sys
import unittest

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


if __name__ == "__main__":
    unittest.main()
