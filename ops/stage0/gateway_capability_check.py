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
            return None if any(obj.get('id') or obj.get('task_id') or obj.get('status') for obj in dictionaries) else 'video_task_missing'
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
            evidence = any(
                (isinstance(obj.get(field), str) and obj[field].strip())
                for obj in dictionaries for field in ('thinking', 'reasoning_content', 'reasoning'))
            evidence |= any(type(obj.get(field)) is int and obj[field] > 0 for obj in dictionaries
                            for field in ('reasoning_tokens', 'thoughtsTokenCount'))
            evidence |= any(obj.get('type') in ('reasoning', 'redacted_thinking') or
                            obj.get('thought') is True for obj in dictionaries)
            if not evidence:
                return 'thinking_evidence_missing'
        if case['request_type'] == 'multimodal':
            text = ' '.join(obj[field] for obj in dictionaries for field in ('text', 'content')
                            if isinstance(obj.get(field), str))
            if not re.search(r'\bblue\b', text, re.IGNORECASE):
                return 'vision_answer_mismatch'
        return None
    except (ValueError, TypeError, AttributeError):
        return 'invalid_response_encoding'
