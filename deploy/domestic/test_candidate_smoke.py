#!/usr/bin/env python3
"""Behavior tests for the cn-prod candidate smoke report."""
from __future__ import annotations

import json
import pathlib
import tempfile
import unittest

from deploy.domestic import candidate_smoke
from deploy.domestic.candidate_smoke import evaluate, load_and_probe, published_port, report_passed, write_report


REVISION = "4529659c854f7842039914c2065f30b5d9a8de89"
CONTAINER_ID = "b7ade1ce9dbe81f5e94b64c8d6d411f1f693db3d6fe5bd7ad4224f6844556da2"


def inspected(revision: str = REVISION, health: str = "healthy") -> dict:
    return {
        "Id": CONTAINER_ID,
        "Config": {"Labels": {"org.opencontainers.image.revision": revision}},
        "State": {"Health": {"Status": health}},
    }


def responses(port: int, health_code: int = 200, health_body: str = '{"status":"ok"}',
              settings_code: int = 200, settings_body: str = '{"code":0}') -> dict:
    return {
        f"http://127.0.0.1:{port}/health": (health_code, health_body),
        f"http://127.0.0.1:{port}/api/v1/settings/public": (settings_code, settings_body),
    }


class CandidateSmokeTest(unittest.TestCase):
    def test_blue_and_green_use_the_loopback_ports(self) -> None:
        self.assertEqual(published_port("tokenkey-blue"), 18081)
        self.assertEqual(published_port("tokenkey-green"), 18082)

    def test_passing_probe_keeps_docker_inspect_id(self) -> None:
        report = evaluate(
            {"target": "tokenkey-blue", "revision": REVISION},
            inspected(),
            responses(18081, health_body='{"status": "ok"}'),
        )
        self.assertTrue(report_passed(report))
        self.assertEqual(report["container_id"], CONTAINER_ID)
        self.assertEqual(report["revision"], REVISION)
        self.assertEqual(len(report["cases"]), 4)

    def test_failed_health_stays_in_the_report(self) -> None:
        report = evaluate(
            {"target": "tokenkey-green", "revision": REVISION},
            inspected(health="unhealthy"),
            responses(18082, health_code=503, health_body="bad gateway"),
        )
        self.assertFalse(report_passed(report))
        by_name = {case["case"]: case for case in report["cases"]}
        self.assertFalse(by_name["candidate /health on 127.0.0.1:18082"]["passed"])
        self.assertEqual(by_name["candidate /health on 127.0.0.1:18082"]["http"], 503)
        self.assertFalse(by_name["candidate docker health"]["passed"])
        self.assertIn("candidate /api/v1/settings/public on 127.0.0.1:18082", by_name)

    def test_revision_mismatch_fails_and_records_the_label(self) -> None:
        report = evaluate(
            {"target": "tokenkey-blue", "revision": REVISION},
            inspected(revision="0" * 40),
            responses(18081),
        )
        revision_case = next(case for case in report["cases"] if case["case"] == "candidate image revision")
        self.assertFalse(revision_case["passed"])
        self.assertEqual(revision_case["revision"], "0" * 40)
        self.assertFalse(report_passed(report))

    def test_load_and_probe_uses_prepared_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            state = pathlib.Path(raw)
            (state / "prepared.json").write_text(json.dumps({
                "target": "tokenkey-blue",
                "revision": REVISION,
            }))
            seen = {}

            def inspect(name: str) -> dict:
                seen["name"] = name
                return inspected()

            def http_get(url: str) -> tuple[int, str]:
                seen.setdefault("urls", []).append(url)
                body = '{"status":"ok"}' if url.endswith("/health") else '{"code":0}'
                return 200, body

            original_inspect, original_http = candidate_smoke._inspect, candidate_smoke._http_get
            candidate_smoke._inspect, candidate_smoke._http_get = inspect, http_get
            try:
                report = load_and_probe(state)
            finally:
                candidate_smoke._inspect, candidate_smoke._http_get = original_inspect, original_http
            self.assertEqual(seen["name"], "tokenkey-blue")
            self.assertEqual(seen["urls"], [
                "http://127.0.0.1:18081/health",
                "http://127.0.0.1:18081/api/v1/settings/public",
            ])
            self.assertTrue(report_passed(report))
            out = state / "candidate-smoke.json"
            write_report(report, out)
            self.assertEqual(json.loads(out.read_text())["container_id"], CONTAINER_ID)
            self.assertEqual(out.stat().st_mode & 0o777, 0o600)


def single_responses(health_code: int, health_body: str) -> dict:
    return {
        "http://127.0.0.1:18080/health": (health_code, health_body),
        "http://127.0.0.1:18080/health/live": (200, '{"status":"alive"}'),
        "http://127.0.0.1:18080/api/v1/settings/public": (200, '{"code":0}'),
    }


def single_inspected(health: str = "starting", running: bool = True) -> dict:
    data = inspected(health=health)
    data["State"]["Running"] = running
    return data


class SingleContainerSmokeTest(unittest.TestCase):
    def test_drained_node_passes_with_503_draining_and_unhealthy_docker_health(self) -> None:
        report = candidate_smoke.evaluate_single(
            "tokenkey", 18080, REVISION, single_inspected(health="unhealthy"),
            single_responses(503, '{"status":"draining"}'), expect_draining=True)
        self.assertTrue(report_passed(report), report)
        self.assertEqual(len(report["cases"]), 5)

    def test_drained_node_fails_when_health_is_200(self) -> None:
        report = candidate_smoke.evaluate_single(
            "tokenkey", 18080, REVISION, single_inspected(),
            single_responses(200, '{"status":"ok"}'), expect_draining=True)
        self.assertFalse(report_passed(report))

    def test_serving_node_needs_200_ok(self) -> None:
        report = candidate_smoke.evaluate_single(
            "tokenkey", 18080, REVISION, single_inspected(health="healthy"),
            single_responses(200, '{"status":"ok"}'), expect_draining=False)
        self.assertTrue(report_passed(report))

    def test_stopped_container_and_wrong_revision_fail(self) -> None:
        report = candidate_smoke.evaluate_single(
            "tokenkey", 18080, "0" * 40, single_inspected(running=False),
            single_responses(503, '{"status":"draining"}'), expect_draining=True)
        failed = {c["case"] for c in report["cases"] if not c["passed"]}
        self.assertEqual(failed, {"tokenkey image revision", "tokenkey process running"})

    def test_cli_modes_are_exclusive(self) -> None:
        with self.assertRaises(SystemExit):
            candidate_smoke.main(["--container", "tokenkey", "--port", "18080"])
        with self.assertRaises(SystemExit):
            candidate_smoke.main(["--state", "/tmp/x", "--port", "18080"])
        with self.assertRaises(SystemExit):
            candidate_smoke.main([])


if __name__ == "__main__":
    unittest.main()
