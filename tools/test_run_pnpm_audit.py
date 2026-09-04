from __future__ import annotations

import io
import json
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path

from tools.run_pnpm_audit import AuditRequestError, OSV_QUERY_URL, run_audit


GHSA_ID = "GHSA-5pgg-2g8v-p4x9"
INVENTORY = [
    {
        "name": "sub2api-frontend",
        "dependencies": {
            "xlsx": {
                "version": "0.18.5",
                "dependencies": {"tiny": {"version": "1.0.0"}},
            }
        },
    }
]
DETAIL = {
    "id": GHSA_ID,
    "aliases": ["CVE-2024-22363"],
    "summary": "SheetJS Regular Expression Denial of Service (ReDoS)",
    "database_specific": {"severity": "HIGH"},
}


def inventory_result(returncode: int = 0) -> subprocess.CompletedProcess[str]:
    return subprocess.CompletedProcess(
        args=["pnpm", "list"],
        returncode=returncode,
        stdout=json.dumps(INVENTORY),
        stderr="inventory failed" if returncode else "",
    )


class OsvRequester:
    def __init__(
        self,
        *,
        fail_batches: int = 0,
        vulnerable: bool = True,
        detail: dict | None = None,
    ) -> None:
        self.fail_batches = fail_batches
        self.vulnerable = vulnerable
        self.detail = detail or DETAIL
        self.batch_calls = 0
        self.queries: list[dict] = []

    def __call__(self, url: str, data: object | None, timeout: float) -> object:
        if timeout <= 0:
            raise AssertionError("timeout must be positive")
        if url == OSV_QUERY_URL:
            self.batch_calls += 1
            if self.batch_calls <= self.fail_batches:
                raise AuditRequestError("temporary OSV failure")
            if not isinstance(data, dict):
                raise AssertionError("batch request must carry an object")
            self.queries = data["queries"]
            results = [{} for _ in self.queries]
            if self.vulnerable:
                vulnerable_index = next(
                    index
                    for index, query in enumerate(self.queries)
                    if query["package"]["name"] == "xlsx"
                )
                results[vulnerable_index] = {
                    "vulns": [{"id": GHSA_ID, "modified": "2026-01-01T00:00:00Z"}]
                }
            return {"results": results}
        return self.detail


class RunPnpmAuditTest(unittest.TestCase):
    def test_maps_complete_production_inventory_to_existing_audit_contract(self) -> None:
        requester = OsvRequester()
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "audit.json"

            result = run_audit(
                output,
                attempts=3,
                retry_delay_seconds=0,
                timeout_seconds=1,
                runner=lambda *args, **kwargs: inventory_result(),
                requester=requester,
            )

            report = json.loads(output.read_text(encoding="utf-8"))
            advisory = report["advisories"][GHSA_ID]
            self.assertEqual(result, 0)
            self.assertEqual(advisory["module_name"], "xlsx")
            self.assertEqual(advisory["severity"], "high")
            self.assertEqual(advisory["github_advisory_id"], GHSA_ID)
            self.assertEqual(report["metadata"]["dependencies"], 2)
            self.assertEqual(
                {(query["package"]["name"], query["version"]) for query in requester.queries},
                {("tiny", "1.0.0"), ("xlsx", "0.18.5")},
            )

    def test_retries_osv_failure_then_writes_empty_valid_report(self) -> None:
        requester = OsvRequester(fail_batches=1, vulnerable=False)
        sleeps: list[float] = []
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "audit.json"

            with redirect_stderr(io.StringIO()):
                result = run_audit(
                    output,
                    attempts=3,
                    retry_delay_seconds=2,
                    timeout_seconds=1,
                    runner=lambda *args, **kwargs: inventory_result(),
                    requester=requester,
                    sleeper=sleeps.append,
                )

            self.assertEqual(result, 0)
            self.assertEqual(sleeps, [2])
            self.assertEqual(
                json.loads(output.read_text(encoding="utf-8"))["advisories"], {}
            )

    def test_fails_closed_without_reusing_stale_report(self) -> None:
        requester = OsvRequester(fail_batches=10)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "audit.json"
            output.write_text("stale", encoding="utf-8")

            with redirect_stderr(io.StringIO()):
                result = run_audit(
                    output,
                    attempts=2,
                    retry_delay_seconds=0,
                    timeout_seconds=1,
                    runner=lambda *args, **kwargs: inventory_result(),
                    requester=requester,
                    sleeper=lambda _: None,
                )

            self.assertEqual(result, 1)
            self.assertFalse(output.exists())

    def test_fails_closed_when_inventory_is_unavailable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "audit.json"

            with redirect_stderr(io.StringIO()):
                result = run_audit(
                    output,
                    attempts=3,
                    retry_delay_seconds=0,
                    timeout_seconds=1,
                    runner=lambda *args, **kwargs: inventory_result(returncode=1),
                    requester=OsvRequester(),
                )

            self.assertEqual(result, 1)
            self.assertFalse(output.exists())

    def test_fails_closed_when_osv_severity_is_unknown(self) -> None:
        requester = OsvRequester(detail={"id": GHSA_ID, "database_specific": {}})
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "audit.json"

            with redirect_stderr(io.StringIO()):
                result = run_audit(
                    output,
                    attempts=1,
                    retry_delay_seconds=0,
                    timeout_seconds=1,
                    runner=lambda *args, **kwargs: inventory_result(),
                    requester=requester,
                )

            self.assertEqual(result, 1)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
