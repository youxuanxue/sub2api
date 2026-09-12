#!/usr/bin/env python3
"""Execute synthetic capability fixtures only inside the existing isolated sandbox.

No production request bodies or customer keys are read. Bindings name dedicated
probe key IDs; their actual key type is verified in the snapshot before sending.
"""
from __future__ import annotations

import json
import re
import time

import prod_replay as replay
from gateway_capability_matrix import encoded, delta, select, validate_plan


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


def binding_key(case, bindings, box):
    # Exact model binding takes precedence; a dedicated default may cover a model set.
    binding = bindings.get(case['model'], bindings.get('*', {}))
    key_id = binding.get(case['key_type']) if isinstance(binding, dict) else None
    if type(key_id) is not int or key_id <= 0:
        return None, 'test_key_binding_missing'
    # Database/pg names are supplied by Sandbox, never by the binding file.
    rows = [json.loads(line) for line in replay.sql(
        'SELECT row_to_json(t) FROM (SELECT id,key,name,status,routing_mode FROM api_keys '
        f'WHERE id={key_id} AND deleted_at IS NULL) t;', box.pg, box.database).splitlines()]
    if len(rows) != 1:
        return None, 'test_key_missing'
    key = rows[0]
    if not key['name'].startswith('__tk_probe_'):
        return None, 'dedicated_test_key_required'
    if key['routing_mode'] != case['key_type']:
        return None, 'test_key_type_mismatch'
    if key['status'] != 'active':
        return None, 'test_key_inactive'
    return key['key'], None


def run(plan, tag, bindings, limit=32, previous=None, root=replay.ROOT):
    validate_plan(plan)
    selected = select(delta(plan, previous), limit)
    replay.require(re.fullmatch(r'\d+\.\d+\.\d+', tag), 'invalid_tag')
    replay.require(isinstance(bindings, dict) and bindings, 'test_key_bindings_required')
    _, before = replay.prepared(tag, root)
    report = {'schema': 1, 'plan_sha256': plan['plan_sha256'], 'execution_kind': 'isolated_gateway',
              'tag': tag, 'target_image': before['target_image'], 'cutover': False, 'results': []}
    box = replay.Sandbox(replay.inspect('tokenkey-' + before['target']), root)
    deadline = time.monotonic() + 3600
    try:
        port = box.start()
        for case in selected:
            replay.require(replay.snapshot(root) == before, 'public_or_prepared_state_changed')
            box.verify(replay.inspect(box.app), box.env)
            result = {'id': case['id'], 'case_sha256': case['case_sha256']}
            observed = None
            key, reason = binding_key(case, bindings, box)
            if reason or time.monotonic() >= deadline:
                result.update(status='blocked-by-test-infrastructure', reason=reason or 'run_budget_exceeded')
            else:
                request = case['request']
                sample = {'path': request['path'], 'body': encoded(request['body']),
                          'row': {'stream': request['stream'], 'case_id': case['id']}}
                observed = replay.execute(sample, key, port, box.name + '-' + case['id'][:12],
                                          validator=lambda *args: validate_response(case, *args),
                                          budget=min(120, max(1, deadline - time.monotonic())))
                result.update(status='passed' if observed['passed'] else 'failed', reason=observed['reason'],
                              http_status=observed['http_status'], elapsed_ms=observed['elapsed_ms'])
            if observed and observed.get('response_request_id'):
                result['response_request_id'] = observed['response_request_id']
            report['results'].append(result)
        ids = [r['response_request_id'] for r in report['results'] if r.get('response_request_id')]
        literals = ','.join("'" + ident + "'" for ident in ids) or 'NULL'
        count = int(replay.sql("SELECT count(*) FROM usage_logs WHERE created_at >= now()-interval '3 hours' "
                               "AND (request_id LIKE '" + box.name + "-%' OR request_id IN (" + literals + "));" ).strip())
        replay.require(count == 0, 'production_usage_written')
        report['production_usage_rows'] = count
    finally:
        box.close()
        replay.require(replay.snapshot(root) == before, 'public_or_prepared_state_changed')
    # This file is a coverage result, NEVER a deploy/promote receipt.
    return report
