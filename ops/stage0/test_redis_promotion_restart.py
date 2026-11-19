"""Real Redis regression rehearsal. Opt in with TK_REDIS_REHEARSAL=1.

Only uniquely named containers on a new --internal network, with new volumes.
No published ports, host network, production data or external Redis connections.
"""
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "ops/lib/redis-promotion.sh"
ENTRYPOINT = ROOT / "ops/stage0/dataplane-redis-entrypoint.sh"


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True, stderr=subprocess.PIPE, timeout=30).strip()


@unittest.skipUnless(os.environ.get("TK_REDIS_REHEARSAL") == "1", "isolated Docker rehearsal is opt-in")
class RedisRestartTest(unittest.TestCase):
    def setUp(self):
        self.prefix = "tk-redis-rehearsal-" + secrets.token_hex(5)
        self.network = self.prefix + "-net"
        self.containers = []
        self.volumes = []
        self.addCleanup(self.cleanup_resources)
        self.secret_dir = tempfile.TemporaryDirectory(prefix="tk-redis-rehearsal-", dir=Path.home())
        self.addCleanup(self.secret_dir.cleanup)
        self.password = secrets.token_hex(16)
        self.secret = Path(self.secret_dir.name) / "password"
        self.secret.write_text(self.password)
        self.secret.chmod(0o600)
        self.image = os.environ.get("TK_REDIS_REHEARSAL_IMAGE", "redis:8-alpine")
        # The entrypoint admits private 10.x literals. Network is internal and
        # has no host bindings; a collision fails before any containers start.
        docker("network", "create", "--internal", "--subnet", f"10.253.{secrets.randbelow(250)}.0/24", self.network)
        self.master = self.prefix + "-master"
        self.containers.append(self.master)
        docker("run", "-d", "--name", self.master, "--network", self.network, "--cpus", "0.5", "--memory", "128m",
               self.image, "redis-server", "--requirepass", self.password)
        self.wait(self.master)
        self.master_ip = docker("inspect", "-f", '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}', self.master)
        self.assertEqual(self.cli(self.master, "SET", "rehearsal-seed", "preserved"), "OK")

    def cleanup_resources(self):
        # Names are generated in this fixture, never accepted from caller env.
        for name in reversed(self.containers):
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        for name in self.volumes:
            subprocess.run(["docker", "volume", "rm", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        subprocess.run(["docker", "network", "rm", self.network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)

    def cli(self, name, *args):
        return docker("exec", "-e", "REDISCLI_AUTH=" + self.password, name,
                      "redis-cli", "--no-auth-warning", *args)

    def wait(self, name, *, replicated=False):
        for _ in range(100):
            try:
                if replicated:
                    if self.cli(name, "GET", "rehearsal-seed") == "preserved":
                        return
                elif self.cli(name, "PING") == "PONG":
                    return
            except subprocess.CalledProcessError:
                pass  # startup not ready; bounded by the loop budget
            time.sleep(0.1)
        self.fail("isolated Redis did not become ready")

    def replica(self, suffix, *, legacy=False, volume=None):
        name = self.prefix + "-" + suffix
        self.containers.append(name)
        if volume is None:
            volume = name + "-data"
            docker("volume", "create", volume)
            self.volumes.append(volume)
        args = ["run", "-d", "--name", name, "--network", self.network, "--cpus", "0.5", "--memory", "128m",
                "--mount", f"type=volume,src={volume},dst=/data"]
        if legacy:
            args += [self.image, "redis-server", "--appendonly", "yes", "--requirepass", self.password,
                     "--masterauth", self.password, "--replicaof", self.master_ip, "6379"]
        else:
            args += ["--mount", f"type=bind,src={ENTRYPOINT},dst=/runtime-entrypoint.sh,readonly",
                     "--mount", f"type=bind,src={self.secret},dst=/run/requirepass,readonly",
                     "--mount", f"type=bind,src={self.secret},dst=/run/masterauth,readonly",
                     "-e", "REDIS_INITIAL_MASTER_HOST=" + self.master_ip,
                     "-e", "REDIS_INITIAL_MASTER_PORT=6379", "--entrypoint", "sh",
                     self.image, "/runtime-entrypoint.sh", "--appendonly", "yes", "--save", ""]
        docker(*args)
        self.wait(name, replicated=True)
        return name, volume

    def set_role(self, name, *args):
        # Exercise the same helper used by the migration, with a fixture-only client.
        # Keep callback arguments independent from role arguments.
        script = 'source "$1"; container="$2"; password="$3"; shift 3; rc(){ docker exec -e REDISCLI_AUTH="$password" "$container" redis-cli --no-auth-warning "$@"; }; tk_redis_persistence_check rc && tk_redis_startup_check "$container" && tk_redis_set_role rc "$@"'
        return subprocess.run(["bash", "-c", script, "rehearsal", str(HELPER), name, self.password, *args],
                              capture_output=True, text=True, timeout=20)

    def test_legacy_runtime_promotion_reverts_after_restart(self):
        name, _ = self.replica("legacy", legacy=True)
        self.assertEqual(self.cli(name, "REPLICAOF", "NO", "ONE"), "OK")
        self.assertIn("role:master", self.cli(name, "INFO", "replication"))
        # New migration refuses the legacy server before it can change its role.
        refused = self.set_role(name, "NO")
        self.assertNotEqual(refused.returncode, 0)
        self.assertIn("persistent", refused.stderr)
        docker("restart", name)
        self.wait(name)
        self.assertIn("role:slave", self.cli(name, "INFO", "replication"))
        self.assertIn("READONLY", self.cli(name, "SET", "isolated-write", "1"))

    def test_promote_restart_recreate_and_rollback_preserve_durable_role(self):
        name, volume = self.replica("durable")
        result = self.set_role(name, "NO")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.cli(name, "SET", "after-promote", "also-preserved"), "OK")
        self.assertEqual(docker("exec", name, "stat", "-c", "%a", "/data/tokenkey-redis.conf"), "600")
        docker("restart", name)
        self.wait(name)
        self.assertIn("role:master", self.cli(name, "INFO", "replication"))
        self.assertEqual(self.cli(name, "GET", "rehearsal-seed"), "preserved")
        self.assertEqual(self.cli(name, "GET", "after-promote"), "also-preserved")
        # Container recreation is stricter than a process restart: startup env
        # still names the old master, yet the persisted promotion must win.
        docker("stop", name)
        docker("rm", name)
        self.containers.remove(name)
        name, _ = self.replica("recreated", volume=volume)
        self.assertIn("role:master", self.cli(name, "INFO", "replication"))
        self.assertEqual(self.cli(name, "GET", "after-promote"), "also-preserved")
        self.assertEqual(self.cli(name, "SET", "after-recreate", "ok"), "OK")
        result = self.set_role(name, self.master_ip, "6379")
        self.assertEqual(result.returncode, 0, result.stderr)
        docker("restart", name)
        self.wait(name)
        self.assertIn("role:slave", self.cli(name, "INFO", "replication"))
        self.assertIn("READONLY", self.cli(name, "SET", "isolated-write", "1"))

    def test_entrypoint_refuses_missing_credentials(self):
        result = subprocess.run(["docker", "run", "--rm", "--network", "none",
                                 "--cpus", "0.5", "--memory", "128m",
                                 "--mount", f"type=bind,src={ENTRYPOINT},dst=/runtime-entrypoint.sh,readonly",
                                 "--entrypoint", "sh", self.image, "/runtime-entrypoint.sh"],
                                capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 64)
        self.assertIn("credential files required", result.stderr)

    def test_entrypoint_refuses_overrides_and_missing_role_on_existing_data(self):
        name, _ = self.replica("guards")
        result = subprocess.run(["docker", "exec", name, "sh", "/runtime-entrypoint.sh",
                                 "--replicaof", self.master_ip, "6379"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 64)
        self.assertIn("replication flags override", result.stderr)
        self.assertIn("role:slave", self.cli(name, "INFO", "replication"))
        # A missing config must not bootstrap against the retired old primary.
        docker("exec", name, "rm", "/data/tokenkey-redis.conf")
        docker("restart", name)
        for _ in range(30):
            if docker("inspect", "-f", "{{.State.Running}}", name) == "false":
                break
            time.sleep(0.1)
        self.assertEqual(docker("inspect", "-f", "{{.State.ExitCode}}", name), "64")
        logs = subprocess.check_output(["docker", "logs", name], stderr=subprocess.STDOUT, text=True, timeout=5)
        self.assertIn("existing Redis data without persistent role", logs)


class PersistenceFailureTest(unittest.TestCase):
    def test_startup_check_rejects_ephemeral_or_readonly_data(self):
        script = 'source "$1"; fixture_mount="$2"; docker(){ case "$*" in *Mounts*) echo "$fixture_mount" ;; *) echo "redis-server /data/tokenkey-redis.conf" ;; esac; }; tk_redis_startup_check isolated'
        for mount, accepted in [("volume:true", True), ("bind:true", True),
                                ("tmpfs:true", False), ("bind:false", False), ("", False)]:
            with self.subTest(mount=mount):
                result = subprocess.run(["bash", "-c", script, "test", str(HELPER), mount], capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)

    def test_error_reply_is_not_treated_as_success(self):
        with tempfile.TemporaryDirectory() as temp:
            calls = Path(temp) / "calls"
            script = 'source "$1"; log="$2"; rc(){ echo "$*" >> "$log"; if [ "$1" = REPLICAOF ]; then echo OK; else echo "ERR Rewriting config file: Permission denied"; fi; }; tk_redis_set_role rc NO'
            result = subprocess.run(["bash", "-c", script, "test", str(HELPER), str(calls)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("do not resume writers", result.stderr)
            self.assertEqual(calls.read_text().splitlines(), ["REPLICAOF NO ONE", "CONFIG REWRITE"])


if __name__ == "__main__":
    unittest.main()
