"""Redis role/config alerts use reads and the existing dedupe/recovery lifecycle."""
import subprocess
import unittest
from unittest.mock import patch

import host_https_watch as watch


class RedisWatchTest(unittest.TestCase):
    def fact(self, role="master", cmd=None, config=""):
        return watch.redis_primary_fact({"Config": {"Cmd": cmd or ["redis-server"]}},
                                        "role:" + role + "\r\n", config)

    def test_runtime_replica_is_p0_even_if_healthy_endpoint_is_green(self):
        fact = self.fact("slave")
        self.assertFalse(fact["healthy"])
        self.assertEqual(fact["severity"], "P0")

    def test_next_boot_risk_detected_in_array_shell_or_config(self):
        for cmd, config in [(["redis-server", "--replicaof", "10.1.2.3", "6379"], ""),
                            (["-c", "exec redis-server --slaveof 10.1.2.3 6379"], ""),
                            (["redis-server", "/data/redis.conf"], "# comment\nreplicaof 10.1.2.3 6379\n")]:
            with self.subTest(cmd=cmd):
                fact = self.fact(cmd=cmd, config=config)
                self.assertFalse(fact["healthy"])
                self.assertEqual(fact["severity"], "P1")
        self.assertFalse(self.fact(config="include /etc/other-redis.conf")["healthy"])
        self.assertTrue(self.fact(config="replica-read-only yes\n# replicaof old\n")["healthy"])

    def test_alert_dedup_escalation_and_recovery(self):
        sent = []
        bad = lambda: self.fact(config="replicaof 10.1.2.3 6379")
        state = watch.tick_redis_primary({}, 1000, bad, sent.append, "prod-data")
        self.assertEqual(sent, [])
        state = watch.tick_redis_primary(state, 1120, bad, sent.append, "prod-data")
        self.assertEqual(len(sent), 1)
        self.assertIn("P1", sent[0])
        state = watch.tick_redis_primary(state, 1240, bad, sent.append, "prod-data")
        self.assertEqual(len(sent), 1)
        state = watch.tick_redis_primary(state, 1360, lambda: self.fact("slave"), sent.append, "prod-data")
        self.assertEqual(len(sent), 2)
        self.assertIn("P0", sent[1])
        state = watch.tick_redis_primary(state, 1480, self.fact, sent.append, "prod-data")
        self.assertIn("已恢复", sent[-1])
        self.assertFalse(state["dependency:redis-primary"]["active"])
        watch.tick_redis_primary(state, 1600, self.fact, sent.append, "prod-data")
        self.assertEqual(len(sent), 3)

    def test_probe_failure_is_redacted(self):
        with patch.object(watch.subprocess, "run", side_effect=subprocess.TimeoutExpired("secret", 5)):
            fact = watch.probe_redis_primary()
        self.assertFalse(fact["healthy"])
        self.assertEqual(fact["reason"], "Redis primary probe failed")

    def test_config_read_failure_does_not_claim_runtime_outage(self):
        def run(args, **kwargs):
            if args[1] == "inspect":
                result = '[{"Config":{"Cmd":["redis-server"]}}]'
            elif args[-1] == "replication":
                result = "role:master\r\n"
            elif args[-1] == "server":
                result = "config_file:/data/redis.conf\r\n"
            else:
                raise subprocess.CalledProcessError(1, args, stderr="never-print")
            return subprocess.CompletedProcess(args, 0, stdout=result)
        with patch.object(watch, "resolve_redis_server", return_value="isolated-redis"), \
             patch.object(watch.subprocess, "run", side_effect=run):
            fact = watch.probe_redis_primary()
        self.assertFalse(fact["healthy"])
        self.assertEqual(fact["severity"], "P1")
        self.assertNotIn("never-print", str(fact))

    def test_probe_reads_role_and_actual_config_without_writes(self):
        calls = []
        def run(args, **kwargs):
            calls.append(args)
            if args[1] == "inspect":
                result = '[{"Config":{"Cmd":["redis-server","/data/redis.conf"]}}]'
            elif args[-1] == "replication":
                result = "role:master\r\n"
            elif args[-1] == "server":
                result = "config_file:/data/redis.conf\r\n"
            else:
                self.assertEqual(args[-3:], ["cat", "--", "/data/redis.conf"])
                result = "requirepass never-print\nreplicaof 10.1.2.3 6379\n"
            return subprocess.CompletedProcess(args, 0, stdout=result)
        with patch.object(watch, "resolve_redis_server", return_value="isolated-redis"), \
             patch.object(watch.subprocess, "run", side_effect=run):
            fact = watch.probe_redis_primary()
        self.assertEqual(fact["severity"], "P1")
        self.assertNotIn("never-print", str(fact))
        self.assertEqual(len(calls), 4)


if __name__ == "__main__":
    unittest.main()
