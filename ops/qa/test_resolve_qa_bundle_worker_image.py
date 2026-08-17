#!/usr/bin/env python3
"""Contract tests for the QA Bundle Worker release resolver."""
from __future__ import annotations

import json
import pathlib
import subprocess
import tempfile
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
RESOLVER = REPO_ROOT / "ops" / "qa" / "resolve_qa_bundle_worker_image.py"
IMAGE_REPOSITORY = "ghcr.io/youxuanxue/sub2api"


def rollout_manifest(root: pathlib.Path, contract: object = ...,) -> pathlib.Path:
    path = root / "deploy_rollout.yaml"
    if contract is ...:
        user_export = "    bundle_runtime_contract: phase3_v1\n"
    elif contract is None:
        user_export = ""
    else:
        user_export = f"    bundle_runtime_contract: {contract}\n"
    path.write_text(f"prod:\n  user_export:\n{user_export}    marker: true\n")
    return path


def resolve(
    target_tag: str,
    target_rollout: pathlib.Path,
    existing_image: str = "",
) -> subprocess.CompletedProcess[str]:
    command = [
        "python3",
        str(RESOLVER),
        "--target-tag",
        target_tag,
        "--target-rollout",
        str(target_rollout),
    ]
    if existing_image:
        command.extend(("--verified-existing-image", existing_image))
    return subprocess.run(command, capture_output=True, text=True, check=False)


class ResolveQABundleWorkerImageTest(unittest.TestCase):
    def assert_resolution(
        self,
        target_tag: str,
        target_rollout: pathlib.Path,
        existing_image: str,
        *,
        mode: str,
        image: str,
        worker_source: str,
        run_canary: bool,
        host_runtime_mode: str,
    ) -> None:
        proc = resolve(target_tag, target_rollout, existing_image)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(
            json.loads(proc.stdout),
            {
                "mode": mode,
                "resolved_worker_image": image,
                "worker_source": worker_source,
                "run_canary": run_canary,
                "host_runtime_mode": host_runtime_mode,
            },
        )

    def test_phase3_contract_uses_target_release_image(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            manifest = rollout_manifest(pathlib.Path(temp_dir))
            self.assert_resolution(
                "1.8.200",
                manifest,
                f"{IMAGE_REPOSITORY}:1.8.999",
                mode="phase3",
                image=f"{IMAGE_REPOSITORY}:1.8.200",
                worker_source="target_release",
                run_canary=True,
                host_runtime_mode="target_release",
            )

    def test_missing_contract_is_legacy_and_preserves_verified_tag(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            manifest = rollout_manifest(pathlib.Path(temp_dir), None)
            image = f"{IMAGE_REPOSITORY}:1.8.200"
            self.assert_resolution(
                "1.8.155",
                manifest,
                image,
                mode="legacy_rollback",
                image=image,
                worker_source="verified_live_worker",
                run_canary=False,
                host_runtime_mode="current_safe_degraded",
            )

    def test_legacy_manifest_without_user_export_preserves_verified_tag(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            manifest = pathlib.Path(temp_dir) / "deploy_rollout.yaml"
            manifest.write_text(
                "prod:\n"
                "  QA_ARCHIVE_ENABLED:\n"
                "    deploy_inject_default: false\n",
                encoding="utf-8",
            )
            image = f"{IMAGE_REPOSITORY}:1.8.155"
            self.assert_resolution(
                "1.8.140",
                manifest,
                image,
                mode="legacy_rollback",
                image=image,
                worker_source="verified_live_worker",
                run_canary=False,
                host_runtime_mode="current_safe_degraded",
            )

    def test_legacy_accepts_verified_repository_digest(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            manifest = rollout_manifest(pathlib.Path(temp_dir), None)
            image = f"{IMAGE_REPOSITORY}@sha256:{'a' * 64}"
            self.assert_resolution(
                "1.8.155",
                manifest,
                image,
                mode="legacy_rollback",
                image=image,
                worker_source="verified_live_worker",
                run_canary=False,
                host_runtime_mode="current_safe_degraded",
            )

    def test_legacy_without_verified_worker_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            manifest = rollout_manifest(pathlib.Path(temp_dir), None)
            proc = resolve("1.8.155", manifest)
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn("requires a fully verified immutable", proc.stderr)

    def test_legacy_rejects_foreign_or_mutable_worker(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            manifest = rollout_manifest(pathlib.Path(temp_dir), None)
            for image in (
                "ghcr.io/example/sub2api:1.8.200",
                f"{IMAGE_REPOSITORY}:latest",
                f"{IMAGE_REPOSITORY}@sha256:not-a-digest",
            ):
                with self.subTest(image=image):
                    proc = resolve("1.8.155", manifest, image)
                    self.assertNotEqual(proc.returncode, 0)
                    self.assertIn("requires a fully verified immutable", proc.stderr)

    def test_unknown_or_malformed_contract_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = pathlib.Path(temp_dir)
            for manifest in (
                rollout_manifest(root, "phase4_future"),
                root / "missing.yaml",
            ):
                with self.subTest(manifest=manifest):
                    proc = resolve("1.8.200", manifest)
                    self.assertNotEqual(proc.returncode, 0)
                    self.assertRegex(
                        proc.stderr,
                        r"target rollout manifest|unsupported target Bundle runtime contract",
                    )

    def test_invalid_target_tag_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            manifest = rollout_manifest(pathlib.Path(temp_dir))
            for target in ("v1.8.200", "1.8", "1.8.200-preview.1"):
                with self.subTest(target=target):
                    proc = resolve(target, manifest)
                    self.assertNotEqual(proc.returncode, 0)
                    self.assertIn("target tag must match", proc.stderr)


if __name__ == "__main__":
    unittest.main()
