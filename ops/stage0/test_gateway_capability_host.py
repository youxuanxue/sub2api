import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import gateway_capability_host as host
import gateway_capability_matrix as matrix
from gateway_capability_scenarios import tool_continuation
from gateway_capability_check import validate_response
from test_gateway_capability_matrix import inventory, plan


class BindingTests(unittest.TestCase):
    def test_same_model_cannot_substitute_another_account_class_or_mapping(self):
        cls = inventory()['classes'][0]
        case = plan()['entries'][0]
        account = {'id': 1, 'platform': 'newapi', 'type': 'apikey', 'channel_type': 1,
                   'supported_protocols': ['chat_completions'],
                   'credentials': {'base_url': 'https://supplier.example/v1', 'model_mapping': {'model-a': 'model-a'}}}
        self.assertTrue(host.account_matches(account, cls, case))
        for field, value in [('channel_type', 17), ('platform', 'openai'), ('type', 'oauth')]:
            changed = copy.deepcopy(account); changed[field] = value
            self.assertFalse(host.account_matches(changed, cls, case))
        for field, value in [('protocol_endpoints_exclusive', True), ('model_mapping', {'model-a': 'other-model'}),
                             ('base_url', 'https://integrate.api.nvidia.com/v1')]:
            changed = copy.deepcopy(account); changed['credentials'][field] = value
            self.assertFalse(host.account_matches(changed, cls, case))

    def test_mutated_key_group_or_account_binding_is_rejected(self):
        box = host.CapabilitySandbox({'Image': 'test'}, plan(), inventory())
        binding = {'account_id': 11, 'api_key_id': 22, 'group_id': 33}
        expected = {'routing_mode': 'universal', 'restricted': True, 'groups': [33], 'accounts': [11]}
        for field, wrong in [('routing_mode', 'direct'), ('restricted', False), ('groups', [33, 34]), ('accounts', [11, 12])]:
            with patch.object(host, 'rows', return_value=[{**expected, field: wrong}]), \
                 self.assertRaisesRegex(host.replay.ReplayError, 'isolated_binding_changed'):
                box.verify_binding(binding)
        with patch.object(host, 'rows', return_value=[expected]):
            box.verify_binding(binding)

    def test_healthy_response_without_correct_usage_cannot_pass(self):
        binding = {'account_id': 11, 'api_key_id': 22}
        good = {'account_id': 11, 'api_key_id': 22, 'request_id': 'server-id'}
        with patch.object(host, 'rows', return_value=[good]):
            self.assertEqual(host.attribution(None, binding, ['server-id'], {'request_type': 'plain'}), ([11], None, ['server-id']))
        with patch.object(host, 'rows', return_value=[{**good, 'account_id': 12}]):
            self.assertEqual(host.attribution(None, binding, ['server-id'], {'request_type': 'plain'})[1], 'wrong_account_or_key')
        with patch.object(host, 'rows', return_value=[]), patch.object(host.time, 'sleep'):
            self.assertEqual(host.attribution(None, binding, ['server-id'], {'request_type': 'plain'})[1], 'usage_attribution_missing')

    def test_spare_capacity_is_required_and_live_state_is_not_reset(self):
        with patch.object(host, 'rows', return_value=[{'healthy': True, 'concurrency': 3}]), \
             patch.object(host, 'live_occupancy', return_value=2):
            self.assertEqual(host.account_guard(11, {}), 'account_headroom_insufficient')
        with patch.object(host, 'rows', return_value=[{'healthy': False, 'concurrency': 30}]), \
             patch.object(host, 'live_occupancy') as occupancy:
            self.assertEqual(host.account_guard(11, {}), 'account_not_healthy')
            occupancy.assert_not_called()


