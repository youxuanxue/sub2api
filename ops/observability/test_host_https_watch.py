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
        self.prod_matrix = json.loads((root / 'deploy/tencent/candidate-usw-20261022.json').read_text())
        self.watch = self.prod_matrix['host_watch']
        self.domestic = json.loads((root / 'deploy/domestic/prod-target.json').read_text())

    def config(self, role, hostname):
        return watch.build_config(role, hostname, self.prod_matrix, self.domestic)

    def test_both_observers_cover_cn_ip_and_all_customer_domains(self):
        for role, host in [('cn-prod', self.domestic['instance_name'])]:
            with self.subTest(role=role):
                config = watch.build_config(role, host, self.prod_matrix, self.domestic)
                targets = config['targets']
                self.assertEqual(targets['cn-prod'], self.domestic['public_origin'] + '/health')
                self.assertEqual(targets['prod:cn-domain'], 'https://' + self.domestic['fallback_domain'] + '/health')
                for domain in self.watch['domains']:
                    self.assertIn('https://' + domain + '/health', targets.values())
        cn = self.config('cn-prod', self.domestic['instance_name'])
        # cn-prod runs the same local QA jobs as prod, carried by its own
        # domestic config rather than a single hardcoded entry that predated
        # tokenkey-qa-archive.
        self.assertEqual(set(cn['jobs']),
                         {'tokenkey-disk-metrics', 'tokenkey-qa-provision',
                          'tokenkey-qa-cleanup', 'tokenkey-qa-archive'})

    def test_refuses_wrong_host_and_retains_prod_monitor_peer(self):
        with self.assertRaisesRegex(ValueError, 'host identity'):
            self.config('cn-prod', 'some-other-host')
        prod = self.config('prod', self.watch['hostname'])
        # 2026-10-17 prod-only: no live edge peer → watch cn-prod.
        self.assertEqual(prod['targets'], {'cn-prod': self.domestic['public_origin'].rstrip('/') + '/health'})
        self.assertIn('tokenkey-qa-maintenance', prod['jobs'])
        # The archive push (tar stream to node1) must be monitored on both
        # hosts; it went unmonitored in production until 2026-11-03.
        self.assertIn('tokenkey-qa-archive', prod['jobs'])
        self.assertIn('tokenkey-qa-archive',
                      self.config('cn-prod', self.domestic['instance_name'])['jobs'])
        # Retired Tokyo identity is not an accepted prod watch host.
        with self.assertRaisesRegex(ValueError, 'host identity'):
            self.config('prod', 'tokenkey-prod-tokyo-20261017')

    def test_live_matrix_owns_watch(self):
        self.assertEqual(self.watch['hostname'], self.prod_matrix['target']['hostname'])
        self.assertIn(self.prod_matrix['target']['api_domain'], self.watch['domains'])
        cn = self.config('cn-prod', self.domestic['instance_name'])
        self.assertEqual(cn['targets']['prod'], 'https://' + self.prod_matrix['target']['api_domain'] + '/health')
        # Phase2 live health reads the same pause owner.
        sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))
        from ops.qa.prod_phase2_live_health import load_maintenance_pause_reason
        self.assertEqual(load_maintenance_pause_reason(), self.watch['paused_jobs']['tokenkey-qa-maintenance'])

    def test_prod_pause_does_not_silence_other_observers_or_jobs(self):
        prod = self.config('prod', self.watch['hostname'])
        self.assertEqual(prod['paused_jobs'], self.watch['paused_jobs'])
        self.assertEqual(
            set(prod['paused_jobs']),
            {
                'tokenkey-qa-maintenance',
                'tokenkey-qa-boundary',
                'tokenkey-pgdump',
                'tokenkey-ghcr-prune-daily',
            },
        )
        self.assertIn('tokenkey-disk-metrics', prod['jobs'])
        self.assertNotIn('tokenkey-disk-metrics', prod['paused_jobs'])
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
        # Provision and cleanup are expected-running local jobs, never paused.
        prod = self.config('prod', self.watch['hostname'])
        for job in ('tokenkey-qa-provision', 'tokenkey-qa-cleanup'):
            with self.subTest(job=job):
                self.assertIn(job, prod['jobs'])
                self.assertNotIn(job, prod['paused_jobs'])
                self.assertIn(job, watch.JOB_LIMITS)
        limit = watch.JOB_LIMITS['tokenkey-qa-cleanup']
        self.assertFalse(watch.job_fact({'ActiveState':'inactive', 'Result':'success'},
                                       {'ActiveState':'inactive', 'UnitFileState':'disabled'},
                                       10000, limit)['healthy'])

    def test_missing_provision_timer_alerts(self):
        # 2026-10-22: provision was not installed on the Tencent host (systemctl show
        # returns empty UnitFileState). Unlike paused jobs, absence must alert.
        absent_timer = {'ActiveState': 'inactive', 'UnitFileState': ''}
        absent_service = {'ActiveState': 'inactive', 'Result': 'success'}
        limit = watch.JOB_LIMITS['tokenkey-qa-provision']
        fact = watch.job_fact(absent_service, absent_timer, 10000, limit)
        self.assertFalse(fact['healthy'])
        sent = []
        state = watch.tick_jobs(['tokenkey-qa-provision'], {}, 1000, lambda _: fact, sent.append, 'prod')
        watch.tick_jobs(['tokenkey-qa-provision'], state, 1120, lambda _: fact, sent.append, 'prod')
        self.assertEqual(len(sent), 1)
        self.assertIn('prod/tokenkey-qa-provision', sent[0])

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

    def test_bootstrapping_repository_is_advisory_not_a_fault(self):
        # A newly configured repo2 reports "no valid backups" until its first
        # full lands. That is a guaranteed initialisation state, so it annotates
        # rather than paging -- but it is still degraded, never silently ok.
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / 'health.json'
            path.write_text(json.dumps({
                'status': 'degraded',
                'observed_at': 990,
                'reasons': ['repository_bootstrapping'],
                'repository_bootstrapping': {
                    'status': 'ok', 'completed_at': 990, 'repositories': [2],
                },
            }))
            fact = watch.physical_backup_fact(path, 1000)
            self.assertTrue(fact['healthy'])
            self.assertEqual(fact['severity'], 'warning')
            self.assertIn('repo2', fact['reason'])
            self.assertIn('初始化', fact['reason'])

    def test_bootstrap_reason_without_its_evidence_stays_a_fault(self):
        # The advisory band must not be reachable by naming a reason alone.
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / 'health.json'
            path.write_text(json.dumps({
                'status': 'degraded',
                'observed_at': 990,
                'reasons': ['repository_bootstrapping'],
            }))
            fact = watch.physical_backup_fact(path, 1000)
            self.assertFalse(fact['healthy'])
            self.assertEqual(fact['severity'], 'error')

    def test_wal_archive_failing_is_a_hard_fault_with_its_reason_visible(self):
        # A failing archive_command must stay an error and must say so. The
        # watcher surfaces the reason verbatim, which is what tells the operator
        # it is WAL archiving rather than a repository problem.
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / 'health.json'
            path.write_text(json.dumps({
                'status': 'degraded',
                'observed_at': 990,
                'reasons': ['wal_archive_failing'],
                'wal_archive_failing': {
                    'last_archived_wal': '000000010000001600000030',
                    'last_failed_wal': '000000010000001600000031',
                },
            }))
            fact = watch.physical_backup_fact(path, 1000)
            self.assertFalse(fact['healthy'])
            self.assertEqual(fact['severity'], 'error')
            self.assertIn('wal_archive_failing', fact['reason'])

    def test_check_command_failure_is_a_hard_fault_not_an_advisory(self):
        # The producer writes this receipt when `pgbackrest check` itself blew
        # up. It must NOT reach the advisory band that bootstrapping uses: a
        # broken check is actionable now, and the detail has to survive so the
        # operator sees why without opening a shell.
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / 'health.json'
            path.write_text(json.dumps({
                'status': 'degraded',
                'observed_at': 990,
                'reasons': ['check_command_failed'],
                'check_command_failed': {
                    'detail': "ERROR: [055] | HINT: has a stanza-create been performed?",
                },
            }))
            fact = watch.physical_backup_fact(path, 1000)
            self.assertFalse(fact['healthy'])
            self.assertEqual(fact['severity'], 'error')
            self.assertIn('check_command_failed', fact['reason'])

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


