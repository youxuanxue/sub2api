"""Cutover boundaries, real file state, preserved locks and rollback ordering."""
from contextlib import ExitStack
from datetime import datetime, timezone
import fcntl
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

from ops.stage0 import cutover_app_luks_exit as cutover


class CutoverLuksExitTest(unittest.TestCase):
    def test_only_exact_approved_window_and_confirmation_can_apply(self):
        cutover.check_window(cutover.WINDOW_START, cutover.CONFIRM)
        cutover.check_window(cutover.WINDOW_START + 119, cutover.CONFIRM)
        for now, confirm in ((cutover.WINDOW_START - 1, cutover.CONFIRM),
                             (cutover.WINDOW_START + 120, cutover.CONFIRM),
                             (cutover.WINDOW_START, ''), (cutover.WINDOW_END, cutover.CONFIRM)):
            with self.subTest(now=now, confirm=confirm), self.assertRaises(ValueError):
                cutover.check_window(now, confirm)

    def test_final_copy_uses_checksum_and_does_not_replace_held_locks(self):
        state = {}
        calls = []
        with patch.object(cutover, 'assert_offline'), patch.object(cutover, 'save'), \
             patch.object(cutover, 'run', side_effect=lambda args, **kw: calls.append(args) or 'Total transferred file size: 12 bytes\n'):
            cutover.sync_offline(Path('/source'), Path('/dest'), state, 'final-synced')
        self.assertEqual(calls[0][-2:], ['/source/', '/dest/'])
        self.assertIn('--checksum', calls[0])
        self.assertIn('--exclude=/bluegreen-deploy.lock', calls[0])
        self.assertIn('--exclude=/qa-lifecycle/host.lock', calls[0])
        self.assertEqual(calls[1], ['sync'])
        self.assertTrue(state['final-synced']['consistent'])

    def test_failed_copy_never_claims_consistency(self):
        state = {}
        with patch.object(cutover, 'assert_offline'), patch.object(cutover, 'save'), \
             patch.object(cutover, 'run', side_effect=RuntimeError('rsync failed')):
            with self.assertRaises(RuntimeError):
                cutover.sync_offline(Path('/source'), Path('/dest'), state, 'final-synced')
        self.assertEqual(state, {})

    def test_apply_requires_fresh_measured_preparation_with_bounded_delta(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(cutover, 'WORK', Path(directory)):
            work = Path(directory)
            for phase in ('precopy', 'delta'):
                row = {'phase': phase, 'returncode': 0, 'finished_at_unix': 1000,
                       'elapsed_seconds': 214, 'consistent': False,
                       'stats': ['Total transferred file size: 644,137,981 bytes']}
                (work / (phase + '.json')).write_text(json.dumps(row))
            cutover.require_recent_copy(1100)
            original = json.loads((work / 'delta.json').read_text())
            for change in ({'returncode': 23}, {'finished_at_unix': -1000}, {'stats': []},
                           {'stats': ['Total transferred file size: 99999999999 bytes']},
                           {'elapsed_seconds': 301}, {'consistent': True}):
                (work / 'delta.json').write_text(json.dumps(dict(original, **change)))
                with self.subTest(change=change), self.assertRaises(ValueError):
                    cutover.require_recent_copy(1100)

    def test_boot_rewrite_preserves_root_swap_and_unrelated_mounts(self):
        original = '# keep comment\nUUID=root / ext4 defaults 0 1\n/swap.img none swap sw 0 0\n'
        original += '/dev/mapper/tokenkey_recovery /var/lib/tokenkey ext4 noauto,nofail 0 2\n'
        original += '/dev/mapper/tokenkey_recovery /srv/tokenkey-rehearsal ext4 noauto,nofail 0 2\n'
        result = cutover.render_fstab(original)
        self.assertEqual(result, '# keep comment\nUUID=root / ext4 defaults 0 1\n/swap.img none swap sw 0 0\n'
                         '/var/lib/tokenkey /srv/tokenkey-rehearsal none bind,x-systemd.requires-mounts-for=/var/lib/tokenkey 0 0\n')
        for text in (original.replace('/dev/mapper/tokenkey_recovery /var', '/dev/vdb /var'),
                     original + '/dev/mapper/tokenkey_recovery /var/lib/tokenkey ext4 defaults 0 0\n',
                     original.replace('/srv/tokenkey-rehearsal', '/other')):
            with self.subTest(text=text), self.assertRaises(ValueError):
                cutover.render_fstab(text)

    def test_historical_receipts_do_not_hide_current_drain_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'runtimes').mkdir()
            old = {'runtime_id': 'old', 'drained': False, 'hours': {'hour': {'failed': 1}}}
            current = {'runtime_id': 'current', 'drained': True,
                       'hours': {'hour': {'pending': 0, 'inflight': 0, 'failed': 0, 'persisted': 99}}}
            (root / 'runtimes/old.json').write_text(json.dumps(old))
            (root / 'runtimes/current.json').write_text(json.dumps(current))
            cutover.require_drained(root, {'current'})
            for changed in (dict(current, drained=False), dict(current, hours={'hour': {'pending': 1}}),
                            dict(current, hours={'hour': {'inflight': 1}})):
                (root / 'runtimes/current.json').write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    cutover.require_drained(root, {'current'})

    def test_held_new_locks_survive_directory_rename(self):
        with tempfile.TemporaryDirectory() as directory:
            staging, root = Path(directory) / 'staging', Path(directory) / 'root'
            (staging / 'qa-lifecycle').mkdir(parents=True)
            for name in cutover.LOCKS:
                (staging / name).touch()
            with ExitStack() as locks:
                cutover.lock_storage(locks, staging)
                staging.rename(root)
                for name in cutover.LOCKS:
                    with (root / name).open('r') as competitor:
                        with self.assertRaises(BlockingIOError):
                            fcntl.flock(competitor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with (root / cutover.LOCKS[0]).open('r') as after:
                fcntl.flock(after, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_drain_is_executed_from_the_shared_owner(self):
        source = Path(__file__).resolve().parent
        script = cutover.drain_script(source, 'gateway-name')
        self.assertIn('sudo docker kill -s USR1 "${container}"', script)
        self.assertIn('[[ "${d}" = true && "${n}" = 0 ]]', script)
        self.assertTrue(script.endswith('drain_container gateway-name'))

    def test_restart_preserves_standby_and_checks_identity_before_starting_any_writer(self):
        rows = [dict(Id='app', Name='/tokenkey-blue', Image='image', Mounts=[], running=False, restart_policy={}),
                dict(Id='proxy', Name='/tokenkey-caddy', Image='caddy', Mounts=[], running=False, restart_policy={}),
                dict(Id='standby', Name='/tokenkey-green', Image='old', Mounts=[], running=False, restart_policy={})]
        state = {'app': 'app', 'pg': 'pg-client', 'redis': 'redis-client',
                 'containers': [dict(r, running=r['Id'] != 'standby') for r in rows]}
        calls = []
        def run(args, **kw):
            calls.append(args)
            if args[:2] == ['docker', 'inspect']:
                return json.dumps([{'State': {'Health': {'Status': 'healthy'}}}])
            if args[:3] == ['docker', 'exec', 'pg-client']:
                return 'f\n'
            if args[:3] == ['docker', 'exec', 'redis-client']:
                return 'PONG\n'
            return ''
        with patch.object(cutover, 'container_rows', return_value=rows), \
             patch.object(cutover, 'retained', side_effect=lambda r: r), patch.object(cutover, 'save'), \
             patch.object(cutover, 'run', side_effect=run):
            cutover.start_retained(state)
        self.assertEqual([c for c in calls if c[:2] == ['docker', 'start']],
                         [['docker', 'start', 'app'], ['docker', 'start', 'proxy']])
        self.assertTrue(state['writers_started'])
        calls.clear()
        with patch.object(cutover, 'container_rows', return_value=[dict(rows[0], Image='wrong'), *rows[1:]]), \
             patch.object(cutover, 'retained', side_effect=lambda r: r), \
             patch.object(cutover, 'save'), \
             patch.object(cutover, 'run', side_effect=run):
            with self.assertRaises(ValueError):
                cutover.start_retained(state)
        self.assertEqual(calls, [['systemctl', 'start', 'docker.service']])

    def test_rollback_after_start_syncs_new_state_before_restoring_old_mounts(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory) / 'work'
            root, compat, old = (Path(directory) / p for p in ('root', 'compat', 'old'))
            for path in (work, root, compat, old):
                path.mkdir()
            (work / 'underlay-root').mkdir()
            (work / 'underlay-compat').mkdir()
            (root / 'ledger').write_text('new request and certificate state')
            (old / 'ledger').write_text('old state must never be restarted')
            state = {'writers_started': True, 'staging_inode': root.stat().st_ino}
            events = []
            def sync(source, destination, state, phase):
                events.append('reverse-sync')
                shutil.copy2(source / 'ledger', destination / 'ledger')
            with patch.multiple(cutover, ROOT=root, COMPAT=compat, OLD=old, WORK=work), \
                 patch.object(cutover, 'save'), patch.object(cutover, 'stop_writers', side_effect=lambda *a, **k: events.append('stop-new')), \
                 patch.object(cutover, 'sync_offline', side_effect=sync), \
                 patch.object(cutover, 'assert_offline'), patch.object(cutover, 'restore_boot', side_effect=lambda *a: events.append('restore-boot')), \
                 patch.object(cutover.runtime, 'check_volume'), patch.object(cutover, 'start_retained', side_effect=lambda *a: events.append('start-old')), \
                 patch.object(cutover, 'run', side_effect=lambda args, **k: events.append(args[0]) or ''):
                cutover.rollback(state, Path(directory))
            self.assertEqual(events, ['stop-new', 'reverse-sync', 'restore-boot', 'mount', 'mount', 'start-old'])
            self.assertEqual((old / 'ledger').read_text(), 'new request and certificate state')
            self.assertEqual((work / 'retained-new-after-rollback/ledger').read_text(), (old / 'ledger').read_text())

    def test_failed_reverse_sync_cannot_start_the_stale_copy(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = {'writers_started': True, 'staging_inode': root.stat().st_ino}
            with patch.object(cutover, 'ROOT', root), patch.object(cutover, 'save'), \
                 patch.object(cutover, 'stop_writers'), \
                 patch.object(cutover, 'sync_offline', side_effect=RuntimeError('checksum failure')), \
                 patch.object(cutover, 'restore_boot') as restore, patch.object(cutover, 'start_retained') as start:
                with self.assertRaises(RuntimeError):
                    cutover.rollback(state, root)
            restore.assert_not_called()
            start.assert_not_called()

    def test_busy_qa_aborts_before_drain_without_restarting_the_live_app(self):
        with tempfile.TemporaryDirectory() as directory:
            work, root = Path(directory) / 'work', Path(directory) / 'root'
            staging, old = work / 'staging', work / 'old-volume'
            staging.mkdir(parents=True)
            (root / 'app/qa_capture_ledger/runtimes').mkdir(parents=True)
            (root / 'live-state').write_text('still serving')
            state = {'identity': {'config_sha256': {}}}
            events = []
            def run(args, **kw):
                events.append(args[0])
                if args[0] == 'mount':
                    (old / cutover.LOCKS[0]).touch()
                return ''
            with patch.multiple(cutover, ROOT=root, WORK=work, STAGING=staging, OLD=old), \
                 patch.object(cutover, 'check_window'), patch.object(cutover, 'plan', return_value=state), \
                 patch.object(cutover, 'require_recent_copy'), \
                 patch.object(cutover.prep, 'configs', return_value={}), \
                 patch.object(cutover, 'backup', side_effect=lambda s: events.append('backup')), \
                 patch.object(cutover, 'pause_jobs', side_effect=RuntimeError('QA still running')), \
                 patch.object(cutover, 'save'), patch.object(cutover, 'rollback') as rollback, \
                 patch.object(cutover, 'stop_writers') as stop, \
                 patch.object(cutover, 'restore_jobs', side_effect=lambda s: events.append('restore-jobs')), \
                 patch.object(cutover, 'run', side_effect=run):
                with self.assertRaises(RuntimeError):
                    cutover.apply(root, cutover.CONFIRM)
            self.assertEqual(events, ['mount', 'backup', 'restore-jobs', 'umount'])
            self.assertEqual((root / 'live-state').read_text(), 'still serving')
            rollback.assert_not_called()
            stop.assert_not_called()

    def test_acceptance_requires_real_metering_and_capture_after_ingress(self):
        state = {'pg': 'client', 'ingress_restored_unix': 1000, 'unresolved_before': ['historical'],
                 'new_runtime_id': 'new', 'timers': {'qa': {'ActiveState': 'active'}}}
        row = {'drained': False, 'last_snapshot_at': datetime.now(timezone.utc).isoformat(),
               'hours': {'hour': {'persisted': 2, 'failed': 0}}}
        with patch.object(cutover, 'unresolved', return_value=['historical']), \
             patch.object(cutover, 'ledger', return_value={'new': row}), \
             patch.object(cutover, 'properties', return_value={'ActiveState': 'active'}), \
             patch.object(cutover, 'run', return_value='3\n') as run:
            self.assertEqual(cutover.acceptance_facts(state), {'metered_requests_after_ingress': 3,
                             'new_runtime_persisted': 2, 'original_timers_restored': True})
            self.assertIn('to_timestamp(1000.0)', run.call_args.args[0][-1])
        events = []
        facts = {'metered_requests_after_ingress': 0, 'new_runtime_persisted': 2, 'original_timers_restored': True}
        with patch.object(cutover, 'acceptance_facts', side_effect=[facts, dict(facts, metered_requests_after_ingress=3)]), \
             patch.object(cutover.time, 'sleep'), patch.object(cutover, 'save', side_effect=lambda s, p: events.append(p)):
            cutover.accept(state)
        self.assertEqual(events, ['accepted'])
        with patch.object(cutover, 'acceptance_facts', return_value=facts), \
             patch.object(cutover.time, 'monotonic', side_effect=[0, 301]), \
             patch.object(cutover, 'save', side_effect=lambda s, p: events.append(p)):
            with self.assertRaises(RuntimeError):
                cutover.accept(state)
        self.assertEqual(events[-1], 'acceptance-unproven')

    def test_disk_alert_guard_uses_root_contract_and_rollback_restores_old_mount_check(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            system, work, bundle = (base / p for p in ('system', 'work', 'bundle'))
            for path in (system, work, bundle):
                path.mkdir()
            fstab, crypttab, helper = (base / p for p in ('fstab', 'crypttab', 'runtime.py'))
            original = 'UUID=root / ext4 defaults 0 1\n'
            for path in (cutover.ROOT, cutover.COMPAT):
                original += f'/dev/mapper/tokenkey_recovery {path} ext4 noauto,nofail 0 2\n'
            fstab.write_text(original)
            crypttab.write_text('tokenkey_recovery /old/luks none luks,noauto,nofail\n')
            helper.write_text('old helper')
            (bundle / 'bluegreen_host_runtime.py').write_text('new helper')
            files = [fstab, crypttab]
            for unit in ('docker', 'tokenkey'):
                parent = system / (unit + '.service.d')
                parent.mkdir()
                guard = parent / 'tokenkey-volume.conf'
                guard.write_text('old LUKS guard')
                files.append(guard)
            for name in ('20-tokenkey-encrypted-storage.conf', '40-tokenkey-luks-unlock.conf', '30-tokenkey-recovery-egress.conf'):
                path = system / 'docker.service.d' / name
                path.write_text('retain ' + name)
                files.append(path)
            (system / 'tokenkey-disk-metrics.service').write_text('Environment=TOKENKEY_DATA_VOLUME_PATH=/var/lib/tokenkey\n')
            disk_guard = system / 'tokenkey-disk-metrics.service.d/90-tokenkey-root-storage.conf'
            state = {'unlock': {'UnitFileState': 'enabled'}}
            def properties(unit, *keys):
                if unit == 'tokenkey-disk-metrics.service':
                    return {'Environment': 'TOKENKEY_DATA_VOLUME_PATH='}
                return {'Requires': 'sysinit.target', 'After': 'network.target', 'Wants': ''}
            with patch.multiple(cutover, SYSTEM=system, WORK=work, HELPER=helper, DISK_GUARD=disk_guard,
                                FSTAB=fstab, CRYPTTAB=crypttab), \
                 patch.object(cutover.prep, 'configs', return_value={str(p): 'hash' for p in files}), \
                 patch.object(cutover, 'save'), patch.object(cutover, 'run', return_value=''), \
                 patch.object(cutover, 'properties', side_effect=properties), patch.object(cutover.runtime, 'check_volume'):
                cutover.backup(state)
                cutover.install_guards(state, bundle)
                app_guard = (system / 'docker.service.d/tokenkey-volume.conf').read_text()
                self.assertEqual(disk_guard.read_text(), app_guard + 'Environment=TOKENKEY_DATA_VOLUME_PATH=\n')
                self.assertEqual(helper.read_text(), 'new helper')
                self.assertEqual((system / 'docker.service.d/30-tokenkey-recovery-egress.conf').read_text(),
                                 'retain 30-tokenkey-recovery-egress.conf')
                cutover.restore_boot(state)
            self.assertFalse(disk_guard.exists())
            self.assertEqual(helper.read_text(), 'old helper')
            self.assertEqual(fstab.read_text(), original)
            self.assertEqual((system / 'docker.service.d/tokenkey-volume.conf').read_text(), 'old LUKS guard')


if __name__ == '__main__':
    unittest.main()
