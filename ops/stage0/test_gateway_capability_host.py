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
    def test_blocked_capabilities_cannot_execute_even_when_called_directly(self):
        for status in ('unknown', 'unsupported'):
            source = inventory()
            source['classes'][0]['branches']['vision']['status'] = status
            case = next(e for e in matrix.build(source, matrix.load())['entries'] if e['scenario'] == 'vision')
            with patch.object(host.replay, 'execute', side_effect=AssertionError('must not send')) as send:
                result = host.execute_case(case, {}, '', {}, Mock(), source, 'test', Path('/tmp'))
            self.assertEqual(result, {'status': matrix.blocked_status(case), 'reason': 'capability_' + status})
            send.assert_not_called()

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


    def test_audio_usage_requires_unique_session_and_same_test_key(self):
        case = {**plan()['entries'][0], 'scenario': 'speech'}
        usage = {'account_id': 11, 'api_key_id': 22, 'request_id': 'grok_audio:provider-id', 'session_id': 'unique-call'}
        with patch.object(host, 'rows', side_effect=[[usage], []]) as query:
            actual = host.attribution({'api_key_id': 22}, ['gateway-id'], case, inventory(), {'gateway-id': 'unique-call'})
            self.assertEqual(actual[:3], ([11], None, ['grok_audio:provider-id']))
            self.assertIn("created_at >= now() - interval '10 minutes'", query.call_args_list[0].args[0])
            self.assertIn('UNION ALL SELECT', query.call_args_list[0].args[0])
        for found, expected in [([{**usage, 'api_key_id': 23}], 'wrong_test_key_attribution'),
                                ([usage, {**usage, 'request_id': 'grok_audio:another'}], 'ambiguous_usage_attribution'),
                                ([{**usage, 'session_id': 'other-call'}], 'usage_attribution_missing')]:
            with patch.object(host, 'rows', return_value=found), patch.object(host.time, 'sleep'):
                self.assertEqual(host.attribution({'api_key_id': 22}, ['gateway-id'], case, inventory(),
                                                 {'gateway-id': 'unique-call'})[1], expected)

    def test_audio_session_is_sent_over_real_http(self):
        from test_prod_replay import server
        with server(response_id='gateway-id') as (port, received):
            host.replay.execute({'row': {'stream': False}, 'path': '/test', 'body': b'{}'},
                                'test-key', port, 'marker', session_id='unique-call')
        self.assertEqual(received[0][2]['X-Session-Id'], 'unique-call')


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


    def test_thinking_acceptance_keeps_answer_and_terminal_checks_without_claiming_evidence(self):
        from gateway_capability_check import thinking_evidence
        response = {'object': 'response', 'status': 'completed',
                    'output': [{'type': 'output_text', 'text': '6661'}],
                    'usage': {'input_tokens': 1, 'output_tokens_details': {'reasoning_tokens': 0}}}
        case = {'protocol': 'openai-responses', 'request_type': 'thinking',
                'request': {'thinking_validation': 'request_acceptance', 'expected_answer': '6661'}}
        wire = lambda: json.dumps(response).encode()
        self.assertIsNone(validate_response(case, 200, 'application/json', wire(), False))
        self.assertEqual(thinking_evidence(wire(), False), 'not_observed')
        response['output'][0]['text'] = '16661'
        self.assertEqual(validate_response(case, 200, 'application/json', wire(), False), 'thinking_answer_mismatch')
        response['output'][0]['text'] = '6661'
        response['status'] = 'incomplete'
        self.assertEqual(validate_response(case, 200, 'application/json', wire(), False), 'generation_not_completed')
        response['status'] = 'completed'
        response['usage']['output_tokens_details']['reasoning_tokens'] = 12
        self.assertEqual(thinking_evidence(wire(), False), 'observed')

    def test_thinking_answer_joins_stream_tokens_and_excludes_hidden_reasoning(self):
        from gateway_capability_check import answer_text
        self.assertEqual(answer_text('openai-chat', [{'choices': [{'delta': {'content': x}}]} for x in ('66', '61')], True), '6661')
        self.assertEqual(answer_text('gemini-content', [{'candidates': [{'content': {'parts': [
            {'text': '6661', 'thought': True}, {'text': 'wrong'}]}}]}], False), 'wrong')
        for path in Path(matrix.__file__).parent.glob('fixtures/gateway/*thinking*.json'):
            fixture = json.loads(path.read_text())
            self.assertIn('173*29 + 47*83 - 61*37', json.dumps(fixture['body']))
            self.assertEqual(fixture['expected_answer'], '6661')

    def test_video_pending_operation_and_inline_mp4_completion(self):
        import base64
        case = {**plan()['entries'][0], 'scenario': 'video'}
        mp4 = base64.b64encode(b'\x00\x00\x00\x18ftypmp42' + b'0' * 40).decode()
        completed = {'name': 'projects/test/operations/job', 'done': True,
                     'response': {'videos': [{'bytesBase64Encoded': mp4, 'mimeType': 'video/mp4'}]}}
        for obj in ({'name': 'projects/test/operations/job'}, completed):
            self.assertIsNone(validate_response(case, 200, 'application/json', json.dumps(obj).encode(), False))
        from gateway_capability_check import video_output_present
        for bad in ('invalid!', __import__('base64').b64encode(b'not video').decode()):
            self.assertFalse(video_output_present({'mimeType': 'video/mp4', 'bytesBase64Encoded': bad}))
        for terminal, expected in ((completed, None),
                                   ({**completed, 'response': {}}, 'video_output_missing'),
                                   ({'name': 'operation', 'done': True, 'error': {'message': 'failed'}}, 'video_task_failed')):
            replies = iter([{'id': 'vt_test', 'status': 'queued'}, {'name': 'operation'}, terminal])
            def execute(request, key, port, marker, **kwargs):
                obj = next(replies)
                kwargs['on_response_id'](marker)
                reason = kwargs['validator'](200, 'application/json', json.dumps(obj).encode(), False)
                return {'response_request_id': marker, 'reason': reason}
            with patch.object(host.replay, 'execute', side_effect=execute) as sender, \
                 patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
                 patch.object(host.replay, 'inspect'), patch.object(host, 'candidate_address', return_value='10.0.0.2'), \
                 patch.object(host, 'attribution', return_value=([59], None, ['submit'], True)) as attribute:
                result = host.execute_case(case, {'key': 'test', 'api_key_id': 22}, '10.0.0.2',
                                           {'target': 'green'}, lambda: None, inventory(), 'run', Path('/tmp'))
                self.assertEqual(result['reason'], expected)
                self.assertEqual(sender.call_count, 3)
                self.assertEqual(len(attribute.call_args.args[1]), 1)

    def test_thinking_proof_separates_acceptance_reasoning_and_quality(self):
        case = next(e for e in plan()['entries'] if e['scenario'] == 'thinking')
        for answer, tokens, reason, accepted, quality in (
                ('6661', 0, None, True, 'passed'),
                ('6663', 0, 'thinking_answer_mismatch', True, 'failed'),
                ('6661', 8, None, True, 'passed'),
                ('6661', 0, 'http_status_error', False, 'not_evaluated')):
            def execute(request, key, port, marker, **kwargs):
                wire = {'choices': [{'finish_reason': 'stop', 'message': {'content': answer}}],
                        'usage': {'prompt_tokens': 10, 'completion_tokens': 10,
                                  'completion_tokens_details': {'reasoning_tokens': tokens}}}
                kwargs['on_response_id'](marker)
                return {'reason': kwargs['validator'](400 if reason == 'http_status_error' else 200,
                                                      'application/json', json.dumps(wire).encode(), False)}
            with patch.object(host.replay, 'execute', side_effect=execute), \
                 patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
                 patch.object(host.replay, 'inspect'), patch.object(host, 'candidate_address', return_value='10.0.0.2'), \
                 patch.object(host, 'attribution', return_value=([59], None, ['submit'], True)):
                result = host.execute_case(case, {'key': 'test', 'api_key_id': 22}, '10.0.0.2',
                                           {'target': 'green'}, lambda: None, inventory(), 'run', Path('/tmp'))
            self.assertEqual(result['reason'], reason)
            self.assertEqual(result['status'], 'failed' if reason else 'passed')
            self.assertEqual(result['execution_proof']['thinking_request_accepted'], accepted)
            self.assertEqual(result['execution_proof']['answer_quality'], quality)
            self.assertEqual(result['execution_proof']['thinking_evidence'], 'observed' if tokens else 'not_observed')

    def test_generated_requests_use_valid_operations_and_sufficient_budgets(self):
        value = matrix.build(json.loads(matrix.DEFAULT_INVENTORY.read_text()), matrix.load())
        self.assertFalse(any(c['protocol'] == 'openai-chat' and c['scenario'] == 'count-tokens' for c in value['entries']))
        for case in value['entries']:
            body = case['request']['body']
            if case['scenario'] == 'video':
                self.assertIn(body['seconds'], ('8', '15'))
            if case['request_type'] == 'thinking':
                limit = next((body[k] for k in ('max_tokens', 'max_output_tokens', 'max_completion_tokens') if k in body),
                             body.get('generationConfig', {}).get('maxOutputTokens'))
                self.assertGreater(limit, 8192)
                self.assertEqual(case['request']['expected_answer'], str(173*29 + 47*83 - 61*37))
                self.assertIn('173*29 + 47*83 - 61*37', json.dumps(body))
            elif case['scenario'] in ('plain-buffered', 'plain-stream', 'vision', 'tool-roundtrip'):
                limit = next((body[k] for k in ('max_tokens', 'max_output_tokens', 'max_completion_tokens') if k in body),
                             body.get('generationConfig', {}).get('maxOutputTokens'))
                self.assertGreaterEqual(limit, 2048)
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

    def test_transcription_correlates_ordinary_session_to_usage(self):
        case = {**plan()['entries'][0], 'scenario': 'transcription'}
        with patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
             patch.object(host.replay, 'inspect', return_value={}), \
             patch.object(host, 'candidate_address', return_value='172.18.0.5'), \
             patch.object(host, 'attribution', return_value=([144], None, ['grok_audio:upstream'], True)) as attribute, \
             patch.object(host.replay, 'execute') as execute:
            def send(*args, **kwargs):
                kwargs['on_response_id']('gateway-id')
                return {'reason': None, 'response_request_id': 'gateway-id'}
            execute.side_effect = send
            result = host.execute_case(case, {'key': 'secret', 'api_key_id': 334}, '172.18.0.5', {'target': 'green'},
                                       lambda: None, inventory(), 'test', Path('/unused'))
        self.assertEqual(result['status'], 'passed')
        session = execute.call_args.kwargs['session_id']
        self.assertTrue(session.startswith('test-'))
        self.assertEqual(attribute.call_args.args[-1], {'gateway-id': session})

    def test_full_plan_uses_existing_candidate_and_keeps_failures(self):
        source = inventory()
        source['classes'][0]['branches']['vision']['status'] = 'unsupported'
        source['classes'][0]['branches']['thinking']['status'] = 'unknown'
        value = matrix.build(source, matrix.load())
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(host.replay, 'prepared', return_value=('p'*64, {'target': 'green'})), \
             patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
             patch.object(host.replay, 'inspect', return_value={}), \
             patch.object(host, 'candidate_address', return_value='172.18.0.5'), \
             patch.object(host, 'test_key', return_value={'api_key_id': 334, 'key': 'secret'}), \
             patch.object(host, 'execute_case', return_value={'status': 'failed', 'reason': 'http_status_error',
                 'execution_proof': {'account_class_matched': False}}) as execute, \
             patch.object(host.replay, 'run', side_effect=AssertionError('must not create resources')):
            result = host.run(value, source, '1.2.3', Path(directory))
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

        self.assertEqual(result['coverage']['unsupported'], 1)
        self.assertEqual(result['capability_coverage']['unknown'], 1)
        self.assertEqual({r['status'] for r in details['results']},
                         {'failed', 'unsupported', 'declared-but-untested'})

    def test_selected_rerun_preserves_unselected_obligations(self):
        value = plan()
        selected = [value['entries'][0]['id']]
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(host.replay, 'prepared', return_value=('p'*64, {'target': 'green'})), \
             patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
             patch.object(host.replay, 'inspect', return_value={}), \
             patch.object(host, 'candidate_address', return_value='172.18.0.5'), \
             patch.object(host, 'test_key', return_value={'api_key_id': 334, 'key': 'secret'}), \
             patch.object(host, 'execute_case', return_value={'status': 'failed', 'reason': 'http_status_error'}) as execute:
            result = host.run(value, inventory(), '1.2.3', Path(directory), case_ids=selected)
            details = json.loads(Path(directory, 'bluegreen-capability-results.json').read_text())
        self.assertEqual(execute.call_count, 1)
        self.assertEqual(execute.call_args.args[0]['id'], selected[0])
        self.assertEqual(result['selected_case_ids'], selected)
        self.assertEqual(result['coverage']['declared-but-untested'], len(value['entries']) - 1)
        self.assertEqual(len(details['results']), len(value['entries']))
        self.assertEqual(result['verdict'], 'red')
        self.assertFalse(result['cutover'])
        for ids in ([], ['unknown'], selected * 2):
            with patch.object(host.replay, 'prepared') as prepare, self.assertRaisesRegex(host.replay.ReplayError, 'invalid_case_selection'):
                host.run(value, inventory(), '1.2.3', case_ids=ids)
            prepare.assert_not_called()

    def test_missing_test_key_retains_every_obligation(self):
        source = inventory()
        source['classes'][0]['branches']['vision']['status'] = 'unsupported'
        source['classes'][0]['branches']['thinking']['status'] = 'unknown'
        value = matrix.build(source, matrix.load())
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(host.replay, 'prepared', return_value=('p'*64, {'target': 'green'})), \
             patch.object(host.replay, 'snapshot', return_value={'target': 'green'}), \
             patch.object(host, 'test_key', side_effect=host.replay.ReplayError('unique_active_universal_test_key_required')):
            result = host.run(value, source, '1.2.3', Path(directory))
        self.assertEqual(result['coverage'], {'blocked-by-test-infrastructure': len(value['entries']) - 2,
                                              'unsupported': 1, 'declared-but-untested': 1})
        self.assertFalse(result['cutover'])


if __name__ == '__main__':
    unittest.main()
