import contextlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import entry_caddy  # noqa: E402
import node_compose  # noqa: E402

# /opt/tokenkey/compose.appb.yml on app-b, read 2026-11-15. M2 requires the
# rendered compose.node.yml to be equivalent to it.
LIVE_APPB = '''services:
  tokenkey:
    mem_limit: 3g
    memswap_limit: 3g
    ports: !override
      - "10.2.4.13:8080:8080"
      - "127.0.0.1:18080:8080"
    environment:
      GATEWAY_MAX_IN_FLIGHT_REQUESTS: "512"
      TOKENKEY_NODE_NAME: "cn-prod-app-b"
      GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_CONCURRENCY_MIRROR_ENABLED: "true"
      GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED: "false"
'''

# Entry Caddyfile as observed on lhins-j89o28j0 2026-11-15 (comments dropped).
# Captured read-only from the entry host on 2026-11-15 (no secrets in this file).
LIVE_ENTRY = (Path(__file__).resolve().parent / 'testdata' / 'entry-Caddyfile.live-20261115').read_text()


def run(main, argv):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = main(argv)
    return code, out.getvalue()


class NodeComposeTest(unittest.TestCase):
    def setUp(self):
        self.target = node_compose.load_target()

    def test_app_b_render_is_equivalent_to_the_live_hand_written_file(self):
        rendered = node_compose.render(node_compose.find_app(self.target, 'app-b'))
        self.assertIsNotNone(node_compose.semantic(rendered), 'PyYAML is required for the M2 proof')
        self.assertEqual(node_compose.semantic(rendered), node_compose.semantic(LIVE_APPB))
        self.assertTrue(node_compose.equivalent(rendered, LIVE_APPB))
        self.assertIn('ports: !override', rendered)

    def test_text_fallback_agrees_without_yaml(self):
        rendered = node_compose.render(node_compose.find_app(self.target, 'app-b'))
        self.assertEqual(node_compose.normalized(rendered), node_compose.normalized(LIVE_APPB))

    def test_dropping_override_tag_or_port_is_a_difference(self):
        rendered = node_compose.render(node_compose.find_app(self.target, 'app-b'))
        self.assertFalse(node_compose.equivalent(rendered, LIVE_APPB.replace(' !override', '')))
        self.assertFalse(node_compose.equivalent(
            rendered, LIVE_APPB.replace('      - "127.0.0.1:18080:8080"\n', '')))

    def test_max_in_flight_comes_from_the_shared_override(self):
        self.assertEqual(node_compose.default_max_in_flight(), '512')
        rendered = node_compose.render(node_compose.find_app(self.target, 'app-a'))
        doc = node_compose.semantic(rendered)['services']['tokenkey']
        self.assertEqual(doc['environment']['GATEWAY_MAX_IN_FLIGHT_REQUESTS'], '512')
        self.assertEqual(doc['environment']['TOKENKEY_NODE_NAME'], 'cn-prod-app-a')
        self.assertEqual(doc['ports'], {'!override': ['10.2.4.11:8080:8080', '127.0.0.1:18080:8080']})
        self.assertEqual(doc['mem_limit'], '3g')

    def test_invalid_nodes_are_refused(self):
        base = {'name': 'app-x', 'private_ip': '10.2.4.20', 'memory_limit': '3g'}
        for patch in ({'private_ip': '8.8.8.8'}, {'memory_limit': '3G '}, {'name': 'App X'},
                      {'env': {'TOKENKEY_NODE_NAME': 'other'}}, {'env': {'bad key': 'x'}},
                      {'env': {'K': 'a\nb'}}):
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                node_compose.render({**base, **patch}, '512')
        with self.assertRaises(ValueError):
            node_compose.find_app(self.target, 'app-z')

    def test_cli_out_and_read_only_diff(self):
        with tempfile.TemporaryDirectory() as root:
            existing = Path(root) / 'compose.appb.yml'
            existing.write_text(LIVE_APPB)
            before = existing.read_bytes()
            code, out = run(node_compose.main, ['--node', 'app-b', '--diff', str(existing)])
            self.assertEqual((code, json.loads(out)), (0, {'node': 'app-b', 'equivalent': True}))
            self.assertEqual(existing.read_bytes(), before)
            existing.write_text(LIVE_APPB.replace('"512"', '"256"'))
            code, out = run(node_compose.main, ['--node', 'app-b', '--diff', str(existing)])
            self.assertEqual(code, 1)
            self.assertIn('"512"', out)
            dest = Path(root) / 'compose.node.yml'
            code, _ = run(node_compose.main, ['--node', 'app-b', '--out', str(dest)])
            self.assertEqual(code, 0)
            self.assertTrue(node_compose.equivalent(dest.read_text(), LIVE_APPB))


