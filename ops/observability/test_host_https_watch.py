import pathlib
import json
import sys
import unittest
from unittest.mock import patch
from types import SimpleNamespace

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import host_https_watch as watch
from https_health import probe_health


class CoveragePlanTest(unittest.TestCase):
    def setUp(self):
        root = pathlib.Path(__file__).resolve().parents[2]
        self.fresh = json.loads((root / 'deploy/aws/fresh-migration-20261015.json').read_text())
        self.domestic = json.loads((root / 'deploy/domestic/prod-target.json').read_text())

    def config(self, role, hostname):
        return watch.build_config(role, hostname, self.fresh, self.domestic)

    def test_both_observers_cover_cn_ip_and_all_customer_domains(self):
        for role, host in [('cn-prod', self.domestic['instance_name'])]:
            with self.subTest(role=role):
                config = watch.build_config(role, host, self.fresh, self.domestic)
                targets = config['targets']
                self.assertEqual(targets['cn-prod'], self.domestic['public_origin'] + '/health')
                self.assertEqual(targets['prod:cn-domain'], 'https://' + self.domestic['fallback_domain'] + '/health')
                for domain in self.fresh['prod']['domains']:
                    self.assertIn('https://' + domain + '/health', targets.values())
        cn = self.config('cn-prod', self.domestic['instance_name'])
        self.assertEqual(cn['jobs'], ['tokenkey-disk-metrics'])

    def test_refuses_wrong_host_and_retains_prod_monitor_peer(self):
        with self.assertRaisesRegex(ValueError, 'host identity'):
            self.config('cn-prod', 'some-other-host')
        prod = self.config('prod', self.fresh['prod']['instance_name'])
        # 2026-10-17 prod-only: no live edge peer → watch cn-prod.
        self.assertEqual(prod['targets'], {'cn-prod': self.domestic['public_origin'].rstrip('/') + '/health'})
        self.assertIn('tokenkey-qa-maintenance', prod['jobs'])
        # Tencent live watch hostname is also accepted.
        watch_host = self.fresh['prod'].get('https_watch_hostname')
        if watch_host:
            self.assertEqual(
                self.config('prod', watch_host)['targets'],
                prod['targets'],
            )
    def test_prod_pause_does_not_silence_other_observers_or_jobs(self):
        prod = self.config('prod', self.fresh['prod']['instance_name'])
        self.assertEqual(prod['paused_jobs'], self.fresh['prod']['paused_jobs'])
        self.assertEqual(
            set(prod['paused_jobs']),
            {
                'tokenkey-qa-maintenance',
                'tokenkey-qa-boundary',
                'tokenkey-pgdump',
                'tokenkey-disk-metrics',
                'tokenkey-ghcr-prune-daily',
            },
        )
        self.assertEqual(self.config('cn-prod', self.domestic['instance_name'])['paused_jobs'], {})
        self.assertIn('tokenkey-pgdump', prod['jobs'])
        # Held-disabled boundary must stay monitored as expected-paused so
        # accidental re-enable still alerts; healthy when disabled+inactive.
        boundary_limit = watch.JOB_LIMITS['tokenkey-qa-boundary']
        inactive = {'ActiveState': 'inactive', 'Result': 'success'}
        disabled = {'ActiveState': 'inactive', 'UnitFileState': 'disabled'}
        self.assertTrue(
            watch.job_fact(inactive, disabled, 1000, boundary_limit, expected_paused=True)['healthy']
        )
        self.assertFalse(watch.job_fact(inactive, disabled, 1000, boundary_limit)['healthy'])
        # Absent unit (not installed on cutover host) is healthy when paused.
        absent = {'ActiveState': 'inactive', 'UnitFileState': ''}
        self.assertTrue(
            watch.job_fact(inactive, absent, 1000, boundary_limit, expected_paused=True)['healthy']
        )

    def test_local_provision_is_monitored_independently_of_paused_archive(self):
        # After Tencent cutover, local_qa_jobs is cleanup-only until provision is installed.
        prod = self.config('prod', self.fresh['prod']['instance_name'])
        self.assertIn('tokenkey-qa-cleanup', prod['jobs'])
        self.assertNotIn('tokenkey-qa-cleanup', prod['paused_jobs'])
        self.assertNotIn('tokenkey-qa-provision', prod['jobs'])
        self.assertIn('tokenkey-qa-cleanup', watch.JOB_LIMITS)
        limit = watch.JOB_LIMITS['tokenkey-qa-cleanup']
        self.assertFalse(watch.job_fact({'ActiveState':'inactive', 'Result':'success'},
                                       {'ActiveState':'inactive', 'UnitFileState':'disabled'},
                                       10000, limit)['healthy'])
    def test_pause_checks_disabled_state_and_alerts_on_accidental_resume(self):
        inactive = {'ActiveState': 'inactive', 'Result': 'exit-code'}
        disabled = {'ActiveState': 'inactive', 'UnitFileState': 'disabled'}
        limit = watch.JOB_LIMITS['tokenkey-qa-maintenance']
        self.assertTrue(watch.job_fact(inactive, disabled, 1000, limit, expected_paused=True)['healthy'])
        self.assertFalse(watch.job_fact(inactive, disabled, 1000, limit)['healthy'])
        for service, timer in ((inactive, {**disabled, 'UnitFileState': 'enabled'}),
                               ({**inactive, 'ActiveState': 'active'}, disabled),
                               (inactive, {**disabled, 'ActiveState': 'active'})):
            with self.subTest(service=service, timer=timer):
                fact = watch.job_fact(service, timer, 1000, limit, expected_paused=True)
                self.assertFalse(fact['healthy'])
                sent = []
                probe = lambda name: fact
                state = watch.tick_jobs(['tokenkey-qa-maintenance'], {}, 1000, probe, sent.append)
                watch.tick_jobs(['tokenkey-qa-maintenance'], state, 1120, probe, sent.append)
                self.assertEqual(len(sent), 1)
                self.assertIn('unexpectedly enabled or running', sent[0])


