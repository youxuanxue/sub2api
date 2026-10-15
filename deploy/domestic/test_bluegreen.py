"""Domestic host adapter checks; shared cutover/drain behavior has Stage0 tests."""
from pathlib import Path
import subprocess
import tempfile
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

    def test_promote_uses_shared_cutover_before_drain_and_keeps_old_on_failure(self):
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            receipt = {'old': 'tokenkey', 'target': 'tokenkey-blue', 'revision': 'reviewed'}
            commands = []

            def shell(_state, body):
                commands.append(body)
                raise subprocess.CalledProcessError(1, 'shared-cutover')

            with patch.object(bg, 'verify_receipt', return_value=receipt), \
                    patch.object(bg, 'inspect', return_value={'Image': 'pinned-caddy'}), \
                    patch.object(bg, 'run') as run, patch.object(bg, 'shell', side_effect=shell):
                with self.assertRaises(subprocess.CalledProcessError):
                    bg.promote(SimpleNamespace(state=state, receipt='hash'))
                self.assertEqual(run.call_args.args, ('docker', 'tag', 'pinned-caddy', 'caddy:2-alpine'))
                self.assertFalse((state / 'completed.json').exists())
            body = commands[0]
            self.assertLess(body.index('commit_cutover blue'), body.index('drain_container tokenkey'))
            self.assertLess(body.index('observe_routed_health blue'), body.index('docker stop'))


if __name__ == '__main__':
    unittest.main()
