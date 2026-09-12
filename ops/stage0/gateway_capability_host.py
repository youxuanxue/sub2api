"""Serial account-supply verification on an isolated prepared-image replica.

Only this host process sees snapshot credentials. Production SQL and Redis are
read-only; all test identities and bindings are created before replica startup.
"""
from __future__ import annotations

import argparse
import base64
import collections
from contextlib import contextmanager
import copy
import fcntl
import json
import os
from pathlib import Path
import re
import secrets
import signal
import time
from urllib.parse import urlsplit

import prod_replay as replay
import gateway_capability_matrix as matrix
from gateway_capability_check import validate_response, objects as response_objects
from gateway_capability_scenarios import tool_continuation

INTERVAL_SECONDS = 10
REQUEST_SECONDS = 90
RUN_SECONDS = 5400
PROTOCOL_NAMES = {'messages': 'anthropic-messages', 'chat_completions': 'openai-chat',
                  'responses': 'openai-responses', 'gemini_generate_content': 'gemini-content'}


class HostPressure(replay.ReplayDeadline):
    """Abort the run; ordinary ReplayError health retries must not catch this."""


def literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def rows(query, sandbox=None):
    args = (sandbox.pg, sandbox.database) if sandbox else ()
    return [json.loads(line) for line in replay.sql(query, *args).splitlines() if line]


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


class CapabilitySandbox(replay.Sandbox):
    app_cpus = '0.5'
    postgres_cpus = '0.25'
    def __init__(self, candidate, plan, inventory, root=replay.ROOT):
        super().__init__(candidate, root)
        self.plan, self.inventory = plan, inventory
        self.bindings = {}
        self.response_ids = []

    def configure_database(self):
        # Called by Sandbox.start only after pg_restore and before app startup.
        accounts = rows("""SELECT row_to_json(t) FROM (
          SELECT a.id,a.platform,a.type,a.channel_type,a.credentials,a.concurrency,
                 COALESCE(c.supported_protocols,'[]'::jsonb) supported_protocols
          FROM accounts a LEFT JOIN protocol_endpoint_capabilities c ON c.id=a.protocol_endpoint_capability_id
          WHERE a.deleted_at IS NULL AND a.status='active' AND a.schedulable=true
            AND (a.expires_at IS NULL OR a.expires_at>now())
            AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at<=now())
            AND (a.overload_until IS NULL OR a.overload_until<=now())
            AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until<=now())
            AND EXISTS(SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id
                       WHERE ag.account_id=a.id AND g.status='active' AND g.deleted_at IS NULL)
          ORDER BY a.id) t;""", self)
        classes = {c['id']: c for c in self.inventory['classes']}
        identities = {}
        # Original user keys cannot authenticate to this replica.
        replay.sql("UPDATE api_keys SET status='inactive';", self.pg, self.database)
        for case in self.plan['entries']:
            candidates = [a for a in accounts if account_matches(a, classes[case['account_class']], case)]
            if not candidates:
                continue
            # Prefer unused spare capacity; do not reset cooldowns or fabricate model mappings.
            account = min(candidates, key=lambda a: (live_occupancy(a['id'], self.candidate), a['id']))
            aid = account['id']
            if aid not in identities:
                name = self.name + '-' + str(aid)
                key = 'sk-' + secrets.token_hex(24)
                q = f"""WITH u AS (
                  INSERT INTO users(email,password_hash,role,balance,concurrency,status,restrict_public_groups)
                  VALUES ({literal(name+'@example.invalid')},'!','user',1000,1,'active',true) RETURNING id
                ), g AS (
                  INSERT INTO groups(name,description,platform,rate_multiplier,is_exclusive,status,
                    subscription_type,claude_code_only,allow_messages_dispatch,supported_model_scopes,
                    allow_image_generation,rpm_limit)
                  VALUES ({literal(name)},'isolated capability verification',{literal(account['platform'])},
                    1,true,'active','standard',false,true,'["claude","gemini_text","gemini_image"]'::jsonb,true,6)
                  RETURNING id
                ), entitled AS (
                  INSERT INTO user_allowed_groups(user_id,group_id,created_at) SELECT u.id,g.id,now() FROM u,g
                ), bound AS (
                  INSERT INTO account_groups(account_id,group_id,priority,created_at) SELECT {aid},g.id,1,now() FROM g
                ), k AS (
                  INSERT INTO api_keys(user_id,key,name,status,routing_mode,quota,quota_used)
                  SELECT u.id,{literal(key)},{literal(name)},'active','universal',0,0 FROM u RETURNING id,user_id
                ) SELECT json_build_object('api_key_id',k.id,'user_id',k.user_id,'group_id',g.id) FROM k,g;"""
                identity = rows(q, self)[0]
                identities[aid] = {**identity, 'key': key, 'account_id': aid}
            self.bindings[case['id']] = identities[aid]

    def verify_binding(self, binding):
        q = f"""SELECT json_build_object('routing_mode',k.routing_mode,'restricted',u.restrict_public_groups,
          'groups',(SELECT json_agg(group_id ORDER BY group_id) FROM user_allowed_groups WHERE user_id=u.id),
          'accounts',(SELECT json_agg(account_id ORDER BY account_id) FROM account_groups WHERE group_id={binding['group_id']}))
          FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.id={binding['api_key_id']} AND k.deleted_at IS NULL AND u.deleted_at IS NULL;"""
        found = rows(q, self)
        replay.require(len(found) == 1 and found[0] == {
            'routing_mode': 'universal', 'restricted': True, 'groups': [binding['group_id']],
            'accounts': [binding['account_id']]}, 'isolated_binding_changed')


