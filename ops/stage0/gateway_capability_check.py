#!/usr/bin/env python3
"""Validate synthetic HTTP responses; production execution lives in the host runner."""
from __future__ import annotations

import json
import base64
import re
from urllib.parse import urlsplit

import prod_replay as replay


def objects(value):
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from objects(child)
    elif isinstance(value, list):
        for child in value:
            yield from objects(child)


def generation_terminal(protocol, events, stream):
    if protocol == 'openai-chat':
        reasons = [c.get('finish_reason') for e in events for c in e.get('choices', [])
                   if c.get('finish_reason') is not None]
        allowed = {'stop', 'tool_calls', 'function_call'}
    elif protocol == 'anthropic-messages':
        reasons = [obj['stop_reason'] for e in events for obj in objects(e)
                   if obj.get('stop_reason') is not None]
        allowed = {'end_turn', 'tool_use', 'stop_sequence'}
    elif protocol == 'openai-responses':
        reasons = [obj['status'] for e in events for obj in objects(e)
                   if obj.get('object') == 'response' and obj.get('status') is not None]
        allowed = {'completed'}
    elif protocol == 'gemini-content':
        reasons = [c.get('finishReason') for e in events for c in e.get('candidates', [])
                   if c.get('finishReason') is not None]
        allowed = {'STOP'}
    else:
        return None
    # Streaming may contain intermediate in_progress response snapshots.
    if stream and protocol == 'openai-responses':
        reasons = [r for r in reasons if r not in ('queued', 'in_progress')]
    if any(r not in allowed for r in reasons):
        return 'generation_not_completed'
    # response_failure already verifies stream termination, including [DONE].
    if not stream and not reasons:
        return 'generation_terminal_missing'
    return None


def answer_text(protocol, events, stream):
    # Read user-visible output only, and join incremental tokens without spaces.
    if protocol == 'openai-chat':
        return ''.join(choice.get('delta' if stream else 'message', {}).get('content') or ''
                       for event in events for choice in event.get('choices', []))
    if protocol == 'anthropic-messages':
        if stream:
            return ''.join(event.get('delta', {}).get('text', '') for event in events)
        return ''.join(part.get('text', '') for event in events for part in event.get('content', [])
                       if part.get('type') == 'text')
    if protocol == 'gemini-content':
        return ''.join(part.get('text', '') for event in events for candidate in event.get('candidates', [])
                       for part in candidate.get('content', {}).get('parts', []) if not part.get('thought'))
    if protocol == 'openai-responses':
        if stream:
            return ''.join(event.get('delta', '') for event in events if event.get('type') == 'response.output_text.delta')
        return ''.join(obj.get('text', '') for event in events for obj in objects(event)
                       if obj.get('type') == 'output_text')
    return ''


