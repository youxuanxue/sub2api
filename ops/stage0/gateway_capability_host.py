"""Run synthetic requests with an existing universal test key on the blue/green candidate.

Uses normal gateway routing and billing. Never creates resources or changes routing.
"""
from __future__ import annotations

import argparse
import base64
import collections
import copy
import fcntl
import json
import ipaddress
import os
import re
import secrets
import signal
import time
from urllib.parse import urlsplit

import prod_replay as replay
import gateway_capability_matrix as matrix
from gateway_capability_check import validate_response, thinking_evidence, objects as response_objects
from gateway_capability_scenarios import tool_continuation

INTERVAL_SECONDS = 10
REQUEST_SECONDS = 90
RUN_SECONDS = 5400
PROTOCOL_NAMES = {'messages': 'anthropic-messages', 'chat_completions': 'openai-chat',
                  'responses': 'openai-responses', 'gemini_generate_content': 'gemini-content'}


def literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def rows(query):
    return [json.loads(line) for line in replay.sql(query).splitlines() if line]


def test_key(name):
    # The configured test key stays on the prod host; never send it through SSM output.
    found = rows(f"""SELECT json_build_object('api_key_id',k.id,'key',k.key) FROM api_keys k
      JOIN users u ON u.id=k.user_id WHERE k.name={literal(name)}
      AND k.deleted_at IS NULL AND u.deleted_at IS NULL AND k.routing_mode='universal'
      AND k.status='active' AND u.status='active' AND u.balance>0
      AND (k.expires_at IS NULL OR k.expires_at>now()) AND (k.quota=0 OR k.quota_used<k.quota);""")
    replay.require(len(found) == 1, 'unique_active_universal_test_key_required')
    return found[0]


def candidate_address(candidate):
    replay.require(candidate['State']['Running'] and
                   candidate['State'].get('Health', {}).get('Status') == 'healthy', 'candidate_not_healthy')
    networks = candidate['NetworkSettings']['Networks']
    replay.require(len(networks) == 1, 'candidate_network_ambiguous')
    address = next(iter(networks.values()))['IPAddress']
    replay.require(ipaddress.ip_address(address).is_private, 'candidate_address_not_private')
    return address


def account_matches(account, cls, case):
    if (account['platform'], account['type'], account['channel_type']) != (
            cls['platform'], cls['auth_type'], cls['channel_type']):
        return False
    creds = account['credentials']
    endpoint = creds.get('base_url', '')
    if not endpoint:
        endpoint = next((v for v in (creds.get('api_base_urls') or {}).values() if isinstance(v, str)), '')
    host = (urlsplit(endpoint).hostname or '').lower()
    if host.endswith('.tokenkey.dev'):
        dialect = 'edge-' + (creds.get('mirror_platform') or account['platform'])
    elif host == 'integrate.api.nvidia.com':
        dialect = 'nvidia-build'
    elif host == 'token-plan.cn-beijing.maas.aliyuncs.com':
        dialect = 'dashscope-token-plan'
    elif account['channel_type'] == 54:
        dialect = 'fmgo'
    else:
        dialect = 'standard'
    native = {PROTOCOL_NAMES[p] for p in account['supported_protocols'] if p in PROTOCOL_NAMES}
    exclusive = creds.get('protocol_endpoints_exclusive')
    exclusive = exclusive is True or isinstance(exclusive, str) and exclusive.strip().lower() == 'true'
    return (dialect == cls['dialect'] and native == set(cls['native_protocols'])
            and exclusive == cls['exclusive_endpoints']
            and (creds.get('model_mapping') or {}).get(case['model']) == case['upstream_model'])


def request_wire(request):
    if request['path'] != '/v1/audio/transcriptions':
        return matrix.encoded(request['body']), 'application/json'
    body = request['body']
    audio = base64.b64decode(body['audio_base64'], validate=True)
    replay.require(audio[:4] == b'RIFF' and len(audio) <= 128 * 1024, 'invalid_audio_fixture')
    boundary = 'tk-capability-' + secrets.token_hex(12)
    chunks = []
    for key in ('model', 'response_format'):
        if key in body:
            chunks.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{body[key]}\r\n'.encode())
    chunks.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="hello.wav"\r\nContent-Type: audio/wav\r\n\r\n'.encode() + audio + b'\r\n')
    chunks.append(f'--{boundary}--\r\n'.encode())
    return b''.join(chunks), 'multipart/form-data; boundary=' + boundary


