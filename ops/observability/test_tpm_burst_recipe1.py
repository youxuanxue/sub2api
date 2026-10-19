#!/usr/bin/env python3
"""Unit tests for tpm_burst_recipe1 estimate math and formal gate."""

from __future__ import annotations

import json
import math
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(__file__).resolve().parent / "tpm_burst_recipe1.py"

sys.path.insert(0, str(Path(__file__).resolve().parent))
from tpm_burst_recipe1 import (  # noqa: E402
    FORMAL_APPROVE_TOKEN,
    _run_formal_load,
    estimate_plan,
)


class EstimateMathTest(unittest.TestCase):
    def test_n_uses_p10_tokens(self) -> None:
        # Spread tokens so p10 < mean → N sized on the lower tail.
        toks = [30000, 40000, 40000, 40000, 50000]
        est = estimate_plan(
            samples_tok=toks,
            samples_duration_s=[40, 42, 44, 46, 48],
            target_tpm=20_000_000,
            pool_slots=252,
            failure_overhead=1.0,
        )
        self.assertGreater(est.tok_per_req, est.tok_per_req_p10)
        self.assertEqual(est.n_min_success, math.ceil(20_000_000 / est.tok_per_req_p10))

    def test_ceiling_flags_infeasible_for_6k_shape(self) -> None:
        est = estimate_plan(
            samples_tok=[6000],
            samples_duration_s=[33],
            target_tpm=20_000_000,
            pool_slots=252,
        )
        self.assertFalse(est.feasible)
        self.assertLess(est.tpm_ceiling_conservative, 20_000_000)

    def test_high_tok_shape_can_be_feasible(self) -> None:
        # 50k tok, 20s, 252 slots → rpm_ceil=756, tpm≈37.8M
        est = estimate_plan(
            samples_tok=[50000, 50000, 50000, 50000, 50000],
            samples_duration_s=[20, 20, 20, 20, 20],
            target_tpm=20_000_000,
            pool_slots=252,
            failure_overhead=1.15,
        )
        self.assertTrue(est.feasible)
        self.assertEqual(est.n_min_success, 400)  # ceil(20e6/50000)
        self.assertEqual(est.n_plan_total, 460)
        self.assertLessEqual(est.threads, 252)

    def test_threads_capped_by_pool(self) -> None:
        est = estimate_plan(
            samples_tok=[40000],
            samples_duration_s=[45],
            target_tpm=20_000_000,
            pool_slots=10,
            failure_overhead=1.0,
        )
        self.assertEqual(est.threads, 10)

    def test_target_plan_notes_when_not_saturating_pool(self) -> None:
        # 20M target with 100k tok needs ~200 inflight threads; 1464 slots stay mostly idle.
        est = estimate_plan(
            samples_tok=[100000],
            samples_duration_s=[50],
            target_tpm=20_000_000,
            pool_slots=1464,
            failure_overhead=1.0,
        )
        self.assertLess(est.threads, int(1464 * 0.9))
        joined = " ".join(est.accuracy_notes)
        self.assertIn("per-account TPM ceilings", joined)
        self.assertIn("target-TPM burst", joined)


