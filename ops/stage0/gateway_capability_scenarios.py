"""Small synthetic media requests and protocol-preserving tool continuations."""
from __future__ import annotations

import copy
import json
from urllib.parse import quote


def media_request(operation, model, audio_base64=''):
    bodies = {
        'image': ('/v1/images/generations', {'model': model, 'prompt': 'A solid blue square.', 'n': 1,
                  'size': '2048x2048' if 'seedream' in model else '1024x1024'}),
        'speech': ('/v1/audio/speech', {'model': model, 'input': 'Hello.', 'response_format': 'mp3',
                   'voice': 'zh_female_vv_uranus_bigtts' if model == 'doubao-seed-tts-2.0' else 'longanlingxin'}),
        'video': ('/v1/videos', {'model': model, 'prompt': 'A static blue square on a white background.',
                  'seconds': '8' if model.startswith('veo') else '15', 'size': '1280x720'}),
        'content-image': ('/v1beta/models/' + quote(model, safe='') + ':generateContent', {
            'contents': [{'role': 'user', 'parts': [{'text': 'Generate an image of a solid blue square.'}]}],
            'generationConfig': {'responseModalities': ['TEXT', 'IMAGE']}}),
        'transcription': ('/v1/audio/transcriptions', {'model': model, 'audio_base64': audio_base64,
                                                     'response_format': 'json'}),
    }
    if operation not in bodies:
        return None
    path, body = bodies[operation]
    return {'method': 'POST', 'path': path, 'body': body, 'stream': False,
            'assertions': ['protocol_envelope', 'no_error', 'terminal', 'usage']}


def tool_continuation(protocol, request_body, response):
    """Return the actual model tool call with its result; never fabricate a call ID."""
    body = copy.deepcopy(request_body)

    def result(name, arguments):
        if isinstance(arguments, str):
            arguments = json.loads(arguments)
        if name != 'echo' or not isinstance(arguments, dict) or arguments.get('value') != 'OK':
            raise ValueError('unexpected_tool_arguments')
        return 'OK'

    if protocol == 'openai-chat':
        message = response['choices'][0]['message']
        calls = message.get('tool_calls', [])
        if not calls:
            raise ValueError('tool_call_missing')
        body['messages'].append(message)
        for call in calls:
            output = result(call['function']['name'], call['function']['arguments'])
            body['messages'].append({'role': 'tool', 'tool_call_id': call['id'], 'content': output})
        body['tool_choice'] = 'none'
    elif protocol == 'anthropic-messages':
        content = response['content']
        calls = [c for c in content if c.get('type') == 'tool_use']
        if not calls:
            raise ValueError('tool_call_missing')
        body['messages'].append({'role': 'assistant', 'content': content})
        body['messages'].append({'role': 'user', 'content': [
            {'type': 'tool_result', 'tool_use_id': c['id'], 'content': result(c['name'], c['input'])}
            for c in calls]})
        body.pop('tool_choice', None)
        body.pop('tools', None)
    elif protocol == 'openai-responses':
        output = response['output']
        calls = [c for c in output if c.get('type') == 'function_call']
        if not calls:
            raise ValueError('tool_call_missing')
        original = body['input']
        if isinstance(original, str):
            original = [{'role': 'user', 'content': original}]
        body['input'] = original + output + [
            {'type': 'function_call_output', 'call_id': c['call_id'],
             'output': result(c['name'], c['arguments'])} for c in calls]
        body['tool_choice'] = 'none'
    elif protocol == 'gemini-content':
        content = response['candidates'][0]['content']
        calls = [p['functionCall'] for p in content['parts'] if p.get('functionCall')]
        if not calls:
            raise ValueError('tool_call_missing')
        body['contents'].append(content)  # Preserve thought signatures on the real model turn.
        body['contents'].append({'role': 'user', 'parts': [
            {'functionResponse': {'name': c['name'], 'response': {'result': result(c['name'], c['args'])}}}
            for c in calls]})
        body['toolConfig'] = {'functionCallingConfig': {'mode': 'NONE'}}
    else:
        raise ValueError('unsupported_tool_protocol')
    return body
