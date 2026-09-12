#!/usr/bin/env python3
"""Prod-host retained request replay. Never writes the public route.

Only the compact receipt leaves the host. Captures, credentials and responses
stay off stdout; private temporary data and Docker resources are removed in a
finally block. The prepared color stays available for reviewed promotion.
"""
from __future__ import annotations

import argparse
import collections
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import subprocess
import time
from urllib.parse import urlsplit

ROOT = Path('/var/lib/tokenkey')
IMAGE = 'ghcr.io/youxuanxue/sub2api'
MAX_BYTES = 16 * 1024 * 1024
MAX_SAMPLES = 200
MAX_CORPUS_BYTES = 64 * 1024 * 1024
REPLAY_SECONDS = 5400


class ReplayError(RuntimeError):
    pass


class ReplayDeadline(Exception):
    """Must bypass per-capture and health retries and enter teardown immediately."""


def require(condition, reason):
    if not condition:
        raise ReplayError(reason)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def run(args, **kwargs):
    # Never propagate subprocess stderr/arguments: they may contain credentials.
    try:
        return subprocess.run(args, check=True, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, timeout=300, **kwargs).stdout
    except (subprocess.SubprocessError, OSError) as exc:
        raise ReplayError('host_command_failed') from exc


def inspect(name):
    return json.loads(run(['docker', 'inspect', name]))[0]


def environment(state):
    return dict(item.split('=', 1) for item in state['Config']['Env'] if '=' in item)


def fingerprint(state):
    return f"{state['Id']} {state['Image']} {state['State']['StartedAt']} {state['RestartCount']}"


def snapshot(root=ROOT):
    active = (root / 'active-color').read_text().strip()
    require(active in ('blue', 'green'), 'requires_bluegreen_prod')
    target = 'green' if active == 'blue' else 'blue'
    live, candidate = inspect('tokenkey-' + active), inspect('tokenkey-' + target)
    route = (root / 'caddy/Caddyfile').read_bytes()
    routes = set(re.findall(rb'tokenkey-(blue|green):8080', route))
    require(routes == {active.encode()}, 'caddy_active_mismatch')
    require(candidate['State']['Running'], 'candidate_not_running')
    return {'active': active, 'target': target,
            'active_container': fingerprint(live), 'target_container': fingerprint(candidate),
            'target_image': candidate['Config']['Image'], 'caddy_sha': digest(route),
            'compose_sha': digest((root / 'docker-compose.bluegreen.yml').read_bytes()),
            'env_sha': digest((root / '.env').read_bytes())}


def prepared(tag, root=ROOT):
    raw = (root / 'bluegreen-prepared.json').read_bytes()
    value = json.loads(raw)
    require(value['tag'] == tag, 'prepared_tag_mismatch')
    require(value['target_image'] == IMAGE + ':' + tag, 'prepared_image_mismatch')
    current = snapshot(root)
    require(all(value.get(k) == v for k, v in current.items()), 'prepared_state_changed')
    return digest(raw), current


def write_json(path, value):
    tmp = path.with_suffix('.tmp')
    tmp.write_bytes(encoded(value) + b'\n')
    tmp.chmod(0o600)
    tmp.replace(path)


def seal(receipt):
    receipt = dict(receipt)
    receipt.pop('receipt_sha256', None)
    receipt['receipt_sha256'] = digest(encoded(receipt))
    return receipt


def validate_receipt(receipt, expected, prepared_sha, tag):
    require(re.fullmatch(r'[a-f0-9]{64}', expected or ''), 'approval_receipt_required')
    require(seal(receipt)['receipt_sha256'] == expected == receipt.get('receipt_sha256'), 'replay_receipt_mismatch')
    require(receipt.get('verdict') == 'green' and receipt.get('cutover') is False
            and receipt.get('approval_pending') is True, 'replay_not_green')
    require(receipt.get('prepared_receipt') == prepared_sha and receipt.get('tag') == tag,
            'replay_candidate_mismatch')
    require(0 <= time.time() - receipt['finished_at'] <= 86400, 'replay_receipt_expired')


