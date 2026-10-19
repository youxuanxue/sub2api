#!/usr/bin/env python3
"""Unit tests for tpm_burst_recipe1 estimate math and formal gate."""

from __future__ import annotations

import json
import math
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(__file__).resolve().parent / "tpm_burst_recipe1.py"

sys.path.insert(0, str(Path(__file__).resolve().parent))
from tpm_burst_recipe1 import (  # noqa: E402
    FORMAL_APPROVE_TOKEN,
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


if __name__ == "__main__":
    unittest.main()
