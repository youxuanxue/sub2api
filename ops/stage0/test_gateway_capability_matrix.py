import copy
import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import gateway_capability_matrix as matrix
import gateway_capability_check as check
from post_release_replay_check import main
from test_prod_replay import server


def inventory(*models):
    return {'schema': 1, 'kind': 'account-supply-representatives', 'classes': [{
        'id': 'example-chat', 'platform': 'newapi', 'auth_type': 'apikey', 'channel_type': 1,
        'dialect': 'standard', 'native_protocols': ['openai-chat'], 'exclusive_endpoints': False,
        'branch_family': 'family-0', 'representatives': [
            {'family': f'family-{i}', 'model': name, 'upstream_model': name, 'operation': 'generation',
             'baseline_protocol': 'openai-chat', 'represented_models': [name]}
            for i, name in enumerate(models or ('model-a',))]}]}


def plan(*models):
    return matrix.build(inventory(*models), matrix.load())


class MatrixTests(unittest.TestCase):
    def test_new_family_and_template_changes_create_delta(self):
        before = plan('model-a')
        after = plan('model-a', 'model-z')
        # Inventory semantics change all obligations of this class, including its new family.
        added = matrix.delta(after, before)
        self.assertIn('model-z', {e['model'] for e in added})
        self.assertEqual({e['key_type'] for e in added}, {'universal'})
        profiles = matrix.load()
        profiles[0]['request']['body']['max_completion_tokens'] = 32
        changed = matrix.build(inventory('model-a'), profiles)
        self.assertEqual({e['profile'] for e in matrix.delta(changed, before)}, {'openai-chat.plain'})
        self.assertEqual(matrix.delta(before, before), [])

    def test_account_class_is_part_of_case_identity(self):
        value = inventory()
        sibling = copy.deepcopy(value['classes'][0]); sibling['id'] = 'other-account-path'
        value['classes'].append(sibling)
        result = matrix.build(value, matrix.load())
        self.assertEqual(len(result['entries']), 2 * len(plan()['entries']))
        self.assertEqual(len({e['id'] for e in result['entries']}), len(result['entries']))

    def test_reviewed_supply_replaces_catalog_and_has_no_direct_or_implicit_limit(self):
        value = matrix.build(json.loads(matrix.DEFAULT_INVENTORY.read_text()), matrix.load())
        self.assertEqual(len(value['entries']), 157)
        self.assertEqual(len({e['account_class'] for e in value['entries']}), 15)
        self.assertEqual(sum(e['selection'] == 'model-family-baseline' for e in value['entries']), 54)
        self.assertEqual({e['key_type'] for e in value['entries']}, {'universal'})
        usable = [e for e in value['entries'] if not e['blocked_reason']]
        self.assertGreater(len(usable), 32)
        self.assertEqual(len(matrix.select(value['entries'])), len(usable))
        self.assertEqual(matrix.select(value['entries']), matrix.select(list(reversed(value['entries']))))
        self.assertEqual(len(matrix.select(value['entries'], 2)), 2)
        self.assertEqual(matrix.report(value)['total'], 157)
        with self.assertRaises(ValueError):
            matrix.select(value['entries'], 0)
        # Missing media/tool-roundtrip executors stay visible rather than testing text as media.
        image = next(e for e in value['entries'] if e['scenario'] == 'content-image')
        self.assertIsNone(image['request'])
        self.assertEqual(image['blocked_reason'], 'image_generation_fixture_required')
        self.assertTrue(all(e['blocked_reason'] == 'tool_roundtrip_executor_required'
                            for e in value['entries'] if e['scenario'] == 'tool-roundtrip'))

    def test_all_fixtures_bind_model_and_exact_actions(self):
        for p in matrix.load():
            if p['request']:
                rendered = matrix.render(p['request'], 'vendor/model')
                self.assertNotIn('${model}', json.dumps(rendered))
                self.assertTrue(matrix.valid_path(rendered['path']))
                if p['protocol'] == 'gemini-content':
                    self.assertIn('vendor%2Fmodel:', rendered['path'])

    def test_no_execution_or_harness_result_never_claims_gateway_pass(self):
        value = plan()
        summary = matrix.report(value)
        self.assertEqual(summary['verdict'], 'incomplete')
        self.assertFalse(summary['scope_complete'])
        result = {'plan_sha256': value['plan_sha256'], 'execution_kind': 'harness', 'results': [
            {'id': e['id'], 'case_sha256': e['case_sha256'], 'status': 'passed'} for e in value['entries']]}
        self.assertEqual(matrix.report(value, result)['verdict'], 'incomplete')
        result['execution_kind'] = 'isolated_gateway'
        with self.assertRaisesRegex(ValueError, 'account_class_execution_binding_required'):
            matrix.report(value, result)
        result['results'][0]['case_sha256'] = 'stale'
        with self.assertRaisesRegex(ValueError, 'stale fixture'):
            matrix.report(value, result)

    def test_invalid_inventory_and_unsafe_paths_fail_closed(self):
        with self.assertRaisesRegex(ValueError, 'account supply inventory required'):
            matrix.build({'schema': 1, 'models': [{'id': 'catalog-only'}]}, matrix.load())
        value = inventory(); value['classes'][0]['account_id'] = 1
        with self.assertRaisesRegex(ValueError, 'account class fields'):
            matrix.build(value, matrix.load())
        value = inventory(); value['classes'][0]['native_protocols'] = ['future']
        with self.assertRaisesRegex(ValueError, 'invalid native protocols'):
            matrix.build(value, matrix.load())
        value = inventory(); value['classes'][0]['branch_family'] = 'missing'
        with self.assertRaisesRegex(ValueError, 'branch representative'):
            matrix.build(value, matrix.load())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'matrix.json'
            for profiles in ([], [{'id': 'x'}, {'id': 'x'}]):
                path.write_text(json.dumps({'schema': 1, 'profiles': profiles}))
                with self.assertRaises(ValueError):
                    matrix.load(path)
        value = plan(); value['entries'][0]['request']['path'] = '/api/v1/admin/accounts'
        with self.assertRaises(ValueError):
            matrix.validate_plan(value)

    def test_inventory_permutations_do_not_change_plan_or_delta(self):
        original = json.loads(matrix.DEFAULT_INVENTORY.read_text())
        shuffled = copy.deepcopy(original)
        shuffled['classes'].reverse()
        for cls in shuffled['classes']:
            cls['representatives'].reverse()
            cls['native_protocols'].reverse()
            for rep in cls['representatives']:
                rep['represented_models'].reverse()
        before = matrix.build(original, matrix.load())
        after = matrix.build(shuffled, matrix.load())
        self.assertEqual(before, after)
        self.assertEqual(matrix.delta(after, before), [])
        self.assertNotEqual(original['classes'][0]['id'], shuffled['classes'][0]['id'])

    def test_tag_baseline_is_reused_only_with_identical_generator(self):
        def git_result(args, **kwargs):
            if args[1] == 'rev-parse':
                return subprocess.CompletedProcess(args, 0)
            path = args[2].split(':', 1)[1]
            payload = (matrix.ROOT / path).read_bytes()
            return subprocess.CompletedProcess(args, 0, stdout=payload)
        with patch.object(matrix.subprocess, 'run', side_effect=git_result):
            baseline = matrix.from_tag('1.2.3')
        current = matrix.build(json.loads(matrix.DEFAULT_INVENTORY.read_text()), matrix.load())
        self.assertEqual(current, baseline)

        def old_generator(args, **kwargs):
            result = git_result(args, **kwargs)
            if args[1] == 'show' and args[2].endswith(':ops/stage0/gateway_capability_matrix.py'):
                result.stdout += b'\n# previous generation rules\n'
            return result
        with patch.object(matrix.subprocess, 'run', side_effect=old_generator):
            baseline = matrix.from_tag('1.2.3')
        self.assertIsNone(baseline)
        self.assertEqual(len(matrix.delta(current, baseline)), 157)

    def test_legacy_tag_without_account_supply_has_no_baseline(self):
        completed = subprocess.CompletedProcess([], 0, stdout=b'', stderr=b'')
        missing = subprocess.CompletedProcess([], 128, stdout=b'', stderr=b'')
        with patch.object(matrix.subprocess, 'run', side_effect=[completed, missing, missing]):
            self.assertIsNone(matrix.from_tag('1.2.3'))

    def test_stripping_account_class_cannot_reenable_model_only_execution(self):
        value = plan()
        for case in value['entries']:
            case.pop('account_class')
            case['case_sha256'] = matrix.digest({k: v for k, v in case.items() if k != 'case_sha256'})
        value['plan_sha256'] = matrix.digest({k: v for k, v in value.items() if k != 'plan_sha256'})
        with self.assertRaisesRegex(ValueError, 'invalid case identity'):
            matrix.validate_plan(value)

    def test_offline_cli_reports_full_plan_and_strict_gate_is_explicit(self):
        with tempfile.TemporaryDirectory() as directory, contextlib.redirect_stdout(io.StringIO()):
            root = Path(directory); ip = root/'inventory.json'; pp = root/'plan.json'; rp = root/'report.json'
            ip.write_text(json.dumps(inventory('model-a')))
            with patch('socket.socket', side_effect=AssertionError('offline must not open network')):
                self.assertEqual(main(['plan', '--inventory', str(ip), '--out', str(pp)]), 0)
                self.assertEqual(main(['report', '--plan', str(pp), '--out', str(rp)]), 0)
                self.assertEqual(main(['report', '--plan', str(pp), '--out', str(rp), '--require-complete']), 1)
            self.assertFalse(json.loads(rp.read_text())['deployment_gate'])



