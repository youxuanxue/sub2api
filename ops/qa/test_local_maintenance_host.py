import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('local_host', Path(__file__).with_name('local_maintenance_host.py'))
local = importlib.util.module_from_spec(spec)
spec.loader.exec_module(local)


class LocalHostTest(unittest.TestCase):
    def test_database_environment_excludes_cloud_and_application_secrets(self):
        result = local.pg_environment({'Config': {'Env': ['POSTGRES_PASSWORD=test-only', 'AWS_SECRET_ACCESS_KEY=not-for-job', 'JWT_SECRET=not-for-job']}})
        self.assertIn('PGPASSWORD=test-only\n', result)
        self.assertIn('PGHOST=tokenkey-postgres\n', result)
        self.assertNotIn('not-for-job', result)
        for env in ([], ['POSTGRES_PASSWORD=bad\nPGHOST=remote']):
            with self.assertRaises(ValueError):
                local.pg_environment({'Config': {'Env': env}})

    def test_pg_environment_falls_back_to_active_app_database_password(self):
        app = {'Config': {'Env': [
            'DATABASE_PASSWORD=from-app',
            'DATABASE_USER=tokenkey',
            'DATABASE_DBNAME=tokenkey',
            'AWS_SECRET_ACCESS_KEY=not-for-job',
        ]}}
        with mock.patch.object(local, 'invoke', return_value=local.json.dumps([app])):
            result = local.pg_environment({'Config': {'Env': []}})
        self.assertIn('PGPASSWORD=from-app\n', result)
        self.assertIn('PGHOST=tokenkey-postgres\n', result)
        self.assertNotIn('not-for-job', result)

    def test_pg_environment_rejects_when_postgres_and_app_passwords_missing(self):
        with mock.patch.object(local, 'invoke', side_effect=local.subprocess.CalledProcessError(1, ['docker', 'inspect'])):
            with self.assertRaisesRegex(ValueError, 'PostgreSQL password is unavailable'):
                local.pg_environment({'Config': {'Env': []}})

    def test_active_legacy_owner_blocks_local_work(self):
        with mock.patch.object(local, 'invoke', side_effect=['ActiveState=active\nUnitFileState=enabled\n', 'inactive']):
            with self.assertRaisesRegex(ValueError, 'legacy QA owner'):
                local.require_legacy_paused()
        with mock.patch.object(local, 'invoke', side_effect=['ActiveState=inactive\nUnitFileState=disabled\n', 'failed']*2):
            local.require_legacy_paused()

    def test_wrong_binary_digest_blocks_before_docker_or_database(self):
        with tempfile.TemporaryDirectory() as root:
            binary = Path(root)/'job'
            binary.write_bytes(b'changed-artifact')
            config = {'phases':['provision'],'hostname':'test-host','binary':str(binary),'sha256':'0'*64}
            with mock.patch.object(local.socket, 'gethostname', return_value='test-host'), mock.patch.object(local, 'invoke') as invoke:
                with self.assertRaisesRegex(ValueError, 'digest mismatch'):
                    local.run_phase(config, 'provision')
                invoke.assert_not_called()

    def test_receipt_is_atomically_replaced_on_update(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)/'receipt.json'
            local.atomic_json(path, {'ok':False})
            local.atomic_json(path, {'ok':True,'completed':42})
            self.assertEqual(local.json.loads(path.read_text()), {'ok':True,'completed':42})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(list(Path(root).iterdir()), [path])

    def test_failed_service_install_restores_prior_units_and_timer(self):
        with tempfile.TemporaryDirectory() as root:
            units = Path(root)/'units'
            units.mkdir()
            service = units/'tokenkey-qa-provision.service'
            timer = units/'tokenkey-qa-provision.timer'
            service.write_text('old-service')
            timer.write_text('old-timer')
            calls = []
            failed = False
            def invoke(args, **kwargs):
                nonlocal failed
                calls.append(args)
                if args[:2] == ['systemctl','show']:
                    return 'ActiveState=active\nUnitFileState=enabled\n' if args[2].endswith('.timer') else 'inactive'
                if args == ['systemctl','start',service.name] and not failed:
                    failed = True
                    raise RuntimeError('first run failed')
                return ''
            real_path = local.Path
            def mapped_path(value):
                return real_path(root)/'backups' if value == '/var/backups/tokenkey-qa-local' else real_path(value)
            with mock.patch.object(local, 'UNITS', units), mock.patch.object(local, 'invoke', side_effect=invoke), mock.patch.object(local, 'Path', side_effect=mapped_path):
                with self.assertRaisesRegex(RuntimeError, 'first run failed'):
                    local.install_units(Path('/bundle'), ['provision'])
            self.assertEqual(service.read_text(), 'old-service')
            self.assertEqual(timer.read_text(), 'old-timer')
            self.assertIn(['systemctl','enable',timer.name], calls)
            self.assertEqual(calls[-1], ['systemctl','start',timer.name])

    def test_cleanup_mounts_reject_symlinks_and_expose_only_qa_directories(self):
        with tempfile.TemporaryDirectory() as root:
            data=Path(root).resolve()
            (data/'qa_blobs').mkdir()
            (data/'qa_dlq').mkdir()
            with mock.patch.object(local,'HOT_DATA',data):
                mounts=local.cleanup_mounts()
                self.assertEqual(mounts,['--mount',f'type=bind,src={data}/qa_blobs,dst=/app/data/qa_blobs',
                                         '--mount',f'type=bind,src={data}/qa_dlq,dst=/app/data/qa_dlq'])
                (data/'qa_dlq').rmdir()
                (data/'qa_dlq').symlink_to(data/'qa_blobs')
                with self.assertRaisesRegex(ValueError,'canonical real QA directory'):
                    local.cleanup_mounts()

    def test_cleanup_mounts_resolves_symlinked_hot_data_root(self):
        with tempfile.TemporaryDirectory() as root:
            real=(Path(root)/'formal'/'app').resolve()
            real.mkdir(parents=True)
            (real/'qa_blobs').mkdir()
            (real/'qa_dlq').mkdir()
            link=Path(root)/'app'
            link.symlink_to(real)
            with mock.patch.object(local,'HOT_DATA',link):
                mounts=local.cleanup_mounts()
            self.assertEqual(mounts,['--mount',f'type=bind,src={real}/qa_blobs,dst=/app/data/qa_blobs',
                                     '--mount',f'type=bind,src={real}/qa_dlq,dst=/app/data/qa_dlq'])

    def test_resolve_postgres_runtime_prefers_tokenkey_postgres_alias_network(self):
        pg={'State':{'Running':True},'Image':'sha256:fixture',
            'NetworkSettings':{'Networks':{
                'tk-cutover-prod':{'Aliases':['tk-archive-standby','postgres']},
                'tokenkey_tokenkey-network':{'Aliases':['postgres','tokenkey-postgres']},
            }},
            'Config':{'Env':[]}}
        with mock.patch.object(local,'invoke',return_value=local.json.dumps([pg])):
            container, network = local.resolve_postgres_runtime()
        self.assertEqual(container['NetworkSettings']['Networks'], pg['NetworkSettings']['Networks'])
        self.assertEqual(network, 'tokenkey_tokenkey-network')

    def test_failed_cleanup_preserves_partial_result_and_dry_run_mounts_are_readonly(self):
        from types import SimpleNamespace
        real_temporary=local.tempfile.TemporaryDirectory
        with real_temporary() as root:
            data=Path(root).resolve()
            binary=data/'job'
            binary.write_bytes(b'fixture')
            for name in ('qa_blobs','qa_dlq'):(data/name).mkdir()
            config={'phases':['cleanup'],'hostname':'fixture','binary':str(binary),
                    'sha256':local.hashlib.sha256(binary.read_bytes()).hexdigest()}
            child={'phase':'cleanup','ok':False,'upload_authorized':False,'deletion_authorized':False,
                   'cleanup':{'expiries':[{'hot_files_cleaned':False}],'dry_run':True}}
            pg={'State':{'Running':True},'Image':'sha256:fixture','NetworkSettings':{'Networks':{'qa':{}}},
                'Config':{'Env':['POSTGRES_PASSWORD=fixture']}}
            with mock.patch.object(local.socket,'gethostname',return_value='fixture'), \
                 mock.patch.object(local,'require_legacy_paused'), \
                 mock.patch.object(local,'STATE',data/'state'), \
                 mock.patch.object(local,'HOST_LOCK',data/'lock'), \
                 mock.patch.object(local,'HOT_DATA',data), \
                 mock.patch.object(local,'invoke',return_value=local.json.dumps([pg])), \
                 mock.patch.object(local.tempfile,'TemporaryDirectory',side_effect=lambda **kw:real_temporary(dir=data)), \
                 mock.patch.object(local.subprocess,'run',return_value=SimpleNamespace(returncode=1,stdout=local.json.dumps(child))) as process:
                with self.assertRaisesRegex(ValueError,'invalid local phase receipt'):
                    local.run_phase(config,'cleanup',dry_run=True)
            receipt=local.json.loads((data/'state/cleanup-plan.json').read_text())
            self.assertFalse(receipt['ok'])
            self.assertEqual(receipt['result']['cleanup']['expiries'],[{'hot_files_cleaned':False}])
            self.assertFalse((data/'state/cleanup.json').exists())
            command=process.call_args.args[0]
            self.assertIn('--dry-run',command)
            for mount in (arg for arg in command if 'dst=/app/data/' in arg):
                self.assertTrue(mount.endswith(',readonly'))

    def test_provision_service_runs_only_provision_and_timer_survives_reboot(self):
        files=local.unit_files(Path('/immutable/bundle'), 'provision')
        command=next(line for line in files['tokenkey-qa-provision.service'].splitlines() if line.startswith('ExecStart='))
        self.assertTrue(command.endswith('--phase provision'))
        timer=files['tokenkey-qa-provision.timer']
        self.assertIn('Persistent=true',timer)
        self.assertIn('OnBootSec=2min',timer)
        with self.assertRaises(ValueError):
            local.unit_files(Path('/immutable/bundle'), 'archive')


if __name__=='__main__':
    unittest.main()
