"""Exercise real post-release evaluation with synthetic host/network boundaries."""
from datetime import datetime, timezone
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from deploy.domestic import bluegreen as bg, post_release as pr


class PostReleaseTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.state = self.root / '1.8.300-test'
        self.state.mkdir()
        (self.root / 'caddy').mkdir()
        (self.root / 'caddy/Caddyfile').write_text('api-cn.tokenkey.dev {\n reverse_proxy tokenkey-green:8080 {\n }\n}\n')
        self.old_sha, self.new_sha = 'a' * 40, 'b' * 40
        self.old = self.container('old', '1.8.298', self.old_sha)
        self.new = self.container('new', '1.8.300', self.new_sha)
        self.receipt = {'old': 'tokenkey-blue', 'target': 'tokenkey-green', 'image_id': 'sha256:new', 'revision': self.new_sha}
        public_origin = pr.read_json(pr.asset_source('prod-target.json'))['public_origin']
        self.args = SimpleNamespace(state=self.state, postcheck_plan=self.root / 'plan.json',
                                    smoke_report=self.root / 'smoke.json', public_url=public_origin)
        self.plan = {'range': {'live': self.old_sha, 'new': self.new_sha}, 'changes': [], 'checks': []}
        pr.save(self.args.postcheck_plan, self.plan)
        self.smoke = {'revision': self.new_sha, 'container_id': 'new', 'cases': [{'passed': True}]}
        pr.save(self.args.smoke_report, self.smoke)
        self.now = 1800000000.0
        self.cutover = datetime.fromtimestamp(self.now, timezone.utc).isoformat()
        pr.save(self.root / ('bluegreen-cutover-' + self.state.name + '.json'),
                {'token': self.state.name, 'tag': self.state.name, 'cutover_at': self.cutover})
        self.events = []
        self.panic = 0
        self.delivery = 'sent'
        self.real_execute = pr.execute

    @staticmethod
    def container(identity, version, revision):
        return {'Id': identity, 'Image': 'sha256:' + identity,
                'State': {'Running': True, 'Health': {'Status': 'healthy'}},
                'Config': {'Labels': {'org.opencontainers.image.version': version,
                                      'org.opencontainers.image.revision': revision}}}

    def inspect(self, name):
        return self.old if name == 'tokenkey-blue' else self.new

    def configure(self):
        return pr.configure(self.args, self.receipt, self.inspect)

    def execute(self, command, *, env=None):
        if command[0] == 'curl':
            self.events.append(('health', self.now))
            return '{"status":"ok"}'
        if Path(command[1]).name == 'probe-post-release-tick.sh':
            self.events.append(('probe', self.now))
            return ('=== panic ===\n' + json.dumps({'count': self.panic}) + '\n=== traffic ===\n'
                    + json.dumps({'completed_total': 5, 'top_paths': [], 'status_5xx': {}, 'final_status_available': True}) + '\n')
        if Path(command[1]).name == 'notify-feishu-release.sh':
            self.events.append(('send', self.now))
            self.assertEqual(command[command.index('--environment') + 1], 'cn-prod')
            self.assertEqual(command[command.index('--deployed-at') + 1], self.cutover)
            self.assertEqual(command[command.index('--previous-tag') + 1], '1.8.298')
            pr.save(Path(command[command.index('--result-file') + 1]), {'status': self.delivery})
            return 'delivery=' + self.delivery
        return self.real_execute(command, env=env)

    def finish(self, context):
        def sleep(seconds):
            self.now += seconds
        with patch.object(pr, 'execute', side_effect=self.execute), \
                patch.object(pr, 'credentials', return_value={}), \
                patch.object(pr.time, 'time', side_effect=lambda: self.now), \
                patch.object(pr.time, 'sleep', side_effect=sleep):
            pr.finish(self.args, self.receipt, context, data=self.root,
                      inspect=self.inspect, active_container=bg.active_container)

    def test_notifies_only_after_both_checks_and_five_minutes_then_deduplicates(self):
        context = self.configure()
        self.finish(context)
        self.assertEqual([x[0] for x in self.events], ['health', 'probe', 'health', 'probe', 'send'])
        self.assertGreaterEqual(self.events[-1][1] - pr.timestamp(self.cutover), 300)
        self.finish(context)
        self.assertEqual(sum(name == 'send' for name, _ in self.events), 1)

    def test_red_evaluation_never_sends(self):
        context = self.configure()
        self.panic = 1
        with self.assertRaises(RuntimeError):
            self.finish(context)
        self.assertNotIn('send', [x[0] for x in self.events])

    def test_failed_delivery_can_be_retried_without_cutover(self):
        context = self.configure()
        self.delivery = 'failed'
        self.finish(context)
        self.delivery = 'sent'
        self.finish(context)
        self.assertEqual(sum(name == 'send' for name, _ in self.events), 2)

    def test_rejects_wrong_revision_failed_smoke_or_wrong_candidate_before_mutation(self):
        for field, value in [('revision', 'c' * 40), ('container_id', 'different'), ('cases', [{'passed': False}]), ('cases', [])]:
            with self.subTest(field=field, value=value):
                pr.save(self.args.smoke_report, self.smoke | {field: value})
                with self.assertRaises(ValueError):
                    self.configure()
        pr.save(self.args.smoke_report, self.smoke)
        pr.save(self.args.postcheck_plan, self.plan | {'range': {'live': 'stale', 'new': self.new_sha}})
        with self.assertRaises(ValueError):
            self.configure()

    def test_rejects_plan_without_changes_before_cutover(self):
        # A hand-written plan carrying only 'range' used to pass configure() and
        # then raise KeyError('changes') in finish(), after the public route had
        # already moved (cn-prod 1.8.318, 2026-10-23). Reject it up front.
        incomplete = {'range': {'live': self.old_sha, 'new': self.new_sha}}
        for plan in [incomplete, incomplete | {'changes': None}, incomplete | {'changes': {}}]:
            with self.subTest(plan=plan):
                pr.save(self.args.postcheck_plan, plan)
                with self.assertRaisesRegex(ValueError, 'changes list'):
                    self.configure()
                self.assertFalse((self.state / 'post-release').exists())
        # A generated plan with an empty changes list stays valid: a release can
        # legitimately carry no product commits.
        pr.save(self.args.postcheck_plan, self.plan)
        self.assertEqual(self.configure()['range'], {'live': self.old_sha, 'new': self.new_sha})

    def test_failed_notification_receipt_keeps_the_cause(self):
        context = self.configure()
        (self.state / 'post-release/plan.json').write_text(json.dumps({'range': context['range']}))
        pr.save(self.state / 'post-release/context.json', context | {
            'hashes': context['hashes'] | {'plan.json': pr.sha(self.state / 'post-release/plan.json')}})
        self.finish(pr.read_json(self.state / 'post-release/context.json'))
        receipt = pr.read_json(self.state / 'post-release/notification.json')
        self.assertEqual(receipt['status'], 'failed')
        # Without the cause, an operator chases the webhook instead of the plan.
        self.assertIn('KeyError', receipt['cause'])
        self.assertIn('changes', receipt['cause'])
        self.assertNotIn(('send', self.now), self.events)

    def test_rejects_inactive_release_and_pinned_evidence_drift(self):
        context = self.configure()
        self.new['State']['Running'] = False
        with self.assertRaises(ValueError):
            self.finish(context)
        self.new['State']['Running'] = True
        (self.state / 'post-release/plan.json').write_text('{}')
        with self.assertRaises(ValueError):
            self.finish(context)
        self.assertEqual(self.events, [])

    def test_rejects_aws_domain_http_and_credentials_in_url(self):
        expected = pr.read_json(pr.asset_source('prod-target.json'))['public_origin']
        for url in ['https://api-cn.tokenkey.dev', 'https://api.galaxywaveai.com']:
            with self.subTest(url=url), self.assertRaisesRegex(ValueError, 'public_origin is ' + expected):
                self.args.public_url = url
                self.configure()
        for url in ['http://154.8.214.179', 'https://user:pass@154.8.214.179', 'https://127.0.0.1']:
            with self.subTest(url=url), self.assertRaises(ValueError):
                self.args.public_url = url
                self.configure()

    def test_rejects_other_public_ip_before_pinning_release(self):
        self.args.public_url = 'https://8.8.8.8'  # valid global IP, not this deployment
        expected = pr.read_json(pr.asset_source('prod-target.json'))['public_origin']
        with self.assertRaisesRegex(ValueError, 'domestic target manifest public_origin ' + expected):
            self.configure()
        self.assertFalse((self.state / 'post-release').exists())

    def test_rejects_pinned_target_manifest_drift_before_probing_or_sending(self):
        context = self.configure()
        pr.save(self.state / 'post-release/prod-target.json', {'public_origin': 'https://8.8.8.8'})
        with self.assertRaisesRegex(ValueError, 'pinned post-release input changed'):
            self.finish(context)
        self.assertEqual(self.events, [])


if __name__ == '__main__':
    unittest.main()