class PhysicalBackupWatchTest(unittest.TestCase):
    def test_only_expired_successful_evidence_is_advisory(self):
        import tempfile
        sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / 'stage0'))
        import postgres_backup as backup
        now = 2_000_000
        current = {'backup': [{'timestamp': {'stop': now - 30}}]}
        archive = {'status': 'ok', 'started_at': now - 10}
        old = {'status': 'ok', 'completed_at': now - 9 * 86400}
        cases = [
            (current, archive, old, old, True, 'warning'),
            (current, archive, None, old, False, 'error'),
            (current, archive, old, None, False, 'error'),
            (current, archive, dict(old, status='failed'), old, False, 'error'),
            (current, archive, old, dict(old, status='failed'), False, 'error'),
            (current, archive, old, dict(old, completed_at=now + 1), False, 'error'),
            (current, archive, old, dict(old, completed_at=0), False, 'error'),
            (current, None, old, old, False, 'error'),
            ({'backup': []}, archive, old, old, False, 'error'),
        ]
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / 'health.json'
            for entry, check, verified, restored, healthy, severity in cases:
                receipt = backup.status(entry, now, check, verified, restored)
                path.write_text(json.dumps(dict(receipt, observed_at=now)))
                with self.subTest(receipt=receipt):
                    fact = watch.physical_backup_fact(path, now)
                    self.assertEqual(fact['healthy'], healthy)
                    self.assertEqual(fact['severity'], severity)

    def test_validation_reminder_is_daily_and_never_masks_a_new_fault(self):
        name = 'tokenkey-postgres-recovery'
        sent = []
        warning = {'healthy': True, 'severity': 'warning', 'reason': 'restore evidence overdue'}
        good = {'healthy': True, 'severity': 'info', 'reason': 'ok'}
        bad = {'healthy': False, 'severity': 'error', 'reason': 'WAL gap'}
        state = {}
        for stamp, fact in [(1000, warning), (1120, warning), (1240, good), (1360, warning)]:
            state = watch.tick_jobs([name], state, stamp, lambda _: fact, sent.append, 'prod')
        self.assertEqual(len(sent), 1)
        self.assertIn('备份验证提醒', sent[0])
        self.assertFalse(state['job:' + name].get('active', False))
        for stamp in (1480, 1600):
            state = watch.tick_jobs([name], state, stamp, lambda _: bad, sent.append, 'prod')
        self.assertEqual(len(sent), 2)
        self.assertIn('unhealthy', sent[-1])
        self.assertIn('WAL gap', sent[-1])
        state = watch.tick_jobs([name], state, 1720, lambda _: warning, sent.append, 'prod')
        self.assertIn('recovered', sent[-1])
        state = watch.tick_jobs([name], state, 1000 + 86400, lambda _: warning, sent.append, 'prod')
        self.assertEqual(sum('备份验证提醒' in msg for msg in sent), 2)
        state = watch.tick_jobs([name], state, 1000 + 86520, lambda _: good, sent.append, 'prod')
        self.assertFalse(state['job:' + name]['warning_active'])

    def test_failed_or_stale_chain_alerts_despite_successful_backup_job(self):
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / 'health.json'
            self.assertFalse(watch.physical_backup_fact(path, 1000)['healthy'])
            path.write_text(json.dumps({'status':'ok','observed_at':990,'reasons':[]}))
            self.assertTrue(watch.physical_backup_fact(path, 1000)['healthy'])
            self.assertFalse(watch.physical_backup_fact(path, 2000)['healthy'])
            path.write_text(json.dumps({'status':'failed','observed_at':990,'reasons':['WAL continuity gap']}))
            fact = watch.physical_backup_fact(path, 1000)
            sent = []
            state = watch.tick_jobs(['tokenkey-postgres-recovery'], {}, 1000, lambda _: fact, sent.append)
            watch.tick_jobs(['tokenkey-postgres-recovery'], state, 1120, lambda _: fact, sent.append)
            self.assertEqual(len(sent), 1)
            self.assertIn('WAL continuity gap', sent[0])

    def test_tencent_archive_check_receipt_is_accepted_without_health_json(self):
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            health = pathlib.Path(root) / 'health.json'
            archive = pathlib.Path(root) / 'archive_check.json'
            archive.write_text(json.dumps({
                'status': 'ok',
                'started_at': 980,
                'completed_at': 990,
                'failed_count': 0,
            }))
            fact = watch.physical_backup_fact(health, 1000)
            self.assertTrue(fact['healthy'])
            self.assertEqual(fact['severity'], 'info')