class ExecutionTests(unittest.TestCase):
    def test_real_http_assertions_reject_empty_success_errors_and_missing_tools(self):
        case = next(e for e in plan()['entries'] if e['profile'] == 'openai-chat.plain')
        responses = [
            (b'{"choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":1}}', True),
            (b'{"choices":[{}],"usage":{"prompt_tokens":1}}', False),
            (b'{"choices":[{"message":{"content":"OK"}}]}', False),
            (b'{"error":{"type":"upstream_error"}}', False),
        ]
        for body, expected in responses:
            with server(body=body) as (port, received):
                sample = {'row': {'stream': False}, 'path': case['request']['path'], 'body': matrix.encoded(case['request']['body'])}
                result = check.replay.execute(sample, 'private-test-secret', port, 'fixture',
                    validator=lambda *args: check.validate_response(case, *args), budget=1)
                self.assertEqual(result['passed'], expected)
                self.assertNotIn('private-test-secret', json.dumps(result))
                self.assertEqual(received[0][0], '/v1/chat/completions')
        case['request_type'] = 'tool'
        self.assertEqual(check.validate_response(case, 200, 'application/json', responses[0][0], False), 'tool_call_missing')

    def test_token_count_and_sse_errors_are_not_confused_with_generation(self):
        count = {'protocol': 'anthropic-messages', 'request_type': 'count_tokens'}
        self.assertIsNone(check.validate_response(count, 200, 'application/json', b'{"input_tokens":0}', False))
        self.assertEqual(check.validate_response(count, 200, 'application/json', b'{"content":[]}', False), 'token_count_missing')
        plain = {'protocol': 'anthropic-messages', 'request_type': 'plain'}
        raw = b'event: error\ndata: {"error":{"type":"upstream_error"}}\n\n'
        self.assertEqual(check.validate_response(plain, 200, 'text/event-stream', raw, True), 'sse_error_event')
        raw = b'data: {"choices":[{"delta":{"content":"OK"}}],"usage":{"prompt_tokens":1}}\n\ndata: [DONE]\n\n'
        self.assertIsNone(check.validate_response({'protocol':'openai-chat','request_type':'plain'}, 200, 'text/event-stream', raw, True))

    def test_disabled_cli_execution_is_structured_and_has_no_side_effects(self):
        for args in (['run'], ['run', '--plan', '/missing/plan.json', '--tag', '1.2.3',
                               '--bindings', '/missing/keys.json', '--out', '/must-not-write/results.json',
                               '--allow-upstream-quota']):
            with contextlib.redirect_stdout(io.StringIO()) as output, \
                 patch('post_release_replay_check.write', side_effect=AssertionError('must not write')), \
                 patch('pathlib.Path.open', side_effect=AssertionError('must not open files or locks')), \
                 patch('socket.socket', side_effect=AssertionError('must not open network')):
                self.assertEqual(main(args), 2)
            result = json.loads(output.getvalue())
            self.assertEqual(result['execution_blocker'], 'account_class_execution_binding_required')
            self.assertEqual(result['execution'], 'not_run')

    def test_account_class_plan_cannot_succeed_through_unrelated_fallback_account(self):
        with patch.object(check.replay, 'Sandbox') as sandbox, patch.object(check.replay, 'prepared') as prepared:
            with self.assertRaisesRegex(check.replay.ReplayError, 'account_class_execution_binding_required'):
                check.run(plan(), '1.2.3', {'*': {'universal': 1}})
            sandbox.assert_not_called()
            prepared.assert_not_called()

    def test_optional_historical_receipt_does_not_block_normal_staged_approval(self):
        source = (matrix.ROOT/'ops/stage0/deploy_via_ssm_bluegreen.sh').read_text()
        function = source[source.index('validate_replay_gate() {'):source.index('\npromote_prepared_color()')]
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, 'bluegreen-replay.json').write_text('{"verdict":"red"}')
            script = 'set -eu\nROOT="$1"\nAPPROVED_REPLAY=""\n' + function + '\nvalidate_replay_gate\n'
            result = subprocess.run(['bash','-c',script,'test',directory], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            # Explicit continuation still rejects a missing receipt.
            script = 'set -eu\nROOT="$1/missing"\nAPPROVED_REPLAY="sha"\ndie() { return 1; }\n' + function + '\nvalidate_replay_gate\n'
            result = subprocess.run(['bash','-c',script,'test',directory], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()