class QaCleanupBacklogTest(unittest.TestCase):
    """Cleanup now reclaims verified hours and holds back only unarchived ones,
    so the unit stays green while the archive falls behind. node1 was down
    2026-11-04..07 with no receipt-age alert; this is that alert."""

    def write(self, cleanup):
        import tempfile
        d = pathlib.Path(tempfile.mkdtemp())
        p = d / "cleanup.json"
        p.write_text(json.dumps({"ok": True, "result": {"cleanup": cleanup}}))
        return p

    def test_old_blocked_hour_is_unhealthy(self):
        p = self.write({"oldest_receipt_blocked_hour_utc": "2026-11-04T16:00:00Z", "receipt_blocked_hours": 2})
        now = watch.datetime.fromisoformat("2026-11-05T17:00:00+00:00").timestamp()
        fact = watch.qa_cleanup_backlog_fact(p, now)
        self.assertFalse(fact["healthy"])
        self.assertIn("2026-11-04T16:00:00Z", fact["reason"])
        self.assertIn("2 hours held back", fact["reason"])

    def test_recent_blocked_hour_is_not_an_alert(self):
        p = self.write({"oldest_receipt_blocked_hour_utc": "2026-11-05T10:00:00Z", "receipt_blocked_hours": 1})
        now = watch.datetime.fromisoformat("2026-11-05T17:00:00+00:00").timestamp()
        self.assertIsNone(watch.qa_cleanup_backlog_fact(p, now))

    def test_no_blocked_hour_or_missing_receipt_is_not_an_alert(self):
        self.assertIsNone(watch.qa_cleanup_backlog_fact(self.write({"receipt_blocked_hours": 0}), 0))
        self.assertIsNone(watch.qa_cleanup_backlog_fact(pathlib.Path("/nonexistent/cleanup.json"), 0))

    def test_unparseable_hour_is_reported(self):
        p = self.write({"oldest_receipt_blocked_hour_utc": "not-a-time"})
        self.assertFalse(watch.qa_cleanup_backlog_fact(p, 0)["healthy"])

    def test_receipt_path_matches_the_host_runner(self):
        root = pathlib.Path(__file__).resolve().parents[2]
        runner = (root / "ops/qa/local_maintenance_host.py").read_text()
        self.assertIn("STATE = Path('/var/lib/tokenkey/qa-local')", runner)
        self.assertIn("STATE/(phase+('-plan' if dry_run else '')+'.json')", runner)
        self.assertEqual(watch.QA_CLEANUP_RECEIPT, pathlib.Path("/var/lib/tokenkey/qa-local/cleanup.json"))