def sql(query, container='tokenkey-postgres', database='tokenkey', user='tokenkey'):
    return run(['docker', 'exec', '-i', container, 'psql', '-X', '-v', 'ON_ERROR_STOP=1',
                '-U', user, '-d', database, '-At'], input=query.encode()).decode()


def stratum(row):
    return (row['user_id'], row['requested_model'], row['inbound_endpoint'],
            row['stream'], row['tool_calls_present'], row['multimodal_present'])


def body_intact(value):
    if isinstance(value, dict):
        return not any(k in ('_truncated', 'truncated') and v is True for k, v in value.items()) and all(body_intact(v) for v in value.values())
    if isinstance(value, list):
        return all(body_intact(v) for v in value)
    return not isinstance(value, str) or value not in ('***', '[REDACTED]', '[TRUNCATED]')


def sample_from_capture(row, payload):
    require(payload.get('request_id') == row['request_id'], 'capture_identity_mismatch')
    request = payload.get('request', {})
    body = request.get('body')
    path = request.get('path')
    require(isinstance(body, dict) and body and body_intact(body), 'body_missing_or_redacted')
    require(isinstance(path, str) and len(path) < 2048 and '\r' not in path and '\n' not in path,
            'path_missing')
    parsed = urlsplit(path)
    require(not parsed.scheme and not parsed.netloc and not parsed.fragment, 'unsupported_path')
    supported = {'/v1/messages', '/v1/messages/count_tokens', '/v1/chat/completions', '/v1/responses',
                 '/v1/images/generations', '/v1/audio/speech', '/v1/embeddings'}
    require(parsed.path in supported or re.fullmatch(r'/v1beta/models/[A-Za-z0-9_.-]+:(?:streamGenerateContent|generateContent|countTokens)', parsed.path), 'unsupported_path')
    # Never fabricate a missing Gemini model/action or mutate the user's prompt.
    require(body.get('model', row['requested_model']) == row['requested_model'], 'capture_model_mismatch')
    return {'row': row, 'path': path, 'body': encoded(body)}