def validate_response(case, status, ctype, raw, stream):
    failure = replay.response_failure(status, ctype, raw, stream)
    if failure:
        return failure
    try:
        if case.get('scenario') == 'speech':
            return None if ctype.startswith('audio/') and len(raw) > 32 and (
                raw[:3] == b'ID3' or raw[:1] == b'\xff' or raw[:4] in (b'RIFF', b'OggS', b'fLaC')) else 'audio_payload_missing'
        events = ([json.loads(data) for _, data in replay.sse_events(raw) if data and data != '[DONE]']
                  if stream else [json.loads(raw)])
        dictionaries = [obj for event in events for obj in objects(event)]
        if case.get('scenario') in ('image', 'content-image'):
            for obj in dictionaries:
                encoded = obj.get('b64_json') or (obj.get('data') if str(obj.get('mimeType', '')).startswith('image/') else None)
                if isinstance(encoded, str):
                    decoded = base64.b64decode(encoded, validate=True)
                    if decoded.startswith((b'\x89PNG\r\n', b'\xff\xd8\xff', b'RIFF')):
                        return None
                url = obj.get('url')
                if isinstance(url, str) and urlsplit(url).scheme == 'https' and urlsplit(url).hostname:
                    return None
            return 'image_payload_missing'
        if case.get('scenario') == 'video':
            return None if any(obj.get('id') or obj.get('task_id') or obj.get('status') or obj.get('name') for obj in dictionaries) else 'video_task_missing'
        if case.get('scenario') == 'transcription':
            return None if any(isinstance(obj.get('text'), str) and obj['text'].strip() for obj in dictionaries) else 'transcription_text_missing'
        if case['request_type'] == 'count_tokens':
            return None if any(type(obj.get(field)) is int and obj[field] >= 0
                               for obj in dictionaries for field in ('input_tokens', 'totalTokens')) else 'token_count_missing'
        protocol = case['protocol']
        envelope = {
            'openai-chat': any(isinstance(e, dict) and isinstance(e.get('choices'), list) and e['choices'] for e in events),
            'openai-responses': any(obj.get('object') == 'response' or obj.get('type', '').startswith('response.') for obj in dictionaries),
            'anthropic-messages': any(obj.get('type') in ('message', 'message_start') for obj in dictionaries),
            'gemini-content': any(isinstance(e, dict) and isinstance(e.get('candidates'), list) and e['candidates'] for e in events),
            'openai-embeddings': any(isinstance(obj.get('embedding'), list) and obj['embedding'] for obj in dictionaries),
        }.get(protocol, False)
        if not envelope:
            return 'protocol_envelope_missing'
        terminal = generation_terminal(protocol, events, stream)
        if terminal:
            return terminal
        if not any(type(obj.get(field)) is int and obj[field] >= 0 for obj in dictionaries
                   for field in ('input_tokens', 'prompt_tokens', 'promptTokenCount', 'total_tokens')):
            return 'usage_missing'
        if protocol != 'openai-embeddings' and case['request_type'] != 'tool' and not any(
                isinstance(obj.get(field), str) and obj[field] for obj in dictionaries for field in ('text', 'content', 'refusal')):
            return 'semantic_output_missing'
        if case['request_type'] == 'tool':
            tool = any(obj.get('type') in ('tool_use', 'function_call') or obj.get('tool_calls') or obj.get('functionCall') for obj in dictionaries)
            if not tool:
                return 'tool_call_missing'
        if case['request_type'] == 'thinking':
            request = case.get('request', {})
            answer = request.get('expected_answer')
            text = answer_text(protocol, events, stream)
            if answer and not re.search(r'(?<![\w.])' + re.escape(answer) + r'(?![\w.])', text):
                return 'thinking_answer_mismatch'
            if not has_thinking_evidence(dictionaries) and request.get('thinking_validation') != 'request_acceptance':
                return 'thinking_evidence_missing'
        if case['request_type'] == 'multimodal':
            text = ' '.join(obj[field] for obj in dictionaries for field in ('text', 'content')
                            if isinstance(obj.get(field), str))
            expected = (case.get('request') or {}).get('expected_visual_answer', 'blue')
            if not isinstance(expected, str) or not expected.strip():
                return 'vision_answer_mismatch'
            colors = re.findall(r'\b(?:blue|red|green|yellow|pink|brown|white|black|orange|purple)\b', text, re.IGNORECASE)
            if not colors or any(color.lower() != expected.strip().lower() for color in colors):
                return 'vision_answer_mismatch'
        return None
    except (ValueError, TypeError, AttributeError):
        return 'invalid_response_encoding'


def video_output_present(response):
    for obj in objects(response):
        for key in ('url', 'video_url', 'uri'):
            url = obj.get(key)
            if isinstance(url, str) and urlsplit(url).scheme == 'https' and urlsplit(url).hostname:
                return True
        encoded = obj.get('bytesBase64Encoded')
        if obj.get('mimeType') == 'video/mp4' and isinstance(encoded, str):
            try:
                decoded = base64.b64decode(encoded, validate=True)
                if len(decoded) > 32 and decoded[4:8] == b'ftyp':
                    return True
            except ValueError:
                continue
    return False


def has_thinking_evidence(dictionaries):
    """Actual reasoning output/usage, not an echoed reasoning configuration."""
    return any(
        any(isinstance(obj.get(field), str) and obj[field].strip()
            for field in ('thinking', 'reasoning_content', 'reasoning')) or
        any(type(obj.get(field)) is int and obj[field] > 0
            for field in ('reasoning_tokens', 'thoughtsTokenCount')) or
        obj.get('type') in ('reasoning', 'redacted_thinking') or obj.get('thought') is True
        for obj in dictionaries)


def thinking_evidence(raw, stream):
    try:
        events = ([json.loads(data) for _, data in replay.sse_events(raw) if data and data != '[DONE]']
                  if stream else [json.loads(raw)])
        return 'observed' if has_thinking_evidence([obj for event in events for obj in objects(event)]) else 'not_observed'
    except (ValueError, TypeError):
        return 'not_observed'
