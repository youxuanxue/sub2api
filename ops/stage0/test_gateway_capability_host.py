import copy
import json
from pathlib import Path
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
            self.assertEqual(host.attribution(None, binding, ['server-id'], {'request_type': 'plain'}), ([11], None))
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


class ScenarioTests(unittest.TestCase):
    def test_thinking_and_vision_cannot_pass_with_generic_text(self):
        response = {'choices': [{'message': {'content': 'OK'}}], 'usage': {'prompt_tokens': 1}}
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
        response = {'choices': [{'message': {'role': 'assistant', 'tool_calls': [
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
        response = {'choices': [{'message': {'role': 'assistant', 'tool_calls': [
            {'id': 'real-call', 'function': {'name': 'echo', 'arguments': '{"value":"OK"}'}}]}}],
            'usage': {'prompt_tokens': 1}}
        def execute(*args, **kwargs):
            self.assertIsNone(kwargs['validator'](200, 'application/json', json.dumps(response).encode(), False))
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
