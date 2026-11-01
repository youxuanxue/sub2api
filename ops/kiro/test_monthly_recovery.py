from __future__ import annotations

import importlib.util
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
MODULE_PATH = ROOT / "ops" / "kiro" / "monthly_recovery.py"
SPEC = importlib.util.spec_from_file_location("monthly_recovery", MODULE_PATH)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def test_classify_probe_prefers_servable_model_result() -> None:
    raw = "\n".join(
        [
            json.dumps({"verdict": "catalog_snapshot", "models": ["claude-sonnet-4.5"]}),
            json.dumps(
                {
                    "verdict": "servable",
                    "account_id": 42,
                    "model": "claude-sonnet-4.5",
                    "http_status": 200,
                }
            ),
        ]
    )
    result = MODULE.classify_probe_output(raw, 42, "claude-sonnet-4.5")
    assert result["verdict"] == "servable"
    assert result["http_status"] == 200
    assert result["catalog_has_model"] is True


def test_classify_probe_keeps_upstream_failure_bounded() -> None:
    raw = json.dumps(
        {
            "verdict": "upstream_rejected",
            "account_id": 42,
            "model": "claude-sonnet-4.5",
            "http_status": 403,
            "error": "secret-token " + ("x" * 400),
        }
    )
    result = MODULE.classify_probe_output(raw, 42, "claude-sonnet-4.5")
    assert result["verdict"] == "upstream_rejected"
    assert result["http_status"] == 403
    assert len(result["error"]) <= 240
    assert result["failure_class"] == "oauth"


def test_classify_probe_maps_quota_status() -> None:
    result = MODULE.classify_probe_output(
        json.dumps(
            {
                "verdict": "upstream_rejected",
                "model": "claude-sonnet-4.5",
                "http_status": 402,
            }
        ),
        42,
        "claude-sonnet-4.5",
    )
    assert result["failure_class"] == "quota"


def test_summary_is_partial_failure_when_any_account_fails() -> None:
    result = MODULE.summarize_results(
        [
            {"account_id": 1, "model": "m", "verdict": "servable"},
            {"account_id": 2, "model": "m", "verdict": "upstream_rejected"},
        ]
    )
    assert result["verdict"] == "partial_failure"
    assert result["counts"] == {"servable": 1, "upstream_rejected": 1}
    assert [row["account_id"] for row in result["failures"]] == [2]


def test_summary_marks_dry_run_without_calling_it_a_failure() -> None:
    result = MODULE.summarize_results(
        [{"account_id": 1, "model": "m", "verdict": "dry_run"}]
    )
    assert result["verdict"] == "dry_run"
    assert result["failures"] == []


def test_summary_marks_schedulable_restore_failure() -> None:
    result = MODULE.summarize_results(
        [{"account_id": 1, "model": "m", "verdict": "enable_failed"}]
    )
    assert result["verdict"] == "partial_failure"
    assert result["failures"][0]["account_id"] == 1


def test_dry_run_does_not_require_app_or_admin_key_and_caps_workers() -> None:
    original_container = MODULE._container
    original_load_ids = MODULE._load_ids
    calls = []
    MODULE._container = lambda name: calls.append(name) or "postgres"
    MODULE._load_ids = lambda _postgres: [9]
    try:
        result = MODULE.run(
            mode="dry-run",
            model="claude-sonnet-4.5",
            timeout=60,
            max_workers=100,
            force=False,
        )
    finally:
        MODULE._container = original_container
        MODULE._load_ids = original_load_ids
    assert calls == ["tk_require_postgres_container"]
    assert result["verdict"] == "dry_run"
    assert result["max_workers"] == MODULE.MAX_WORKERS_CAP


def test_apply_success_recovers_then_restores_schedulable() -> None:
    original_admin_post = MODULE._admin_post
    original_probe_one = MODULE._probe_one
    calls = []
    MODULE._admin_post = lambda app, key, path, body: calls.append((path, body)) or (True, "")
    MODULE._probe_one = lambda account_id, model, timeout: {
        "account_id": account_id,
        "model": model,
        "verdict": "servable",
        "http_status": 200,
        "catalog_has_model": True,
        "error": None,
    }
    try:
        result = MODULE._recover_and_probe(7, "app", "key", "m", 60, True)
    finally:
        MODULE._admin_post = original_admin_post
        MODULE._probe_one = original_probe_one
    assert result["verdict"] == "servable"
    assert result["enabled"] is True
    assert calls == [
        ("/accounts/7/recover-state", {}),
        ("/accounts/7/schedulable", {"schedulable": True}),
    ]


def test_apply_probe_failure_disables_schedulable() -> None:
    original_admin_post = MODULE._admin_post
    original_probe_one = MODULE._probe_one
    calls = []
    MODULE._admin_post = lambda app, key, path, body: calls.append((path, body)) or (True, "")
    MODULE._probe_one = lambda account_id, model, timeout: {
        "account_id": account_id,
        "model": model,
        "verdict": "upstream_rejected",
        "http_status": 403,
        "catalog_has_model": False,
        "error": "HTTP 403",
        "failure_class": "oauth",
    }
    try:
        result = MODULE._recover_and_probe(8, "app", "key", "m", 60, True)
    finally:
        MODULE._admin_post = original_admin_post
        MODULE._probe_one = original_probe_one
    assert result["verdict"] == "upstream_rejected"
    assert result["disabled"] is True
    assert calls == [
        ("/accounts/8/recover-state", {}),
        ("/accounts/8/schedulable", {"schedulable": False}),
    ]