def live_occupancy(aid, candidate):
    source = replay.environment(candidate)
    script = "return redis.call('ZCARD',KEYS[1])+redis.call('ZCARD',KEYS[2])"
    # ZCARD includes stale leases, deliberately overestimating production usage.
    value = replay.run(['docker', 'exec', '-e', 'REDISCLI_AUTH=' + source.get('REDIS_PASSWORD', ''),
                        'tokenkey-redis', 'redis-cli', '-n', source.get('REDIS_DB', '0'), '--raw',
                        'EVAL', script, '2', f'concurrency:account:{aid}', f'concurrency:live:account:{aid}'])
    return int(value.strip())


def host_guard():
    def require(condition, reason):
        if not condition:
            raise HostPressure(reason)
    available = int(re.search(r'MemAvailable:\s+(\d+)', Path('/proc/meminfo').read_text())[1])
    require(available >= 1024 * 1024, 'host_memory_headroom')
    require(os.getloadavg()[0] < (os.cpu_count() or 1) * .75, 'host_load_headroom')
    pressure = Path('/proc/pressure/memory').read_text()
    full = re.search(r'full avg10=([\d.]+)', pressure)
    require(full is not None and float(full[1]) < .1, 'host_memory_pressure')


@contextmanager
def guarded_setup():
    """Interrupt dump/restore/startup on pressure, preserving the run deadline."""
    previous_handler = signal.getsignal(signal.SIGALRM)
    previous_timer = signal.getitimer(signal.ITIMER_REAL)
    started = time.monotonic()

    def check(_sig, _frame):
        if previous_timer[0] and time.monotonic() - started >= previous_timer[0]:
            raise replay.ReplayDeadline()
        host_guard()

    signal.signal(signal.SIGALRM, check)
    signal.setitimer(signal.ITIMER_REAL, 2, 2)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_handler)
        if previous_timer[0]:
            signal.setitimer(signal.ITIMER_REAL,
                            max(.001, previous_timer[0] - (time.monotonic() - started)), previous_timer[1])


def account_guard(aid, candidate):
    data = rows(f"""SELECT json_build_object('healthy',status='active' AND schedulable=true
      AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>now())
      AND (rate_limit_reset_at IS NULL OR rate_limit_reset_at<=now())
      AND (overload_until IS NULL OR overload_until<=now())
      AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until<=now()),
      'concurrency',concurrency) FROM accounts WHERE id={int(aid)} AND deleted_at IS NULL;""")
    if not data or not data[0]['healthy']:
        return 'account_not_healthy'
    occupied = live_occupancy(aid, candidate)
    if data[0]['concurrency'] - occupied < 2:
        return 'account_headroom_insufficient'
    return None


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


def attribution(sandbox, binding, request_ids, case):
    expected = {rid: set(replay.usage_request_ids([rid])) for rid in request_ids}
    quoted = ','.join(literal(rid) for rid in replay.usage_request_ids(request_ids))
    query = f"""SELECT json_build_object('account_id',account_id,'request_id',request_id,'api_key_id',api_key_id)
        FROM usage_logs WHERE request_id IN ({quoted});"""
    for _ in range(10):
        usage = rows(query, sandbox)
        recorded = {u['request_id'] for u in usage}
        complete = all(ids & recorded for ids in expected.values())
        if case['request_type'] == 'count_tokens' or complete:
            break
        time.sleep(1)
    observed = sorted({u['account_id'] for u in usage})
    if any(u['account_id'] != binding['account_id'] or u['api_key_id'] != binding['api_key_id'] for u in usage):
        return observed, 'wrong_account_or_key', sorted(recorded)
    if case['request_type'] != 'count_tokens' and not complete:
        return observed, 'usage_attribution_missing', sorted(recorded)
    return observed, None, sorted(recorded)


