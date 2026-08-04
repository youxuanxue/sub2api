#!/usr/bin/env python3
"""Behavior tests for protected-main pricing registry publication."""
from __future__ import annotations

import base64
import copy
import gzip
import hashlib
import importlib.util
import json
import pathlib
import sys
import unittest
from unittest import mock

_MODULE_PATH = pathlib.Path(__file__).with_name("manage-overlay-runtime.py")
_SPEC = importlib.util.spec_from_file_location("manage_overlay_runtime", _MODULE_PATH)
runtime = importlib.util.module_from_spec(_SPEC)
assert _SPEC and _SPEC.loader
sys.modules[_SPEC.name] = runtime
_SPEC.loader.exec_module(runtime)


class PricingRegistryRuntimeTests(unittest.TestCase):
    COMMIT = "a" * 40
    REGISTRY = b'{"_config":{"x":1},"model":{"mode":"chat"}}\n'

    def test_envelope_round_trip_preserves_exact_registry_bytes(self) -> None:
        envelope = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        inspection = runtime.inspect_runtime_document(envelope)
        self.assertEqual(inspection.state, "valid")
        self.assertEqual(inspection.source_commit, self.COMMIT)
        self.assertEqual(inspection.registry_bytes, self.REGISTRY)
        self.assertEqual(
            inspection.registry_sha256, hashlib.sha256(self.REGISTRY).hexdigest()
        )

    def test_legacy_bad_schema_digest_and_gzip_are_rejected(self) -> None:
        legacy = {"model": {"input_cost_per_token": 1e-6}}
        self.assertEqual(runtime.inspect_runtime_document(legacy).state, "legacy")

        valid = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        bad_schema = copy.deepcopy(valid)
        bad_schema["_snapshot"]["schema_version"] = 2
        self.assertEqual(runtime.inspect_runtime_document(bad_schema).state, "invalid")

        bad_digest = copy.deepcopy(valid)
        bad_digest["_snapshot"]["registry_sha256"] = "b" * 64
        self.assertIn("digest mismatch", runtime.inspect_runtime_document(bad_digest).error)

        bad_gzip = copy.deepcopy(valid)
        bad_gzip["_registry_gzip_base64"] = base64.b64encode(b"not-gzip").decode()
        self.assertEqual(runtime.inspect_runtime_document(bad_gzip).state, "invalid")

    def test_envelope_rejects_unknown_fields_and_oversized_registry(self) -> None:
        valid = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        valid["unexpected"] = True
        self.assertEqual(runtime.inspect_runtime_document(valid).state, "invalid")
        with mock.patch.object(runtime, "MAX_REGISTRY_BYTES", 4):
            with self.assertRaisesRegex(ValueError, "exceeds"):
                runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)

    def test_only_exact_commit_digest_and_bytes_are_current(self) -> None:
        expected = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        exact = runtime.inspect_runtime_document(
            runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        )
        self.assertTrue(exact.is_current(expected))

        newer_bytes = runtime.inspect_runtime_document(
            runtime.build_runtime_envelope(self.REGISTRY + b" ", self.COMMIT)
        )
        self.assertFalse(newer_bytes.is_current(expected))
        other_commit = runtime.inspect_runtime_document(
            runtime.build_runtime_envelope(self.REGISTRY, "b" * 40)
        )
        self.assertFalse(other_commit.is_current(expected))

    def test_decode_ssm_readback_handles_empty_and_large_documents(self) -> None:
        large_registry = json.dumps({"model": {"source": "x" * 100_000}}).encode()
        envelope = runtime.build_runtime_envelope(large_registry, self.COMMIT)
        stored = json.dumps(envelope).encode() + b"\n"
        encoded = base64.b64encode(gzip.compress(stored)).decode()
        self.assertEqual(runtime._decode_runtime_value(encoded), envelope)
        self.assertEqual(runtime._decode_runtime_value(""), {})

    def test_sync_dry_run_requires_publishable_checkout_and_skips_aws(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": True})()
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ) as load_artifact, mock.patch.object(runtime, "_run_registry_gate") as gate, \
                mock.patch.object(runtime._SSM, "resolve_prod_instance") as resolve:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        load_artifact.assert_called_once_with(require_publishable_checkout=True)
        gate.assert_called_once_with()
        resolve.assert_not_called()

    def test_sync_skips_write_only_for_exact_current_snapshot(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        document = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(runtime, "read_runtime_document", return_value=document), \
                mock.patch.object(runtime, "_write_runtime_document") as write:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        write.assert_not_called()

    def test_sync_verifies_readback_after_one_atomic_write(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        current = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document", side_effect=[{}, current]
        ), mock.patch.object(runtime, "_write_runtime_document") as write:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        write.assert_called_once()

    def test_sync_refuses_to_downgrade_newer_runtime_source(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        newer = runtime.build_runtime_envelope(self.REGISTRY + b" ", "b" * 40)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document", return_value=newer
        ), mock.patch.object(
            runtime, "_git_is_ancestor", return_value=False
        ) as ancestry, mock.patch.object(runtime, "_write_runtime_document") as write:
            with self.assertRaisesRegex(SystemExit, "2"):
                runtime.cmd_sync_runtime(args)
        ancestry.assert_called_once_with("b" * 40, self.COMMIT)
        write.assert_not_called()

    def test_sync_allows_new_descendant_for_git_revert_publication(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        current = runtime.build_runtime_envelope(self.REGISTRY + b" ", "b" * 40)
        expected_document = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document", side_effect=[current, expected_document]
        ), mock.patch.object(
            runtime, "_git_is_ancestor", return_value=True
        ) as ancestry, mock.patch.object(runtime, "_write_runtime_document") as write:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        ancestry.assert_called_once_with("b" * 40, self.COMMIT)
        write.assert_called_once()

    def test_check_is_read_only_and_reports_drift(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        stale = runtime.build_runtime_envelope(self.REGISTRY, "b" * 40)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ) as load_artifact, mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(runtime, "read_runtime_document", return_value=stale), \
                mock.patch.object(runtime, "_write_runtime_document") as write:
            self.assertEqual(runtime.cmd_check(type("Args", (), {})()), 1)
        load_artifact.assert_called_once_with(require_publishable_checkout=False)
        write.assert_not_called()


if __name__ == "__main__":
    unittest.main()