def attribution(binding, request_ids, case, inventory, sessions=None):
    if case['request_type'] == 'count_tokens':
        return [], None, [], None
    expected = {rid: set(replay.usage_request_ids([rid])) for rid in request_ids}
    quoted = ','.join(literal(rid) for rid in replay.usage_request_ids(request_ids)) or 'NULL'
    # Audio uses an upstream-owned durable billing ID, independent of X-Request-ID.
    # A unique ordinary client session ties that one call to its existing usage row.
    sessions = sessions or {}
    projection = "json_build_object('account_id',account_id,'request_id',request_id,'api_key_id',api_key_id,'session_id',session_id)"
    query = f"SELECT {projection} FROM usage_logs WHERE request_id IN ({quoted})"
    if sessions:
        # session_id has no index: bound its scan by the existing created_at index,
        # separately from the exact request-ID lookup so that lookup stays indexed.
        query += f""" UNION ALL SELECT {projection} FROM usage_logs
            WHERE created_at >= now() - interval '10 minutes'
            AND session_id IN ({','.join(literal(v) for v in sessions.values())})
            AND request_id NOT IN ({quoted})"""
    query += ';'

    for _ in range(10):
        usage = rows(query)
        recorded = {u['request_id'] for u in usage}
        matches = {rid: [u for u in usage if u['request_id'] in ids or
                        (rid in sessions and u.get('session_id') == sessions[rid])]
                   for rid, ids in expected.items()}
        complete = bool(expected) and all(matches.values())
        if complete:
            break
        time.sleep(1)
    observed = sorted({u['account_id'] for u in usage if u['account_id'] is not None})
    if any(u['api_key_id'] != binding['api_key_id'] for u in usage):
        return observed, 'wrong_test_key_attribution', sorted(recorded), None
    if any(len(matches[rid]) > 1 for rid in sessions if rid in matches):
        return observed, 'ambiguous_usage_attribution', sorted(recorded), None
    if not complete or not observed:
        return observed, 'usage_attribution_missing', sorted(recorded), None
    accounts = rows(f"""SELECT row_to_json(t) FROM (
      SELECT a.id,a.platform,a.type,a.channel_type,a.credentials,
        COALESCE(c.supported_protocols,'[]'::jsonb) supported_protocols
      FROM accounts a LEFT JOIN protocol_endpoint_capabilities c ON c.id=a.protocol_endpoint_capability_id
      WHERE a.deleted_at IS NULL AND a.id IN ({','.join(str(int(a)) for a in observed)})) t;""")
    cls = next(c for c in inventory['classes'] if c['id'] == case['account_class'])
    # Normal universal routing chooses the account. Report actual class coverage;
    # never force bindings or pretend a different class was exercised.
    matched = len(accounts) == len(observed) and all(account_matches(a, cls, case) for a in accounts)
    return observed, None, sorted(recorded), matched


