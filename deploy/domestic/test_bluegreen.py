"""Domestic host adapter checks; shared cutover/drain behavior has Stage0 tests."""
from pathlib import Path
import json
import os
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
                               'GOMEMLIMIT=768MiB', 'DATABASE_HOST=postgres']},
            'HostConfig': {'RestartPolicy': {'Name': 'unless-stopped'}},
            'Mounts': [{'Type': 'bind', 'Source': '/var/lib/tokenkey/app',
                        'Destination': '/app/data', 'RW': True}],
            'NetworkSettings': {'Networks': {'tokenkey_tokenkey-network': {}}}}


class DomesticBluegreenTest(unittest.TestCase):
    def test_candidate_preserves_runtime_and_reuses_only_private_network(self):
        current = fixture()
        service = {'image': 'old', 'environment': {'JWT_SECRET': 'stale'},
                   'depends_on': {'postgres': {}}, 'ports': ['18080:8080'],
                   'networks': {'old': {}}, 'volumes': [{'source': '/var/lib/tokenkey/app',
                                                        'target': '/app/data', 'type': 'bind'}]}
        actual = bg.candidate_compose(service, current, 'sha256:new', 'tokenkey-blue')
        app = actual['services']['tokenkey-blue']
        self.assertEqual(app['environment']['JWT_SECRET'], 'literal$${value}')
        self.assertEqual(app['environment']['GOMEMLIMIT'], '768MiB')
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

    def test_candidate_rejects_mount_drift_before_starting(self):
        service = {'volumes': [{'type': 'bind', 'source': '/var/lib/tokenkey/app',
                                'target': '/app/data'}]}
        for change in ({'source': '/tmp/empty'}, {'target': '/other'},
                       {'read_only': True}, {'type': 'volume'}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                bg.candidate_compose({'volumes': [service['volumes'][0] | change]},
                                     fixture(), 'sha256:new', 'tokenkey-blue')
        with self.assertRaises(ValueError):
            bg.candidate_compose({}, fixture(), 'sha256:new', 'tokenkey-blue')
        current = fixture()
        current['Mounts'][0]['Source'] = '/tmp/empty'
        with self.assertRaises(ValueError):
            bg.candidate_compose({'volumes': [service['volumes'][0] | {'source': '/tmp/empty'}]},
                                 current, 'sha256:new', 'tokenkey-blue')

    def test_candidate_refuses_overseas_or_changed_qa_boundary(self):
        for replacement in ('SERVER_FRONTEND_URL=https://api.tokenkey.dev',
                            'QA_CAPTURE_ENABLED=true'):
            current = fixture()
            name = replacement.split('=')[0]
            current['Config']['Env'] = [replacement if v.startswith(name + '=') else v
                                         for v in current['Config']['Env']]
            with self.subTest(value=replacement), self.assertRaises(ValueError):
                bg.candidate_compose({}, current, 'sha256:new', 'tokenkey-blue')

    def test_route_resolution_rejects_foreign_or_ambiguous_routes(self):
        caddy = 'api-cn.tokenkey.dev {\n reverse_proxy tokenkey:8080 {\n }\n}\n'
        self.assertEqual(bg.active_container(caddy), 'tokenkey')
        for invalid in (caddy.replace('api-cn.', 'api.'), caddy + caddy):
            with self.assertRaises(ValueError):
                bg.active_container(invalid)

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
elif a[:5] == ['exec', '-i', 'tokenkey-caddy', 'caddy', 'validate']:
    text = sys.stdin.read()
    code = int(re.search(r'reverse_proxy (\S+):8080', text) is None)
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