def collect(root=ROOT):
    # Five recent alternatives per observed combination; select a complete
    # retained body where possible. Missing/truncated combinations stay gaps.
    # The receipt gate is embedded without a module filename on the host.
    # Only collection needs the shipped manifest and its loader.
    from prod_replay_manifest import load as load_capability_manifest
    capabilities = load_capability_manifest(Path(__file__).with_name('prod-replay-capabilities.json'))
    query = """WITH ranked AS (
 SELECT request_id,user_id,api_key_id,requested_model,inbound_endpoint,stream,
 tool_calls_present,multimodal_present,blob_uri,created_at,
 row_number() OVER (PARTITION BY user_id,requested_model,inbound_endpoint,stream,
 tool_calls_present,multimodal_present ORDER BY created_at DESC,request_id) rn
 FROM qa_records WHERE created_at >= now()-interval '24 hours' AND success=true
 ) SELECT row_to_json(ranked) FROM ranked WHERE rn<=5
 ORDER BY user_id,requested_model,inbound_endpoint,stream,rn LIMIT 5001;"""
    rows = [json.loads(line) for line in sql(query).splitlines()]
    require(0 < len(rows) <= 5000, 'capture_empty_or_scan_limit')
    groups = collections.defaultdict(list)
    for row in rows:
        groups[stratum(row)].append(row)
    samples, gaps = [], collections.Counter()
    corpus_bytes = 0
    blob_root = (root / 'app').resolve()
    for values in groups.values():
        selected = None
        reason = 'capture_missing'
        for row in values:
            try:
                uri = urlsplit(row['blob_uri'] or '')
                require(uri.scheme == 'file' and not uri.netloc, 'capture_not_local')
                relative = Path(uri.path).relative_to('/app/data')
                path = (blob_root / relative).resolve()
                require(path.is_relative_to(blob_root) and path.is_file(), 'capture_missing')
                # Bound decompression output before parsing; retain no decoded file.
                proc = subprocess.Popen(['zstd', '-q', '-d', '-c', str(path)], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
                try:
                    data = proc.stdout.read(MAX_BYTES + 1)
                    require(len(data) <= MAX_BYTES, 'capture_too_large')
                    require(proc.wait(timeout=10) == 0, 'capture_decode_failed')
                finally:
                    if proc.poll() is None:
                        proc.terminate()
                    proc.wait(timeout=10)
                    proc.stdout.close()
                candidate = sample_from_capture(row, json.loads(data))
                # Historical evidence may only satisfy a declared protocol.
                # Unknown protocol rows stay gaps instead of expanding the
                # denominator implicitly.
                protocol = row.get('inbound_endpoint', '')
                if not any(protocol_hint(protocol, c.protocol) for c in capabilities):
                    raise ReplayError('capability_not_declared')
                selected = candidate
                break
            except (ReplayError, ValueError, OSError) as exc:
                reason = str(exc) if isinstance(exc, ReplayError) else 'capture_decode_failed'
        if selected is None:
            gaps[reason] += 1
        elif corpus_bytes + len(selected['body']) > MAX_CORPUS_BYTES:
            gaps['corpus_byte_budget_exceeded'] += 1
        elif len(samples) < MAX_SAMPLES:
            samples.append(selected)
            corpus_bytes += len(selected['body'])
        else:
            gaps['sample_budget_exceeded'] += 1
    return samples, dict(gaps), len(groups)


def protocol_hint(endpoint, protocol):
    endpoint = endpoint.lower()
    return {
        'openai-chat': 'chat' in endpoint,
        'openai-responses': 'responses' in endpoint,
        'anthropic-messages': 'messages' in endpoint,
        'gemini-content': 'gemini' in endpoint or 'models' in endpoint,
    }.get(protocol, False)


def response_ok(status, ctype, raw, stream):
    if not 200 <= status < 300 or not raw:
        return False
    try:
        if stream or 'text/event-stream' in ctype:
            if 'text/event-stream' not in ctype:
                return False
            terminal = False
            for line in raw.decode('utf-8').splitlines():
                if not line.startswith('data:'):
                    continue
                data = line[5:].strip()
                if data == '[DONE]':
                    terminal = True
                elif data:
                    event = json.loads(data)
                    if not isinstance(event, dict) or event.get('error') or event.get('type') in ('error', 'response.failed', 'response.incomplete'):
                        return False
                    terminal |= event.get('type') in ('message_stop', 'response.completed')
                    terminal |= any(c.get('finish_reason') for c in event.get('choices', []))
                    terminal |= any(c.get('finishReason') for c in event.get('candidates', []))
            return bool(terminal)
        if 'json' in ctype:
            data = json.loads(raw)
            return isinstance(data, (list, dict)) and bool(data) and not (isinstance(data, dict) and data.get('error'))
        return ctype.startswith(('audio/', 'image/'))
    except (ValueError, TypeError, AttributeError):
        return False


def execute(sample, key, port, replay_id):
    # http.client has no proxy or redirect handling: credentials cannot be sent
    # to a Location supplied by a replayed response.
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=30)
    status, ok, response_id = 0, False, None
    try:
        connection.request('POST', sample['path'], body=sample['body'], headers={
            'Content-Type': 'application/json', 'Authorization': 'Bearer ' + key,
            'x-api-key': key, 'anthropic-version': '2023-06-01',
            'User-Agent': 'tokenkey-private-replay', 'X-Client-Request-ID': replay_id})
        response = connection.getresponse()
        status = response.status
        response_id = response.getheader('X-Request-ID')
        if not response_id or not re.fullmatch(r'[A-Za-z0-9_.:-]{1,128}', response_id):
            response_id = None
        raw = bytearray()
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            part = response.read1(65536)
            if not part:
                ok = response_ok(status, response.getheader('Content-Type', ''), raw,
                                 sample['row']['stream'])
                break
            raw.extend(part)
            if len(raw) > MAX_BYTES:
                break
    except (OSError, http.client.HTTPException):
        pass  # Classified as a failed result, never as a successful replay.
    finally:
        connection.close()
    return {'sample_sha256': digest(encoded(sample['row'])), 'http_status': status,
            'passed': bool(ok), 'body_sha256': digest(sample['body']), 'response_request_id': response_id}