@unittest.skipUnless(os.environ.get('CAPABILITY_TEST_POSTGRES_CONTAINER'), 'requires disposable local PostgreSQL')
class LocalIntegrationTests(unittest.TestCase):
    def setUp(self):
        self.container = os.environ['CAPABILITY_TEST_POSTGRES_CONTAINER']
        self.assertTrue(self.container.startswith('tk-xj-review-'))
        def sql(query, *args):
            return subprocess.check_output(['docker', 'exec', '-i', self.container, 'psql', '-X', '-U', 'postgres',
                '-v', 'ON_ERROR_STOP=1', '-At'], input=query.encode()).decode()
        self.sql = sql

    def test_real_sql_resolves_billing_ids_and_detects_production_writes(self):
        self.sql('CREATE TABLE IF NOT EXISTS usage_logs(account_id bigint, api_key_id bigint, request_id text, created_at timestamptz DEFAULT now()); TRUNCATE usage_logs;')
        self.sql("INSERT INTO usage_logs(account_id,api_key_id,request_id) VALUES (11,22,'local:s1'),(11,22,'grok-video:local:s2');")
        with patch.object(host.replay, 'sql', side_effect=self.sql), patch.object(host.time, 'sleep'):
            self.assertEqual(host.attribution(None, {'account_id': 11, 'api_key_id': 22}, ['s1', 's2'], {'request_type': 'plain'}),
                             ([11], None, ['grok-video:local:s2', 'local:s1']))
            self.assertEqual(host.production_usage_count(['s1', 's2'], 'tk-replay-test', 0), 2)
            self.assertEqual(host.production_usage_count(['unrelated'], 'tk-replay-test', 0), 0)
            self.sql("UPDATE usage_logs SET account_id=12 WHERE request_id='local:s1';")
            self.assertEqual(host.attribution(None, {'account_id': 11, 'api_key_id': 22}, ['s1'], {'request_type': 'plain'})[1],
                             'wrong_account_or_key')
            self.assertEqual(host.attribution(None, {'account_id': 11, 'api_key_id': 22}, ['absent'], {'request_type': 'plain'})[1],
                             'usage_attribution_missing')

    def test_real_orphan_network_is_removed_without_touching_other_containers(self):
        with tempfile.TemporaryDirectory() as directory:
            box = host.CapabilitySandbox({}, plan(), inventory(), Path(directory))
            box.path.mkdir()
            host.replay.run(['docker', 'network', 'create', '--label', 'tokenkey.release-replay=' + box.name, box.name])
            try:
                self.assertFalse(getattr(box, 'network_created', False))
                box.close()
                check = subprocess.run(['docker', 'network', 'inspect', box.name], capture_output=True)
                self.assertNotEqual(check.returncode, 0)
                self.assertEqual(self.sql('SELECT 1;').strip(), '1')
            finally:
                box.close()


class ScenarioTests(unittest.TestCase):
    def test_truncated_or_missing_generation_terminal_cannot_pass(self):
        from test_prod_replay import server
        bodies = {
            'openai-chat': ({'choices': [{'message': {'content': 'partial'}, 'finish_reason': 'length'}],
                            'usage': {'prompt_tokens': 1}}, 'stop'),
            'anthropic-messages': ({'type': 'message', 'content': [{'type': 'text', 'text': 'partial'}],
                                   'stop_reason': 'max_tokens', 'usage': {'input_tokens': 1}}, 'end_turn'),
            'openai-responses': ({'object': 'response', 'status': 'incomplete',
                                 'output': [{'type': 'output_text', 'text': 'partial'}], 'usage': {'input_tokens': 1}}, 'completed'),
            'gemini-content': ({'candidates': [{'content': {'parts': [{'text': 'partial'}]}, 'finishReason': 'MAX_TOKENS'}],
                               'usageMetadata': {'promptTokenCount': 1}}, 'STOP'),
        }
        for protocol, (response, terminal) in bodies.items():
            case = {'protocol': protocol, 'request_type': 'plain'}
            with self.subTest(protocol=protocol), server(body=json.dumps(response).encode()) as (port, _):
                result = host.replay.execute({'row': {'stream': False}, 'body': b'{}', 'path': '/test'},
                    'synthetic', port, 'id', validator=lambda *args: validate_response(case, *args))
                self.assertEqual(result['reason'], 'generation_not_completed')
            obj, key = {'openai-chat': (response.get('choices', [{}])[0], 'finish_reason'),
                        'anthropic-messages': (response, 'stop_reason'),
                        'openai-responses': (response, 'status'),
                        'gemini-content': (response.get('candidates', [{}])[0], 'finishReason')}[protocol]
            obj[key] = terminal
            self.assertIsNone(validate_response(case, 200, 'application/json', json.dumps(response).encode(), False))
            del obj[key]
            self.assertEqual(validate_response(case, 200, 'application/json', json.dumps(response).encode(), False),
                             'generation_terminal_missing')

    def test_thinking_and_vision_cannot_pass_with_generic_text(self):
        response = {'choices': [{'finish_reason': 'stop', 'message': {'content': 'OK'}}], 'usage': {'prompt_tokens': 1}}
        case = {'protocol': 'openai-chat', 'request_type': 'thinking'}
        check = lambda c: validate_response(c, 200, 'application/json', json.dumps(response).encode(), False)
        self.assertEqual(check(case), 'thinking_evidence_missing')
        response['usage']['completion_tokens_details'] = {'reasoning_tokens': 8}
        self.assertIsNone(check(case))
        case['request_type'] = 'multimodal'
        self.assertEqual(check(case), 'vision_answer_mismatch')
        response['choices'][0]['message']['content'] = 'Blue.'
        self.assertIsNone(check(case))

    def test_tool_roundtrip_uses_actual_call_id_and_validated_result(self):
        body = {'messages': [{'role': 'user', 'content': 'Call echo.'}], 'tools': [], 'tool_choice': 'required'}
        response = {'choices': [{'finish_reason': 'tool_calls', 'message': {'role': 'assistant', 'tool_calls': [
            {'id': 'call-real', 'type': 'function', 'function': {'name': 'echo', 'arguments': '{"value":"OK"}'}}]}}]}
        continued = tool_continuation('openai-chat', body, response)
        self.assertEqual(continued['messages'][-1], {'role': 'tool', 'tool_call_id': 'call-real', 'content': 'OK'})
        self.assertEqual(len(body['messages']), 1)
        response['choices'][0]['message']['tool_calls'][0]['function']['name'] = 'shell'
        with self.assertRaisesRegex(ValueError, 'unexpected_tool_arguments'):
            tool_continuation('openai-chat', body, response)

    def test_responses_and_gemini_preserve_real_reasoning_state(self):
        response = {'output': [{'type': 'reasoning', 'encrypted_content': 'opaque'},
                              {'type': 'function_call', 'call_id': 'real', 'name': 'echo', 'arguments': '{"value":"OK"}'}]}
        value = tool_continuation('openai-responses', {'input': 'Call echo.'}, response)
        self.assertEqual(value['input'][1], response['output'][0])
        self.assertEqual(value['input'][-1]['call_id'], 'real')
        content = {'role': 'model', 'parts': [{'functionCall': {'name': 'echo', 'args': {'value': 'OK'}}, 'thoughtSignature': 'opaque'}]}
        value = tool_continuation('gemini-content', {'contents': []}, {'candidates': [{'content': content}]})
        self.assertEqual(value['contents'][0], content)
        self.assertEqual(value['contents'][-1]['parts'][0]['functionResponse']['response'], {'result': 'OK'})

    def test_media_fixtures_and_multipart_are_concrete_and_small(self):
        value = matrix.build(json.loads(matrix.DEFAULT_INVENTORY.read_text()), matrix.load())
        transcription = next(c for c in value['entries'] if c['scenario'] == 'transcription')
        payload, ctype = host.request_wire(transcription['request'])
        self.assertIn('multipart/form-data; boundary=', ctype)
        self.assertIn(b'filename="hello.wav"', payload)
        self.assertIn(b'RIFF', payload)
        self.assertLess(len(payload), 128 * 1024)
        self.assertNotIn(b'audio_base64', payload)
        for c in value['entries']:
            if c['scenario'] in ('image', 'speech', 'video', 'content-image', 'transcription'):
                self.assertIsNone(c['blocked_reason'])
                self.assertIsNotNone(c['request'])