def production_usage_count(response_ids, prefix, started_at):
    quoted = ','.join(literal(rid) for rid in replay.usage_request_ids(response_ids)) or 'NULL'
    return int(replay.sql(f"SELECT count(*) FROM usage_logs WHERE created_at>=to_timestamp({float(started_at)}) AND (request_id IN ({quoted}) OR request_id LIKE {literal(prefix+'%')});").strip())


def execute_case(case, binding, sandbox, port, before, throttle):
    observations = []
    sandbox.current_observations = observations
    request_ids = []
    final_reason = None

    def retain_id(rid):
        request_ids.append(rid)
        sandbox.response_ids.append(rid)

    def send(request, *, method='POST', validation_case=case):
        nonlocal final_reason
        throttle()
        host_guard()
        replay.require(replay.snapshot(sandbox.root) == before, 'public_or_prepared_state_changed')
        sandbox.verify(replay.inspect(sandbox.app), sandbox.env)
        sandbox.verify_binding(binding)
        replay.require(account_guard(binding['account_id'], sandbox.candidate) is None, 'account_headroom_changed')
        payload, content_type = request_wire(request)
        captured = {}

        def validator(status, ctype, raw, stream):
            # Only the synthetic response remains in host memory for the next tool turn.
            captured['raw'] = bytes(raw)
            return validate_response(validation_case, status, ctype, raw, stream)

        obs = replay.execute({'path': request['path'], 'body': payload, 'row': {'stream': request['stream']}},
            binding['key'], port, sandbox.name + '-' + case['id'][:16] + '-' + str(len(observations)),
            validator=validator, budget=REQUEST_SECONDS, method=method, content_type=content_type,
            on_response_id=retain_id)
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
        replay.require(terminal, 'video_task_still_running')
        # Poll requests are unmetered. Submit attribution owns the generated video.
        request_ids[:] = request_ids[:1]
    observed, reason, usage_ids = attribution(sandbox, binding, request_ids, case) if request_ids else ([], 'response_request_id_missing', [])
    final_reason = final_reason or reason
    proof = {'account_class': case['account_class'], 'key_type': 'universal',
             'bound_account_id': binding['account_id'], 'observed_account_ids': observed,
             'request_ids': request_ids, 'usage_request_ids': usage_ids, 'routing_validation':
             'tokenizer_endpoint' if case['request_type'] == 'count_tokens' else
             'media_handler' if case['scenario'] in ('image', 'content-image', 'speech', 'video', 'transcription', 'embedding') else
             'canonical_gateway', 'attribution': 'unmetered_endpoint' if case['request_type'] == 'count_tokens' else 'usage_rows'}
    stop = any(o['http_status'] in (429, 502, 503, 504) or o['reason'] in (
        'request_deadline_exceeded', 'remote_disconnected', 'transport_error',
        'response_incomplete', 'response_byte_budget_exceeded', 'http_protocol_error') for o in observations)
    return {'status': 'failed' if final_reason else 'passed', 'reason': final_reason,
            'stop_reason': 'upstream_pressure_or_unfinished_request' if stop else None,
            'execution_proof': proof, 'observations': observations}