def isolated_environment(source, name, password):
    env = dict(source)
    for k in list(env):
        if k.startswith(('AWS_', 'QA_', 'TELEMETRY_', 'SMTP_', 'EMAIL_', 'MEDIA_STORAGE_', 'IMAGE_STORAGE_')):
            del env[k]
    env.update({'DATABASE_HOST': name + '-pg', 'DATABASE_PORT': '5432',
                'DATABASE_USER': 'tokenkey', 'DATABASE_PASSWORD': password,
                'DATABASE_DBNAME': name.replace('-', '_'), 'DATABASE_SSLMODE': 'disable',
                'REDIS_HOST': name + '-redis', 'REDIS_PORT': '6379', 'REDIS_DB': '0',
                'REDIS_PASSWORD': password, 'REDIS_USERNAME': 'default', 'REDIS_ENABLE_TLS': 'false',
                'SERVER_PORT': '8080', 'DATA_DIR': '/app/data', 'AUTO_SETUP': 'false',
                'TOKEN_REFRESH_ENABLED': 'false', 'QA_CAPTURE_ENABLED': 'false',
                'QA_ARCHIVE_ENABLED': 'false', 'QA_BUNDLE_ENABLED': 'false',
                'TELEMETRY_ARCHIVE_ENABLED': 'false', 'OPS_CLEANUP_ENABLED': 'false',
                'OPS_ENABLED': 'false', 'DASHBOARD_AGGREGATION_ENABLED': 'false',
                'USAGE_CLEANUP_ENABLED': 'false', 'GATEWAY_CN_PROVIDERS_BALANCE_CHECK_ENABLED': 'false',
                'GATEWAY_USAGE_RECORD_WORKER_COUNT': '2', 'GATEWAY_USAGE_RECORD_AUTO_SCALE_ENABLED': 'false',
                'DATABASE_MAX_OPEN_CONNS': '6', 'DATABASE_MAX_IDLE_CONNS': '2',
                'GOMEMLIMIT': '600MiB', 'MEDIA_STORAGE_DRIVER': 'local',
                'IMAGE_STORAGE_DRIVER': 'local', 'MEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED': 'false'})
    return env