class HealthBodyTest(unittest.TestCase):
    def test_flat_ssm_bundle_can_load_probe_without_repository_ancestors(self):
        # SSM stages companion files directly in /tmp, unlike a repo checkout
        # or the final content-addressed installation directory.
        source = pathlib.Path(watch.__file__).with_name('https_health.py').read_text()
        namespace = {'__file__': '/tmp/https_health.py', '__name__': 'flat_probe'}
        exec(compile(source, namespace['__file__'], 'exec'), namespace)
        fact = namespace['probe_health']('https://example.invalid/health',
                                        opener=lambda *a, **kw: (_ for _ in ()).throw(TimeoutError()))
        self.assertFalse(fact['healthy'])
        self.assertEqual(fact['http_code'], 0)

    def test_200_interception_pages_and_degraded_json_are_not_healthy(self):
        class Response:
            status = 200
            def __init__(self, body): self.body = body
            def read(self, size): return self.body[:size]
            def __enter__(self): return self
            def __exit__(self, *args): return False
        for body, healthy in [(b'{"status":"ok"}', True), (b'<html>blocked</html>', False),
                              (b'{"status":"draining"}', False), (b'{}', False), (b'[]', False)]:
            with self.subTest(body=body):
                fact = probe_health('https://example.invalid/health', opener=lambda *a, **kw: Response(body))
                self.assertEqual(fact['healthy'], healthy)
                self.assertTrue(fact['reachable'])
                self.assertEqual(fact['http_code'], 200)


class HostHTTPSWatchTest(unittest.TestCase):
    def test_alert_identifies_observer_endpoint_and_bad_health_body(self):
        sent = []
        targets = {'cn-prod': 'https://example.invalid/health'}
        probe = lambda _: {'healthy': False, 'http_code': 200, 'error': 'unexpected health response'}
        state = watch.tick(targets, {}, 1000, probe, sent.append, 'cn-prod')
        state = watch.tick(targets, state, 1120, probe, sent.append, 'cn-prod')
        self.assertEqual(len(sent), 1)
        self.assertIn('探测节点：cn-prod', sent[0])
        self.assertIn(targets['cn-prod'], sent[0])
        self.assertIn('unexpected health response', sent[0])
        self.assertEqual(state['cn-prod']['http_code'], 200)
        self.assertTrue(state['cn-prod']['active'])

    def test_two_failures_then_recovery_and_cooldown(self):
        sent = []
        targets = {"prod": "https://api.tokenkey.dev/health"}
        bad = lambda url: {"healthy": False, "http_code": 503}
        good = lambda url: {"healthy": True, "http_code": 200}
        state = watch.tick(targets, {}, 1000, bad, sent.append)
        self.assertEqual(sent, [])
        state = watch.tick(targets, state, 1120, bad, sent.append)
        self.assertTrue(state["prod"]["active"])
        self.assertEqual(len(sent), 1)
        state = watch.tick(targets, state, 1240, bad, sent.append)
        self.assertEqual(len(sent), 1)
        state = watch.tick(targets, state, 1360, good, sent.append)
        self.assertFalse(state["prod"]["active"])
        self.assertIn("recovered", sent[-1])
        state = watch.tick(targets, state, 1480, good, sent.append)
        self.assertEqual(len(sent), 2)

    def test_delivery_failure_keeps_previous_state_for_retry(self):
        previous = {"prod": {"failures": 1, "observed_at": 1000}}
        original = {k: dict(v) for k, v in previous.items()}
        def fail(message):
            raise RuntimeError("delivery unavailable")
        with self.assertRaisesRegex(RuntimeError, "delivery unavailable"):
            watch.tick({"prod": "https://api.tokenkey.dev/health"}, previous, 1120,
                       lambda url: {"healthy": False, "http_code": 0}, fail)
        self.assertEqual(previous, original)

    def test_stale_failure_is_not_a_consecutive_outage(self):
        result, action = watch.next_state({"failures": 1, "observed_at": 1000}, False, 2000)
        self.assertEqual(result["failures"], 1)
        self.assertEqual(action, "")

    def test_healthy_initial_tick_never_sends(self):
        sent = []
        result = watch.tick({"prod": "https://api.tokenkey.dev/health"}, {}, 1000,
                            lambda url: {"healthy": True, "http_code": 200}, sent.append)
        self.assertEqual(result["prod"]["failures"], 0)
        self.assertEqual(sent, [])


