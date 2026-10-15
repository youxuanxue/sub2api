"""Exercise the generated domestic ingress with real Caddy and a local stub.

Run via scripts/checks/caddyfile-syntax.sh (requires Docker and caddy:2-alpine).
No production credentials, traffic, DNS, or upstream calls are involved.
"""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[2]


def run(*args, input=None):
    result = subprocess.run(args, input=input, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr}")
    return result.stdout


class DomesticDiskGuardTest(unittest.TestCase):
    def test_only_successful_empty_disk_probes_allow_formatting(self):
        with tempfile.TemporaryDirectory(prefix="tk-domestic-disk-test-") as tmp:
            fakebin = Path(tmp)
            commands = {
                "lsblk": '''#!/usr/bin/env bash
case "$2" in
  NAME) [[ "$PROBE_FAIL" != topology ]] || exit 23
        printf '%s\\n' "$NAME_OUTPUT" ;;
  MOUNTPOINTS) [[ "$PROBE_FAIL" != mounts ]] || exit 24
              printf '%s\\n' "$MOUNTS_OUTPUT" ;;
  *) exit 25 ;;
esac
''',
                "wipefs": '''#!/usr/bin/env bash
[[ "$PROBE_FAIL" != signatures ]] || exit 26
printf '%s\\n' "$SIGNATURE_OUTPUT"
''',
            }
            for name, source in commands.items():
                command = fakebin / name
                command.write_text(source)
                command.chmod(0o700)
            cases = (
                ("empty", {}, True),
                ("topology error", {"PROBE_FAIL": "topology"}, False),
                ("mount probe error", {"PROBE_FAIL": "mounts"}, False),
                ("signature probe error", {"PROBE_FAIL": "signatures"}, False),
                ("missing topology", {"NAME_OUTPUT": ""}, False),
                ("partitions", {"NAME_OUTPUT": "/dev/fixture\n/dev/fixture1"}, False),
                ("mounted", {"MOUNTS_OUTPUT": "/srv/data"}, False),
                ("existing filesystem", {"SIGNATURE_OUTPUT": "ext4"}, False),
            )
            for label, overrides, allowed in cases:
                with self.subTest(case=label):
                    env = dict(os.environ, PATH=str(fakebin) + os.pathsep + os.environ["PATH"],
                               PROBE_FAIL="", NAME_OUTPUT="/dev/fixture",
                               MOUNTS_OUTPUT="", SIGNATURE_OUTPUT="")
                    env.update(overrides)
                    result = subprocess.run(
                        ["bash", "-c", 'source "$1"; require_empty_data_disk /dev/fixture; echo SAFE_TO_FORMAT',
                         "disk-guard", str(ROOT / "deploy/domestic/bootstrap-host.sh")],
                        env=env, capture_output=True, text=True,
                    )
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)
                    self.assertEqual("SAFE_TO_FORMAT" in result.stdout, allowed)


class DomesticRuntimeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix="tk-domestic-test-")
        cls.addClassCleanup(cls.tmp.cleanup)
        cls.state = Path(cls.tmp.name)
        (cls.state / "images.json").write_text(json.dumps({
            name: {"tag": f"fixture/{name}:verified"}
            for name in ("tokenkey", "caddy", "postgres", "redis")
        }))
        cls.prepare()
        adapted = json.loads(run(
            "docker", "run", "--rm", "-i", "caddy:2-alpine", "caddy", "adapt",
            "--config", "/dev/stdin", "--adapter", "caddyfile",
            input=(cls.state / "runtime/Caddyfile").read_text(),
        ))
        # Keep the rendered routes/matchers/order intact. Only replace TLS and
        # network destinations so the test never needs public DNS or a real app.
        servers = adapted["apps"]["http"]["servers"]
        for server in servers.values():
            server["listen"] = [":8080"]
            server.pop("tls_connection_policies", None)
            server["automatic_https"] = {"disable": True}

        def local_upstream(obj):
            if isinstance(obj, dict):
                if obj.get("handler") == "reverse_proxy":
                    obj["upstreams"] = [{"dial": "127.0.0.1:18081"}]
                    # The fixture backend is created by this same Caddy
                    # process. Active health checks add a startup race here;
                    # production keeps them in the shared template.
                    health_checks = obj.get("health_checks", {})
                    health_checks.pop("active", None)
                    if not health_checks:
                        obj.pop("health_checks", None)
                for value in obj.values():
                    local_upstream(value)
            elif isinstance(obj, list):
                for value in obj:
                    local_upstream(value)

        local_upstream(servers)
        servers["stub"] = {
            "listen": [":18081"],
            "routes": [{"handle": [{"handler": "static_response",
                                     "status_code": 202, "body": "app reached"}]}],
        }
        adapted["apps"].pop("tls", None)
        adapted["admin"] = {"disabled": True}
        config = cls.state / "test-caddy.json"
        config.write_text(json.dumps(adapted))
        cls.container = run(
            "docker", "create", "--rm", "-p", "127.0.0.1::8080", "caddy:2-alpine",
            "caddy", "run", "--config", "/etc/caddy/test.json",
        ).strip()
        cls.addClassCleanup(run, "docker", "rm", "-f", cls.container)
        run("docker", "cp", str(config), cls.container + ":/etc/caddy/test.json")
        run("docker", "start", cls.container)
        address = run("docker", "port", cls.container, "8080/tcp").strip()
        cls.base = "http://" + address
        for _ in range(50):
            try:
                if cls.request("/health")[0] == 202:
                    break
            except (urllib.error.URLError, TimeoutError):
                pass  # Bounded startup retry; fail below if Caddy never starts.
            time.sleep(0.1)
        else:
            raise RuntimeError("Test Caddy did not become ready")

    @classmethod
    def prepare(cls):
        return run(sys.executable, str(ROOT / "deploy/domestic/prepare-runtime.py"),
                   str(cls.state))

    @classmethod
    def request(cls, path, method="GET"):
        request = urllib.request.Request(
            cls.base + path, method=method,
            data=b"{}" if method == "POST" else None,
            headers={"Host": "api-cn.tokenkey.dev"},
        )
        try:
            response = urllib.request.urlopen(request, timeout=2)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, response.read().decode()

    def test_public_paths_remain_shared_application_owned(self):
        # The shared Caddy template forwards product and legacy paths to the
        # application; capability and retired-service decisions stay there.
        paths = (
            "/v1/images/generations", "/images/generations",
            "/v1/video/generations", "/video/generations",
            "/v1/videos", "/videos", "/videos/job/content",
            "/api/v3/contents/generations/tasks", "/v3/contents/generations/tasks/job",
            "/v1/contents/generations/tasks", "/openrouter/v1/images",
            "/openrouter/v1/videos/job", "/audio/speech", "/v1/audio/speech",
            "/v1/live", "/v1/live/call", "/backend-api/codex/realtime/calls",
            "/backend-api/codex/call-id", "/v1/realtime",
            "/studio", "/studio/image", "/batch-image", "/api/v1/studio/models",
            "/api/v1/media/upload", "/api/v1/batch-image/jobs",
            "/api/v1/qa", "/api/v1/qa/raw", "/api/v1/admin/qa/stats",
            "/v1/%76ideo/generations", "/v1//video/generations",
        )
        for path in paths:
            for method in ("GET", "POST"):
                with self.subTest(path=path, method=method):
                    self.assertEqual(self.request(path, method), (202, "app reached"))

    def test_text_api_and_management_still_reach_app(self):
        for path in (
            "/v1/chat/completions", "/v1/responses", "/v1/messages",
            "/v1/messages/count_tokens", "/v1/models", "/v1/usage",
            "/chat/completions", "/responses", "/health", "/login",
            "/api/v1/auth/login", "/api/v1/keys", "/api/v1/usage",
            "/api/v1/admin/users", "/api/v1/admin/ops/dashboard",
            "/backend-api/codex/responses", "/backend-api/codex/models",
        ):
            with self.subTest(path=path):
                for method in ("GET", "POST"):
                    self.assertEqual(self.request(path, method), (202, "app reached"))

    def test_prepare_preserves_secrets_and_private_permissions(self):
        before = (self.state / "runtime/.env").read_bytes()
        output = self.prepare()
        self.assertEqual((self.state / "runtime/.env").read_bytes(), before)
        for secret in json.loads((self.state / "login-secrets.json").read_text()).values():
            self.assertNotIn(secret, output)
        self.assertEqual(self.state.stat().st_mode & 0o777, 0o700)
        self.assertEqual((self.state / "runtime/.env").stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