class Sandbox:
    def __init__(self, candidate, root=ROOT):
        self.root = root
        self.candidate = candidate
        self.name = 'tk-replay-' + secrets.token_hex(6)
        self.path = root / self.name
        self.created = []
        self.network_created = False

    def start_container(self, suffix, arguments):
        name = self.name + suffix
        self.created.append(name)
        run(['docker', 'run', '-d', '--name', name, '--network', self.name,
             '--label', 'tokenkey.release-replay=' + self.name, '--restart', 'no',
             '--log-driver', 'none', *arguments])
        return name

    def start(self):
        self.path.mkdir(mode=0o700)
        # Resource limits are per-container; refuse to pressure a busy prod host.
        available = int(re.search(r'MemAvailable:\s+(\d+)', Path('/proc/meminfo').read_text())[1]) * 1024
        require(available >= 1800 * 1024**2, 'insufficient_memory')
        require(shutil.disk_usage(self.root).free >= 5 * 1024**3, 'insufficient_disk')
        password = secrets.token_hex(32)
        source = environment(self.candidate)
        env = isolated_environment(source, self.name, password)
        database = env['DATABASE_DBNAME']
        run(['docker', 'network', 'create', '--label', 'tokenkey.release-replay=' + self.name, self.name])
        self.network_created = True
        pg_env = self.path / 'postgres.env'
        pg_env.write_text(f'POSTGRES_USER=tokenkey\nPOSTGRES_DB={database}\nPOSTGRES_PASSWORD={password}\n')
        pg_env.chmod(0o600)
        pg = self.start_container('-pg', ['--memory', '512m', '--cpus', '0.5', '--env-file', str(pg_env),
                                        inspect('tokenkey-postgres')['Image']])
        self.pg, self.database = pg, database
        for _ in range(60):
            try:
                sql('SELECT 1;', pg, database)
                break
            except ReplayError:
                time.sleep(1)
        else:
            raise ReplayError('snapshot_database_not_ready')
        dump = self.path / 'snapshot.dump'
        args = ['docker', 'exec', 'tokenkey-postgres', 'pg_dump', '-U', source.get('DATABASE_USER', 'tokenkey'),
                '-d', source.get('DATABASE_DBNAME', 'tokenkey'), '-Fc', '--no-owner', '--no-acl', '--lock-wait-timeout=10s']
        for pattern in ('usage_logs*', 'usage_billing_dedup*', 'ops_*', 'qa_*', 'telemetry_*', 'idempotency*',
                        'scheduler_outbox*', 'dashboard_*', 'channel_monitor_*', 'audit_logs*', 'payment_orders*',
                        'redeem_codes*', 'email_verification*', 'refresh_tokens*', 'gateway_cache*'):
            args.append('--exclude-table-data=' + pattern)
        with dump.open('xb') as output:
            subprocess.run(args, stdout=output, stderr=subprocess.PIPE, check=True, timeout=300)
        with dump.open('rb') as source_file:
            run(['docker', 'exec', '-i', pg, 'pg_restore', '-U', 'tokenkey', '-d', database,
                 '--no-owner', '--no-acl', '--exit-on-error'], stdin=source_file)
        sql('UPDATE users SET balance_notify_enabled=false; UPDATE accounts SET auto_pause_on_expired=false;', pg, database)
        self.start_container('-redis', ['--memory', '128m', '--cpus', '0.25', inspect('tokenkey-redis')['Image'],
                                      'redis-server', '--save', '', '--appendonly', 'no', '--requirepass', password,
                                      '--maxmemory', '96mb', '--maxmemory-policy', 'allkeys-lru'])
        env_file = self.path / 'app.env'
        require(all('\n' not in v and '\r' not in v for v in env.values()), 'invalid_environment')
        env_file.write_text(''.join(k + '=' + v + '\n' for k, v in sorted(env.items())))
        env_file.chmod(0o600)
        data = self.path / 'data'
        data.mkdir(mode=0o755)
        prod_data = self.root / 'app'
        os.chown(data, prod_data.stat().st_uid, prod_data.stat().st_gid)
        for name in ('config.yaml', '.installed', 'model_prices_and_context_window.json',
                     'model_prices_and_context_window.sha256', 'pricing-overlay.json'):
            src = prod_data / name
            if src.is_file():
                shutil.copy2(src, data / name)
                os.chown(data / name, src.stat().st_uid, src.stat().st_gid)
        # The sandbox runs the prepared container's immutable image ID.
        app = self.start_container('-app', ['--memory', '768m', '--cpus', '1', '--env-file', str(env_file),
                                          '-p', '127.0.0.1::8080', '-v', str(data) + ':/app/data', self.candidate['Image']])
        self.app = app
        state = inspect(app)
        self.verify(state, env)
        port = int(state['NetworkSettings']['Ports']['8080/tcp'][0]['HostPort'])
        for _ in range(120):
            connection = http.client.HTTPConnection('127.0.0.1', port, timeout=2)
            try:
                connection.request('GET', '/health')
                if connection.getresponse().status == 200:
                    self.env = env
                    return port
            except OSError:
                pass  # Bounded health retry; exhaustion is a hard failure below.
            finally:
                connection.close()
            time.sleep(1)
        raise ReplayError('replay_app_not_healthy')

    def verify(self, state, env):
        observed = environment(state)
        require(state['Image'] == self.candidate['Image'], 'replay_image_changed')
        require(set(state['NetworkSettings']['Networks']) == {self.name}, 'replay_network_not_isolated')
        ports = state['NetworkSettings']['Ports'].get('8080/tcp')
        require(ports and all(p['HostIp'] == '127.0.0.1' for p in ports), 'replay_listener_not_loopback')
        require(all(observed.get(k) == env[k] for k in ('DATABASE_HOST', 'DATABASE_DBNAME', 'DATABASE_PASSWORD',
                    'REDIS_HOST', 'REDIS_PASSWORD', 'REDIS_DB')), 'replay_data_layer_not_isolated')
        require(all(Path(m['Source']).is_relative_to(self.path) for m in state['Mounts']), 'replay_mount_not_isolated')

    def close(self):
        errors = []
        for name in reversed(self.created):
            try:
                run(['docker', 'rm', '-f', '-v', name])
            except ReplayError:
                errors.append(name)
        if self.network_created:
            try:
                run(['docker', 'network', 'rm', self.name])
            except ReplayError:
                errors.append(self.name)
        if not errors and self.path.exists():
            shutil.rmtree(self.path)
        require(not errors, 'replay_cleanup_failed')


