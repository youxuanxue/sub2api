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
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(__file__).resolve().parent / "tpm_burst_recipe1.py"

sys.path.insert(0, str(Path(__file__).resolve().parent))
from tpm_burst_recipe1 import (  # noqa: E402
    FORMAL_APPROVE_TOKEN,
    _run_formal_load,
    cn_prod_host_header_default,
    cn_prod_public_origin,
    estimate_plan,
    case_replay_fields,
    load_chat_case_pool,
    _chat_completion,
    _loadtest_opener,
    loadtest_base_url,
    prepare_replay_body,
    refuse_aws_cutover_domain,
    request_record_from_sample,
    synthetic_context_case,
    resolve_host_header,
    sample_from_stream_events,
    select_operating_point,
    summarize_perf,
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


class OriginGuardTest(unittest.TestCase):
    def setUp(self) -> None:
        self._prev_base = os.environ.pop("TOKENKEY_BASE_URL", None)
        self._prev_host = os.environ.pop("TOKENKEY_HOST_HEADER", None)

    def tearDown(self) -> None:
        if self._prev_base is None:
            os.environ.pop("TOKENKEY_BASE_URL", None)
        else:
            os.environ["TOKENKEY_BASE_URL"] = self._prev_base
        if self._prev_host is None:
            os.environ.pop("TOKENKEY_HOST_HEADER", None)
        else:
            os.environ["TOKENKEY_HOST_HEADER"] = self._prev_host

    def test_refuse_api_cn_domain(self) -> None:
        origin = cn_prod_public_origin()
        host = cn_prod_host_header_default()
        self.assertIsNotNone(refuse_aws_cutover_domain(f"https://{host}"))
        self.assertIsNone(refuse_aws_cutover_domain(origin))

    def test_host_header_for_ip_origin(self) -> None:
        origin = cn_prod_public_origin()
        self.assertEqual(resolve_host_header(origin), cn_prod_host_header_default())
        self.assertIsNone(resolve_host_header("https://example.com"))

    def test_env_base_url_overrides_prod_target(self) -> None:
        os.environ["TOKENKEY_BASE_URL"] = "https://127.0.0.1:8443"
        self.assertEqual(cn_prod_public_origin(), "https://127.0.0.1:8443")

    def test_loadtest_default_is_galaxywave(self) -> None:
        self.assertEqual(loadtest_base_url(), "https://api.galaxywaveai.com")
        self.assertIsNone(refuse_aws_cutover_domain(loadtest_base_url()))
        self.assertIsNone(resolve_host_header(loadtest_base_url()))

    def test_copy_under_tmp_imports_without_repo_parents(self) -> None:
        import importlib.util

        dest = Path("/tmp/tk-recipe1-import-check.py")
        dest.write_text(SCRIPT.read_text(encoding="utf-8"), encoding="utf-8")
        try:
            spec = importlib.util.spec_from_file_location("tk_recipe1_tmp", dest)
            self.assertIsNotNone(spec)
            self.assertIsNotNone(spec.loader)
            module = importlib.util.module_from_spec(spec)
            sys.modules[spec.name] = module
            spec.loader.exec_module(module)
            self.assertEqual(module.loadtest_base_url(), "https://api.galaxywaveai.com")
            self.assertIsNone(module._prod_target_path_for(Path("/tmp/tpm_burst_recipe1.py")))
            self.assertEqual(
                module._prod_target_path_for(SCRIPT),
                ROOT / "deploy/domestic/prod-target.json",
            )
        finally:
            sys.modules.pop("tk_recipe1_tmp", None)
            dest.unlink(missing_ok=True)


class PerfMeasureTest(unittest.TestCase):
    def test_stream_sample_reports_ttft_tpot_and_cache(self) -> None:
        t0 = 100.0
        sample = sample_from_stream_events(
            [
                (100.5, {"choices": [{"delta": {"content": "Hi"}}]}),
                (
                    101.5,
                    {
                        "choices": [
                            {
                                "delta": {"content": " there"},
                                "finish_reason": "stop",
                            }
                        ],
                        "usage": {
                            "prompt_tokens": 50000,
                            "completion_tokens": 2,
                            "total_tokens": 50002,
                            "prompt_tokens_details": {"cached_tokens": 1000},
                            "completion_tokens_details": {"reasoning_tokens": 0},
                        },
                    },
                ),
            ],
            t0=t0,
            status=200,
            done=True,
            duration_s=1.6,
        )
        self.assertTrue(sample.ok)
        self.assertAlmostEqual(sample.ttft_s or 0, 0.5)
        self.assertAlmostEqual(sample.tpot_s or 0, 1.0)
        self.assertEqual(sample.cached_tokens, 1000)
        self.assertTrue(sample.cache_field_present)
        record = request_record_from_sample(
            sample,
            account_id=34,
            model="kimi-k3",
            step="ladder",
            inflight=5,
            seq=1,
            started_unix=1_700_000_000.0,
            request_id="req-1",
        )
        self.assertEqual(record["phase"], "request")
        self.assertEqual(record["account_id"], 34)
        self.assertEqual(record["inflight"], 5)
        self.assertEqual(record["prompt_tokens"], 50000)
        self.assertEqual(record["cached_tokens"], 1000)
        self.assertEqual(record["request_id"], "req-1")
        self.assertIsNotNone(record["ttft_s"])
        self.assertIsNotNone(record["tpot_s"])
        self.assertTrue(record["content_emitted"])
        self.assertIsNone(record["ttft_reasoning_s"])

    def test_reasoning_only_completion_is_transport_success(self) -> None:
        t0 = 10.0
        sample = sample_from_stream_events(
            [
                (10.4, {"choices": [{"delta": {"reasoning_content": "think"}}]}),
                (
                    12.0,
                    {
                        "choices": [{"delta": {}, "finish_reason": "length"}],
                        "usage": {
                            "prompt_tokens": 4000,
                            "completion_tokens": 1000,
                            "total_tokens": 5000,
                            "prompt_tokens_details": {"cached_tokens": 100},
                            "completion_tokens_details": {"reasoning_tokens": 1000},
                        },
                    },
                ),
            ],
            t0=t0,
            status=200,
            done=True,
            duration_s=2.0,
        )
        self.assertTrue(sample.ok)
        self.assertFalse(sample.content_emitted)
        self.assertIsNone(sample.ttft_s)
        self.assertAlmostEqual(sample.ttft_reasoning_s or 0, 0.4)
        self.assertEqual(sample.visible_tokens, 0)
        self.assertEqual(sample.error, "")

    def test_case_pool_keeps_output_limit_and_skips_truncated_body(self) -> None:
        import gzip

        directory = Path(tempfile.mkdtemp())
        bodies = directory / "bodies.jsonl.gz"
        index = directory / "index.jsonl.gz"
        good = {
            "model": "kimi-k3",
            "messages": [{"role": "user", "content": "secret-prompt"}],
            "reasoning_effort": "max",
            "max_completion_tokens": 1,
            "stream": True,
        }
        with gzip.open(bodies, "wt", encoding="utf-8") as handle:
            handle.write(json.dumps(good) + "\n")
            handle.write('{"messages": "unterminated\n')
        with gzip.open(index, "wt", encoding="utf-8") as handle:
            handle.write(json.dumps({"request_sha256": "abc", "redacted_marker": True}) + "\n")
            handle.write("{}\n")
        cases = load_chat_case_pool(str(bodies))
        self.assertEqual(len(cases), 1)
        self.assertEqual(cases[0]["sha256"], "abc")
        replay = prepare_replay_body(cases[0]["body"], model="kimi-k3")
        self.assertEqual(replay["max_completion_tokens"], 1)
        self.assertNotIn("max_tokens", replay)
        self.assertTrue(replay["stream_options"]["include_usage"])
        fields = case_replay_fields(cases[0])
        self.assertEqual(fields["output_limit"], 1)
        self.assertEqual(fields["output_limit_field"], "max_completion_tokens")
        self.assertEqual(fields["reasoning_effort"], "max")
        self.assertNotIn("secret-prompt", json.dumps(fields))

    def test_synthetic_context_case_is_about_50k_tokens(self) -> None:
        case = synthetic_context_case(prompt_tokens_target=50000, max_tokens=1000)
        content = case["body"]["messages"][0]["content"]
        self.assertGreaterEqual(len(content), 50000 * 4)
        self.assertEqual(case["body"]["max_tokens"], 1000)
        fields = case_replay_fields(case)
        self.assertEqual(fields["output_limit"], 1000)
        self.assertEqual(fields["output_limit_field"], "max_tokens")
        self.assertNotIn("FILLER_CONTEXT", json.dumps(fields))

    def test_summary_percentiles_and_operating_point(self) -> None:
        samples = [
            sample_from_stream_events(
                [
                    (1.0, {"choices": [{"delta": {"content": "a"}, "finish_reason": "stop"}]}),
                    (2.0, {
                        "choices": [{"delta": {"content": "b"}}],
                        "usage": {
                                "prompt_tokens": 100,
                                "completion_tokens": 2,
                                "total_tokens": 102,
                                "prompt_tokens_details": {"cached_tokens": 50},
                                "completion_tokens_details": {"reasoning_tokens": 0},
                            }
                        },
                    ),
                ],
                t0=0.0,
                status=200,
                done=True,
                duration_s=20.0,
            )
            for _ in range(10)
        ]
        summary = summarize_perf(
            samples,
            wall_s=60.0,
            inflight_trace=[(0.0, 0), (0.0, 4), (60.0, 4), (60.0, 0)],
            success_completion_times=[float(i) for i in range(10)],
            inflight=4,
        )
        self.assertEqual(summary["success_rate"], 1.0)
        self.assertEqual(summary["concurrency_peak"], 4)
        self.assertEqual(summary["concurrency_mean"], 4.0)
        self.assertEqual(summary["rpm_mean"], 10.0)
        self.assertEqual(summary["rpm_peak_rolling_60s"], 10)
        self.assertEqual(summary["cache_token_hit_rate"], 0.5)
        self.assertEqual(summary["cache_request_hit_rate"], 1.0)
        self.assertIsNotNone(summary["ttft_s_p50"])
        self.assertIsNotNone(summary["ttft_s_p90"])
        self.assertIsNotNone(summary["tpot_s_p90"])
        self.assertIsNotNone(summary["tokens_per_second_p50"])
        self.assertEqual(summary["latency_p90_over_p50"], 1.0)
        chosen = select_operating_point(
            [{"success_rate": 1.0, "inflight": 4}, {"success_rate": 0.5, "inflight": 8}],
            0.98,
        )
        self.assertEqual(chosen["inflight"], 4)
        self.assertIsNone(select_operating_point([{"success_rate": 0.5}], 0.98))

    def test_hold_keeps_concurrency_until_window_elapses(self) -> None:
        import tpm_burst_recipe1 as mod

        def fake_chat(**kwargs):  # noqa: ANN003
            time.sleep(0.04)
            return mod.Sample(
                ok=True,
                prompt_tokens=10,
                completion_tokens=2,
                total_tokens=12,
                duration_s=0.04,
                status_code=200,
                ttft_s=0.01,
                tpot_s=0.02,
                tokens_per_second=50.0,
                cached_tokens=1,
                cache_field_present=True,
                visible_tokens=2,
                finish_reason="stop",
            )

        original = mod._chat_completion
        mod._chat_completion = fake_chat  # type: ignore[assignment]
        requests = tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False).name
        try:
            summary = mod._run_perf_step(
                base_url="http://127.0.0.1:1",
                api_key="dummy",
                model="kimi-k3",
                messages=[{"role": "user", "content": "x"}],
                max_tokens=16,
                inflight=2,
                hold_s=0.25,
                timeout_s=2.0,
                host_header=None,
                requests_out=requests,
            )
        finally:
            mod._chat_completion = original  # type: ignore[assignment]

        self.assertGreater(summary["submitted_n"], 2)
        self.assertGreaterEqual(summary["wall_s"], 0.25)
        self.assertTrue(summary["hold_filled"])
        self.assertEqual(summary["hold_s"], 0.25)
        rows = [json.loads(line) for line in Path(requests).read_text(encoding="utf-8").splitlines()]
        self.assertEqual(len(rows), summary["submitted_n"])
        self.assertEqual(rows[0]["phase"], "request")
        self.assertEqual(rows[0]["inflight"], 2)
        self.assertIn("ttft_s", rows[0])
        self.assertIn("cached_tokens", rows[0])

    def test_perf_dry_run_plans_five_minutes_without_traffic(self) -> None:
        env = os.environ.copy()
        env.pop("APPROVE", None)
        env.pop("TOKENKEY_BASE_URL", None)
        env.pop("TOKENKEY_API_KEY", None)
        env.pop("REQUEST_POOL", None)
        env.pop("CASES_PATH", None)
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "perf", "--dry-run", "--steps", "4"],
            capture_output=True,
            text=True,
            env=env,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        plan = json.loads(proc.stdout)
        self.assertEqual(plan["phase"], "perf_dry_run_ok")
        self.assertTrue(plan["will_not_send_traffic"])
        self.assertEqual(plan["base_url"], "https://api.galaxywaveai.com")
        self.assertEqual(plan["hold_s"], 300.0)
        self.assertEqual(plan["shape"]["source"], "synthetic_context")
        self.assertEqual(plan["shape"]["prompt_tokens_target"], 50000)
        self.assertEqual(plan["shape"]["max_tokens"], 1000)
        self.assertNotIn("cases_path", plan["shape"])
        self.assertIn("content_rate", plan["metrics"])
        self.assertIn("ttft_reasoning_s_p50", plan["metrics"])
        self.assertIn("ttft_s_p50", plan["metrics"])
        self.assertIn("china_deployed", plan["group_requirement"])

    def test_perf_without_approval_sends_nothing(self) -> None:
        env = os.environ.copy()
        env.pop("APPROVE", None)
        env["TOKENKEY_BASE_URL"] = "http://127.0.0.1:1"
        env["TOKENKEY_API_KEY"] = "dummy"
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "perf", "--steps", "1", "--hold-s", "300"],
            capture_output=True,
            text=True,
            env=env,
            check=False,
            timeout=10,
        )
        self.assertEqual(proc.returncode, 10, proc.stderr)
        self.assertIn("perf_blocked", proc.stderr)
        self.assertIn("will_not_send_traffic", proc.stderr)


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