class CliGateTest(unittest.TestCase):
    def test_formal_blocked_without_approve(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump({"estimate": None, "samples": []}, f)
            path = f.name
        env = os.environ.copy()
        env.pop("APPROVE", None)
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "formal", "--from", path],
            capture_output=True,
            text=True,
            env=env,
            check=False,
        )
        self.assertEqual(proc.returncode, 10)
        self.assertIn("formal_blocked", proc.stderr)

    def test_formal_dry_run_without_approve(self) -> None:
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as pf:
            pf.write("explain this code pack")
            prompt = pf.name
        est = {
            "target_tpm": 20_000_000,
            "tok_per_req": 100000.0,
            "tok_per_req_p10": 100000.0,
            "tok_per_req_p90": 100000.0,
            "duration_s": 50.0,
            "duration_s_p90": 55.0,
            "pool_slots": 252,
            "n_min_success": 200,
            "n_plan_total": 230,
            "threads": 150,
            "per_thread": 2,
            "rpm_needed": 200.0,
            "rpm_ceiling": 270.0,
            "tpm_ceiling": 27_000_000.0,
            "tpm_ceiling_conservative": 27_000_000.0,
            "feasible": True,
            "feasibility_reason": "ok",
            "accuracy_notes": [],
            "calibrate_n": 5,
        }
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump({"estimate": est, "max_tokens": 1200, "model": "kimi-k3", "prompt_file": prompt}, f)
            path = f.name
        env = os.environ.copy()
        env.pop("APPROVE", None)
        proc = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "formal",
                "--from",
                path,
                "--prompt-file",
                prompt,
                "--dry-run",
            ],
            capture_output=True,
            text=True,
            env=env,
            check=False,
        )
        self.assertEqual(proc.returncode, 0)
        self.assertIn("formal_dry_run_ok", proc.stdout)
        self.assertIn("will_not_send_traffic", proc.stdout)

    def test_calibrate_rejects_n_over_9(self) -> None:
        proc = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "calibrate",
                "--n",
                "10",
                "--api-key",
                "dummy",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 2)

    def test_plan_recomputes_pool_slots_over_embedded_estimate(self) -> None:
        """Stale calibrate JSON (21×12) must not pin plan when --pool-slots changes."""
        samples = [
            {
                "ok": True,
                "prompt_tokens": 100000,
                "completion_tokens": 1200,
                "total_tokens": 101200,
                "duration_s": 50.0,
                "status_code": 200,
                "error": "",
            }
        ] * 5
        stale_est = {
            "target_tpm": 20_000_000,
            "tok_per_req": 101200.0,
            "tok_per_req_p10": 101200.0,
            "tok_per_req_p90": 101200.0,
            "duration_s": 50.0,
            "duration_s_p90": 50.0,
            "pool_slots": 252,
            "n_min_success": 198,
            "n_plan_total": 228,
            "threads": 165,
            "per_thread": 2,
            "rpm_needed": 198.0,
            "rpm_ceiling": 302.0,
            "tpm_ceiling": 30_000_000.0,
            "tpm_ceiling_conservative": 30_000_000.0,
            "feasible": True,
            "feasibility_reason": "stale",
            "accuracy_notes": [],
            "calibrate_n": 5,
        }
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump({"estimate": stale_est, "samples": samples, "model": "kimi-k3"}, f)
            path = f.name
        proc = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "plan",
                "--from",
                path,
                "--pool-slots",
                "1464",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        plan = json.loads(proc.stdout)
        self.assertEqual(plan["estimate"]["pool_slots"], 1464)
        self.assertGreater(plan["estimate"]["tpm_ceiling_conservative"], 100_000_000)


class FormalRunnerTest(unittest.TestCase):
    def test_wall_limit_stops_new_submits(self) -> None:
        calls = {"n": 0}

        def fake_chat(**kwargs):  # noqa: ANN003
            from tpm_burst_recipe1 import Sample

            calls["n"] += 1
            time.sleep(0.05)
            return Sample(
                ok=True,
                prompt_tokens=10,
                completion_tokens=1,
                total_tokens=11,
                duration_s=0.05,
                status_code=200,
            )

        import tpm_burst_recipe1 as mod

        original = mod._chat_completion
        mod._chat_completion = fake_chat  # type: ignore[assignment]
        try:
            with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as pf:
                pf.write("tiny prompt")
                prompt = pf.name
            summary = _run_formal_load(
                base_url="http://example.invalid",
                api_key="dummy",
                model="kimi-k3",
                prompt_file=prompt,
                max_tokens=16,
                total_requests=40,
                max_inflight=4,
                timeout_s=5.0,
                wall_limit_s=0.08,
            )
        finally:
            mod._chat_completion = original  # type: ignore[assignment]

        self.assertTrue(summary["stopped_early"])
        self.assertLess(summary["submitted_n"], 40)
        self.assertEqual(summary["submitted_n"], calls["n"])
        self.assertEqual(summary["fail_status_hist"], {})


if __name__ == "__main__":
    unittest.main()
