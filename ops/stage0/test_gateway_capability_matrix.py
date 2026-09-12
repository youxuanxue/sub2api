import copy
import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch, MagicMock

import gateway_capability_matrix as matrix
import gateway_capability_check as check
from post_release_replay_check import main
from test_prod_replay import server


def catalog(*models):
    return {'schema': 1, 'protocols': ['messages', 'chat_completions', 'responses', 'gemini_generate_content'],
            'models': [{'id': name, 'mode': 'chat', 'vendor': 'openai', 'capabilities': ['tool_use']} for name in models]}


def plan(*models):
    return matrix.build(catalog(*(models or ['model-a'])), matrix.load())


class MatrixTests(unittest.TestCase):
    def test_new_model_and_template_changes_create_delta_without_losing_key_type(self):
        before = plan('model-a')
        after = plan('model-a', 'model-z')
        added = matrix.delta(after, before)
        self.assertEqual({e['model'] for e in added}, {'model-z'})
        self.assertEqual({e['key_type'] for e in added}, {'direct', 'universal'})
        self.assertTrue(all(e['selection'] == 'model_baseline' for e in added))
        profiles = matrix.load()
        profiles[0]['request']['body']['max_completion_tokens'] = 32
        changed = matrix.build(catalog('model-a'), profiles)
        self.assertEqual({e['profile'] for e in matrix.delta(changed, before)}, {profiles[0]['id']})
        self.assertEqual(matrix.delta(before, before), [])

    def test_all_fixtures_bind_model_and_exact_actions(self):
        profiles = matrix.load()
        for p in profiles:
            if p['request']:
                rendered = matrix.render(p['request'], 'vendor/model')
                self.assertNotIn('${model}', json.dumps(rendered))
                self.assertTrue(matrix.valid_path(rendered['path']))
                if p['protocol'] == 'gemini-content':
                    self.assertIn('vendor%2Fmodel:', rendered['path'])
        paths = {p['request']['path'] for p in profiles if p['request']}
        self.assertIn('/v1/messages/count_tokens', paths)
        self.assertIn('/v1/responses/input_tokens', paths)
        self.assertIn('/v1beta/models/${model}:countTokens', paths)

    def test_no_execution_or_harness_result_never_claims_gateway_pass(self):
        value = plan()
        summary = matrix.report(value)
        self.assertEqual(summary['verdict'], 'incomplete')
        self.assertFalse(summary['scope_complete'])
        result = {'plan_sha256': value['plan_sha256'], 'execution_kind': 'harness', 'results': [
            {'id': e['id'], 'case_sha256': e['case_sha256'], 'status': 'passed'} for e in value['entries']]}
        self.assertEqual(matrix.report(value, result)['verdict'], 'incomplete')
        result['execution_kind'] = 'isolated_gateway'
        self.assertEqual(matrix.report(value, result)['verdict'], 'passed')
        result['results'][0]['case_sha256'] = 'stale'
        with self.assertRaisesRegex(ValueError, 'stale fixture'):
            matrix.report(value, result)

    def test_budget_does_not_shrink_coverage_and_unknown_modes_remain_gaps(self):
        value = catalog('model-a')
        value['models'][0]['mode'] = 'future-media'
        result = matrix.report(matrix.build(value, matrix.load()))
        self.assertEqual(result['coverage'], {'blocked-by-test-infrastructure': 2})
        self.assertEqual({r['reason'] for r in result['entries']}, {'catalog_mode_has_no_profile'})
        value = plan()
        selected = matrix.select(value['entries'], 2)
        self.assertEqual(len(selected), 2)
        self.assertEqual(matrix.report(value)['total'], len(value['entries']))
        with self.assertRaises(ValueError):
            matrix.select(value['entries'], 0)

    def test_invalid_declarations_and_future_protocols_fail_closed(self):
        value = catalog('model-a'); value['protocols'].append('future')
        with self.assertRaisesRegex(ValueError, 'protocol set changed'):
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

    def test_offline_cli_reports_gaps_and_strict_gate_is_explicit(self):
        with tempfile.TemporaryDirectory() as directory, contextlib.redirect_stdout(io.StringIO()):
            root = Path(directory); cp = root/'catalog.json'; pp = root/'plan.json'; rp = root/'report.json'
            cp.write_text(json.dumps(catalog('model-a')))
            with patch('socket.socket', side_effect=AssertionError('offline must not open network')):
                self.assertEqual(main(['plan', '--catalog', str(cp), '--out', str(pp)]), 0)
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

    def test_key_bindings_require_reserved_key_and_actual_type_in_snapshot(self):
        case = {'model': 'm', 'key_type': 'universal'}
        box = MagicMock(pg='sandbox-pg', database='sandbox')
        self.assertEqual(check.binding_key(case, {}, box), (None, 'test_key_binding_missing'))
        key = {'id': 9, 'key': 'secret', 'name': '__tk_probe_check_key', 'status': 'active', 'routing_mode': 'direct'}
        with patch.object(check.replay, 'sql', return_value=json.dumps(key)) as sql:
            self.assertEqual(check.binding_key(case, {'m': {'universal':9}}, box), (None, 'test_key_type_mismatch'))
            self.assertEqual(sql.call_args.args[1:], ('sandbox-pg', 'sandbox'))
        for changes, reason in [({'routing_mode':'universal'}, None), ({'name':'customer'}, 'dedicated_test_key_required'), ({'status':'quota_exhausted'}, 'test_key_inactive')]:
            value = {**key, 'routing_mode': 'universal', **changes}
            with patch.object(check.replay, 'sql', return_value=json.dumps(value)):
                self.assertEqual(check.binding_key(case, {'m': {'universal':9}}, box)[1], reason)

    def test_isolated_run_uses_synthetic_body_and_checks_production_usage(self):
        value = plan()
        before = {'target': 'green', 'target_image': 'image'}
        box = MagicMock(pg='sandbox-pg', database='sandbox', env={})
        box.name = 'tk-replay-test'
        body = b'{"choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":1}}'
        with server(body=body) as (port, received):
            box.start.return_value = port
            with patch.object(check.replay, 'prepared', return_value=('sha', before)), \
                 patch.object(check.replay, 'snapshot', return_value=before), \
                 patch.object(check.replay, 'inspect'), patch.object(check.replay, 'Sandbox', return_value=box), \
                 patch.object(check, 'binding_key', return_value=('probe-secret', None)), \
                 patch.object(check.replay, 'sql', return_value='0') as sql:
                # Choose the Chat baseline explicitly to match this HTTP response.
                case = next(e for e in value['entries'] if e['profile'] == 'openai-chat.plain')
                with patch.object(check, 'select', return_value=[case]):
                    result = check.run(value, '1.2.3', {'*': {'direct':1}}, limit=1)
                self.assertEqual(result['results'][0]['status'], 'passed')
                self.assertEqual(result['production_usage_rows'], 0)
                self.assertIn('usage_logs', sql.call_args.args[0])
                self.assertNotIn('probe-secret', json.dumps(result))
                self.assertEqual(json.loads(received[0][1]), case['request']['body'])
                box.close.assert_called_once()

    def test_sandbox_is_cleaned_on_execution_error(self):
        value = plan()
        before = {'target': 'green', 'target_image': 'image'}
        box = MagicMock()
        with patch.object(check.replay, 'prepared', return_value=('sha', before)), \
             patch.object(check.replay, 'snapshot', return_value=before), \
             patch.object(check.replay, 'inspect'), patch.object(check.replay, 'Sandbox', return_value=box), \
             patch.object(check, 'binding_key', side_effect=RuntimeError('failed')):
            with self.assertRaises(RuntimeError):
                check.run(value, '1.2.3', {'*': {'direct':1}}, limit=1)
            box.close.assert_called_once()

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