def execute_case(case, binding, address, before, throttle, inventory, run_id, root):
    observations = []
    request_ids = []
    final_reason = None
    sessions = {}
    reasoning_evidence = None

    def retain_id(rid):
        request_ids.append(rid)

    def send(request, *, method='POST', validation_case=case):
        nonlocal final_reason, reasoning_evidence
        throttle()
        replay.require(replay.snapshot(root) == before, 'public_or_prepared_state_changed')
        candidate = replay.inspect('tokenkey-' + before['target'])
        replay.require(candidate_address(candidate) == address, 'candidate_address_changed')
        payload, content_type = request_wire(request)
        captured = {}
        marker = run_id + '-' + case['id'][:16] + '-' + str(len(observations))
        session_id = marker if case['scenario'] in ('speech', 'transcription') else None

        def validator(status, ctype, raw, stream):
            nonlocal reasoning_evidence
            if validation_case['request_type'] == 'thinking':
                reasoning_evidence = thinking_evidence(raw, stream)
            # Only the synthetic response remains in host memory for the next tool turn.
            captured['raw'] = bytes(raw)
            return validate_response(validation_case, status, ctype, raw, stream)

        obs = replay.execute({'path': request['path'], 'body': payload, 'row': {'stream': request['stream']}},
            binding['key'], 8080, marker,
            validator=validator, budget=REQUEST_SECONDS, method=method, content_type=content_type,
            on_response_id=retain_id, host=address, session_id=session_id)
        if session_id and obs.get('response_request_id'):
            sessions[obs['response_request_id']] = session_id
        observations.append(obs)
        final_reason = obs['reason']
        raw = captured.get('raw', b'')
        try:
            return json.loads(raw)
        except ValueError:
            return None

    response = send(case['request'])
    if final_reason is None and case['scenario'] == 'tool-roundtrip':
        try:
            follow = copy.deepcopy(case['request'])
            follow['body'] = tool_continuation(case['protocol'], follow['body'], response)
            send(follow, validation_case={**case, 'request_type': 'plain'})
        except (ValueError, KeyError, TypeError):
            final_reason = 'invalid_tool_continuation'
    if final_reason is None and case['scenario'] == 'video':
        task = next((obj[key] for obj in response_objects(response) for key in ('id', 'task_id')
                     if isinstance(obj.get(key), str) and obj[key].startswith('vt_')), None)
        replay.require(isinstance(task, str) and re.fullmatch(r'[A-Za-z0-9_-]{1,160}', task), 'invalid_video_task_id')
        terminal = False
        for _ in range(24):
            response = send({'path': '/v1/videos/' + task, 'body': {}, 'stream': False}, method='GET')
            states = {str(obj.get('status', '')).lower() for obj in response_objects(response)}
            if any(obj.get('done') is True for obj in response_objects(response)):
                states.add('completed')
            state = next((s for s in ('failed', 'failure', 'cancelled', 'canceled',
                                     'success', 'succeeded', 'completed') if s in states), '')
            if state in ('success', 'succeeded', 'completed', 'failed', 'failure', 'cancelled', 'canceled'):
                terminal = True
                if state not in ('success', 'succeeded', 'completed'):
                    final_reason = 'video_task_failed'
                elif not any(isinstance(obj.get(key), str) and urlsplit(obj[key]).scheme == 'https'
                             for obj in response_objects(response) for key in ('url', 'video_url')):
                    final_reason = 'video_output_missing'
                break
            if final_reason:
                break
        if not terminal:
            final_reason = final_reason or 'video_task_still_running'
        # Poll requests are unmetered. Submit attribution owns the generated video.
        request_ids[:] = request_ids[:1]
    observed, reason, usage_ids, matched = attribution(binding, request_ids, case, inventory, sessions) if request_ids else ([], 'response_request_id_missing', [], None)
    final_reason = final_reason or reason
    proof = {'account_class': case['account_class'], 'key_type': 'universal',
             'test_api_key_id': binding['api_key_id'], 'observed_account_ids': observed,
             'account_class_matched': matched, 'request_ids': request_ids, 'usage_request_ids': usage_ids,
             'routing_validation': 'normal_universal',
             'attribution': 'unmetered_endpoint' if case['request_type'] == 'count_tokens' else 'usage_rows'}
    if case['request_type'] == 'thinking':
        proof['thinking_evidence'] = reasoning_evidence or 'not_observed'
        proof['thinking_validation'] = case['request'].get('thinking_validation', 'evidence_required')
    return {'status': 'failed' if final_reason else 'passed', 'reason': final_reason,
            'execution_proof': proof, 'observations': observations}


