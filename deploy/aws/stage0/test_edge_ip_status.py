import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "scripts" / "edge-ip-status.sh"


def _run(env: dict[str, str], *args: str) -> subprocess.CompletedProcess[str]:
    merged = os.environ.copy()
    merged.update(env)
    return subprocess.run(
        [str(SCRIPT), *args],
        check=False,
        capture_output=True,
        text=True,
        env=merged,
    )


def _write_fixtures(tmp: Path) -> dict[str, str]:
    matrix = tmp / "edge-targets-lightsail.json"
    polluted = tmp / "edge-polluted-ips.json"
    doc = tmp / "tokenkey-edge-ip-history.md"
    matrix.write_text(
        json.dumps(
            {
                "targets": {
                    "us9": {
                        "deployable": False,
                        "lightsail_region": "us-east-2",
                        "domain": "api-us9.tokenkey.dev",
                        "static_ip_name": "retired",
                        "porkbun_a_ipv4": "1.1.1.1",
                    },
                    "us8": {
                        "deployable": True,
                        "lightsail_region": "us-west-2",
                        "domain": "api-us8.tokenkey.dev",
                        "static_ip_name": "live-static",
                        "porkbun_a_ipv4": "9.9.9.9",
                    },
                }
            }
        ),
        encoding="utf-8",
    )
    polluted.write_text(
        json.dumps(
            {
                "polluted": [
                    {
                        "ip": "8.8.8.8",
                        "region": "us-west-2",
                        "notes": "fixture-old",
                    }
                ]
            }
        ),
        encoding="utf-8",
    )
    env = {
        "EDGE_IP_STATUS_MATRIX": str(matrix),
        "EDGE_IP_STATUS_POLLUTED": str(polluted),
        "EDGE_IP_STATUS_DOC": str(doc),
    }
    rendered = _run(env)
    if rendered.returncode != 0:
        raise AssertionError(rendered.stderr)
    doc.write_text(rendered.stdout, encoding="utf-8")
    return env


class EdgeIPStatusTests(unittest.TestCase):
    def test_markdown_and_json_include_current_and_skip_retired(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = _write_fixtures(Path(tmp))
            md = _run(env)
            self.assertEqual(md.returncode, 0)
            self.assertIn("BEGIN edge-ip-status:current", md.stdout)
            self.assertIn("BEGIN edge-ip-status:polluted", md.stdout)
            self.assertIn("`us8`", md.stdout)
            self.assertIn("`9.9.9.9`", md.stdout)
            self.assertNotIn("`us9`", md.stdout)
            self.assertIn("`8.8.8.8`", md.stdout)

            payload = _run(env, "--json")
            self.assertEqual(payload.returncode, 0)
            data = json.loads(payload.stdout)
            self.assertEqual(
                data["current"],
                [
                    {
                        "edge": "us8",
                        "region": "us-west-2",
                        "domain": "api-us8.tokenkey.dev",
                        "static_ip_name": "live-static",
                        "ipv4": "9.9.9.9",
                    }
                ],
            )
            self.assertEqual(data["polluted"][0]["ip"], "8.8.8.8")

    def test_check_passes_when_doc_matches_and_fails_on_current_drift(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = _write_fixtures(Path(tmp))
            ok = _run(env, "--check")
            self.assertEqual(ok.returncode, 0, ok.stderr)
            self.assertIn("current table in sync", ok.stdout)
            self.assertIn("polluted table in sync", ok.stdout)

            doc = Path(env["EDGE_IP_STATUS_DOC"])
            drifted = doc.read_text(encoding="utf-8").replace("9.9.9.9", "1.2.3.4")
            doc.write_text(drifted, encoding="utf-8")
            failed = _run(env, "--check")
            self.assertEqual(failed.returncode, 1)
            self.assertIn("current block in", failed.stderr)
            self.assertIn("polluted table in sync", failed.stdout)


if __name__ == "__main__":
    unittest.main()
