#!/usr/bin/env python3
"""Tests for QA Bundle Worker/publisher surface classification."""
from __future__ import annotations

import json
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
CLASSIFIER = REPO_ROOT / "ops" / "qa" / "qa_bundle_release_surface.py"
IMAGE_REPOSITORY = "ghcr.io/youxuanxue/sub2api"


def git(repo: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", "-C", str(repo), *args],
        capture_output=True,
        text=True,
        check=True,
    )


def init_repo(root: Path) -> Path:
    repo = root / "repo"
    repo.mkdir()
    git(repo, "init")
    git(repo, "config", "user.email", "surface@example.com")
    git(repo, "config", "user.name", "Surface")
    (repo / "backend/internal/observability/qa/bundle").mkdir(parents=True)
    (repo / "backend/internal/observability/qa/archive").mkdir(parents=True)
    (repo / "backend/cmd/server").mkdir(parents=True)
    (repo / "deploy/aws/cloudformation").mkdir(parents=True)
    (repo / "ops/qa").mkdir(parents=True)
    (repo / "ops/stage0").mkdir(parents=True)
    (repo / "deploy/aws/stage0").mkdir(parents=True)
    (repo / "backend/internal/service").mkdir(parents=True)
    (repo / "backend/internal/observability/qa/bundle/job.go").write_text("worker\n")
    (repo / "backend/internal/observability/qa/archive/verifier.go").write_text("verify\n")
    (repo / "backend/cmd/server/qa_bundle_worker.go").write_text("worker cmd\n")
    (repo / "backend/cmd/server/qa_bundle_canary.go").write_text("canary cmd\n")
    (repo / "backend/internal/observability/qa/service_bundle.go").write_text("publisher\n")
    (repo / "backend/internal/observability/qa/service_bundle_canary.go").write_text("canary\n")
    (repo / "deploy/aws/cloudformation/stage0-qa-raw-archive.yaml").write_text("cfn\n")
    (repo / "ops/qa/deploy_qa_raw_archive_cfn.sh").write_text("deploy\n")
    (repo / "ops/stage0/run-qa-bundle-canary-via-ssm.sh").write_text("ssm\n")
    (repo / "deploy/aws/stage0/tokenkey-qa-maintenance.sh").write_text("maintenance\n")
    (repo / "backend/internal/service/gateway.go").write_text("gateway\n")
    git(repo, "add", ".")
    git(repo, "commit", "-m", "base")
    git(repo, "tag", "v1.8.163")
    return repo


def classify(repo: Path, image: str, to_tag: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [
            "python3",
            str(CLASSIFIER),
            "--from-image",
            image,
            "--to-tag",
            to_tag,
            "--git-dir",
            str(repo),
            "--json",
        ],
        capture_output=True,
        text=True,
        check=False,
    )


class QABundleReleaseSurfaceTest(unittest.TestCase):
    def test_same_tag_is_unchanged(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = init_repo(Path(temp_dir))
            proc = classify(repo, f"{IMAGE_REPOSITORY}:1.8.163", "1.8.163")
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            payload = json.loads(proc.stdout)
            self.assertEqual(
                payload,
                {
                    "from_ref": "v1.8.163",
                    "publisher_surface_changed": False,
                    "to_ref": "v1.8.163",
                    "worker_surface_changed": False,
                },
            )

    def test_gateway_only_change_is_not_bundle_surface(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = init_repo(Path(temp_dir))
            (repo / "backend/internal/service/gateway.go").write_text("gateway changed\n")
            git(repo, "add", "backend/internal/service/gateway.go")
            git(repo, "commit", "-m", "gateway")
            git(repo, "tag", "v1.8.164")
            proc = classify(repo, f"{IMAGE_REPOSITORY}:1.8.163", "1.8.164")
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            payload = json.loads(proc.stdout)
            self.assertFalse(payload["worker_surface_changed"])
            self.assertFalse(payload["publisher_surface_changed"])

    def test_worker_path_change_is_detected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = init_repo(Path(temp_dir))
            (repo / "backend/internal/observability/qa/bundle/job.go").write_text("worker changed\n")
            git(repo, "add", "backend/internal/observability/qa/bundle/job.go")
            git(repo, "commit", "-m", "worker")
            git(repo, "tag", "v1.8.164")
            proc = classify(repo, f"{IMAGE_REPOSITORY}:1.8.163", "1.8.164")
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            payload = json.loads(proc.stdout)
            self.assertTrue(payload["worker_surface_changed"])
            self.assertFalse(payload["publisher_surface_changed"])

    def test_publisher_path_change_is_detected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = init_repo(Path(temp_dir))
            (repo / "ops/stage0/run-qa-bundle-canary-via-ssm.sh").write_text("canary runner changed\n")
            git(repo, "add", "ops/stage0/run-qa-bundle-canary-via-ssm.sh")
            git(repo, "commit", "-m", "publisher")
            git(repo, "tag", "v1.8.164")
            proc = classify(repo, f"{IMAGE_REPOSITORY}:1.8.163", "1.8.164")
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            payload = json.loads(proc.stdout)
            self.assertFalse(payload["worker_surface_changed"])
            self.assertTrue(payload["publisher_surface_changed"])

    def test_digest_or_foreign_image_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            repo = init_repo(Path(temp_dir))
            for image in (
                f"{IMAGE_REPOSITORY}@sha256:{'a' * 64}",
                "ghcr.io/example/sub2api:1.8.163",
            ):
                with self.subTest(image=image):
                    proc = classify(repo, image, "1.8.164")
                    self.assertNotEqual(proc.returncode, 0)
                    self.assertIn("reusable immutable release tag", proc.stderr)


if __name__ == "__main__":
    unittest.main()
