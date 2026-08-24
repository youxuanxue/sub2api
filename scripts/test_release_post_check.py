#!/usr/bin/env python3
"""Tests for scripts/release_post_check.py — plan from live→new PRs, evaluate live ticks.

The +5min post-release check must be derived from the commits/PRs between the
tag that was actually serving and the tag just deployed. Model-invented hook
strings are not a valid plan.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

_ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT / "scripts"))

import release_post_check as rpc  # noqa: E402


def _clean_env() -> dict[str, str]:
    return {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}


def _git(cwd: pathlib.Path, *args: str) -> str:
    proc = subprocess.run(
        ["git", *args],
        cwd=cwd,
        env=_clean_env(),
        capture_output=True,
        text=True,
        check=True,
    )
    return proc.stdout


class ReleasePostCheckPlanTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self._tmp.name) / "repo"
        self.repo.mkdir()
        _git(self.repo, "init", "-q", "-b", "main")
        _git(self.repo, "config", "user.email", "test@example.com")
        _git(self.repo, "config", "user.name", "Test")
        (self.repo / "README.md").write_text("base\n")
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", "base")
        _git(self.repo, "tag", "-a", "v1.8.169", "-m", "Release 1.8.169")

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _commit(self, files: dict[str, str], msg: str) -> str:
        for relpath, content in files.items():
            target = self.repo / relpath
            target.parent.mkdir(parents=True, exist_ok=True)
            if target.exists() and content == "":
                target.unlink()
            else:
                target.write_text(content)
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", msg)
        return _git(self.repo, "rev-parse", "--short", "HEAD").strip()

    def test_plan_lists_prs_and_skips_version_bump(self) -> None:
        self._commit(
            {"backend/internal/service/gateway_forward.go": "package service\n"},
            "fix(gateway): failover CloudWise-class 424 (#1780)",
        )
        self._commit(
            {"backend/internal/service/subscription_service.go": "package service\n"},
            "fix(gateway): 订阅不可用时回退到余额专属组 (#1781)",
        )
        self._commit({"backend/cmd/server/VERSION": "1.8.170\n"}, "chore: bump VERSION to 1.8.170")
        _git(self.repo, "tag", "-a", "v1.8.170", "-m", "Release 1.8.170")

        plan = rpc.build_plan(self.repo, "v1.8.169", "v1.8.170")
        prs = [c["pr"] for c in plan["changes"]]
        self.assertEqual(prs, [1780, 1781])
        self.assertTrue(all("bump VERSION" not in c["subject"] for c in plan["changes"]))
        self.assertEqual(plan["range"], {"live": "v1.8.169", "new": "v1.8.170"})

    def test_plan_derives_added_failover_status(self) -> None:
        old = (
            "package service\n"
            "func shouldFailover(statusCode int) bool {\n"
            "\tswitch statusCode {\n"
            "\tcase 401, 403, 429, 529:\n"
            "\t\treturn true\n"
            "\t}\n"
            "\treturn false\n"
            "}\n"
        )
        new = old.replace("case 401, 403, 429, 529:", "case 401, 403, 424, 429, 529:")
        self._commit({"backend/internal/service/gateway_forward.go": old}, "base forward")
        _git(self.repo, "tag", "-d", "v1.8.169")
        _git(self.repo, "tag", "-a", "v1.8.169", "-m", "Release 1.8.169")
        self._commit(
            {"backend/internal/service/gateway_forward.go": new},
            "fix(gateway): failover CloudWise-class 424 (#1780)",
        )
        _git(self.repo, "tag", "-a", "v1.8.170", "-m", "Release 1.8.170")

        plan = rpc.build_plan(self.repo, "v1.8.169", "v1.8.170")
        patterns = {c["pattern"] for c in plan["checks"] if c["kind"] == "failover_status"}
        self.assertEqual(patterns, {"Status=424"})
        self.assertNotIn("Status=429", patterns)

    def test_plan_ignores_status_codes_only_mentioned_in_tests(self) -> None:
        old = (
            "package service\n"
            "func shouldFailover(statusCode int) bool {\n"
            "\tswitch statusCode {\n"
            "\tcase 401, 403, 429, 529:\n"
            "\t\treturn true\n"
            "\t}\n"
            "\treturn false\n"
            "}\n"
        )
        new = old.replace("case 401, 403, 429, 529:", "case 401, 403, 424, 429, 529:")
        self._commit({"backend/internal/service/gateway_forward.go": old}, "base forward")
        _git(self.repo, "tag", "-d", "v1.8.169")
        _git(self.repo, "tag", "-a", "v1.8.169", "-m", "Release 1.8.169")
        self._commit(
            {
                "backend/internal/service/gateway_forward.go": new,
                "backend/internal/service/gateway_forward_failover_status_test.go": (
                    "package service\n"
                    "func TestNotFailover() {\n"
                    "\t_ = []int{400, 404, 408, 422}\n"
                    "}\n"
                ),
            },
            "fix(gateway): failover CloudWise-class 424 (#1780)",
        )
        _git(self.repo, "tag", "-a", "v1.8.170", "-m", "Release 1.8.170")

        plan = rpc.build_plan(self.repo, "v1.8.169", "v1.8.170")
        patterns = {c["pattern"] for c in plan["checks"] if c["kind"] == "failover_status"}
        self.assertEqual(patterns, {"Status=424"})
        self.assertNotIn("Status=400", patterns)

    def test_plan_derives_subscription_error_checks_from_path(self) -> None:
        self._commit(
            {
                "backend/internal/service/subscription_service.go": "package service\nfunc SubscriptionGroupUsable() {}\n",
                "backend/internal/service/universal_routing_tk_resolver.go": "package service\nfunc pickUsableBackingGroup() {}\n",
            },
            "fix(gateway): fall back when subscription cannot serve (#1781)",
        )
        _git(self.repo, "tag", "-a", "v1.8.170", "-m", "Release 1.8.170")

        plan = rpc.build_plan(self.repo, "v1.8.169", "v1.8.170")
        by_id = {c["id"]: c for c in plan["checks"]}
        self.assertIn("pr-1781-WEEKLY_LIMIT_EXCEEDED", by_id)
        self.assertEqual(by_id["pr-1781-WEEKLY_LIMIT_EXCEEDED"]["kind"], "observe")
        self.assertEqual(by_id["pr-1781-WEEKLY_LIMIT_EXCEEDED"]["source"], "#1781")
        self.assertFalse(any(c["pattern"] == "error: code=" for c in plan["checks"]))

    def test_hook_patterns_come_from_plan_not_empty(self) -> None:
        self._commit(
            {"backend/internal/service/subscription_service.go": "package service\n"},
            "fix(gateway): subscription fallback (#1781)",
        )
        _git(self.repo, "tag", "-a", "v1.8.170", "-m", "Release 1.8.170")
        plan = rpc.build_plan(self.repo, "v1.8.169", "v1.8.170")
        patterns = rpc.hook_patterns(plan)
        self.assertIn("WEEKLY_LIMIT_EXCEEDED", patterns)
        self.assertTrue(patterns)

    def test_real_v1_8_169_to_v1_8_170_lists_prs_and_only_new_424(self) -> None:
        try:
            plan = rpc.build_plan(_ROOT, "v1.8.169", "v1.8.170")
        except RuntimeError as exc:
            self.skipTest(f"release tags unavailable: {exc}")
        self.assertEqual([c["pr"] for c in plan["changes"]], [1780, 1781])
        failover = {c["pattern"] for c in plan["checks"] if c["kind"] == "failover_status"}
        self.assertEqual(failover, {"Status=424"})
        sources = {c["source"] for c in plan["checks"]}
        self.assertIn("#1780", sources)
        self.assertIn("#1781", sources)
        self.assertFalse(any(c["pattern"] == "error: code=" for c in plan["checks"]))
        self.assertTrue(
            all(c["kind"] == "observe" for c in plan["checks"] if c["source"] == "#1781")
        )


class ReleasePostCheckEvaluateTest(unittest.TestCase):
    def _plan(self) -> dict:
        return {
            "range": {"live": "v1.8.169", "new": "v1.8.170"},
            "changes": [
                {"pr": 1780, "subject": "fix 424 (#1780)", "files": ["backend/internal/service/gateway_forward.go"]},
                {"pr": 1781, "subject": "fix sub (#1781)", "files": ["backend/internal/service/subscription_service.go"]},
            ],
            "checks": [
                {
                    "id": "pr-1780-Status=424",
                    "source": "#1780",
                    "kind": "failover_status",
                    "pattern": "Status=424",
                    "expect": "failover_if_present",
                },
                {
                    "id": "pr-1781-WEEKLY_LIMIT_EXCEEDED",
                    "source": "#1781",
                    "kind": "observe",
                    "pattern": "WEEKLY_LIMIT_EXCEEDED",
                    "expect": "report_only",
                },
                {
                    "id": "pr-1781-NEW_CODE",
                    "source": "#1781",
                    "kind": "error_absent",
                    "pattern": "NEW_LIMIT_CODE",
                    "expect": "not_storming",
                },
            ],
        }

    def test_evaluate_inconclusive_when_new_path_has_no_traffic(self) -> None:
        tick = {
            "hooks": {"Status=424": 0, "WEEKLY_LIMIT_EXCEEDED": 0, "[Forward] Upstream error (failover)": 0},
            "panic": 0,
            "status_5xx": {},
            "completed_total": 20,
        }
        result = rpc.evaluate(self._plan(), tick, control_plane_ok=True)
        self.assertEqual(result["verdict"], "green")
        by_id = {c["id"]: c for c in result["checks"]}
        self.assertEqual(by_id["pr-1780-Status=424"]["verdict"], "inconclusive")
        self.assertEqual(by_id["pr-1781-WEEKLY_LIMIT_EXCEEDED"]["verdict"], "observe")

    def test_evaluate_red_when_424_is_terminal_without_failover(self) -> None:
        tick = {
            "hooks": {"Status=424": 4, "[Forward] Upstream error (failover)": 0},
            "panic": 0,
            "status_5xx": {},
            "completed_total": 20,
        }
        result = rpc.evaluate(self._plan(), tick, control_plane_ok=True)
        self.assertEqual(result["verdict"], "red")
        by_id = {c["id"]: c for c in result["checks"]}
        self.assertEqual(by_id["pr-1780-Status=424"]["verdict"], "fail")

    def test_evaluate_observe_weekly_limit_and_sparse_5xx_do_not_redden(self) -> None:
        tick = {
            "hooks": {"WEEKLY_LIMIT_EXCEEDED": 12, "Status=424": 0, "NEW_LIMIT_CODE": 0},
            "panic": 0,
            "status_5xx": {"500": 2},
            "completed_total": 20,
        }
        result = rpc.evaluate(self._plan(), tick, control_plane_ok=True)
        self.assertEqual(result["verdict"], "green")
        by_id = {c["id"]: c for c in result["checks"]}
        self.assertEqual(by_id["pr-1781-WEEKLY_LIMIT_EXCEEDED"]["verdict"], "observe")
        self.assertEqual(by_id["status-5xx"]["verdict"], "observe")
        self.assertEqual(by_id["status-5xx"]["observed"]["total"], 2)

    def test_evaluate_red_on_5xx_storm_seen_in_v1_8_171_release(self) -> None:
        tick = {
            "hooks": {},
            "panic": 0,
            "status_5xx": {"502": 21},
            "completed_total": 125,
        }
        result = rpc.evaluate(self._plan(), tick, control_plane_ok=True)
        self.assertEqual(result["verdict"], "red")
        by_id = {c["id"]: c for c in result["checks"]}
        self.assertEqual(by_id["status-5xx"]["verdict"], "fail")
        self.assertEqual(by_id["status-5xx"]["observed"]["total"], 21)
        self.assertEqual(
            by_id["status-5xx"]["observed"]["storm_threshold"],
            rpc.ERROR_STORM_THRESHOLD,
        )

    def test_evaluate_5xx_storm_threshold_is_inclusive_across_statuses(self) -> None:
        tick = {
            "hooks": {},
            "panic": 0,
            "status_5xx": {"500": 9, "502": 10, "503": 1},
            "completed_total": 40,
        }
        result = rpc.evaluate(self._plan(), tick, control_plane_ok=True)
        self.assertEqual(result["verdict"], "red")
        by_id = {c["id"]: c for c in result["checks"]}
        self.assertEqual(by_id["status-5xx"]["observed"]["total"], 20)

    def test_evaluate_red_on_added_error_ctor_storm(self) -> None:
        tick = {
            "hooks": {"NEW_LIMIT_CODE": 20, "WEEKLY_LIMIT_EXCEEDED": 0, "Status=424": 0},
            "panic": 0,
            "status_5xx": {},
            "completed_total": 20,
        }
        result = rpc.evaluate(self._plan(), tick, control_plane_ok=True)
        self.assertEqual(result["verdict"], "red")

    def test_evaluate_red_on_panic_or_control_plane_not_sparse_5xx(self) -> None:
        tick = {"hooks": {}, "panic": 1, "status_5xx": {}, "completed_total": 1}
        self.assertEqual(rpc.evaluate(self._plan(), tick, control_plane_ok=True)["verdict"], "red")
        tick = {"hooks": {}, "panic": 0, "status_5xx": {"500": 2}, "completed_total": 1}
        self.assertEqual(rpc.evaluate(self._plan(), tick, control_plane_ok=True)["verdict"], "green")
        tick = {"hooks": {}, "panic": 0, "status_5xx": {}, "completed_total": 1}
        self.assertEqual(rpc.evaluate(self._plan(), tick, control_plane_ok=False)["verdict"], "red")

    def test_parse_tick_stdout(self) -> None:
        stdout = """
