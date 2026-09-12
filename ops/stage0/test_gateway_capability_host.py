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

    def test_key_requires_one_active_universal_identity(self):
        for found in ([], [{'api_key_id': 1}, {'api_key_id': 2}]):
            with patch.object(host, 'rows', return_value=found), self.assertRaisesRegex(host.replay.ReplayError, 'unique_active'):
                host.test_key('test')
        with patch.object(host, 'rows', return_value=[{'api_key_id': 1, 'key': 'secret'}]) as query:
            self.assertEqual(host.test_key("test'key")['api_key_id'], 1)
            sql = query.call_args.args[0]
            self.assertIn("k.routing_mode='universal'", sql)
            self.assertIn("k.name='test''key'", sql)
            self.assertTrue(sql.startswith('SELECT'))

    def test_usage_verifies_test_key_and_reports_normal_account_selection(self):
        cls = inventory()['classes'][0]; case = plan()['entries'][0]
        account = {'id': 11, 'platform': 'newapi', 'type': 'apikey', 'channel_type': 1,
                   'supported_protocols': ['chat_completions'],
                   'credentials': {'base_url': 'https://supplier.example/v1', 'model_mapping': {'model-a': 'model-a'}}}
        usage = {'account_id': 11, 'api_key_id': 22, 'request_id': 'local:server-id'}
        with patch.object(host, 'rows', side_effect=[[usage], [account]]):
            self.assertEqual(host.attribution({'api_key_id': 22}, ['server-id'], case, inventory()),
                             ([11], None, ['local:server-id'], True))
        account['channel_type'] = 17
        with patch.object(host, 'rows', side_effect=[[usage], [account]]):
            observed, reason, ids, matched = host.attribution({'api_key_id': 22}, ['server-id'], case, inventory())
            self.assertIsNone(reason)  # Ordinary universal fallback remains a functional success.
            self.assertFalse(matched)  # It cannot prove the originally planned account class.
        with patch.object(host, 'rows', return_value=[{**usage, 'api_key_id': 23}]):
            self.assertEqual(host.attribution({'api_key_id': 22}, ['server-id'], case, inventory())[1], 'wrong_test_key_attribution')
        with patch.object(host, 'rows', return_value=[]), patch.object(host.time, 'sleep'):
            self.assertEqual(host.attribution({'api_key_id': 22}, ['server-id'], case, inventory())[1], 'usage_attribution_missing')



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
    def test_candidate_address_requires_healthy_single_private_network(self):
        candidate = {'State': {'Running': True, 'Health': {'Status': 'healthy'}},
                     'NetworkSettings': {'Networks': {'private': {'IPAddress': '172.18.0.5'}}}}
        self.assertEqual(host.candidate_address(candidate), '172.18.0.5')
        candidate['NetworkSettings']['Networks']['private']['IPAddress'] = '8.8.8.8'
        with self.assertRaisesRegex(host.replay.ReplayError, 'candidate_address_not_private'):
            host.candidate_address(candidate)
        candidate['State']['Running'] = False
        with self.assertRaisesRegex(host.replay.ReplayError, 'candidate_not_healthy'):
            host.candidate_address(candidate)

    def test_route_change_blocks_send_before_key_leaves_host(self):
        case = plan()['entries'][0]
        with patch.object(host.replay, 'snapshot', return_value={'target': 'blue'}), \
             patch.object(host.replay, 'execute') as send, self.assertRaisesRegex(host.replay.ReplayError, 'public_or_prepared'):
            host.execute_case(case, {'key': 'secret', 'api_key_id': 1}, '172.18.0.5', {'target': 'green'},
                              lambda: None, inventory(), 'test', Path('/unused'))
        send.assert_not_called()

    def test_http_errors_and_timeouts_are_recorded_without_skipping_suite(self):
        case = plan()['entries'][0]
        for observation in ({'reason': 'http_status_error', 'http_status': 503},
                            {'reason': 'request_deadline_exceeded', 'http_status': 0}):
            with patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
                 patch.object(host.replay, 'inspect', return_value={}), \
                 patch.object(host, 'candidate_address', return_value='172.18.0.5'), \
                 patch.object(host.replay, 'execute', return_value=observation) as send:
                result = host.execute_case(case, {'key': 'secret', 'api_key_id': 1}, '172.18.0.5', {'target': 'green'},
                                           lambda: None, inventory(), 'test', Path('/unused'))
            self.assertEqual(send.call_args.kwargs['host'], '172.18.0.5')
            self.assertEqual(send.call_args.args[2], 8080)
            self.assertEqual(result['status'], 'failed')
            self.assertIsNone(result.get('stop_reason'))

    def test_full_plan_uses_existing_candidate_and_keeps_failures(self):
        value = plan()
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(host.replay, 'prepared', return_value=('p'*64, {'target': 'green'})), \
             patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
             patch.object(host.replay, 'inspect', return_value={}), \
             patch.object(host, 'candidate_address', return_value='172.18.0.5'), \
             patch.object(host, 'test_key', return_value={'api_key_id': 334, 'key': 'secret'}), \
             patch.object(host, 'execute_case', return_value={'status': 'failed', 'reason': 'http_status_error',
                 'execution_proof': {'account_class_matched': False}}) as execute, \
             patch.object(host.replay, 'run', side_effect=AssertionError('must not create resources')):
            result = host.run(value, inventory(), '1.2.3', Path(directory))
            details = json.loads(Path(directory, 'bluegreen-capability-results.json').read_text())
        self.assertEqual(execute.call_count, sum(not c['blocked_reason'] for c in value['entries']))
        self.assertEqual(len(details['results']), len(value['entries']))
        self.assertEqual(result['execution_kind'], 'prepared_gateway')
        self.assertTrue(result['route_unchanged'])
        self.assertFalse(result['cutover'])
        self.assertTrue(result['approval_pending'])
        self.assertEqual(result['account_class_coverage'],
                         {'matched': 0, 'unmatched': 0, 'unmetered_or_absent': 0})
        self.assertNotIn('secret', json.dumps(details))

    def test_missing_test_key_retains_every_obligation(self):
        value = plan()
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(host.replay, 'prepared', return_value=('p'*64, {'target': 'green'})), \
             patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
             patch.object(host, 'test_key', side_effect=host.replay.ReplayError('unique_active_universal_test_key_required')):
            result = host.run(value, inventory(), '1.2.3', Path(directory))
        self.assertEqual(result['coverage'], {'blocked-by-test-infrastructure': len(value['entries'])})
        self.assertFalse(result['cutover'])


if __name__ == '__main__':
    unittest.main()