class EntryCaddyTest(unittest.TestCase):
    def setUp(self):
        self.target = json.loads(entry_caddy.TARGET.read_text())

    def test_render_matches_the_live_entry_caddyfile(self):
        rendered = entry_caddy.render(self.target)
        self.assertEqual(entry_caddy.normalized(rendered), entry_caddy.normalized(LIVE_ENTRY))

    def test_backend_list_follows_apps_order_and_weights(self):
        self.target['apps'] = [
            {'name': 'app-b', 'private_ip': '10.2.4.13', 'weight': 1},
            {'name': 'app-c', 'private_ip': '10.2.4.15', 'weight': 2},
        ]
        rendered = entry_caddy.normalized(entry_caddy.render(self.target))
        self.assertIn('to 10.2.4.13:8080 10.2.4.15:8080', rendered)
        self.assertIn('lb_policy weighted_round_robin 1 2', rendered)

    def test_live_order_puts_heavier_app_a_first_while_apps_order_is_rolling_order(self):
        # apps[] is rolling order (app-b first). The live entry lists app-a
        # first; Caddy weights are positional, so the renderer sorts by the
        # private IP to keep the to/weight pairs and the live file aligned.
        rendered = entry_caddy.normalized(entry_caddy.render(self.target))
        self.assertIn('to 10.2.4.11:8080 10.2.4.13:8080', rendered)
        self.assertIn('lb_policy weighted_round_robin 3 1', rendered)

    def test_invalid_targets_are_refused(self):
        for mutate in (lambda t: t['apps'].clear(),
                       lambda t: t['apps'][0].update(private_ip='154.8.214.179'),
                       lambda t: t['apps'][0].update(weight=0),
                       lambda t: t['apps'][1].update(private_ip=t['apps'][0]['private_ip']),
                       lambda t: t['entry'].update(domain='API.example.com')):
            target = json.loads(entry_caddy.TARGET.read_text())
            mutate(target)
            with self.assertRaises(ValueError):
                entry_caddy.render(target)

    def test_cli_diff_is_read_only_and_ignores_comments(self):
        with tempfile.TemporaryDirectory() as root:
            live = Path(root) / 'Caddyfile'
            live.write_text('# hand-written 2026-11-15\n' + LIVE_ENTRY.replace('    ', '\t'))
            before = live.read_bytes()
            code, out = run(entry_caddy.main, ['--diff', str(live)])
            self.assertEqual((code, json.loads(out)), (0, {'equivalent': True}))
            self.assertEqual(live.read_bytes(), before)
            live.write_text(LIVE_ENTRY.replace('weighted_round_robin 3 1', 'weighted_round_robin 1 1'))
            code, out = run(entry_caddy.main, ['--diff', str(live)])
            self.assertEqual(code, 1)
            self.assertIn('weighted_round_robin 3 1', out)


if __name__ == '__main__':
    unittest.main()


class ExternalNetworkTest(unittest.TestCase):
    def test_app_a_declares_existing_bluegreen_network_external(self):
        target = json.loads(node_compose.TARGET.read_text())
        text = node_compose.render(node_compose.find_app(target, "app-a"), "512")
        self.assertIn("networks:\n  tokenkey-network: !override\n    name: tokenkey_tokenkey-network\n    external: true\n", text)

    def test_app_b_keeps_compose_owned_network(self):
        target = json.loads(node_compose.TARGET.read_text())
        self.assertNotIn("networks:", node_compose.render(node_compose.find_app(target, "app-b"), "512"))

    def test_bad_network_name_refused(self):
        with self.assertRaises(ValueError):
            node_compose.render({"name": "app-x", "private_ip": "10.2.4.20", "memory_limit": "3g",
                                 "external_network": "bad name"}, "512")