class LoadtestDirectOpenerTest(unittest.TestCase):
    def test_opener_replaces_system_proxy(self) -> None:
        previous = {name: os.environ.get(name) for name in ("http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY")}
        os.environ["http_proxy"] = "http://127.0.0.1:7890"
        os.environ["https_proxy"] = "http://127.0.0.1:7890"
        os.environ["HTTP_PROXY"] = "http://127.0.0.1:7890"
        os.environ["HTTPS_PROXY"] = "http://127.0.0.1:7890"
        try:
            opener = _loadtest_opener()
            default = urllib.request.build_opener()
        finally:
            for name, value in previous.items():
                if value is None:
                    os.environ.pop(name, None)
                else:
                    os.environ[name] = value
        # An empty ProxyHandler installs no proxy methods, so the default
        # env-based ProxyHandler is skipped and nothing proxy-related remains.
        default_proxy = [
            h.proxies for h in default.handlers if isinstance(h, urllib.request.ProxyHandler)
        ]
        self.assertTrue(any("127.0.0.1:7890" in str(proxies) for proxies in default_proxy))
        self.assertFalse(any(isinstance(h, urllib.request.ProxyHandler) for h in opener.handlers))

    def test_chat_completion_uses_direct_opener(self) -> None:
        opened: dict[str, object] = {}

        class _Resp:
            status = 200

            def __enter__(self) -> "_Resp":
                return self

            def __exit__(self, *args: object) -> bool:
                return False

            def read(self) -> bytes:
                return json.dumps(
                    {"usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}}
                ).encode()

        class _Opener:
            def open(self, req: urllib.request.Request, timeout: float | None = None) -> _Resp:
                opened["url"] = req.full_url
                opened["timeout"] = timeout
                return _Resp()

        import tpm_burst_recipe1 as mod

        original = mod._loadtest_opener
        mod._loadtest_opener = lambda: _Opener()  # type: ignore[assignment]
        try:
            sample = _chat_completion(
                base_url="http://127.0.0.1:1",
                api_key="dummy",
                model="kimi-k3",
                messages=[{"role": "user", "content": "hi"}],
                max_tokens=8,
                timeout_s=5,
                stream=False,
            )
        finally:
            mod._loadtest_opener = original  # type: ignore[assignment]
        self.assertTrue(sample.ok)
        self.assertEqual(sample.total_tokens, 4)
        self.assertEqual(opened["url"], "http://127.0.0.1:1/v1/chat/completions")
        self.assertEqual(opened["timeout"], 5)


if __name__ == "__main__":
    unittest.main()