class HostJobWatchTest(unittest.TestCase):
    def test_job_failure_alert_and_recovery_are_deduplicated(self):
        name = "tokenkey-pgdump"
        sent = []
        bad = lambda _: {"healthy": False, "reason": "service failed: exit-code"}
        good = lambda _: {"healthy": True, "reason": "recent completion"}
        state = watch.tick_jobs([name], {}, 1000, bad, sent.append)
        self.assertEqual(sent, [])
        state = watch.tick_jobs([name], state, 1120, bad, sent.append)
        self.assertEqual(len(sent), 1)
        state = watch.tick_jobs([name], state, 1240, bad, sent.append)
        self.assertEqual(len(sent), 1)
        state = watch.tick_jobs([name], state, 1360, good, sent.append)
        self.assertIn("recovered", sent[-1])
        self.assertFalse(state["job:" + name]["active"])

    def test_probe_failure_alert_names_node_and_does_not_hide_other_jobs(self):
        names = ["tokenkey-pgdump", "tokenkey-disk-metrics"]
        def probe(name):
            if name == names[0]:
                raise OSError("synthetic private detail")
            return {"healthy": True, "reason": "recent completion"}
        sent = []
        state = watch.tick_jobs(names, {}, 1000, probe, sent.append, "uk1")
        state = watch.tick_jobs(names, state, 1120, probe, sent.append, "uk1")
        self.assertEqual(len(sent), 1)
        self.assertIn("uk1/tokenkey-pgdump", sent[0])
        self.assertIn("OSError", sent[0])
        self.assertNotIn("private detail", sent[0])
        self.assertEqual(state["job:" + names[1]]["failures"], 0)

    def test_job_starting_during_sampling_is_not_overdue(self):
        # A job starts after probing begins, within one /proc/uptime tick.
        # The clock must be read after systemd and retain sub-centisecond precision.
        clock = [1000.0]
        def show(command, **kwargs):
            clock[0] = 1000.002
            if command[2].endswith(".service"):
                return SimpleNamespace(stdout="ActiveState=activating\nResult=success\nExecMainStartTimestampMonotonic=1000001000\n")
            return SimpleNamespace(stdout="ActiveState=active\nUnitFileState=enabled\n")
        with patch.object(watch.subprocess, "run", side_effect=show), \
             patch.object(watch.time, "monotonic", side_effect=lambda: clock[0]), \
             patch.object(watch.Path, "read_text", return_value="1000.00 0.00"):
            self.assertEqual(watch.probe_job("tokenkey-disk-metrics"),
                             {"healthy": True, "reason": "running"})

    def test_probe_still_rejects_real_execution_timeout(self):
        replies = [SimpleNamespace(stdout="ActiveState=activating\nResult=success\nExecMainStartTimestampMonotonic=1000000000\n"),
                   SimpleNamespace(stdout="ActiveState=active\nUnitFileState=enabled\n")]
        with patch.object(watch.subprocess, "run", side_effect=replies), \
             patch.object(watch.time, "monotonic", return_value=1061.0):
            self.assertEqual(watch.probe_job("tokenkey-disk-metrics"),
                             {"healthy": False, "reason": "execution overdue"})

    def test_failed_stale_disabled_and_running_jobs(self):
        timer = {"ActiveState": "active", "UnitFileState": "enabled"}
        service = {"ActiveState": "inactive", "Result": "success", "ExecMainExitTimestampMonotonic": "1000000000"}
        self.assertTrue(watch.job_fact(service, timer, 1100, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact(service, timer, 1500, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact(service, {**timer, "UnitFileState": "disabled"}, 1100, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact({**service, "Result": "timeout"}, timer, 1100, (300, 60))["healthy"])
        running = {**service, "ActiveState": "activating", "ExecMainStartTimestampMonotonic": "1080000000"}
        self.assertTrue(watch.job_fact(running, timer, 1100, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact(running, timer, 1200, (300, 60))["healthy"])
        self.assertFalse(watch.job_fact({"ActiveState": "inactive", "Result": "success"}, timer, 1100, (300, 60))["healthy"])


if __name__ == "__main__":
    unittest.main()