=== meta ===
{"container": "tokenkey-blue", "log_lines": 10}
=== hooks ===
{"pattern": "Status=424", "count": 2}
{"pattern": "WEEKLY_LIMIT_EXCEEDED", "count": 0}
=== panic ===
{"count": 0}
=== traffic ===
{"completed_total": 9, "top_paths": [{"path": "/v1/messages", "n": 9}], "status_5xx": {}}
"""
        parsed = rpc.parse_tick_stdout(stdout)
        self.assertEqual(parsed["hooks"]["Status=424"], 2)
        self.assertEqual(parsed["completed_total"], 9)
        self.assertEqual(parsed["panic"], 0)


class DeployStage0PostReleaseJobTest(unittest.TestCase):
    def test_workflow_runs_post_release_check_in_deploy_job(self) -> None:
        text = (_ROOT / ".github/workflows/deploy-stage0.yml").read_text(encoding="utf-8")
        self.assertIn("run-post-release-check.sh", text)
        self.assertIn("release_post_check.py plan", text)
        self.assertIn("sleep 300", text)
        self.assertIn("steps.previous_runtime.outputs.tag", text)
        self.assertNotIn("HOOK_PATTERNS=<hook", text)
        self.assertNotIn("\n  post-release-check:\n", text)
        self.assertNotIn("needs: deploy", text)
        deploy_start = text.index("  deploy:")
        next_job = text.find("\n  qa-infra-check:", deploy_start)
        deploy_block = text[deploy_start:next_job]
        self.assertIn("id: post_release_check", deploy_block)
        self.assertIn("--plan-file", deploy_block)
        feishu_pos = deploy_block.index("name: Notify Feishu (release rollout)")
        plan_pos = deploy_block.index("name: Plan checks from live→new PRs")
        self.assertLess(plan_pos, feishu_pos)
        self.assertEqual(deploy_block.split("steps:", 1)[0].count("environment: prod"), 1)

    def test_skill_forbids_model_invented_hooks(self) -> None:
        text = (
            _ROOT / ".cursor/skills/tokenkey-stage0-release-rollout/SKILL.md"
        ).read_text(encoding="utf-8")
        self.assertIn("run-post-release-check.sh", text)
        self.assertIn("release_post_check.py", text)
        self.assertNotIn("HOOK_PATTERNS=<hook1>", text)
        self.assertNotIn("关键词由模型按 hook 命名", text)
        self.assertNotIn("模型按 Step A 命名", text)

    def test_wrapper_evaluates_skip_probe_tick(self) -> None:
        wrapper = _ROOT / "ops/observability/run-post-release-check.sh"
        self.assertTrue(os.access(wrapper, os.X_OK), f"{wrapper} must be executable")
        with tempfile.TemporaryDirectory() as raw:
            repo = pathlib.Path(raw) / "repo"
            repo.mkdir()
            _git(repo, "init", "-q", "-b", "main")
            _git(repo, "config", "user.email", "test@example.com")
            _git(repo, "config", "user.name", "Test")
            (repo / "README.md").write_text("base\n")
            _git(repo, "add", "-A")
            _git(repo, "commit", "-q", "-m", "base")
            _git(repo, "tag", "-a", "v1.8.169", "-m", "Release 1.8.169")
            target = repo / "backend/internal/service/subscription_service.go"
            target.parent.mkdir(parents=True)
            target.write_text("package service\n")
            _git(repo, "add", "-A")
            _git(repo, "commit", "-q", "-m", "fix(gateway): fallback (#1781)")
            _git(repo, "tag", "-a", "v1.8.170", "-m", "Release 1.8.170")
            tick = {
                "hooks": {"WEEKLY_LIMIT_EXCEEDED": 0},
                "panic": 0,
                "status_5xx": {},
                "completed_total": 4,
            }
            tick_path = pathlib.Path(raw) / "tick.json"
            out_dir = pathlib.Path(raw) / "out"
            tick_path.write_text(json.dumps(tick), encoding="utf-8")
            proc = subprocess.run(
                [
                    "bash",
                    str(wrapper),
                    "--live",
                    "1.8.169",
                    "--new",
                    "1.8.170",
                    "--repo",
                    str(repo),
                    "--skip-probe",
                    "--tick-file",
                    str(tick_path),
                    "--out-dir",
                    str(out_dir),
                ],
                cwd=_ROOT,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            result = json.loads((out_dir / "evaluate.json").read_text(encoding="utf-8"))
            self.assertEqual(result["verdict"], "green")
            self.assertEqual(result["changes"][0]["pr"], 1781)

    def test_wrapper_reuses_existing_plan_file(self) -> None:
        wrapper = _ROOT / "ops/observability/run-post-release-check.sh"
        with tempfile.TemporaryDirectory() as raw:
            repo = pathlib.Path(raw) / "repo"
            repo.mkdir()
            _git(repo, "init", "-q", "-b", "main")
            _git(repo, "config", "user.email", "test@example.com")
            _git(repo, "config", "user.name", "Test")
            (repo / "README.md").write_text("base\n")
            _git(repo, "add", "-A")
            _git(repo, "commit", "-q", "-m", "base")
            _git(repo, "tag", "-a", "v1.8.169", "-m", "Release 1.8.169")
            target = repo / "backend/internal/service/subscription_service.go"
            target.parent.mkdir(parents=True)
            target.write_text("package service\n")
            _git(repo, "add", "-A")
            _git(repo, "commit", "-q", "-m", "fix(gateway): fallback (#1781)")
            _git(repo, "tag", "-a", "v1.8.170", "-m", "Release 1.8.170")
            out_dir = pathlib.Path(raw) / "out"
            out_dir.mkdir()
            plan_proc = subprocess.run(
                [
                    "python3",
                    str(_ROOT / "scripts/release_post_check.py"),
                    "plan",
                    "--live",
                    "1.8.169",
                    "--new",
                    "1.8.170",
                    "--repo",
                    str(repo),
                ],
                capture_output=True,
                text=True,
                check=True,
            )
            plan_path = out_dir / "plan.json"
            plan_path.write_text(plan_proc.stdout, encoding="utf-8")
            tick = {
                "hooks": {"WEEKLY_LIMIT_EXCEEDED": 0},
                "panic": 0,
                "status_5xx": {},
                "completed_total": 4,
            }
            tick_path = pathlib.Path(raw) / "tick.json"
            tick_path.write_text(json.dumps(tick), encoding="utf-8")
            proc = subprocess.run(
                [
                    "bash",
                    str(wrapper),
                    "--live",
                    "1.8.169",
                    "--new",
                    "1.8.170",
                    "--repo",
                    str(repo),
                    "--skip-probe",
                    "--tick-file",
                    str(tick_path),
                    "--plan-file",
                    str(plan_path),
                    "--out-dir",
                    str(out_dir),
                ],
                cwd=_ROOT,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn("reusing existing plan", proc.stderr)
            plan_data = json.loads(plan_path.read_text(encoding="utf-8"))
            self.assertEqual(plan_data["range"]["live"], "v1.8.169")


if __name__ == "__main__":
    unittest.main()
