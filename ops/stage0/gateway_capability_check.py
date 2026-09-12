#!/usr/bin/env python3
"""Validate synthetic HTTP fixtures; account-class execution is not implemented."""
from __future__ import annotations

import json

import prod_replay as replay
from gateway_capability_matrix import EXECUTION_BLOCKER, validate_plan


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
        events = ([json.loads(data) for _, data in replay.sse_events(raw) if data and data != '[DONE]']
                  if stream else [json.loads(raw)])
        dictionaries = [obj for event in events for obj in objects(event)]
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
        return None
    except (ValueError, TypeError, AttributeError):
        return 'invalid_response_encoding'



def run(plan, tag, bindings, limit=None, previous=None, root=replay.ROOT):
    """Reject before any I/O; model-only fallback cannot prove account-class coverage."""
    validate_plan(plan)
    raise replay.ReplayError(EXECUTION_BLOCKER)
