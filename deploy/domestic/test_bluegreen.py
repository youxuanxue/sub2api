"""Domestic host adapter checks; shared cutover/drain behavior has Stage0 tests."""
from pathlib import Path
import json
import os
import shlex
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from deploy.domestic import bluegreen as bg


def fixture():
    return {'Id': 'container-id', 'Image': 'sha256:old', 'RestartCount': 0,
            'State': {'StartedAt': 'start', 'Health': {'Status': 'healthy'}},
            'Config': {'Env': ['SERVER_FRONTEND_URL=https://api-cn.tokenkey.dev',
                               'QA_CAPTURE_ENABLED=false', 'JWT_SECRET=literal${value}',
                               'GOMEMLIMIT=1GiB', 'DATABASE_HOST=postgres']},
            'HostConfig': {'RestartPolicy': {'Name': 'unless-stopped'}},
            'Mounts': [{'Type': 'bind', 'Source': '/var/lib/tokenkey/app',
                        'Destination': '/app/data', 'RW': True}],
            'NetworkSettings': {'Networks': {'tokenkey_tokenkey-network': {}}}}


class DomesticBluegreenTest(unittest.TestCase):
    def _pool_env(self, **overrides):
        values = {
            'GOMEMLIMIT': '1GiB',
            'DATABASE_MAX_OPEN_CONNS': '40',
            'DATABASE_MAX_IDLE_CONNS': '10',
            'REDIS_POOL_SIZE': '96',
            'REDIS_MIN_IDLE_CONNS': '8',
        }
        values.update(overrides)
        return values

    def _compose_env(self, **overrides):
        values = self._pool_env()
        values.update({
            'QA_CAPTURE_ENABLED': 'true',
            'TELEMETRY_ARCHIVE_ENABLED': 'false',
            'SECURITY_PROXY_PROBE_PRESET': 'domestic',
        })
        values.update(overrides)
        return values

    def test_candidate_preserves_runtime_and_reuses_only_private_network(self):
        current = fixture()
        service = {'image': 'old',
                   'environment': {'JWT_SECRET': 'stale', **self._compose_env()},
                   'depends_on': {'postgres': {}}, 'ports': ['18080:8080'],
                   'networks': {'old': {}}, 'volumes': [{'source': '/var/lib/tokenkey/app',
                                                        'target': '/app/data', 'type': 'bind'}]}
        actual = bg.candidate_compose(service, current, 'sha256:new', 'tokenkey-blue')
        app = actual['services']['tokenkey-blue']
        self.assertEqual(app['environment']['JWT_SECRET'], 'literal$${value}')
        self.assertEqual(app['environment']['QA_CAPTURE_ENABLED'], 'true')
        self.assertEqual(app['environment']['TELEMETRY_ARCHIVE_ENABLED'], 'false')
        self.assertEqual(app['environment']['GOMEMLIMIT'], '1GiB')
        self.assertEqual(app['volumes'], service['volumes'])
        self.assertEqual(app['image'], 'sha256:new')
        self.assertEqual(app['pull_policy'], 'never')
        self.assertEqual(app['ports'][0]['host_ip'], '127.0.0.1')
        self.assertEqual(app['ports'][0]['published'], '18081')
        self.assertNotIn('depends_on', app)
        self.assertEqual(actual['networks']['existing'], {
            'external': True, 'name': 'tokenkey_tokenkey-network'})
        self.assertEqual(set(actual['services']), {'tokenkey-blue'})
        self.assertEqual(service['image'], 'old')

    def test_candidate_overlays_pool_env_from_compose_not_secrets(self):
        current = fixture()
        volumes = [{'source': '/var/lib/tokenkey/app', 'target': '/app/data', 'type': 'bind'}]
        service = {
            'volumes': volumes,
            'environment': {
                **self._compose_env(GOMEMLIMIT='2GiB'),
                'JWT_SECRET': 'stale-must-not-win',
            },
        }
        app = bg.candidate_compose(service, current, 'sha256:new', 'tokenkey-green')[
            'services']['tokenkey-green']
        self.assertEqual(app['environment']['GOMEMLIMIT'], '2GiB')
        self.assertEqual(app['environment']['DATABASE_MAX_OPEN_CONNS'], '40')
        self.assertEqual(app['environment']['DATABASE_MAX_IDLE_CONNS'], '10')
        self.assertEqual(app['environment']['REDIS_POOL_SIZE'], '96')
        self.assertEqual(app['environment']['REDIS_MIN_IDLE_CONNS'], '8')
        self.assertEqual(app['environment']['JWT_SECRET'], 'literal$${value}')
        self.assertEqual(app['environment']['DATABASE_HOST'], 'postgres')
        self.assertEqual(app['ports'][0]['published'], '18082')

    def test_candidate_rejects_missing_or_blank_pool_env_keys(self):
        volumes = [{'source': '/var/lib/tokenkey/app', 'target': '/app/data', 'type': 'bind'}]
        for broken in (
            {},
            self._pool_env(GOMEMLIMIT=''),
            {k: v for k, v in self._pool_env().items() if k != 'REDIS_POOL_SIZE'},
        ):
            with self.subTest(environment=broken), self.assertRaisesRegex(
                    ValueError, 'missing pool env keys'):
                bg.candidate_compose({'volumes': volumes, 'environment': broken},
                                     fixture(), 'sha256:new', 'tokenkey-blue')

    def test_candidate_rejects_missing_or_blank_config_env_keys(self):
        # Fail closed like pool/qa: a candidate must not silently start without
        # a setting compose.override.yml owns (SECURITY_PROXY_PROBE_PRESET was
        # served unset for every promote before 2026-10-23).
        volumes = [{'source': '/var/lib/tokenkey/app', 'target': '/app/data', 'type': 'bind'}]
        for broken in (
            self._compose_env(SECURITY_PROXY_PROBE_PRESET=''),
            {k: v for k, v in self._compose_env().items()
             if k != 'SECURITY_PROXY_PROBE_PRESET'},
        ):
            with self.subTest(environment=broken), self.assertRaisesRegex(
                    ValueError, 'missing config env keys'):
                bg.candidate_compose({'volumes': volumes, 'environment': broken},
                                     fixture(), 'sha256:new', 'tokenkey-blue')

    def test_candidate_rejects_non_mapping_service_environment(self):
        service = {
            'volumes': [{'source': '/var/lib/tokenkey/app', 'target': '/app/data', 'type': 'bind'}],
            'environment': ['GOMEMLIMIT=2GiB'],
        }
        with self.assertRaises(ValueError):
            bg.candidate_compose(service, fixture(), 'sha256:new', 'tokenkey-blue')

    def test_candidate_rejects_mount_drift_before_starting(self):
        volume = {'type': 'bind', 'source': '/var/lib/tokenkey/app', 'target': '/app/data'}
        for change in ({'source': '/tmp/empty'}, {'target': '/other'},
                       {'read_only': True}, {'type': 'volume'}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                bg.candidate_compose(
                    {'volumes': [volume | change], 'environment': self._compose_env()},
                    fixture(), 'sha256:new', 'tokenkey-blue')
        with self.assertRaises(ValueError):
            bg.candidate_compose({'environment': self._compose_env()}, fixture(),
                                 'sha256:new', 'tokenkey-blue')
        current = fixture()
        current['Mounts'][0]['Source'] = '/tmp/empty'
        with self.assertRaises(ValueError):
            bg.candidate_compose(
                {'volumes': [volume | {'source': '/tmp/empty'}],
                 'environment': self._compose_env()},
                current, 'sha256:new', 'tokenkey-blue')

    def test_candidate_refuses_overseas_frontend(self):
        service = {
            'volumes': [{'source': '/var/lib/tokenkey/app', 'target': '/app/data', 'type': 'bind'}],
            'environment': self._compose_env(),
        }
        current = fixture()
        current['Config']['Env'] = [
            'SERVER_FRONTEND_URL=https://api.tokenkey.dev' if item.startswith('SERVER_FRONTEND_URL=') else item
            for item in current['Config']['Env']
        ]
        with self.assertRaisesRegex(ValueError, 'refusing non-cn-prod'):
            bg.candidate_compose(service, current, 'sha256:new', 'tokenkey-blue')

    def test_candidate_enables_capture_without_s3_upload(self):
        current = fixture()
        current['Config']['Env'].append('TELEMETRY_ARCHIVE_ENABLED=true')
        service = {
            'volumes': [{'source': '/var/lib/tokenkey/app', 'target': '/app/data', 'type': 'bind'}],
            'environment': self._compose_env(),
        }
        app = bg.candidate_compose(service, current, 'sha256:new', 'tokenkey-blue')[
            'services']['tokenkey-blue']
        self.assertEqual(app['environment']['QA_CAPTURE_ENABLED'], 'true')
        self.assertEqual(app['environment']['TELEMETRY_ARCHIVE_ENABLED'], 'false')

    def test_candidate_rejects_capture_off_archive_upload_and_s3(self):
        volumes = [{'source': '/var/lib/tokenkey/app', 'target': '/app/data', 'type': 'bind'}]
        with self.assertRaisesRegex(ValueError, 'QA capture must stay enabled'):
            bg.candidate_compose(
                {'volumes': volumes, 'environment': self._compose_env(QA_CAPTURE_ENABLED='false')},
                fixture(), 'sha256:new', 'tokenkey-blue')
        with self.assertRaisesRegex(ValueError, 'telemetry archive must stay disabled'):
            bg.candidate_compose(
                {'volumes': volumes, 'environment': self._compose_env(TELEMETRY_ARCHIVE_ENABLED='true')},
                fixture(), 'sha256:new', 'tokenkey-blue')
        for extra, message in (
            ('QA_ARCHIVE_ENABLED=true', 'QA archive upload must stay disabled'),
            ('QA_ARCHIVE_STORAGE_DRIVER=s3', 'must not be s3'),
        ):
            current = fixture()
            current['Config']['Env'].append(extra)
            with self.subTest(extra=extra), self.assertRaisesRegex(ValueError, message):
                bg.candidate_compose(
                    {'volumes': volumes, 'environment': self._compose_env()},
                    current, 'sha256:new', 'tokenkey-blue')

    def test_route_resolution_rejects_foreign_or_ambiguous_routes(self):
        caddy = 'api-cn.tokenkey.dev {\n reverse_proxy tokenkey:8080 {\n }\n}\n'
        self.assertEqual(bg.active_container(caddy), 'tokenkey')
        for invalid in (caddy.replace('api-cn.', 'api.'), caddy + caddy):
            with self.assertRaises(ValueError):
                bg.active_container(invalid)

    def test_route_resolution_accepts_beijing_static_ip_site(self):
        upstream = ' reverse_proxy tokenkey-blue:8080 {\n }\n}\n'
        for site in (
            'https://154.8.214.179 {\n',
            '154.8.214.179 {\n',
            'https://154.8.214.179:443 {\n',
        ):
            with self.subTest(site=site.strip()):
                self.assertEqual(bg.active_container(site + upstream), 'tokenkey-blue')
        foreign_ip = 'https://1.2.3.4 {\n' + upstream
        with self.assertRaisesRegex(ValueError, 'non-domestic'):
            bg.active_container(foreign_ip)
        both = (
            'api-cn.tokenkey.dev {\n}\n'
            'https://154.8.214.179 {\n reverse_proxy tokenkey-blue:8080 {\n }\n}\n'
        )
        with self.assertRaisesRegex(ValueError, 'non-domestic'):
            bg.active_container(both)

    def test_domestic_caddyfile_keeps_galaxywave_on_the_shared_upstream(self):
        caddy = (Path(__file__).resolve().parent / 'Caddyfile').read_text()
        self.assertEqual(bg.domestic_site_host(caddy), bg.STATIC_SITE_IP)
        self.assertEqual(bg.active_container(caddy), 'tokenkey-blue')
        bg.galaxywave_api_site_shares_upstream(caddy)
        start = caddy.index(bg.GALAXYWAVE_API_HOST + ' {')
        own_proxy = caddy[:start] + caddy[start:].replace(
            '    import tk_upstream\n',
            '    import tk_upstream\n    reverse_proxy tokenkey-green:8080 {\n    }\n',
            1,
        )
        with self.assertRaisesRegex(ValueError, 'own reverse_proxy'):
            bg.galaxywave_api_site_shares_upstream(own_proxy)
        with self.assertRaisesRegex(ValueError, 'own reverse_proxy'):
            bg.active_container(own_proxy)
        with self.assertRaisesRegex(ValueError, 'missing galaxywave'):
            bg.galaxywave_api_site_shares_upstream(
                'https://154.8.214.179 {\n reverse_proxy tokenkey-blue:8080 {\n }\n}\n'
            )

    def test_env_get_follows_the_live_caddy_site(self):
        owner = Path(__file__).resolve().parents[2] / 'ops/stage0/deploy_via_ssm_bluegreen.sh'
        library = bg.shared_library(owner)
        cases = {
            'api-cn.tokenkey.dev {\n reverse_proxy tokenkey:8080 {\n }\n}\n': 'api-cn.tokenkey.dev',
            'https://154.8.214.179 {\n reverse_proxy tokenkey-blue:8080 {\n }\n}\n': '154.8.214.179',
            'https://154.8.214.179:443 {\n reverse_proxy tokenkey-green:8080 {\n }\n}\n': '154.8.214.179',
        }
        for caddy, host in cases.items():
            with self.subTest(host=host), tempfile.TemporaryDirectory() as tmp:
                path = Path(tmp) / 'Caddyfile'
                path.write_text(caddy)
                script = (
                    'set -euo pipefail\n' + library + '\n'
                    + f'LIVE_CADDY={shlex.quote(str(path))}\nenv_get API_DOMAIN\n'
                )
                result = subprocess.run(['bash', '-c', script], text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), host)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'Caddyfile'
            path.write_text('https://1.2.3.4 {\n reverse_proxy tokenkey-blue:8080 {\n }\n}\n')
            script = (
                'set -euo pipefail\n' + library + '\n'
                + f'LIVE_CADDY={shlex.quote(str(path))}\nenv_get API_DOMAIN\n'
            )
            result = subprocess.run(['bash', '-c', script], text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)

    def test_shared_library_is_real_shell_and_fails_on_owner_drift(self):
        owner = Path(__file__).resolve().parents[2] / 'ops/stage0/deploy_via_ssm_bluegreen.sh'
        library = bg.shared_library(owner)
        result = subprocess.run(['bash', '-n'], input=library, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tempfile.TemporaryDirectory() as tmp:
            broken = Path(tmp) / 'owner.sh'
            broken.write_text(owner.read_text().replace('drain_container() {', 'drain_removed() {'))
            with self.assertRaises(ValueError):
                bg.shared_library(broken)

    def test_receipt_rejects_config_and_container_replacement(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            (state / 'caddy').mkdir()
            for path in ('caddy/Caddyfile', 'compose.json', 'stage0-functions.sh'):
                (state / path).write_text(path)
            old, candidate = fixture(), fixture()
            candidate['Id'] = 'candidate-id'
            receipt = {'old': 'tokenkey', 'target': 'tokenkey-blue',
                       'old_identity': bg.identity(old), 'target_identity': bg.identity(candidate),
                       'caddy_hash': bg.digest(state / 'caddy/Caddyfile'),
                       'compose_hash': bg.digest(state / 'compose.json'),
                       'library_hash': bg.digest(state / 'stage0-functions.sh')}
            bg.write_private(state / 'prepared.json', receipt)
            expected = bg.digest(state / 'prepared.json')
            with patch.object(bg, 'DATA', state), patch.object(bg, 'inspect', side_effect=lambda n: old if n == 'tokenkey' else candidate):
                self.assertEqual(bg.verify_receipt(state, expected), receipt)
                candidate['RestartCount'] = 1
                with self.assertRaises(ValueError):
                    bg.verify_receipt(state, expected)
                candidate['RestartCount'] = 0
                (state / 'compose.json').write_text('changed')
                with self.assertRaises(ValueError):
                    bg.verify_receipt(state, expected)

    def test_invalid_smoke_context_prevents_any_cutover(self):
        args = SimpleNamespace(state=Path('/unused'), receipt='hash')
        with patch.object(bg, 'verify_receipt', return_value={}), \
                patch.object(bg.post_release, 'configure', side_effect=ValueError('failed smoke')), \
                patch.object(bg, 'run') as run, patch.object(bg, 'shell') as shell:
            with self.assertRaises(ValueError):
                bg.promote(args)
            run.assert_not_called()
            shell.assert_not_called()

    def test_promote_uses_shared_cutover_before_drain_and_keeps_old_on_failure(self):
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            receipt = {'old': 'tokenkey', 'target': 'tokenkey-blue', 'revision': 'reviewed'}
            commands = []

            def shell(_state, body):
                commands.append(body)
                raise subprocess.CalledProcessError(1, 'shared-cutover')

            with patch.object(bg.post_release, 'configure', return_value={}), \
                    patch.object(bg, 'verify_receipt', return_value=receipt), \
                    patch.object(bg, 'inspect', return_value={'Image': 'pinned-caddy'}), \
                    patch.object(bg, 'run') as run, patch.object(bg, 'shell', side_effect=shell):
                with self.assertRaises(subprocess.CalledProcessError):
                    bg.promote(SimpleNamespace(state=state, receipt='hash'))
                self.assertEqual(run.call_args.args, ('docker', 'tag', 'pinned-caddy', 'caddy:2-alpine'))
                self.assertFalse((state / 'completed.json').exists())
            body = commands[0]
            self.assertLess(body.index('commit_cutover blue'), body.index('drain_container tokenkey'))
            self.assertLess(body.index('observe_routed_health blue'), body.index('docker stop'))


class DomesticEnvRepinTest(unittest.TestCase):
    """Promote must leave .env able to rebuild the color it just routed to."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.env = self.root / '.env'
        # Byte-exact secrets and ordering must survive the rewrite.
        self.before = ('TOKENKEY_IMAGE=tokenkey-domestic/tokenkey:cn-prod-monitor-704fd77abdbd\n'
                       'CADDY_IMAGE=tokenkey-domestic/caddy:173b26306d71\n'
                       'JWT_SECRET=literal${value}\n'
                       'POSTGRES_PASSWORD=secret-value\n')
        self.env.write_text(self.before)
        patcher = patch.object(bg, 'ROOT', self.root)
        patcher.start()
        self.addCleanup(patcher.stop)

    def repin(self, tags, image_id='sha256:abcdef0123456789'):
        def run(*args):
            self.assertEqual(args[:3], ('docker', 'image', 'inspect'))
            return json.dumps(tags)
        with patch.object(bg, 'run', side_effect=run):
            bg.repin_env_image(image_id)

    def test_repins_only_the_app_image_and_keeps_every_other_line(self):
        self.repin(['tokenkey-domestic/archive:deadbeef',
                    'tokenkey-domestic/tokenkey:1.8.318-63e91c048'])
        text = self.env.read_text()
        self.assertIn('TOKENKEY_IMAGE=tokenkey-domestic/tokenkey:1.8.318-63e91c048\n', text)
        self.assertNotIn('cn-prod-monitor', text)
        # Secrets and unrelated pins stay byte-exact, in the original order.
        for line in ('CADDY_IMAGE=tokenkey-domestic/caddy:173b26306d71\n',
                     'JWT_SECRET=literal${value}\n', 'POSTGRES_PASSWORD=secret-value\n'):
            self.assertIn(line, text)
        self.assertEqual([l.split('=', 1)[0] for l in text.splitlines()],
                         [l.split('=', 1)[0] for l in self.before.splitlines()])
        backup = self.root / '.env.before-promote-repin-abcdef012345'
        self.assertEqual(backup.read_text(), self.before)

    def test_refuses_a_tag_outside_the_repository_already_pinned(self):
        # A registry reference must never replace the host-local repo.
        for tags in [['ghcr.io/youxuanxue/sub2api:1.8.318'], [], ['tokenkey-domestic/archive:x'],
                     ['tokenkey-domestic/tokenkey:a', 'tokenkey-domestic/tokenkey:b']]:
            with self.subTest(tags=tags):
                self.env.write_text(self.before)
                with self.assertRaisesRegex(ValueError, 'tag to pin'):
                    self.repin(tags)
                self.assertEqual(self.env.read_text(), self.before)

    def test_absent_env_or_missing_key_is_a_no_op(self):
        self.env.unlink()
        self.repin(['tokenkey-domestic/tokenkey:1.8.318-63e91c048'])
        self.assertFalse(self.env.exists())
        self.env.write_text('CADDY_IMAGE=tokenkey-domestic/caddy:173b26306d71\n')
        self.repin(['tokenkey-domestic/tokenkey:1.8.318-63e91c048'])
        self.assertEqual(self.env.read_text(), 'CADDY_IMAGE=tokenkey-domestic/caddy:173b26306d71\n')

    def test_promote_repins_before_cutover(self):
        state = self.root / 'release'
        state.mkdir()
        receipt = {'old': 'tokenkey-blue', 'target': 'tokenkey-green', 'revision': 'reviewed'}
        order = []
        with patch.object(bg.post_release, 'configure', return_value={}), \
                patch.object(bg, 'verify_receipt', return_value=receipt), \
                patch.object(bg, 'inspect', return_value={'Image': 'sha256:promoted'}), \
                patch.object(bg, 'repin_env_image', side_effect=lambda i: order.append(('repin', i))), \
                patch.object(bg, 'run'), \
                patch.object(bg, 'shell', side_effect=lambda *a: order.append(('cutover',))), \
                patch.object(bg, 'write_private'), \
                patch.object(bg.post_release, 'finish'):
            bg.promote(SimpleNamespace(state=state, receipt='hash'))
        # A pin written after cutover would be lost if cutover raised.
        self.assertEqual(order, [('repin', 'sha256:promoted'), ('cutover',)])


class DomesticRollbackRuntimeTest(unittest.TestCase):
    """Execute the real shared shell against a deterministic Docker/Caddy host."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.state = self.root / 'release'
        self.state.mkdir()
        (self.root / 'caddy').mkdir()
        self.before = 'api-cn.tokenkey.dev {\n reverse_proxy tokenkey:8080 {\n }\n}\n'
        self.caddy = self.root / 'caddy/Caddyfile'
        self.caddy.write_text(self.before.replace('tokenkey:8080', 'tokenkey-blue:8080'))
        (self.state / 'Caddyfile.before').write_text(self.before)
        owner = Path(__file__).resolve().parents[2] / 'ops/stage0/deploy_via_ssm_bluegreen.sh'
        (self.state / 'stage0-functions.sh').write_text(bg.shared_library(owner))
        self.host_file = self.root / 'host.json'
        old, new = fixture(), fixture()
        old['State'].update(Running=False, Status='exited')
        new['Id'] = 'candidate-id'
        new['State'].update(Running=True, Status='running')
        self.host = {'containers': {'tokenkey': old, 'tokenkey-blue': new},
                     'draining': {'tokenkey': True, 'tokenkey-blue': False},
                     'route': 'tokenkey-blue', 'events': [], 'fail_reload': False}
        self.save_host()
        receipt = {'old': 'tokenkey', 'target': 'tokenkey-blue',
                   'old_identity': bg.identity(old), 'target_identity': bg.identity(new),
                   'caddy_hash': bg.digest(self.state / 'Caddyfile.before'),
                   'library_hash': bg.digest(self.state / 'stage0-functions.sh')}
        bg.write_private(self.state / 'prepared.json', receipt)
        self.args = SimpleNamespace(state=self.state, receipt=bg.digest(self.state / 'prepared.json'))
        bindir = self.root / 'bin'
        bindir.mkdir()
        fake = bindir / 'docker'
        fake.write_text('#!' + sys.executable + '\n' + r'''
import json, os, pathlib, re, sys
p = pathlib.Path(os.environ['FAKE_HOST'])
h = json.loads(p.read_text())
a = sys.argv[1:]
h['events'].append([pathlib.Path(sys.argv[0]).name] + a)
code = 0
if pathlib.Path(sys.argv[0]).name == 'curl':
    name = h['route']
    code = int(not h['containers'][name]['State']['Running'] or h['draining'][name])
    print('{"status":"ok"}')
elif a[0] == 'inspect':
    if a[1] == 'tokenkey-caddy':
        print(json.dumps([{'Image': 'sha256:caddy'}]))
        sys.exit(0)
    if a[1] not in h['containers']:
        sys.exit(1)
    c = h['containers'][a[1]]
    if '--format' in a:
        print('healthy' if c['State']['Running'] else 'exited')
    else:
        print(json.dumps([c]))
elif a[0] == 'start':
    c = h['containers'][a[1]]
    if not c['State']['Running']:
        c['State']['Running'] = True
        c['State']['Status'] = 'running'
        h['draining'][a[1]] = False
elif a[0] == 'stop':
    h['containers'][a[-1]]['State']['Running'] = False
    h['containers'][a[-1]]['State']['Status'] = 'exited'
elif a[0] == 'kill':
    h['draining'][a[-1]] = True
elif a[0] == 'exec' and a[1] == 'tokenkey-caddy':
    if h['fail_reload']:
        h['fail_reload'] = False
        code = 1
    else:
        text = pathlib.Path(os.environ['FAKE_CADDY']).read_text()
        h['route'] = re.search(r'reverse_proxy (\S+):8080', text)[1]
elif a[0] == 'exec':
    if a[-1].endswith('/health/inflight'):
        pending = h.get('pending', {}).get(a[1], [])
        count = pending.pop(0) if pending else 0
        print(json.dumps({'draining': h['draining'][a[1]], 'in_flight': count}))
    else:
        code = int(h['draining'][a[1]])
        print('{"status":"ok"}')
elif a[0] not in ('logs', 'tag', 'run'):
    raise SystemExit('unexpected fake Docker command')
p.write_text(json.dumps(h))
sys.exit(code)
''')
        fake.chmod(0o755)
        (bindir / 'curl').symlink_to(fake)
        for name, body in (('sudo', '#!/bin/sh\nexec "$@"\n'),
                           ('sleep', '#!/bin/sh\nexit 0\n')):
            path = bindir / name
            path.write_text(body)
            path.chmod(0o755)
        patcher = patch.dict(os.environ, {
            'PATH': str(bindir) + os.pathsep + os.environ['PATH'],
            'FAKE_HOST': str(self.host_file), 'FAKE_CADDY': str(self.caddy),
            'TOKENKEY_BLUEGREEN_READY_TRIES': '1', 'TOKENKEY_BLUEGREEN_OBSERVE_SECONDS': '1',
        })
        patcher.start()
        self.addCleanup(patcher.stop)
        patcher = patch.object(bg, 'DATA', self.root)
        patcher.start()
        self.addCleanup(patcher.stop)

    def save_host(self):
        self.host_file.write_text(json.dumps(self.host))

    def test_rollback_restarts_stopped_old_then_routes_and_drains_candidate(self):
        bg.rollback(self.args)
        host = json.loads(self.host_file.read_text())
        self.assertEqual(host['route'], 'tokenkey')
        self.assertTrue(host['containers']['tokenkey']['State']['Running'])
        self.assertFalse(host['draining']['tokenkey'])
        self.assertFalse(host['containers']['tokenkey-blue']['State']['Running'])
        self.assertEqual(json.loads((self.state / 'rolled-back.json').read_text())['active'], 'tokenkey')

    def test_rollback_recovers_running_old_after_interrupted_drain(self):
        self.host['containers']['tokenkey']['State'].update(Running=True, Status='running')
        self.host['pending'] = {'tokenkey': [5, 5, 0]}
        self.save_host()
        self.test_rollback_restarts_stopped_old_then_routes_and_drains_candidate()
        events = json.loads(self.host_file.read_text())['events']
        stopped = events.index(['docker', 'stop', '-t', '180', 'tokenkey'])
        started = events.index(['docker', 'start', 'tokenkey'])
        self.assertLess(stopped, started)
        polls = [event for event in events[:stopped]
                 if event[1:3] == ['exec', 'tokenkey'] and event[-1].endswith('/health/inflight')]
        self.assertEqual(len(polls), 3)

    def test_failed_reload_restores_candidate_route_without_stopping_either(self):
        self.host['fail_reload'] = True
        self.save_host()
        with self.assertRaises(RuntimeError):
            bg.rollback(self.args)
        host = json.loads(self.host_file.read_text())
        self.assertEqual(host['route'], 'tokenkey-blue')
        self.assertEqual(bg.active_container(self.caddy.read_text()), 'tokenkey-blue')
        self.assertTrue(all(c['State']['Running'] for c in host['containers'].values()))
        self.assertFalse(any(event[1] == 'stop' for event in host['events']))

    def test_promote_executes_shared_cutover_and_verified_drain(self):
        self.caddy.write_text(self.before)
        self.host['route'] = 'tokenkey'
        self.host['containers']['tokenkey']['State'].update(Running=True, Status='running')
        self.host['draining']['tokenkey'] = False
        self.save_host()
        bg.write_private(self.state / 'compose.json', {})
        receipt = json.loads((self.state / 'prepared.json').read_text())
        receipt.update(old_identity=bg.identity(self.host['containers']['tokenkey']),
                       caddy_hash=bg.digest(self.caddy), revision='reviewed',
                       compose_hash=bg.digest(self.state / 'compose.json'))
        bg.write_private(self.state / 'prepared.json', receipt)
        self.args.receipt = bg.digest(self.state / 'prepared.json')
        with patch.object(bg.post_release, 'configure', return_value={}), \
                patch.object(bg.post_release, 'finish') as finish:
            bg.promote(self.args)
        finish.assert_called_once()
        host = json.loads(self.host_file.read_text())
        self.assertEqual(host['route'], 'tokenkey-blue')
        self.assertFalse(host['containers']['tokenkey']['State']['Running'])
        self.assertTrue(host['containers']['tokenkey-blue']['State']['Running'])
        self.assertEqual((self.root / 'active-color').read_text().strip(), 'blue')
        self.assertEqual(json.loads((self.state / 'completed.json').read_text())['stage'], 'complete')
        events = host['events']
        observation = next(i for i, event in enumerate(events) if event[0] == 'curl')
        drain = events.index(['docker', 'kill', '-s', 'USR1', 'tokenkey'])
        stop = events.index(['docker', 'stop', '-t', '180', 'tokenkey'])
        self.assertLess(observation, drain)
        self.assertLess(drain, stop)


if __name__ == '__main__':
    unittest.main()