def run(plan, inventory, tag, root=replay.ROOT, key_name='TK_FULLTEST_KEY', case_ids=None):
    matrix.validate_plan(plan)
    replay.require(plan == matrix.build(inventory, matrix.load()), 'plan_not_current')
    selected = set(case_ids) if case_ids is not None else {c['id'] for c in plan['entries']}
    replay.require(bool(selected) and (case_ids is None or len(selected) == len(case_ids)) and
                   selected <= {c['id'] for c in plan['entries']}, 'invalid_case_selection')
    prepared_sha, before = replay.prepared(tag, root)
    result = {'schema': 1, 'kind': 'account-supply-replay', 'tag': tag,
              'plan_sha256': plan['plan_sha256'], 'prepared_receipt': prepared_sha,
              'execution_kind': 'prepared_gateway', 'concurrency': 1, 'interval_seconds': INTERVAL_SECONDS,
              'cutover': False, 'approval_pending': True, 'deployment_gate': False,
              'route_unchanged': False, 'billing': 'normal_test_key',
              'started_at': time.time(), 'selected_case_ids': sorted(selected), 'results': []}
    reason = None
    next_request = 0.0
    run_id = 'tk-capability-' + secrets.token_hex(8)

    def throttle():
        nonlocal next_request
        time.sleep(max(0, next_request - time.monotonic()))
        next_request = time.monotonic() + INTERVAL_SECONDS

    try:
        binding = test_key(key_name)
        result['test_api_key_id'] = binding['api_key_id']
        address = candidate_address(replay.inspect('tokenkey-' + before['target']))
        for case in plan['entries']:
            item = {'id': case['id'], 'case_sha256': case['case_sha256']}
            if case['id'] not in selected:
                item.update(status='declared-but-untested', reason='not_selected_for_rerun')
            elif case['blocked_reason']:
                item.update(status='unsupported' if case['blocked_reason'] == 'protocol_operation_not_defined'
                            else 'blocked-by-test-infrastructure', reason=case['blocked_reason'])
            else:
                try:
                    item.update(execute_case(case, binding, address, before, throttle, inventory, run_id, root))
                except replay.ReplayError as exc:
                    item.update(status='failed', reason=str(exc), stop_reason=str(exc))
            result['results'].append(item)
            replay.write_json(root / 'bluegreen-capability-progress.json', {'tag': tag,
                'started_at': result['started_at'], 'completed': len(result['results']), 'total': len(plan['entries']),
                'counts': dict(collections.Counter(r['status'] for r in result['results'])), 'cutover': False})
            if item.get('stop_reason'):
                raise replay.ReplayError(item['stop_reason'])
    except (replay.ReplayError, replay.ReplayDeadline, OSError, ValueError, KeyError, TypeError) as exc:
        reason = str(exc) if isinstance(exc, replay.ReplayError) else 'execution_stopped'
    finally:
        signal.alarm(0)
        finished = {r['id'] for r in result['results']}
        result['results'].extend({'id': c['id'], 'case_sha256': c['case_sha256'],
            'status': 'blocked-by-test-infrastructure' if c['id'] in selected else 'declared-but-untested',
            'reason': (reason or 'execution_not_completed') if c['id'] in selected else 'not_selected_for_rerun'}
            for c in plan['entries'] if c['id'] not in finished)
        try:
            result['route_unchanged'] = replay.snapshot(root) == before
            result['cutover'] = False if result['route_unchanged'] else None
        except (replay.ReplayError, OSError, ValueError):
            reason = 'route_verification_failed'
        if not result['route_unchanged']:
            for item in result['results']:
                if item['status'] == 'passed':
                    item.update(status='failed', reason='route_verification_failed')
        result['finished_at'] = time.time()
        result['reason'] = reason
        coverage = matrix.report(plan, result)
        result['verdict'] = 'green' if coverage['scope_complete'] else 'red'
        result['selected_verdict'] = 'green' if result['route_unchanged'] and all(
            r['status'] == 'passed' for r in result['results'] if r['id'] in selected) else 'red'
        replay.write_json(root / 'bluegreen-capability-results.json', result)
        summary = {k: v for k, v in result.items() if k != 'results'}
        summary['coverage'] = coverage['coverage']
        summary['account_class_coverage'] = coverage['account_class_coverage']
        summary['total'] = coverage['total']
        summary['results_sha256'] = matrix.digest(result)
        replay.write_json(root / 'bluegreen-capability-replay.json', replay.seal(summary))
    return replay.seal(summary)


def run_locked(plan, inventory, tag, key_name='TK_FULLTEST_KEY', case_ids=None):
    def timeout(_sig, _frame):
        raise replay.ReplayDeadline()
    with (replay.ROOT / 'bluegreen-deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        signal.signal(signal.SIGALRM, timeout)
        signal.signal(signal.SIGTERM, timeout)
        signal.alarm(RUN_SECONDS)
        return run(plan, inventory, tag, key_name=key_name, case_ids=case_ids)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('run', 'results'))
    parser.add_argument('--tag', required=True)
    parser.add_argument('--test-key-name', default='TK_FULLTEST_KEY',
                        help='prod api_keys.name of an active universal test key (not secrets.TK_FULLTEST_KEY material)')
    parser.add_argument('--offset', type=int, default=0)
    parser.add_argument('--case-id', action='append', help='rerun explicit stable case IDs; full plan remains the coverage denominator')
    args = parser.parse_args()
    replay.require(re.fullmatch(r'\d+\.\d+\.\d+', args.tag), 'invalid_tag')
    os.umask(0o077)
    if args.operation == 'results':
        value = json.loads((replay.ROOT / 'bluegreen-capability-results.json').read_bytes())
        replay.require(value['tag'] == args.tag and args.offset >= 0, 'result_identity_mismatch')
        print(json.dumps({'tag': args.tag, 'rows': value['results'][args.offset:args.offset+5]}))
        return
    inventory = json.loads(matrix.DEFAULT_INVENTORY.read_text())
    plan = matrix.build(inventory, matrix.load())
    print(json.dumps(run_locked(plan, inventory, args.tag, args.test_key_name, args.case_id), separators=(',', ':')))


if __name__ == '__main__':
    try:
        main()
    except (replay.ReplayError, OSError, ValueError, KeyError):
        raise SystemExit('capability replay setup failed; inspect private host state') from None
