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
            "newapi_channel_types": {},
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
            "newapi_channel_types": {},
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
    ) -> None:
        common = {
            "schema_version": MODEL_OPS.ACTIVATION_EVIDENCE_SCHEMA_VERSION,
            "current_floor_sha256": self.current["floor_sha256"],
            "target_floor_sha256": target_sha256 or self.target["floor_sha256"],
            "observed_at": (observed_at or self.now).isoformat().replace("+00:00", "Z"),
        }
        model = {
            "scope": "openai",
            "model_id": "gpt-new",
            "target": "gpt-new-upstream",
        }
        self.probe_path.write_text(json.dumps({
            **common,
            "kind": "model_activation_probe",
            "models": [{
                **model,
                "verdict": "servable",
                "source": probe_source,
                "account_id": "test-account",
            }],
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
        ]
        for case in cases:
            with self.subTest(case["name"]):
                self.write_evidence(**case["kwargs"])
                with self.assertRaisesRegex(MODEL_OPS.ActivationError, case["message"]):
                    self.build_context()

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
            "newapi_channel_types": {},
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
