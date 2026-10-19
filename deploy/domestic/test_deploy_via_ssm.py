#!/usr/bin/env python3
"""Contract tests for deploy/domestic/deploy_via_ssm.sh (fake aws, no network)."""
from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "deploy_via_ssm.sh"
_REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
_NORMALIZE = _REPO_ROOT / "ops/stage0/normalize-deploy-tag.sh"
_VALIDATE = _REPO_ROOT / "ops/stage0/validate-deploy-tag.sh"
_MANIFEST = _REPO_ROOT / "deploy/domestic/prod-target.json"


class DeployViaSsmTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self._tmp.name) / "repo"
        self.repo.mkdir()
        for rel in (
            "deploy/domestic",
            "ops/stage0",
            "ops/observability",
            "ops/lib",
            "scripts",
        ):
            (self.repo / rel).mkdir(parents=True)

        shutil.copy(_SCRIPT, self.repo / "deploy/domestic/deploy_via_ssm.sh")
        (self.repo / "deploy/domestic/deploy_via_ssm.sh").chmod(0o755)
        shutil.copy(_NORMALIZE, self.repo / "ops/stage0/normalize-deploy-tag.sh")
        (self.repo / "ops/stage0/normalize-deploy-tag.sh").chmod(0o755)
        shutil.copy(_VALIDATE, self.repo / "ops/stage0/validate-deploy-tag.sh")
        (self.repo / "ops/stage0/validate-deploy-tag.sh").chmod(0o755)

        manifest = json.loads(_MANIFEST.read_text())
        manifest["ssm_instance_id"] = "mi-0efdf781205cff512"
        manifest["ssm_region"] = "eu-west-2"
        (self.repo / "deploy/domestic/prod-target.json").write_text(
            json.dumps(manifest, indent=2) + "\n"
        )

        # Minimal control files for sync-controls.
        for name, body in (
            ("bluegreen.py", "print('bluegreen')\n"),
            ("post_release.py", "print('post')\n"),
            ("prod-target.json", json.dumps(manifest) + "\n"),
        ):
            (self.repo / "deploy/domestic" / name).write_text(body)
        (self.repo / "ops/stage0/deploy_via_ssm_bluegreen.sh").write_text("#!/bin/bash\n")
        (self.repo / "scripts/release_post_check.py").write_text("print(1)\n")
        (self.repo / "ops/observability/probe-post-release-tick.sh").write_text("#!/bin/bash\n")
        (self.repo / "ops/lib/resolve_app_container.py").write_text("print(1)\n")
        (self.repo / "ops/stage0/notify-feishu-release.sh").write_text("#!/bin/bash\n")

        self.fakebin = self.repo / "fakebin"
        self.fakebin.mkdir()
        self.aws_log = self.repo / "aws-args.log"
        self.ssm_parameters = self.repo / "ssm-parameters.json"
        self.stdout_fixture = self.repo / "ssm-stdout.json"
        self.stdout_fixture.write_text(
            json.dumps(
                {
                    "tag": "1.8.306",
                    "source": "ghcr.io/youxuanxue/sub2api:1.8.306",
                    "revision": "2d4b4cdbf8fbae7d1cf888d5c47b6a584ad6d2bd",
                    "architecture": "amd64",
                    "image_id": "sha256:00da",
                    "local_tag": "tokenkey-domestic/tokenkey:1.8.306-2d4b4cdbf",
                },
                indent=2,
            )
            + "\n"
        )
        describe_fixture = self.repo / "ssm-describe.json"
        describe_fixture.write_text(
            json.dumps(
                {
                    "InstanceInformationList": [
                        {
                            "PingStatus": "Online",
                            "Name": "cn-prod",
                            "PlatformType": "Linux",
                        }
                    ]
                }
            )
            + "\n"
        )
        (self.fakebin / "aws").write_text(
            textwrap.dedent(
                f"""\
                #!/usr/bin/env bash
                printf '%s\\n' "$*" >> "{self.aws_log}"
                case "$*" in
                  *describe-instance-information*)
                    cat "{describe_fixture}"
                    ;;
                  *send-command*)
                    for arg in "$@"; do
                      case "$arg" in file://*) cp "${{arg#file://}}" "{self.ssm_parameters}" ;; esac
                    done
                    echo "cmd-test-1"
                    ;;
                  *get-command-invocation*Status*)
                    echo "Success"
                    ;;
                  *get-command-invocation*StandardOutputContent*)
                    cat "{self.stdout_fixture}"
                    ;;
                  *get-command-invocation*StandardErrorContent*)
                    echo ""
                    ;;
                  *)
                    echo "unexpected aws: $*" >&2
                    exit 1
                    ;;
                esac
                """
            ),
        )
        (self.fakebin / "aws").chmod(0o755)
        (self.fakebin / "jq").write_text(
            textwrap.dedent(
                """\
                #!/usr/bin/env bash
                # Minimal jq stub: write {"commands":[...]} from --arg cmd
                out=""
                cmd=""
                timeout="1200"
                while [[ $# -gt 0 ]]; do
                  case "$1" in
                    -n) shift ;;
                    --arg)
                      if [[ "$2" == "cmd" ]]; then cmd="$3"; fi
                      if [[ "$2" == "timeout" ]]; then timeout="$3"; fi
                      shift 3
                      ;;
                    *) shift ;;
                  esac
                done
                python3 -c "import json,sys; print(json.dumps({'commands':[sys.argv[1]],'executionTimeout':[sys.argv[2]]}))" "$cmd" "$timeout"
                """
            ),
        )
        (self.fakebin / "jq").chmod(0o755)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _run(self, *args: str) -> subprocess.CompletedProcess[str]:
        env = {**os.environ, "PATH": f"{self.fakebin}:{os.environ.get('PATH', '')}"}
        return subprocess.run(
            ["bash", "deploy/domestic/deploy_via_ssm.sh", *args],
            cwd=self.repo,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_pull_strips_v_and_sends_ssm(self) -> None:
        proc = self._run("pull", "--tag", "v1.8.306")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("send-command", self.aws_log.read_text())
        self.assertIn("mi-0efdf781205cff512", self.aws_log.read_text())
        self.assertIn('"tag": "1.8.306"', proc.stdout)
        self.assertIn("tokenkey-domestic/tokenkey:1.8.306-2d4b4cdbf", proc.stdout)

    def test_pull_rejects_bad_digest(self) -> None:
        proc = self._run("pull", "--tag", "1.8.306", "--digest", "not-a-digest")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("digest", proc.stderr)

    def test_ssm_pull_executes_under_posix_shell_and_enforces_architecture(self) -> None:
        proc = self._run("pull", "--tag", "1.8.306")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        remote = json.loads(self.ssm_parameters.read_text())["commands"][0]
        docker = self.fakebin / "docker"
        docker.write_text(textwrap.dedent("""\
            #!/usr/bin/env bash
            case "$*" in
              pull*) exit 0 ;;
              *Architecture*) echo "${TEST_ARCH:-amd64}" ;;
              *image.revision*) echo 2d4b4cdbf8fbae7d1cf888d5c47b6a584ad6d2bd ;;
              *image.version*) echo 1.8.306 ;;
              *'.Id'*) echo sha256:test ;;
              tag*) exit 0 ;;
              *) exit 1 ;;
            esac
            """))
        docker.chmod(0o755)
        env = {**os.environ, "PATH": f"{self.fakebin}:{os.environ.get('PATH', '')}"}
        for arch in ("amd64", "arm64"):
            with self.subTest(arch=arch):
                result = subprocess.run(
                    [shutil.which("dash") or "/bin/sh", "-c", remote],
                    env={**env, "TEST_ARCH": arch},
                    capture_output=True, text=True, check=False,
                )
                if arch == "amd64":
                    self.assertEqual(result.returncode, 0, msg=result.stderr)
                    self.assertEqual(json.loads(result.stdout)["local_tag"],
                                     "tokenkey-domestic/tokenkey:1.8.306-2d4b4cdbf")
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, "")

    def test_prepare_requires_full_revision(self) -> None:
        proc = self._run(
            "prepare",
            "--tag",
            "1.8.306",
            "--revision",
            "abc",
            "--image",
            "tokenkey-domestic/tokenkey:1.8.306-2d4b4cdbf",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("40-char", proc.stderr)

    def test_sync_controls_sends_bundle(self) -> None:
        proc = self._run("sync-controls")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        log = self.aws_log.read_text()
        self.assertIn("send-command", log)
        self.assertIn("cn-prod-deploy sync-controls", log)

    def test_pull_dry_run_emits_amd64_and_label_checks(self) -> None:
        env = {
            **os.environ,
            "PATH": f"{self.fakebin}:{os.environ.get('PATH', '')}",
            "CN_PROD_SSM_DRY_RUN": "1",
        }
        proc = subprocess.run(
            ["bash", "deploy/domestic/deploy_via_ssm.sh", "pull", "--tag", "v1.8.306"],
            cwd=self.repo,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        remote = proc.stdout
        self.assertIn('docker pull "$SOURCE"', remote)
        self.assertIn("test \"$ARCH\" = amd64", remote)
        self.assertIn('test "$VER" = "$EXPECT_TAG"', remote)
        self.assertIn("--format '{{.Id}}'", remote)
        self.assertIn(
            '--format \'{{index .Config.Labels "org.opencontainers.image.revision"}}\'',
            remote,
        )
        self.assertNotIn("send-command", self.aws_log.read_text())

    def test_prepare_rejects_image_tag_mismatch(self) -> None:
        proc = self._run(
            "prepare",
            "--tag",
            "1.8.306",
            "--revision",
            "2d4b4cdbf8fbae7d1cf888d5c47b6a584ad6d2bd",
            "--image",
            "tokenkey-domestic/tokenkey:1.8.306-deadbeef0",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("must be tokenkey-domestic/tokenkey:1.8.306-2d4b4cdbf", proc.stderr)

    def test_prepare_dry_run_quotes_shared_source(self) -> None:
        env = {
            **os.environ,
            "PATH": f"{self.fakebin}:{os.environ.get('PATH', '')}",
            "CN_PROD_SSM_DRY_RUN": "1",
        }
        proc = subprocess.run(
            [
                "bash",
                "deploy/domestic/deploy_via_ssm.sh",
                "prepare",
                "--tag",
                "1.8.306",
                "--revision",
                "2d4b4cdbf8fbae7d1cf888d5c47b6a584ad6d2bd",
                "--image",
                "tokenkey-domestic/tokenkey:1.8.306-2d4b4cdbf",
            ],
            cwd=self.repo,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn(
            "--shared-source '/opt/tokenkey/releases/artifacts-ssm/deploy_via_ssm_bluegreen.sh'",
            proc.stdout,
        )
        self.assertIn("--revision '2d4b4cdbf8fbae7d1cf888d5c47b6a584ad6d2bd'", proc.stdout)


if __name__ == "__main__":
    unittest.main()