def replay(tag, root=ROOT):
    prepared_sha, before = prepared(tag, root)
    record = {'schema': 1, 'tag': tag, 'prepared_receipt': prepared_sha,
              'verdict': 'red', 'cutover': False, 'approval_pending': True,
              'billing': 'isolated_snapshot', 'upstream_quota_consumed': True,
              'header_fidelity': 'capture_has_no_headers; protocol/auth headers reconstructed',
              'started_at': time.time()}
    receipt_path = root / 'bluegreen-replay.json'
    # Invalidate an older successful attempt before doing any paid work.
    write_json(receipt_path, seal(record))
    sandbox = Sandbox(inspect('tokenkey-' + before['target']), root)
    results = []
    try:
        samples, gaps, total = collect(root)
        record['coverage'] = {'observed_combinations': total, 'selected': len(samples),
                              'users': len({s['row']['user_id'] for s in samples}),
                              'models': len({s['row']['requested_model'] for s in samples}),
                              'protocols': len({s['row']['inbound_endpoint'] for s in samples}), 'gaps': gaps}
        require(samples, 'no_replayable_captures')
        port = sandbox.start()
        keys = {r['id']: r for r in (json.loads(line) for line in sql(
            'SELECT row_to_json(t) FROM (SELECT id,user_id,key,status FROM api_keys WHERE deleted_at IS NULL) t;',
            sandbox.pg, sandbox.database).splitlines())}
        prefix = sandbox.name + '-'
        for i, sample in enumerate(samples):
            require(snapshot(root) == before, 'public_or_prepared_state_changed')
            sandbox.verify(inspect(sandbox.app), sandbox.env)
            row = sample['row']
            key = keys.get(row['api_key_id'])
            if not key or key['user_id'] != row['user_id'] or key['status'] != 'active':
                results.append({'sample_sha256': digest(encoded(row)), 'passed': False, 'reason': 'key_not_replayable'})
            else:
                results.append(execute(sample, key['key'], port, prefix + str(i)))
            results[-1].update({k: row[k] for k in ('user_id', 'requested_model', 'inbound_endpoint', 'stream',
                                                  'tool_calls_present', 'multimodal_present')})
        # Production user usage must not contain a replay request ID.
        # Storage identity is the server X-Request-ID echoed on the response.
        # Client markers use X-Client-Request-ID and must not appear as request_id.
        response_ids = sorted({r['response_request_id'] for r in results if r.get('response_request_id')})
        id_literals = ','.join("'" + rid + "'" for rid in response_ids) or "NULL"
        production_writes = int(sql("SELECT count(*) FROM usage_logs WHERE created_at >= now()-interval '3 hours' "
                                   "AND (request_id LIKE '" + prefix + "%' OR request_id IN (" + id_literals + "));").strip())
        require(production_writes == 0, 'production_usage_written')
        record['production_usage_rows'] = production_writes
        record['passed'] = sum(r['passed'] for r in results)
        record['failed'] = len(results) - record['passed']
        record['results_sha256'] = digest(encoded(results))
        record['corpus_sha256'] = digest(encoded([{'row': s['row'], 'body_sha256': digest(s['body'])} for s in samples]))
        # Counts are derived from complete execution, not caller-provided claims.
        require(not gaps and len(results) == total and all(r['passed'] for r in results), 'replay_failed_or_coverage_gap')
        require(all(record['coverage'][k] >= 2 for k in ('users', 'models', 'protocols')), 'insufficient_diversity')
        record['verdict'] = 'green'
    except (ReplayDeadline, ReplayError, OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        record['reason'] = str(exc) if isinstance(exc, ReplayError) else ('replay_deadline_exceeded' if isinstance(exc, ReplayDeadline) else 'replay_host_error')
    finally:
        # Alarm must not interrupt teardown and leave a false green receipt.
        signal.alarm(0)
        try:
            sandbox.close()
            require(snapshot(root) == before, 'public_or_prepared_state_changed')
        except (ReplayError, OSError, ValueError) as exc:
            record['verdict'] = 'red'
            record['reason'] = str(exc) if isinstance(exc, ReplayError) else 'replay_cleanup_error'
            record['cutover'] = None  # Cannot certify the public route after failed verification.
        write_json(root / 'bluegreen-replay-results.json', results)
        record['finished_at'] = time.time()
        record = seal(record)
        write_json(receipt_path, record)
    return record


def prepare_status(tag, replace_receipt='', root=ROOT):
    path = root / 'bluegreen-prepared.json'
    if path.exists():
        raw = path.read_bytes()
        old_tag = json.loads(raw)['tag']
        if old_tag != tag:
            require(re.fullmatch(r'[a-f0-9]{64}', replace_receipt or '')
                    and digest(raw) == replace_receipt, 'replacement_receipt_required')
            prepared(old_tag, root)  # Bind replacement to the still-current candidate and route.
            return {'needs_prepare': True, 'replace_receipt': replace_receipt}
        if replace_receipt:
            require(digest(raw) == replace_receipt, 'replacement_receipt_mismatch')
        sha, _ = prepared(tag, root)
        return {'needs_prepare': False, 'prepared_receipt': sha}
    require(not replace_receipt, 'replacement_candidate_missing')
    require((root / 'active-color').read_text().strip() in ('blue', 'green'), 'requires_bluegreen_prod')
    require(shutil.which('zstd'), 'zstd_required')
    write_json(root / 'bluegreen-replay.json', seal({'tag': tag, 'verdict': 'red',
               'cutover': False, 'approval_pending': True, 'reason': 'prepare_pending'}))
    return {'needs_prepare': True}


def main():
    p = argparse.ArgumentParser()
    p.add_argument('operation', choices=('status', 'run', 'gate'))
    p.add_argument('--tag', required=True)
    p.add_argument('--receipt', default='')
    p.add_argument('--replace-receipt', default='')
    args = p.parse_args()
    require(re.fullmatch(r'\d+\.\d+\.\d+', args.tag), 'invalid_tag')
    os.umask(0o077)
    with (ROOT / 'bluegreen-deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.operation == 'status':
            result = prepare_status(args.tag, args.replace_receipt)
        elif args.operation == 'run':
            def timeout(_sig, _frame):
                raise ReplayDeadline()
            signal.signal(signal.SIGALRM, timeout)
            signal.signal(signal.SIGTERM, timeout)
            signal.alarm(REPLAY_SECONDS)
            result = replay(args.tag)
        else:
            sha, _ = prepared(args.tag)
            result = {'needs_prepare': False, 'prepared_receipt': sha}
            if args.operation == 'gate':
                receipt = json.loads((ROOT / 'bluegreen-replay.json').read_bytes())
                validate_receipt(receipt, args.receipt, sha, args.tag)
        print(json.dumps(result, separators=(',', ':')))


if __name__ == '__main__':
    try:
        main()
    except (ReplayDeadline, ReplayError, OSError, ValueError, KeyError, subprocess.SubprocessError):
        raise SystemExit('prod replay failed; inspect private host state (no payload logged)') from None