def run(plan, inventory, tag, root=replay.ROOT):
    matrix.validate_plan(plan)
    replay.require(plan == matrix.build(inventory, matrix.load()), 'plan_not_current')
    prepared_sha, before = replay.prepared(tag, root)
    sandbox = CapabilitySandbox(replay.inspect('tokenkey-' + before['target']), plan, inventory, root)
    result = {'schema': 1, 'kind': 'account-supply-replay', 'tag': tag,
              'plan_sha256': plan['plan_sha256'], 'prepared_receipt': prepared_sha,
              'execution_kind': 'isolated_gateway', 'concurrency': 1, 'interval_seconds': INTERVAL_SECONDS,
              'cutover': False, 'approval_pending': True, 'deployment_gate': False,
              'isolation_verified': False, 'cleanup_verified': False,
              'production_usage_rows': None, 'started_at': time.time(), 'results': []}
    reason = None
    next_request = 0.0

    def throttle():
        nonlocal next_request
        time.sleep(max(0, next_request - time.monotonic()))
        next_request = time.monotonic() + INTERVAL_SECONDS

    try:
        host_guard()
        with guarded_setup():
            port = sandbox.start()
        for case in plan['entries']:
            item = {'id': case['id'], 'case_sha256': case['case_sha256']}
            if case['blocked_reason']:
                item.update(status='unsupported' if case['blocked_reason'] == 'protocol_operation_not_defined'
                            else 'blocked-by-test-infrastructure', reason=case['blocked_reason'])
            elif case['id'] not in sandbox.bindings:
                item.update(status='blocked-by-test-infrastructure', reason='matching_live_supply_unavailable')
            elif block := account_guard(sandbox.bindings[case['id']]['account_id'], sandbox.candidate):
                item.update(status='blocked-by-test-infrastructure', reason=block)
            else:
                try:
                    item.update(execute_case(case, sandbox.bindings[case['id']], sandbox, port, before, throttle))
                except replay.ReplayError as exc:
                    observations = getattr(sandbox, 'current_observations', [])
                    item.update(status='failed' if observations else 'blocked-by-test-infrastructure',
                                reason=str(exc), stop_reason=str(exc), observations=observations)
            result['results'].append(item)
            replay.write_json(root / 'bluegreen-capability-progress.json', {'tag': tag,
                'completed': len(result['results']), 'total': len(plan['entries']),
                'counts': dict(collections.Counter(r['status'] for r in result['results'])), 'cutover': False})
            if item.get('stop_reason'):
                raise replay.ReplayError(item['stop_reason'])
    except (replay.ReplayError, replay.ReplayDeadline, OSError, ValueError, KeyError, TypeError) as exc:
        reason = str(exc) if isinstance(exc, (replay.ReplayError, HostPressure)) else 'execution_stopped'
    finally:
        signal.alarm(0)
        finished = {r['id'] for r in result['results']}
        result['results'].extend({'id': c['id'], 'case_sha256': c['case_sha256'],
            'status': 'blocked-by-test-infrastructure', 'reason': reason or 'execution_not_completed'}
            for c in plan['entries'] if c['id'] not in finished)
        try:
            # Lookup both echoed server IDs and the unique client prefix.
            result['production_usage_rows'] = production_usage_count(sandbox.response_ids, sandbox.name, result['started_at'])
            unchanged = replay.snapshot(root) == before
            result['cutover'] = False if unchanged else None
            result['isolation_verified'] = result['production_usage_rows'] == 0 and unchanged
        except (replay.ReplayError, OSError, ValueError):
            reason = 'isolation_verification_failed'
        try:
            sandbox.close()
            result['cleanup_verified'] = True
        except (replay.ReplayError, OSError):
            reason = 'replay_cleanup_failed'
        result['finished_at'] = time.time()
        result['reason'] = reason
        if not result['isolation_verified'] or not result['cleanup_verified']:
            for item in result['results']:
                if item['status'] == 'passed':
                    item.update(status='failed', reason='execution_safety_not_verified')
        coverage = matrix.report(plan, result)
        result['verdict'] = 'green' if coverage['scope_complete'] else 'red'
        replay.write_json(root / 'bluegreen-capability-results.json', result)
        summary = {k: v for k, v in result.items() if k != 'results'}
        summary['coverage'] = coverage['coverage']
        summary['total'] = coverage['total']
        summary['results_sha256'] = matrix.digest(result)
        replay.write_json(root / 'bluegreen-capability-replay.json', replay.seal(summary))
    return replay.seal(summary)


def run_locked(plan, inventory, tag):
    def timeout(_sig, _frame):
        raise replay.ReplayDeadline()
    with (replay.ROOT / 'bluegreen-deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        signal.signal(signal.SIGALRM, timeout)
        signal.signal(signal.SIGTERM, timeout)
        signal.alarm(RUN_SECONDS)
        return run(plan, inventory, tag)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('run', 'results'))
    parser.add_argument('--tag', required=True)
    parser.add_argument('--offset', type=int, default=0)
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
    print(json.dumps(run_locked(plan, inventory, args.tag), separators=(',', ':')))


if __name__ == '__main__':
    try:
        main()
    except (replay.ReplayError, OSError, ValueError, KeyError):
        raise SystemExit('capability replay setup failed; inspect private host state') from None
