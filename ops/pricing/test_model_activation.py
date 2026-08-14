#!/usr/bin/env python3
"""Offline behavior tests for the explicit model-surface activation gate."""
from __future__ import annotations

import argparse
import contextlib
import datetime as dt
import importlib.util
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
MODEL_OPS_PATH = HERE / "modelops.py"


def load_modelops():
    spec = importlib.util.spec_from_file_location("tk_modelops_activation_test", MODEL_OPS_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


MODEL_OPS = load_modelops()


class ModelActivationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.now = dt.datetime(2026, 7, 15, 8, 0, tzinfo=dt.timezone.utc)
        current_floor = {
            "platforms": {"openai": {"gpt-current": "gpt-current"}},
            "newapi_channel_types": {"41": {"vertex-shared": "vertex-shared"}},
            "vertex_capability_profiles": {
                "core-pro": {"vertex-shared": "vertex-shared"},
            },
            "account_overrides": [],
            "antigravity_group_scopes": ["claude"],
            "forbidden_model_mapping_keys": {},
            "forbidden_model_mapping_prefixes": {},
        }
        target_floor = {
            "platforms": {
                "openai": {
                    "gpt-current": "gpt-current",
                    "gpt-new": "gpt-new-upstream",
                },
            },
            "newapi_channel_types": {"41": {"vertex-shared": "vertex-shared"}},
            "vertex_capability_profiles": {
                "core-pro": {"vertex-shared": "vertex-shared"},
            },
            "account_overrides": [],
            "antigravity_group_scopes": ["claude"],
            "forbidden_model_mapping_keys": {},
            "forbidden_model_mapping_prefixes": {},
        }
        self.current_path, self.current = self.write_bundle("current.json", current_floor)
        self.target_path, self.target = self.write_bundle("target.json", target_floor)
        self.probe_path = self.root / "probe.json"
        self.pricing_path = self.root / "pricing.json"
        self.write_evidence()

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def write_bundle(self, name: str, floor: dict) -> tuple[Path, dict]:
        bundle = {
            "schema_version": MODEL_OPS._BUNDLE.SCHEMA_VERSION,
            "floor_sha256": MODEL_OPS._BUNDLE.floor_sha256(floor),
            "account_model_mapping": floor,
        }
        path = self.root / name
        path.write_text(json.dumps(bundle), encoding="utf-8")
        return path, bundle

    def write_evidence(
        self,
        *,
        observed_at: dt.datetime | None = None,
        target_sha256: str | None = None,
        probe_source: str = "probe_account_model.sh",
        pricing_source: str = "prod-pricing-snapshot",
        account_platform: str = "openai",
        account_scope: str = "openai",
        account_id: str = "test-account",
        account_base_url: str | None = None,
        scope: str = "openai",
        model_id: str = "gpt-new",
        target: str = "gpt-new-upstream",
    ) -> None:
        common = {
            "schema_version": MODEL_OPS.ACTIVATION_EVIDENCE_SCHEMA_VERSION,
            "current_floor_sha256": self.current["floor_sha256"],
            "target_floor_sha256": target_sha256 or self.target["floor_sha256"],
            "observed_at": (observed_at or self.now).isoformat().replace("+00:00", "Z"),
        }
        model = {
            "scope": scope,
            "model_id": model_id,
            "target": target,
        }
        probe_model = {
            **model,
            "verdict": "servable",
            "source": probe_source,
            "account_id": account_id,
            "account_platform": account_platform,
            "account_scope": account_scope,
        }
        if account_base_url is not None:
            probe_model["account_base_url"] = account_base_url
        self.probe_path.write_text(json.dumps({
            **common,
            "kind": "model_activation_probe",
            "models": [probe_model],
        }), encoding="utf-8")
        self.pricing_path.write_text(json.dumps({
            **common,
            "kind": "model_activation_pricing",
            "models": [{**model, "verdict": "priced", "source": pricing_source}],
        }), encoding="utf-8")

    def build_context(self):
        return MODEL_OPS.build_activation_context(
            bundle_path=self.target_path,
            current_bundle_path=self.current_path,
            probe_evidence_path=self.probe_path,
            pricing_evidence_path=self.pricing_path,
            now=self.now,
        )

    def use_account_override_target(self) -> dict:
        target_floor = json.loads(json.dumps(self.current["account_model_mapping"]))
        target_floor["account_overrides"] = [
            {
                "platform": "newapi",
                "channel_type": 45,
                "base_url": "https://ark.cn-beijing.volces.com/api/plan/v3",
                "model_mapping": {"agent-plan-model": "agent-plan-model"},
            },
        ]
        self.target_path, self.target = self.write_bundle("target-account.json", target_floor)
        return self.target

    def test_us035_valid_evidence_builds_activation_delta(self) -> None:
        context = self.build_context()
        self.assertEqual(context["target_floor_sha256"], self.target["floor_sha256"])
        self.assertEqual([row["model_id"] for row in context["delta"]["activated"]], ["gpt-new"])

    def test_us035_invalid_evidence_is_rejected(self) -> None:
        cases = [
            {
                "name": "stale",
                "kwargs": {
                    "observed_at": self.now - MODEL_OPS.ACTIVATION_EVIDENCE_MAX_AGE - dt.timedelta(seconds=1),
                },
                "message": "stale",
            },
            {
                "name": "wrong-target",
                "kwargs": {"target_sha256": "0" * 64},
                "message": "target_floor_sha256",
            },
            {
                "name": "shared-source",
                "kwargs": {"probe_source": "same-source", "pricing_source": "same-source"},
                "message": "independent sources",
            },
            {
                "name": "wrong-account-platform",
                "kwargs": {"account_platform": "kiro"},
                "message": "account_platform .* cannot provide account_scope 'openai'",
            },
            {
                "name": "wrong-account-scope",
                "kwargs": {"account_scope": "kiro"},
                "message": "account_scope .* must match mapping scope 'openai'",
            },
        ]
        for case in cases:
            with self.subTest(case["name"]):
                self.write_evidence(**case["kwargs"])
                with self.assertRaisesRegex(MODEL_OPS.ActivationError, case["message"]):
                    self.build_context()

    def test_us035_account_platform_scope_relationships(self) -> None:
        self.assertTrue(MODEL_OPS._account_platform_allows_scope("kiro", "kiro"))
        self.assertTrue(MODEL_OPS._account_platform_allows_scope("anthropic", "kiro"))
        self.assertTrue(MODEL_OPS._account_platform_allows_scope("newapi", "newapi_channel_type:17"))
        self.assertTrue(MODEL_OPS._account_platform_allows_scope(
            "newapi", "newapi_vertex_profile:core-pro"
        ))
        self.assertTrue(MODEL_OPS._account_platform_allows_scope(
            "newapi", "account_override:newapi:45:https://ark.cn-beijing.volces.com/api/plan/v3"
        ))
        self.assertFalse(MODEL_OPS._account_platform_allows_scope("kiro", "anthropic"))

    def test_vertex_profile_scope_is_part_of_activation_delta_and_evidence(self) -> None:
        target_floor = json.loads(json.dumps(self.current["account_model_mapping"]))
        target_floor["vertex_capability_profiles"]["core-pro"]["gemini-pro"] = "gemini-pro"
        self.target_path, self.target = self.write_bundle("target-vertex-profile.json", target_floor)
        scope = "newapi_vertex_profile:core-pro"
        self.write_evidence(
            scope=scope,
            model_id="gemini-pro",
            target="gemini-pro",
            account_id="47",
            account_platform="newapi",
            account_scope=scope,
        )
        context = self.build_context()
        self.assertEqual([row["scope"] for row in context["delta"]["activated"]], [scope])

    def test_vertex_profile_evidence_must_match_exact_profile_scope(self) -> None:
        target_floor = json.loads(json.dumps(self.current["account_model_mapping"]))
        target_floor["vertex_capability_profiles"]["core-pro"]["gemini-pro"] = "gemini-pro"
        self.target_path, self.target = self.write_bundle("target-vertex-profile-bad.json", target_floor)
        self.write_evidence(
            scope="newapi_vertex_profile:core-pro",
            model_id="gemini-pro",
            target="gemini-pro",
            account_id="47",
            account_platform="newapi",
            account_scope="newapi_channel_type:41",
        )
        with self.assertRaisesRegex(MODEL_OPS.ActivationError, "must match mapping scope"):
            self.build_context()

    def test_us035_account_override_scope_is_part_of_activation_delta(self) -> None:
        target = self.use_account_override_target()
        delta = MODEL_OPS._activation_delta(self.current, target)
        self.assertEqual(
            [row["scope"] for row in delta["activated"]],
            ["account_override:newapi:45:https://ark.cn-beijing.volces.com/api/plan/v3"],
        )

    def test_us035_account_override_evidence_must_match_base_url(self) -> None:
        self.use_account_override_target()
        scope = "account_override:newapi:45:https://ark.cn-beijing.volces.com/api/plan/v3"
        self.write_evidence(
            scope=scope,
            model_id="agent-plan-model",
            target="agent-plan-model",
            account_id="89",
            account_platform="newapi",
            account_scope=scope,
            account_base_url="https://ark.cn-beijing.volces.com/api/v3",
        )
        with self.assertRaisesRegex(MODEL_OPS.ActivationError, "account_base_url"):
            self.build_context()

    def test_us035_account_override_evidence_accepts_selector_without_account_id_binding(self) -> None:
        self.use_account_override_target()
        scope = "account_override:newapi:45:https://ark.cn-beijing.volces.com/api/plan/v3"
        self.write_evidence(
            scope=scope,
            model_id="agent-plan-model",
            target="agent-plan-model",
            account_id="89",
            account_platform="newapi",
            account_scope=scope,
            account_base_url="https://ark.cn-beijing.volces.com/api/plan/v3/",
        )
        context = self.build_context()
        self.assertEqual(context["delta"]["activated"][0]["scope"], scope)

    def test_us035_v1_current_bundle_remains_readable(self) -> None:
        legacy_floor = json.loads(json.dumps(self.current["account_model_mapping"]))
        legacy_floor.pop("account_overrides")
        legacy_floor.pop("vertex_capability_profiles")
        legacy_bundle = {
            "schema_version": 1,
            "floor_sha256": MODEL_OPS._BUNDLE.floor_sha256(legacy_floor),
            "account_model_mapping": legacy_floor,
        }
        legacy_path = self.root / "legacy-v1.json"
        legacy_path.write_text(json.dumps(legacy_bundle), encoding="utf-8")
        self.assertEqual(
            MODEL_OPS._BUNDLE.load_bundle(legacy_path)["schema_version"],
            1,
        )

    def test_schema_v3_bundle_remains_readable(self) -> None:
        legacy_floor = json.loads(json.dumps(self.current["account_model_mapping"]))
        legacy_floor.pop("vertex_capability_profiles")
        legacy_bundle = {
            "schema_version": 3,
            "floor_sha256": MODEL_OPS._BUNDLE.floor_sha256(legacy_floor),
            "account_model_mapping": legacy_floor,
        }
        legacy_path = self.root / "legacy-v3.json"
        legacy_path.write_text(json.dumps(legacy_bundle), encoding="utf-8")
        self.assertEqual(MODEL_OPS._BUNDLE.load_bundle(legacy_path)["schema_version"], 3)

    def test_schema_v4_profile_must_contain_shared_floor(self) -> None:
        bad_floor = json.loads(json.dumps(self.current["account_model_mapping"]))
        bad_floor["vertex_capability_profiles"]["core-pro"] = {"profile-only": "profile-only"}
        bad_path, _ = self.write_bundle("bad-vertex-profile.json", bad_floor)
        with self.assertRaisesRegex(RuntimeError, "complete shared floor"):
            MODEL_OPS._BUNDLE.load_bundle(bad_path)

    def test_us035_id_keyed_schema_v2_is_rejected(self) -> None:
        legacy_floor = json.loads(json.dumps(self.current["account_model_mapping"]))
        legacy_floor["account_overrides"] = {
            "88": {
                "platform": "newapi",
                "channel_type": 45,
                "model_mapping": {"agent-plan-model": "agent-plan-model"},
            },
        }
        legacy_path = self.root / "legacy-v2-id-keyed.json"
        legacy_path.write_text(json.dumps({
            "schema_version": 2,
            "floor_sha256": MODEL_OPS._BUNDLE.floor_sha256(legacy_floor),
            "account_model_mapping": legacy_floor,
        }), encoding="utf-8")
        with self.assertRaisesRegex(RuntimeError, "unsupported model surface bundle schema"):
            MODEL_OPS._BUNDLE.load_bundle(legacy_path)

    def test_us035_runtime_shadow_is_rejected(self) -> None:
        with self.assertRaisesRegex(MODEL_OPS.ActivationError, "shadowed"):
            MODEL_OPS._require_unshadowed_activation_bundle({"runtime_setting_targets": ["prod"]})

    def test_us035_runtime_shadow_stops_before_apply(self) -> None:
        calls = []

        def fake_run(command, _allowed_returncodes):
            calls.append(command)
            return 1, {
                "status": "violation",
                "runtime_setting_targets": ["prod"],
                "resolved_targets": [{
                    "target": "prod",
                    "region": "us-east-1",
                    "instance_id": "i-0123456789abcdef0",
                }],
            }

        args = argparse.Namespace(
            bundle=self.target_path,
            current_bundle=self.current_path,
            probe_evidence=self.probe_path,
            pricing_evidence=self.pricing_path,
            prod_instance_id=None,
            confirm=MODEL_OPS.ACTIVATION_CONFIRM,
            format="json",
        )
        context = self.build_context()
        with mock.patch.object(MODEL_OPS, "build_activation_context", return_value=context), \
                mock.patch.object(MODEL_OPS, "_run_json_command", side_effect=fake_run):
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(MODEL_OPS.cmd_activate(args), 2)
        self.assertEqual([command[2] for command in calls], ["release-gate"])

    def test_us035_commands_are_prod_only_and_confirmed(self) -> None:
        instance_id = "i-0123456789abcdef0"
        floor_sha256 = self.target["floor_sha256"]
        dry_run = MODEL_OPS._mapping_manager_command(
            "apply-accounts-dry-run",
            self.target_path,
            prod_instance_id=instance_id,
            activation_floor_sha256=floor_sha256,
        )
        apply = MODEL_OPS._mapping_manager_command(
            "apply-accounts",
            self.target_path,
            prod_instance_id=instance_id,
            activation_floor_sha256=floor_sha256,
        )
        self.assertIn("prod", dry_run)
        self.assertNotIn("all-deployable-and-prod", dry_run)
        self.assertIn("yes-apply-account-model-mapping", apply)
        self.assertIn(instance_id, dry_run)
        self.assertIn(floor_sha256, apply)

    def test_us035_confirmed_apply_pins_instance_and_requires_post_gate(self) -> None:
        instance_id = "i-0123456789abcdef0"
        gate = {
            "status": "violation",
            "runtime_setting_targets": [],
            "resolved_targets": [{
                "target": "prod",
                "region": "us-east-1",
                "instance_id": instance_id,
            }],
        }
        responses = iter([
            (1, gate),
            (0, {"account_change_count": 1, "group_change_count": 0}),
            (0, {"applied": [{"target": "prod", "account_changes": 1}]}),
            (0, {**gate, "status": "ok", "violation_count": 0}),
        ])
        calls = []

        def fake_run(command, _allowed_returncodes):
            calls.append(command)
            return next(responses)

        args = argparse.Namespace(
            bundle=self.target_path,
            current_bundle=self.current_path,
            probe_evidence=self.probe_path,
            pricing_evidence=self.pricing_path,
            prod_instance_id=None,
            confirm=MODEL_OPS.ACTIVATION_CONFIRM,
            format="json",
        )
        context = self.build_context()
        with mock.patch.object(MODEL_OPS, "build_activation_context", return_value=context), \
                mock.patch.object(MODEL_OPS, "_run_json_command", side_effect=fake_run):
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(MODEL_OPS.cmd_activate(args), 0)

        self.assertEqual(
            [command[2] for command in calls],
            ["release-gate", "apply-accounts", "apply-accounts", "release-gate"],
        )
        self.assertIn("--dry-run", calls[1])
        self.assertNotIn("--dry-run", calls[2])
        for command in calls[1:]:
            self.assertIn(instance_id, command)

    def test_us035_self_digested_invalid_bundle_is_rejected(self) -> None:
        bad_floor = {
            "platforms": {"openai": {"": "bad-target"}},
            "newapi_channel_types": {"41": {"vertex-shared": "vertex-shared"}},
            "vertex_capability_profiles": {
                "core-pro": {"vertex-shared": "vertex-shared"},
            },
            "account_overrides": [],
            "antigravity_group_scopes": ["claude"],
            "forbidden_model_mapping_keys": {},
            "forbidden_model_mapping_prefixes": {},
        }
        bad_path, _ = self.write_bundle("bad.json", bad_floor)
        with self.assertRaisesRegex(RuntimeError, "empty or non-string key"):
            MODEL_OPS._BUNDLE.load_bundle(bad_path)

        missing_policy_floor = dict(bad_floor)
        missing_policy_floor["platforms"] = {"openai": {"gpt-new": "gpt-new"}}
        del missing_policy_floor["forbidden_model_mapping_keys"]
        missing_policy_path, _ = self.write_bundle("missing-policy.json", missing_policy_floor)
        with self.assertRaisesRegex(RuntimeError, "omitted account_model_mapping fields"):
            MODEL_OPS._BUNDLE.load_bundle(missing_policy_path)


if __name__ == "__main__":
    unittest.main()