class QaArchiveCoverageTest(unittest.TestCase):
    """The archive push (tar stream to node1) went unmonitored in production
    until 2026-11-03: prod's qa-cleanup paged every 5 minutes for a correct
    gate decision while the archive itself, which was re-pushing the hours
    the gate was waiting on, had no alert path at all on either host.
    """

    def setUp(self):
        root = pathlib.Path(__file__).resolve().parents[2]
        self.prod_matrix = json.loads((root / 'deploy/tencent/candidate-usw-20261022.json').read_text())
        self.domestic = json.loads((root / 'deploy/domestic/prod-target.json').read_text())

    def test_archive_is_registered_with_a_cadence_matching_its_timer(self):
        # ops/qa/local_maintenance_host.py schedules the archive every 4
        # hours; max age must tolerate at least one missed run without
        # alerting on the next, and max runtime must not be tighter than the
        # unit's own TimeoutStartSec=1800.
        self.assertIn('tokenkey-qa-archive', watch.JOB_LIMITS)
        max_age, max_runtime = watch.JOB_LIMITS['tokenkey-qa-archive']
        self.assertGreaterEqual(max_age, 2 * 4 * 3600)
        self.assertGreaterEqual(max_runtime, 1800)

    def test_both_hosts_configure_the_archive_job(self):
        prod_jobs = watch.build_config('prod', self.prod_matrix['host_watch']['hostname'],
                                       self.prod_matrix, self.domestic)['jobs']
        cn_jobs = watch.build_config('cn-prod', self.domestic['instance_name'],
                                     self.prod_matrix, self.domestic)['jobs']
        self.assertIn('tokenkey-qa-archive', prod_jobs)
        self.assertIn('tokenkey-qa-archive', cn_jobs)

    def test_cn_prod_jobs_come_from_its_own_config_not_a_hardcoded_list(self):
        # Hardcoding cn-prod to exactly one job meant adding a second job
        # anywhere required a code change here instead of a config edit.
        source = (pathlib.Path(__file__).resolve().parent / 'host_https_watch.py').read_text()
        build = source[source.index('def build_config'):source.index('\ndef ', source.index('def build_config') + 1)]
        self.assertIn('domestic.get("local_qa_jobs"', build)

    def test_unknown_job_in_domestic_config_is_rejected_by_main(self):
        # build_config itself does not validate against JOB_LIMITS -- main()
        # does, right before probing. Exercise that same check so a typo in
        # local_qa_jobs is caught rather than silently probed as "unhealthy:
        # unit not found" forever.
        bad = dict(self.domestic, local_qa_jobs=['tokenkey-not-a-real-job'])
        config = watch.build_config('cn-prod', bad['instance_name'], self.prod_matrix, bad)
        jobs = config['jobs']
        self.assertTrue(any(name not in watch.JOB_LIMITS for name in jobs))