class ExecutionTests(unittest.TestCase):
    def test_capacity_abort_is_not_swallowed_by_database_health_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            box = host.CapabilitySandbox({'Config': {'Env': []}, 'Image': 'test'}, plan(), inventory(), Path(directory))
            with patch.object(Path, 'read_text', return_value='MemAvailable: 8000000'), \
                 patch.object(host.os, 'getloadavg', return_value=(1000, 0, 0)), \
                 patch.object(host.replay.shutil, 'disk_usage', return_value=SimpleNamespace(free=10*1024**3)), \
                 patch.object(host.replay, 'inspect', return_value={'Image': 'test'}), \
                 patch.object(host.replay, 'run'), patch.object(box, 'start_container', return_value='test-pg'), \
                 patch.object(host.time, 'sleep'), \
                 patch.object(host.replay, 'sql', side_effect=lambda *args: host.host_guard()) as sql, \
                 self.assertRaisesRegex(host.HostPressure, 'host_load_headroom'):
                box.start()
            self.assertEqual(sql.call_count, 1)

    def test_cleanup_reconciles_resources_even_without_creation_flags(self):
        with tempfile.TemporaryDirectory() as directory:
            box = host.CapabilitySandbox({}, plan(), inventory(), Path(directory))
            box.path.mkdir()
            box.created = [box.name + '-pg']  # Container creation never completed.
            box.network_created = False  # Network creation completed, but its client was interrupted.
            with patch.object(host.replay, 'run', side_effect=[b'', (box.name+'\n').encode(), b'', b'', b'']) as run:
                box.close()
            self.assertIn(['docker', 'network', 'rm', box.name], [c.args[0] for c in run.call_args_list])
            self.assertFalse(any(c.args[0][:3] == ['docker', 'rm', '-f'] for c in run.call_args_list))
            self.assertFalse(box.path.exists())

    def test_setup_pressure_interrupts_restore_and_restores_deadline_handler(self):
        original = host.signal.getsignal(host.signal.SIGALRM)
        with patch.object(host, 'host_guard', side_effect=host.replay.ReplayError('host_cpu_headroom')), \
             self.assertRaisesRegex(host.replay.ReplayError, 'host_cpu_headroom'):
            with host.guarded_setup():
                handler = host.signal.getsignal(host.signal.SIGALRM)
                handler(host.signal.SIGALRM, None)
        self.assertEqual(host.signal.getsignal(host.signal.SIGALRM), original)
        self.assertEqual(host.signal.getitimer(host.signal.ITIMER_REAL), (0, 0))

    def test_tool_continuation_rechecks_capacity_and_preserves_first_request(self):
        case = next(c for c in plan()['entries'] if c['scenario'] == 'tool-roundtrip')
        binding = {'key': 'synthetic', 'account_id': 1, 'api_key_id': 2}
        box = SimpleNamespace(root=Path('/tmp'), app='test', env={}, candidate={},
            name='tk-replay-test', verify=Mock(), verify_binding=Mock(), response_ids=[])
        response = {'choices': [{'finish_reason': 'tool_calls', 'message': {'role': 'assistant', 'tool_calls': [
            {'id': 'real-call', 'function': {'name': 'echo', 'arguments': '{"value":"OK"}'}}]}}],
            'usage': {'prompt_tokens': 1}}
        def execute(*args, **kwargs):
            self.assertIsNone(kwargs['validator'](200, 'application/json', json.dumps(response).encode(), False))
            kwargs['on_response_id']('first-server-id')
            return {'http_status': 200, 'reason': None, 'response_request_id': 'first-server-id'}
        throttle = Mock()
        with patch.object(host, 'host_guard'), patch.object(host.replay, 'snapshot', return_value={}), \
             patch.object(host.replay, 'inspect', return_value={}), \
             patch.object(host, 'account_guard', side_effect=[None, 'account_headroom_insufficient']), \
             patch.object(host.replay, 'execute', side_effect=execute) as send, \
             self.assertRaisesRegex(host.replay.ReplayError, 'account_headroom_changed'):
            host.execute_case(case, binding, box, 1234, {}, throttle)
        send.assert_called_once()
        self.assertEqual(throttle.call_count, 2)
        self.assertEqual(box.response_ids, ['first-server-id'])
        self.assertEqual(len(box.current_observations), 1)

    def test_rate_limit_stops_the_full_plan_without_dropping_remaining_cases(self):
        value = plan()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            box = SimpleNamespace(start=Mock(return_value=1234), close=Mock(), name='tk-replay-test',
                candidate={}, response_ids=['server-first'],
                bindings={c['id']: {'account_id': 1} for c in value['entries']})
            with patch.object(host.replay, 'prepared', return_value=('p'*64, {'target': 'green'})), \
                 patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
                 patch.object(host.replay, 'inspect', return_value={}), \
                 patch.object(host.replay, 'sql', return_value='0'), \
                 patch.object(host, 'CapabilitySandbox', return_value=box), \
                 patch.object(host, 'host_guard'), patch.object(host, 'account_guard', return_value=None), \
                 patch.object(host, 'execute_case', return_value={'status': 'failed', 'reason': 'http_status_429',
                     'stop_reason': 'upstream_pressure_or_unfinished_request'}) as execute:
                result = host.run(value, inventory(), '1.2.3', root)
            execute.assert_called_once()
            box.close.assert_called_once()
            self.assertEqual(result['verdict'], 'red')
            self.assertEqual(result['coverage']['failed'], 1)
            self.assertEqual(sum(result['coverage'].values()), len(value['entries']))
            details = json.loads((root/'bluegreen-capability-results.json').read_text())
            self.assertEqual({r['id'] for r in details['results']}, {c['id'] for c in value['entries']})
            self.assertTrue(result['cleanup_verified'])

    def test_pressure_before_start_still_emits_all_obligations_and_cleans_up(self):
        value = plan()
        with tempfile.TemporaryDirectory() as directory:
            box = SimpleNamespace(start=Mock(), close=Mock(), name='tk-replay-test', response_ids=[])
            with patch.object(host.replay, 'prepared', return_value=('p'*64, {'target': 'green'})), \
                 patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
                 patch.object(host.replay, 'inspect', return_value={}), \
                 patch.object(host.replay, 'sql', return_value='0'), \
                 patch.object(host, 'CapabilitySandbox', return_value=box), \
                 patch.object(host, 'host_guard', side_effect=host.replay.ReplayError('host_memory_headroom')):
                result = host.run(value, inventory(), '1.2.3', Path(directory))
            box.start.assert_not_called()
            box.close.assert_called_once()
            self.assertEqual(result['coverage'], {'blocked-by-test-infrastructure': len(value['entries'])})
            self.assertEqual(result['reason'], 'host_memory_headroom')


if __name__ == '__main__':
    unittest.main()
