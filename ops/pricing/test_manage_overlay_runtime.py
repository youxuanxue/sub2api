#!/usr/bin/env python3
"""Behavior tests for protected-main pricing registry publication."""
from __future__ import annotations

import base64
import contextlib
import copy
import hashlib
import importlib.util
import io
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

    def document(self, value: dict, version: str = "42") -> runtime.RuntimeDocument:
        return runtime.RuntimeDocument(value=value, version=version)

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

    @staticmethod
    def chunk_marker(chunk: bytes) -> str:
        return f"CHUNK|{len(chunk)}|{base64.b64encode(chunk).decode('ascii')}"

    def test_chunked_read_reassembles_payload_larger_than_ssm_stdout(self) -> None:
        models = {
            f"model-{index:05d}": hashlib.sha256(str(index).encode()).hexdigest()
            for index in range(1_200)
        }
        registry = json.dumps({"models": models}, sort_keys=True).encode()
        envelope = runtime.build_runtime_envelope(registry, self.COMMIT)
        payload = json.dumps(envelope, sort_keys=True, separators=(",", ":")).encode()
        legacy_transport = base64.b64encode(payload).decode("ascii")
        self.assertGreater(len(legacy_transport), 24_000)

        outputs = [f"ROW_PRESENT|42|{len(payload)}"]
        chunks = [
            payload[start:start + runtime.RUNTIME_CHUNK_BYTES]
            for start in range(0, len(payload), runtime.RUNTIME_CHUNK_BYTES)
        ]
        outputs.extend(self.chunk_marker(chunk) for chunk in chunks)
        self.assertGreater(len(chunks), 1)
        self.assertTrue(all(len(output) < 24_000 for output in outputs))
        shells: list[str] = []

        def fake_run(_instance: str, encoded: str, _comment: str) -> str:
            shells.append(base64.b64decode(encoded).decode())
            return outputs.pop(0)

        with mock.patch.object(runtime._SSM, "run_shell_b64", side_effect=fake_run):
            document = runtime.read_runtime_document("i-" + "1" * 17)
        self.assertEqual(document, self.document(envelope))
        self.assertFalse(outputs)
        self.assertIn("FROM 1 FOR 12288", shells[1])
        self.assertIn("xmin='42'::xid", shells[1])
        self.assertIn("convert_to(value, 'UTF8')", shells[1])

    def test_runtime_read_only_accepts_explicit_absence(self) -> None:
        with mock.patch.object(
            runtime._SSM, "run_shell_b64", return_value="ROW_ABSENT"
        ):
            self.assertEqual(
                runtime.read_runtime_document("i-" + "1" * 17),
                self.document({}, "absent"),
            )

    def test_runtime_metadata_invalid_states_fail_closed(self) -> None:
        invalid_outputs = (
            "",
            "ROW_INVALID",
            "ROW_PRESENT|bad-xmin|10",
            "ROW_PRESENT|42|bad-size",
            "ROW_PRESENT|42|0",
            f"ROW_PRESENT|42|{runtime.MAX_RUNTIME_DOCUMENT_BYTES + 1}",
            "ROW_ABSENT|42|10",
        )
        for output in invalid_outputs:
            with self.subTest(output=output), mock.patch.object(
                runtime._SSM, "run_shell_b64", return_value=output
            ), self.assertRaisesRegex(SystemExit, "2"):
                runtime.read_runtime_document("i-" + "1" * 17)

    def test_runtime_chunk_corruption_fails_closed(self) -> None:
        corrupt_outputs = (
            "",
            "CHUNK|4|%%%not-base64%%%",
            f"CHUNK|3|{base64.b64encode(b'abcd').decode()}",
            f"CHUNK|4|{base64.b64encode(b'abc').decode()}",
            "CHUNK_UNKNOWN|4|YWJjZA==",
        )
        for output in corrupt_outputs:
            with self.subTest(output=output), mock.patch.object(
                runtime._SSM, "run_shell_b64", return_value=output
            ), self.assertRaisesRegex(SystemExit, "2"):
                runtime._read_runtime_chunk("i-" + "1" * 17, "42", 1, 4)

    def test_runtime_total_length_mismatch_fails_closed(self) -> None:
        with mock.patch.object(
            runtime, "_read_runtime_metadata", return_value=("42", 4)
        ), mock.patch.object(
            runtime, "_read_runtime_chunk", return_value=b"abc"
        ), self.assertRaisesRegex(SystemExit, "2"):
            runtime.read_runtime_document("i-" + "1" * 17)

    def test_runtime_xmin_change_restarts_from_metadata(self) -> None:
        payload = json.dumps(
            runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT),
            sort_keys=True,
            separators=(",", ":"),
        ).encode()
        outputs = [
            f"ROW_PRESENT|42|{len(payload)}",
            "CHUNK_STALE",
            f"ROW_PRESENT|43|{len(payload)}",
            self.chunk_marker(payload),
        ]
        with mock.patch.object(
            runtime._SSM, "run_shell_b64", side_effect=outputs
        ), contextlib.redirect_stdout(io.StringIO()) as stdout:
            document = runtime.read_runtime_document("i-" + "1" * 17)
        self.assertEqual(document.version, "43")
        self.assertIn("changed during read attempt 1", stdout.getvalue())

    def test_runtime_xmin_change_exhausts_bounded_retries(self) -> None:
        outputs: list[str] = []
        for version in range(runtime.READ_MAX_ATTEMPTS):
            outputs.extend((f"ROW_PRESENT|{42 + version}|4", "CHUNK_STALE"))
        with mock.patch.object(
            runtime._SSM, "run_shell_b64", side_effect=outputs
        ), contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(
            SystemExit, "2"
        ):
            runtime.read_runtime_document("i-" + "1" * 17)

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
        document = self.document(
            runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        )
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(runtime, "read_runtime_document", return_value=document), \
                mock.patch.object(runtime, "_write_runtime_document") as write:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        write.assert_not_called()

    def test_sync_republishes_same_content_with_different_source(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        old_source = "b" * 40
        stale = runtime.build_runtime_envelope(self.REGISTRY, old_source)
        exact = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document",
            side_effect=[self.document(stale), self.document(exact, "43")],
        ), mock.patch.object(
            runtime, "_git_is_ancestor", return_value=True
        ) as ancestry, mock.patch.object(
            runtime, "_write_runtime_document", return_value=True
        ) as write:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        ancestry.assert_called_once_with(old_source, self.COMMIT)
        write.assert_called_once_with(mock.ANY, mock.ANY, "42")

    def test_sync_cas_success_verifies_exact_readback(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        current = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document",
            side_effect=[self.document({}, "absent"), self.document(current, "43")],
        ), mock.patch.object(
            runtime, "_write_runtime_document", return_value=True
        ) as write:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        write.assert_called_once_with(mock.ANY, mock.ANY, "absent")

    def test_sync_post_write_provenance_mismatch_fails_closed(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        other_source = "b" * 40
        same_content = runtime.build_runtime_envelope(self.REGISTRY, other_source)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document",
            side_effect=[self.document({}, "absent"), self.document(same_content, "43")],
        ), mock.patch.object(
            runtime, "_write_runtime_document", return_value=True
        ) as write, mock.patch.object(
            runtime, "_git_is_ancestor", return_value=True
        ) as ancestry, self.assertRaisesRegex(
            SystemExit, "2"
        ):
            runtime.cmd_sync_runtime(args)
        write.assert_called_once_with(mock.ANY, mock.ANY, "absent")
        ancestry.assert_called_once_with(other_source, self.COMMIT)

    def test_sync_conflict_then_exact_current_succeeds_without_second_write(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        current = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document",
            side_effect=[self.document({}, "absent"), self.document(current, "44")],
        ), mock.patch.object(
            runtime, "_write_runtime_document", return_value=False
        ) as write:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        write.assert_called_once()

    def test_sync_conflict_then_newer_source_refuses_retry(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        newer = runtime.build_runtime_envelope(self.REGISTRY + b" ", "b" * 40)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document",
            side_effect=[self.document({}, "absent"), self.document(newer, "45")],
        ), mock.patch.object(
            runtime, "_git_is_ancestor", return_value=False
        ) as ancestry, mock.patch.object(
            runtime, "_write_runtime_document", return_value=False
        ) as write:
            with self.assertRaisesRegex(SystemExit, "2"):
                runtime.cmd_sync_runtime(args)
        ancestry.assert_called_once_with("b" * 40, self.COMMIT)
        write.assert_called_once()

    def test_sync_exhausts_bounded_cas_conflicts(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        documents = [self.document({}, str(50 + i)) for i in range(runtime.CAS_MAX_ATTEMPTS)]
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document", side_effect=documents
        ), mock.patch.object(
            runtime, "_write_runtime_document", return_value=False
        ) as write:
            with self.assertRaisesRegex(SystemExit, "2"):
                runtime.cmd_sync_runtime(args)
        self.assertEqual(write.call_count, runtime.CAS_MAX_ATTEMPTS)

    def test_remote_cas_uses_cte_and_conflict_exits_before_redis(self) -> None:
        captured_shell = ""

        def fake_run(_instance: str, encoded: str, _comment: str) -> str:
            nonlocal captured_shell
            captured_shell = base64.b64decode(encoded).decode()
            return "CAS_CONFLICT\n"

        with mock.patch.object(runtime._SSM, "run_shell_b64", side_effect=fake_run):
            self.assertFalse(
                runtime._write_runtime_document("i-" + "1" * 17, b"{}", "42")
            )
        self.assertIn("WITH mutation AS (UPDATE settings", captured_shell)
        self.assertIn("SELECT CASE WHEN EXISTS (SELECT 1 FROM mutation)", captured_shell)
        case_index = captured_shell.index('case "$CAS_RESULT" in')
        redis_index = captured_shell.index("PUBLISH settings_updated")
        self.assertIn(
            "CAS_CONFLICT) echo CAS_CONFLICT; exit 0",
            captured_shell[case_index:redis_index],
        )

    def test_remote_absent_cas_uses_insert_cte(self) -> None:
        captured_shell = ""

        def fake_run(_instance: str, encoded: str, _comment: str) -> str:
            nonlocal captured_shell
            captured_shell = base64.b64decode(encoded).decode()
            return "CAS_CONFLICT\n"

        with mock.patch.object(runtime._SSM, "run_shell_b64", side_effect=fake_run):
            self.assertFalse(
                runtime._write_runtime_document("i-" + "1" * 17, b"{}", "absent")
            )
        self.assertIn("WITH mutation AS (INSERT INTO settings", captured_shell)
        self.assertIn("ON CONFLICT (key) DO NOTHING RETURNING 1)", captured_shell)

    def test_remote_cas_applied_requires_exact_readback(self) -> None:
        payload = b"{}"
        expected = (
            f"{runtime.SETTING_KEY}|2|{hashlib.md5(payload).hexdigest()}\nCAS_APPLIED\n"
        )
        with mock.patch.object(
            runtime._SSM, "run_shell_b64", return_value=expected
        ):
            self.assertTrue(
                runtime._write_runtime_document("i-" + "1" * 17, payload, "42")
            )
        with mock.patch.object(
            runtime._SSM, "run_shell_b64", return_value="CAS_APPLIED\n"
        ), self.assertRaisesRegex(SystemExit, "2"):
            runtime._write_runtime_document("i-" + "1" * 17, payload, "42")

    def test_old_psql_returning_shape_and_unknown_markers_fail_closed(self) -> None:
        for output in ("1\nUPDATE 1\n", "CAS_APPLIED\nCAS_CONFLICT\n"):
            with self.subTest(output=output), mock.patch.object(
                runtime._SSM, "run_shell_b64", return_value=output
            ), self.assertRaisesRegex(SystemExit, "2"):
                runtime._write_runtime_document("i-" + "1" * 17, b"{}", "42")

    def test_sync_allows_new_descendant_for_git_revert_publication(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        args = type("Args", (), {"dry_run": False})()
        stale = runtime.build_runtime_envelope(self.REGISTRY + b" ", "b" * 40)
        current = runtime.build_runtime_envelope(self.REGISTRY, self.COMMIT)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(runtime, "_run_registry_gate"), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document",
            side_effect=[self.document(stale, "42"), self.document(current, "43")],
        ), mock.patch.object(
            runtime, "_git_is_ancestor", return_value=True
        ) as ancestry, mock.patch.object(
            runtime, "_write_runtime_document", return_value=True
        ) as write:
            self.assertEqual(runtime.cmd_sync_runtime(args), 0)
        ancestry.assert_called_once_with("b" * 40, self.COMMIT)
        write.assert_called_once_with(mock.ANY, mock.ANY, "42")

    def test_check_is_read_only_and_reports_provenance_lag_as_healthy(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        stale_source = runtime.build_runtime_envelope(self.REGISTRY, "b" * 40)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ) as load_artifact, mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document", return_value=self.document(stale_source)
        ), mock.patch.object(
            runtime, "_write_runtime_document"
        ) as write, contextlib.redirect_stdout(io.StringIO()) as stdout:
            self.assertEqual(runtime.cmd_check(type("Args", (), {})()), 0)
        self.assertIn("provenance lag", stdout.getvalue())
        self.assertIn("runtime=" + "b" * 40, stdout.getvalue())
        load_artifact.assert_called_once_with(require_publishable_checkout=False)
        write.assert_not_called()

    def test_check_reports_content_drift(self) -> None:
        artifact = runtime.RegistryArtifact(self.COMMIT, self.REGISTRY)
        drifted = runtime.build_runtime_envelope(self.REGISTRY + b" ", "b" * 40)
        with mock.patch.object(
            runtime, "load_origin_main_artifact", return_value=artifact
        ), mock.patch.object(
            runtime._SSM, "resolve_prod_instance", return_value="i-" + "1" * 17
        ), mock.patch.object(
            runtime, "read_runtime_document", return_value=self.document(drifted)
        ), contextlib.redirect_stdout(io.StringIO()) as stdout:
            self.assertEqual(runtime.cmd_check(type("Args", (), {})()), 1)
        self.assertIn("DRIFT", stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