class CnProdPostgresCoverageTest(unittest.TestCase):
    """cn-prod's own pgBackRest backup/archive-check/recovery ran unmonitored
    even with archive_check.json present, because the auto-discovery in
    main() gated on node == "prod" only. Found 2026-11-03.
    """

    def test_cn_prod_units_carry_their_own_cn_prefixed_names(self):
        # The units really are named tokenkey-cn-postgres-backup on cn-prod,
        # confirmed live 2026-11-03 -- not tokenkey-postgres-backup, which
        # would silently probe a unit that does not exist there.
        self.assertIn('tokenkey-cn-postgres-backup', watch.JOB_LIMITS)
        self.assertIn('tokenkey-cn-postgres-archive-check', watch.JOB_LIMITS)

    def test_recovery_evidence_file_is_shared_not_duplicated(self):
        # tokenkey-postgres-recovery reads a local file, not a systemd unit,
        # so cn-prod uses the same job name as prod rather than a cn- copy
        # that would need its own JOB_LIMITS entry for no reason.
        self.assertNotIn('tokenkey-cn-postgres-recovery', watch.JOB_LIMITS)

    def test_auto_discovery_covers_cn_prod_when_archive_check_exists(self):
        source = (pathlib.Path(__file__).resolve().parent / 'host_https_watch.py').read_text()
        main_body = source[source.index('def main('):]
        self.assertIn('node_role == "cn-prod"', main_body)
        self.assertIn('tokenkey-cn-postgres-backup', main_body)
        self.assertIn('tokenkey-postgres-recovery', main_body)
        # And it must still be gated on the evidence file existing, not
        # unconditional for every host.
        self.assertIn('archive_check.json', main_body)
