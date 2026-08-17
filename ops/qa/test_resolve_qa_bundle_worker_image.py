#!/usr/bin/env python3
"""Contract tests for the QA Bundle Worker release resolver."""
from __future__ import annotations

import json
import pathlib
import subprocess
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
RESOLVER = REPO_ROOT / "ops" / "qa" / "resolve_qa_bundle_worker_image.py"
IMAGE_REPOSITORY = "ghcr.io/youxuanxue/sub2api"


def resolve(target_tag: str, existing_image: str = "") -> subprocess.CompletedProcess[str]:
    command = ["python3", str(RESOLVER), "--target-tag", target_tag]
    if existing_image:
        command.extend(("--existing-image", existing_image))
    return subprocess.run(command, capture_output=True, text=True, check=False)


class ResolveQABundleWorkerImageTest(unittest.TestCase):
    def assert_resolution(
        self,
        target_tag: str,
        existing_image: str,
        *,
        mode: str,
        image: str,
    ) -> None:
        proc = resolve(target_tag, existing_image)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(
            json.loads(proc.stdout),
            {"mode": mode, "resolved_worker_image": image},
        )

    def test_first_phase3_release_bootstraps_worker_from_target(self) -> None:
        self.assert_resolution(
            "1.8.156",
            "",
            mode="phase3",
            image=f"{IMAGE_REPOSITORY}:1.8.156",
        )

    def test_phase3_upgrade_advances_worker_to_target(self) -> None:
        self.assert_resolution(
            "1.8.158",
            f"{IMAGE_REPOSITORY}:1.8.156",
            mode="phase3",
            image=f"{IMAGE_REPOSITORY}:1.8.158",
        )

    def test_phase3_target_replaces_an_unproven_existing_image(self) -> None:
        self.assert_resolution(
            "1.8.157",
            "ghcr.io/example/sub2api:9.9.9",
            mode="phase3",
            image=f"{IMAGE_REPOSITORY}:1.8.157",
        )

    def test_phase3_app_rollback_does_not_downgrade_worker(self) -> None:
        self.assert_resolution(
            "1.8.157",
            f"{IMAGE_REPOSITORY}:1.8.158",
            mode="phase3",
            image=f"{IMAGE_REPOSITORY}:1.8.158",
        )

    def test_prerelease_ordering_uses_the_same_monotonic_rule(self) -> None:
        self.assert_resolution(
            "1.8.157-rc.2",
            f"{IMAGE_REPOSITORY}:1.8.157-beta.3",
            mode="phase3",
            image=f"{IMAGE_REPOSITORY}:1.8.157-rc.2",
        )
        self.assert_resolution(
            "1.8.157-rc.2",
            f"{IMAGE_REPOSITORY}:1.8.157-rc.3",
            mode="phase3",
            image=f"{IMAGE_REPOSITORY}:1.8.157-rc.3",
        )

    def test_phase3_release_candidate_is_still_legacy_until_the_compatibility_floor(self) -> None:
        self.assert_resolution(
            "1.8.156-rc.1",
            f"{IMAGE_REPOSITORY}:1.8.156",
            mode="legacy_rollback",
            image=f"{IMAGE_REPOSITORY}:1.8.156",
        )

    def test_legacy_app_rollback_preserves_compatible_worker(self) -> None:
        self.assert_resolution(
            "1.8.155",
            f"{IMAGE_REPOSITORY}:1.8.156",
            mode="legacy_rollback",
            image=f"{IMAGE_REPOSITORY}:1.8.156",
        )

    def test_legacy_app_without_existing_worker_fails_closed(self) -> None:
        proc = resolve("1.8.155")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("requires an existing compatible QA Bundle Worker", proc.stderr)

    def test_legacy_app_rejects_pre_phase3_existing_worker(self) -> None:
        proc = resolve("1.8.155", f"{IMAGE_REPOSITORY}:1.8.155")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("requires an existing compatible QA Bundle Worker", proc.stderr)

    def test_legacy_app_rejects_foreign_or_unversioned_existing_image(self) -> None:
        for image in (
            "ghcr.io/example/sub2api:1.8.156",
            f"{IMAGE_REPOSITORY}:latest",
            f"{IMAGE_REPOSITORY}@sha256:{'a' * 64}",
        ):
            with self.subTest(image=image):
                proc = resolve("1.8.155", image)
                self.assertNotEqual(proc.returncode, 0)
                self.assertIn("requires an existing compatible QA Bundle Worker", proc.stderr)

    def test_invalid_target_tag_is_rejected(self) -> None:
        for target in ("v1.8.156", "1.8", "1.8.156-preview.1"):
            with self.subTest(target=target):
                proc = resolve(target)
                self.assertNotEqual(proc.returncode, 0)
                self.assertIn("target tag must match", proc.stderr)


if __name__ == "__main__":
    unittest.main()
