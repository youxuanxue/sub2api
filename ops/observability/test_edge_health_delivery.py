import json
import pathlib
import tempfile
import unittest

from ops.observability.edge_health_delivery import DeliveryError, apply_decision


class Response:
    status = 200

    def __init__(self, payload):
        self.payload = payload

    def read(self, _limit=-1):
        return json.dumps(self.payload).encode()

    def getcode(self):
        return self.status

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False


def opener(payload):
    return lambda _request, timeout=0: Response(payload)


def valid_state(last_bucket="2026-08-18T12:10:00Z"):
    return {
        "schema_version": 1,
        "model_units": [{
            "edge": "us5",
            "unit": {"kind": "family", "id": "claude"},
            "status": "degraded",
            "started_bucket": "2026-08-18T12:05:00Z",
            "last_evaluated_bucket": last_bucket,
            "last_notified_severity": "degraded",
        }],
        "hosts": [],
        "telemetry": [],
    }


class EdgeHealthDeliveryTest(unittest.TestCase):
    def test_application_rejection_preserves_previous_state(self):
        decision = {"schema_version": 1, "should_alert": True, "message": "page", "state": valid_state()}
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "state.json"
            path.write_text('{"old":true}\n', encoding="utf-8")
            with self.assertRaises(DeliveryError):
                apply_decision(
                    decision,
                    state_file=path,
                    dry_run=False,
                    webhook_url="https://example.test/hook",
                    signing_secret="secret",
                    opener=opener({"code": 19021}),
                )
            self.assertEqual('{"old":true}\n', path.read_text(encoding="utf-8"))

    def test_successful_notification_writes_canonical_structured_state(self):
        decision = {"schema_version": 1, "should_alert": True, "message": "page", "state": valid_state()}
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "nested" / "state.json"
            result = apply_decision(
                decision,
                state_file=path,
                dry_run=False,
                webhook_url="https://example.test/hook",
                signing_secret="secret",
                opener=opener({"code": 0}),
            )
            self.assertEqual("delivered", result)
            self.assertEqual(
                json.dumps(valid_state(), ensure_ascii=False, separators=(",", ":"), sort_keys=True) + "\n",
                path.read_text(encoding="utf-8"),
            )

    def test_successful_no_notification_evaluation_advances_state(self):
        decision = {"schema_version": 1, "should_alert": False, "message": "", "state": valid_state()}
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "state.json"
            result = apply_decision(
                decision,
                state_file=path,
                dry_run=False,
                webhook_url="",
                signing_secret="",
            )
            self.assertEqual("unchanged", result)
            self.assertEqual(valid_state(), json.loads(path.read_text(encoding="utf-8")))

    def test_dry_run_and_invalid_state_never_mutate(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "state.json"
            path.write_text('{"old":true}\n', encoding="utf-8")
            dry_run = {"schema_version": 1, "should_alert": True, "message": "page", "state": valid_state()}
            self.assertEqual(
                "dry-run",
                apply_decision(
                    dry_run,
                    state_file=path,
                    dry_run=True,
                    webhook_url="",
                    signing_secret="",
                ),
            )
            self.assertEqual('{"old":true}\n', path.read_text(encoding="utf-8"))

            invalid_states = [
                {"schema_version": 1, "model_units": "bad"},
                {"schema_version": 1, "model_units": [], "hosts": [{"edge": "us5", "status": "healthy"}], "telemetry": []},
                {"schema_version": 1, "model_units": [], "hosts": [], "telemetry": [{"edge": "us5", "status": "pending", "failure_slots": "bad"}]},
            ]
            for invalid_state in invalid_states:
                invalid = {"schema_version": 1, "should_alert": False, "message": "", "state": invalid_state}
                with self.assertRaises(DeliveryError):
                    apply_decision(
                        invalid,
                        state_file=path,
                        dry_run=False,
                        webhook_url="",
                        signing_secret="",
                    )
                self.assertEqual('{"old":true}\n', path.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
