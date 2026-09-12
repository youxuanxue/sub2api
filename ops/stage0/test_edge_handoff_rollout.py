"""Release-order regression: never infer serving prod from a prepared container."""

import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
from pathlib import Path
import subprocess
import sys
import threading
import unittest
from unittest.mock import patch

import yaml

import check_edge_handoff_rollout as gate


class EdgeHandoffRolloutTest(unittest.TestCase):
    def test_serving_prod_contract_over_http(self):
        class Handler(BaseHTTPRequestHandler):
            status, capability, cache = 404, "", "no-store"

            def do_GET(self):
                self.send_response(self.status)
                self.send_header(gate.HEADER, self.capability)
                self.send_header("Cache-Control", self.cache)
                self.send_header("Location", "/prepared-green")
                self.end_headers()

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            for status, capability, cache, allowed in [
                (404, "", "no-store", False),  # old serving prod, regardless of inactive green
                (200, "", "no-store", False),  # healthy SPA fallback is not a capability
                (503, "", "no-store", False),  # arbitrary outage is not isolation
                (503, "1", "no-store", True),  # isolated prod with handoff disabled
                (200, "1", "no-store", True),
                (502, "1", "no-store", False),
                (504, "1", "no-store", False),
                (302, "1", "no-store", False),  # must not follow a redirect to green
                (200, "1", "max-age=60", False),
            ]:
                with self.subTest(status=status, capability=capability, cache=cache):
                    Handler.status, Handler.capability, Handler.cache = status, capability, cache
                    result = gate.probe_serving_prod(f"http://127.0.0.1:{server.server_port}")
                    self.assertEqual(result["isolated"], allowed)
                    self.assertEqual(result["status"], status)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_image_release_source_selects_gate(self):
        with patch.object(gate.subprocess, "run") as run, patch.object(gate.subprocess, "check_output") as show:
            show.return_value = 'edge.POST("/admin-session", h.Mint)'
            self.assertFalse(gate.requires_isolation("1.8.219"))
            show.return_value = 'v1.POST("/edge/admin-handoff/mint", h.MintCode)'
            self.assertTrue(gate.requires_isolation("1.8.999"))
            self.assertIn("refs/tags/v1.8.999:backend/internal/server/routes/edge_tk_routes.go", show.call_args.args[0])
            run.side_effect = subprocess.CalledProcessError(1, "git")
            with self.assertRaises(subprocess.CalledProcessError):
                gate.requires_isolation("1.8.999")

    def test_cli_blocks_unknown_or_old_prod_but_allows_safe_mixed_versions(self):
        for required, result, expected in [
            (False, None, 0),
            (True, {"status": 404, "isolated": False}, 1),
            (True, {"status": 503, "isolated": True}, 0),
            (True, OSError("offline"), 1),
        ]:
            with self.subTest(required=required, result=result), \
                    patch.object(sys, "argv", ["gate", "--tag", "1.8.999"]), \
                    patch.object(gate, "requires_isolation", return_value=required), \
                    patch.object(gate, "probe_serving_prod") as probe, \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                if isinstance(result, Exception):
                    probe.side_effect = result
                else:
                    probe.return_value = result
                self.assertEqual(gate.main(), expected)
                self.assertEqual(probe.call_count, int(required))

    def test_workflow_gates_before_provision_and_container_mutation(self):
        root = Path(__file__).resolve().parents[2]
        workflow = yaml.safe_load((root / ".github/workflows/deploy-edge-lightsail-stage0.yml").read_text())
        steps = workflow["jobs"]["edge"]["steps"]
        gate_index = next(i for i, step in enumerate(steps)
                          if "check_edge_handoff_rollout.py" in step.get("run", ""))
        step = steps[gate_index]
        self.assertEqual(step["if"], "inputs.operation == 'provision' || inputs.operation == 'upgrade' || inputs.operation == 'rollback'")
        self.assertEqual(step["run"], 'python3 ops/stage0/check_edge_handoff_rollout.py --tag "$INPUT_TAG"')
        for i, candidate in enumerate(steps):
            if candidate.get("name") in ("Prepare safe Lightsail provision", "Upgrade or rollback via shared SSM deploy primitive"):
                self.assertLess(gate_index, i)
        self.assertNotIn("continue-on-error", step)


if __name__ == "__main__":
    unittest.main()
