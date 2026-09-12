#!/usr/bin/env python3
"""Prod-host retained request replay. Never writes the public route.

Only the compact receipt leaves the host. Captures, credentials and responses
stay off stdout; private temporary data and Docker resources are removed in a
finally block. The prepared color stays available for reviewed promotion.
"""
from __future__ import annotations

import argparse
import base64
import datetime
import collections
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import select
import shutil
import signal
import subprocess
import time
from urllib.parse import urlsplit, parse_qsl

ROOT = Path('/var/lib/tokenkey')
IMAGE = 'ghcr.io/youxuanxue/sub2api'
MAX_BYTES = 16 * 1024 * 1024
MAX_SAMPLES = 200
MAX_CORPUS_BYTES = 64 * 1024 * 1024
REPLAY_SECONDS = 5400
CAPTURE_ALTERNATIVES = 50


KEY_STATE_SQL = """CASE WHEN k.id IS NULL THEN 'missing' WHEN k.deleted_at IS NOT NULL THEN 'deleted'
 WHEN k.status IS NULL OR k.status NOT IN ('active','expired','quota_exhausted') THEN 'disabled'
 WHEN k.status='quota_exhausted' THEN 'quota_exhausted'
 WHEN k.status='expired' OR k.expires_at<=now() THEN 'expired' ELSE 'active' END"""

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
    require(receipt.get('schema') == 2 and receipt.get('corpus_manifest_sha256'), 'replay_policy_outdated')
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
        return not any(k in ('_truncated', 'truncated', '_qa_body_omitted') and v is True for k, v in value.items()) and all(body_intact(v) for v in value.values())
    if isinstance(value, list):
        return all(body_intact(v) for v in value)
    return not isinstance(value, str) or value not in ('***', '[REDACTED]', '[TRUNCATED]')


def sample_from_capture(row, payload):
    require(payload.get('request_id') == row['request_id'], 'capture_identity_mismatch')
    request = payload.get('request', {})
    body = request.get('body')
    path = request.get('original_path') or request.get('path')
    method = request.get('method', 'POST')
    if method == 'GET':
        require('original_path' in request and body in (None, {}, ''), 'get_evidence_missing')
    else:
        require(method == 'POST' and isinstance(body, dict) and body and not body.get('_qa_body_omitted'), 'body_missing_or_truncated')
        require(body_intact(body), 'body_redacted')
    require(isinstance(path, str) and len(path) < 2048 and '\r' not in path and '\n' not in path,
            'path_missing')
    parsed = urlsplit(path)
    require(not parsed.scheme and not parsed.netloc and not parsed.fragment, 'unsupported_path')
    supported = {'/v1/messages', '/v1/messages/count_tokens', '/v1/chat/completions', '/v1/responses',
                 '/v1/images/generations', '/v1/audio/speech', '/v1/audio/transcriptions', '/audio/transcriptions', '/v1/embeddings',
                 '/responses', '/chat/completions', '/embeddings', '/images/generations',
                 '/audio/speech', '/messages/count_tokens', '/backend-api/codex/responses'}
    require(all(k in ('alt', 'beta') for k, _ in parse_qsl(parsed.query, keep_blank_values=True)), 'unsupported_query')
    if method == 'GET':
        supported = {'/v1/models', '/v1/usage'}
    require(parsed.path in supported or re.fullmatch(r'/v1beta/models/[A-Za-z0-9_.-]+:(?:streamGenerateContent|generateContent|countTokens)', parsed.path), 'unsupported_path')
    if parsed.path.startswith('/v1beta/models/'):
        require(method == 'POST' and parsed.path.split('/')[-1].split(':')[0] == row['requested_model'], 'capture_model_mismatch')
    # Never fabricate a missing Gemini model/action or mutate the user's prompt.
    require(not body or body.get('model', row['requested_model']) == row['requested_model'], 'capture_model_mismatch')
    # qa_records.success is based on HTTP status and can label a 200 error
    # stream as successful. Such a request is not a positive replay baseline.
    baseline = payload.get('response', {}).get('body')
    baseline_raw = baseline.encode() if isinstance(baseline, str) else encoded(baseline)
    require(not response_error_details(baseline_raw), 'baseline_upstream_error')
    return {'row': row, 'path': path, 'method': method, 'headers': {},
            'body': b'' if method == 'GET' else encoded(body), 'source': 'legacy'}


def capture_query(request_ids=()):
    require(len(request_ids) <= 400 and all(re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}", i) for i in request_ids), "invalid_capture_ids")
    retained = ",".join("'" + i + "'" for i in request_ids) or "NULL"
    # Sample both ends of the window: long-running conversations grow past
    # capture limits, while their earlier complete requests remain retained.
    half = CAPTURE_ALTERNATIVES // 2
    return f"""WITH captures AS (
 SELECT q.request_id,q.user_id,q.api_key_id,q.requested_model,q.inbound_endpoint,q.stream,
 q.tool_calls_present,q.multimodal_present,q.blob_uri,q.created_at,q.duration_ms,
 {KEY_STATE_SQL} AS key_state,
 (k.id IS NOT NULL AND k.user_id=q.user_id AND k.deleted_at IS NULL
  AND ((k.status='active' AND (k.expires_at IS NULL OR k.expires_at>now()))
   OR (q.inbound_endpoint IN ('/v1/models','/v1/usage') AND k.status IN ('active','expired','quota_exhausted')))) AS key_replayable
 FROM qa_records q LEFT JOIN api_keys k ON k.id=q.api_key_id
 -- ops-allow-soft-deleted: retain observed strata after key deletion; key_replayable rejects deleted keys.
 WHERE q.created_at >= now()-interval '24 hours' AND q.success=true
 ), ranked AS (
 SELECT captures.*,
 row_number() OVER (PARTITION BY user_id,requested_model,inbound_endpoint,stream,
 tool_calls_present,multimodal_present ORDER BY key_replayable DESC,created_at DESC,request_id) rn,
 row_number() OVER (PARTITION BY user_id,requested_model,inbound_endpoint,stream,
 tool_calls_present,multimodal_present ORDER BY key_replayable DESC,created_at ASC,request_id) rn_old
 FROM captures
 ) SELECT row_to_json(ranked) FROM ranked WHERE rn<={half} OR rn_old<={half} OR request_id IN ({retained})
 ORDER BY user_id,requested_model,inbound_endpoint,stream,rn LIMIT 5001;"""


def read_legacy(row, root):
    captured = row.get('created_at')
    if captured:
        age = time.time() - datetime.datetime.fromisoformat(captured.replace('Z', '+00:00')).timestamp()
        require(-60 <= age < 86400, 'legacy_capture_expired')
    uri = urlsplit(row['blob_uri'] or '')
    require(uri.scheme == 'file' and not uri.netloc, 'capture_not_local')
    blob_root = (root / 'app').resolve()
    relative = Path(uri.path).relative_to('/app/data')
    path = (blob_root / relative).resolve()
    require(path.is_relative_to(blob_root) and path.is_file(), 'capture_missing')
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
    return sample_from_capture(row, json.loads(data))


def load_capsules(root, candidate):
    directory = root / 'app/release_replay'
    if not directory.exists():
        return {}
    available = int(re.search(r'MemAvailable:\s+(\d+)', Path('/proc/meminfo').read_text())[1]) * 1024
    require(available >= 768 * 1024**2, 'insufficient_decrypt_memory')
    key = root / 'replay-keys/private.pem'
    require(key.is_file() and not key.is_symlink() and key.stat().st_mode & 0o777 == 0o600,
            'capsule_private_key_unavailable')
    require(not directory.is_symlink() and directory.stat().st_mode & 0o777 == 0o700,
            'capsule_directory_not_private')
    # The gateway receives only the public key. Decryption has no network,
    # writable mounts, gateway entrypoint, logs, or plaintext disk output.
    decrypt_name = 'tk-replay-decrypt-' + secrets.token_hex(6)
    proc = subprocess.Popen(['docker', 'run', '--name', decrypt_name, '--network', 'none', '--read-only',
        '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--user', '0:0',
        '--memory', '384m', '--cpus', '0.5', '--pids-limit', '32', '--log-driver', 'none',
        '--mount', 'type=bind,src=' + str(directory) + ',dst=/capsules,readonly',
        '--mount', 'type=bind,src=' + str(key) + ',dst=/private.pem,readonly',
        '--entrypoint', '/app/replay-capsule', candidate['Image'],
        'decrypt', '--dir', '/capsules', '--key', '/private.pem'],
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    capsules, pending, total = {}, bytearray(), 0
    deadline = time.monotonic() + 60
    try:
        while True:
            remaining = deadline - time.monotonic()
            require(remaining > 0 and select.select([proc.stdout], [], [], remaining)[0], 'capsule_decrypt_timeout')
            part = os.read(proc.stdout.fileno(), 65536)
            if not part:
                break
            pending.extend(part)
            total += len(part)
            require(total <= 128 * 1024**2 and len(pending) <= 8 * 1024**2, 'capsule_decode_budget')
            while b'\n' in pending:
                end = pending.index(b'\n')
                item = json.loads(pending[:end])
                del pending[:end + 1]
                request_id = item['request']['request_id']
                require(request_id not in capsules and len(capsules) < 400, 'capsule_duplicate_or_budget')
                capsules[request_id] = item
        require(not pending and proc.wait(timeout=10) == 0, 'capsule_decrypt_failed')
    finally:
        try:
            if proc.poll() is None:
                proc.terminate()
            proc.wait(timeout=10)
        finally:
            proc.stdout.close()
            run(['docker', 'rm', '-f', decrypt_name])

    return capsules


def sample_from_capsule(row, item):
    req = item['request']
    require(req['request_id'] == row['request_id'] and req['api_key_id'] == row['api_key_id']
            and stratum(req) == stratum(row), 'capsule_identity_mismatch')
    body = base64.b64decode(req['body'] or '', validate=True)
    require(len(body) <= 4 * 1024**2, 'capsule_body_budget')
    # Reuse path/model checks, but encrypted original business fields must not
    # be confused with legacy redaction markers supplied by the user itself.
    ctype = next((v for k,v in (req['headers'] or {}).items() if k.lower() == 'content-type'), '')
    multipart_audio = ctype.lower().startswith('multipart/form-data;') and urlsplit(req['path']).path in ('/v1/audio/transcriptions','/audio/transcriptions')
    parsed_body = {'model': row['requested_model']} if multipart_audio else json.loads(body) if body else None
    check_body = {'model': parsed_body.get('model', row['requested_model'])} if isinstance(parsed_body, dict) else parsed_body
    sample = sample_from_capture(row, {'request_id': req['request_id'], 'request': {
        'method': req['method'], 'original_path': req['path'], 'body': check_body}})
    require(req['method'] == 'GET' or isinstance(parsed_body, dict) and parsed_body, 'capsule_body_shape')
    headers = req['headers'] or {}
    allowed = {'content-type', 'accept', 'user-agent', 'anthropic-version', 'anthropic-beta', 'openai-beta'}
    require(all(k.lower() in allowed and isinstance(v, str) and len(v) <= 1024
                and not any(c in v for c in '\r\n') for k, v in headers.items()), 'capsule_headers_invalid')
    sample.update(body=body, headers=headers, source='encrypted',
                  envelope_sha256=item['envelope_sha256'], captured_at=req['captured_at'])
    return sample


def auth_sample(row):
    state = row.get('key_state')
    require(state in ('deleted', 'disabled', 'expired', 'quota_exhausted'), 'original_key_unavailable')
    return {'row': row, 'source': 'synthetic_auth_negative', 'method': 'POST',
            'path': '/v1/chat/completions', 'headers': {},
            'body': encoded({'model': row['requested_model'] or 'auth-rejection-check', 'messages': []}),
            'expected_status': 403 if state == 'expired' else 429 if state == 'quota_exhausted' else 401,
            'expected_code': {'deleted': 'INVALID_API_KEY', 'disabled': 'API_KEY_DISABLED',
                              'expired': 'API_KEY_EXPIRED', 'quota_exhausted': 'insufficient_quota'}[state]}


def collect(root=ROOT, gap_results=None, capsules=None, pin_observations=False):
    capsules = capsules or {}
    rows = [json.loads(line) for line in sql(capture_query(tuple(capsules))).splitlines()]
    require(len(rows) <= 5000 and (rows or pin_observations and (root / 'bluegreen-replay-observed.json').exists()),
            'capture_empty_or_scan_limit')
    if pin_observations:
        path = root / 'bluegreen-replay-observed.json'
        if path.exists():
            observed = json.loads(path.read_bytes())
            required = {stratum(r) for r in observed['rows']}
            # New users may supply missing positive capability evidence for a
            # revoked stratum. Such support is added visibly, never subtracted.
            revoked = {stratum(r)[1:] for r in observed['rows'] if not r.get('key_replayable')}
            support = [r for r in rows if stratum(r) not in required and stratum(r)[1:] in revoked and r.get('key_replayable')]
            if support:
                observed['rows'].extend(support)
                write_json(path, observed)
                required.update(stratum(r) for r in support)
            rows = [r for r in rows if stratum(r) in required] + observed['rows']
        else:
            write_json(path, {'schema': 2, 'created_at': time.time(), 'rows': rows})
    groups = collections.defaultdict(list)
    seen = set()
    for row in rows:
        if row['request_id'] in seen:
            continue
        seen.add(row['request_id'])
        groups[stratum(row)].append(row)
    samples, gaps = [], collections.Counter()
    corpus_bytes = 0
    for values in groups.values():
        selected, failures = None, collections.Counter()
        for row in values:
            try:
                require(row.get('key_replayable'), 'key_not_replayable')
                selected = (sample_from_capsule(row, capsules[row['request_id']])
                            if row['request_id'] in capsules else read_legacy(row, root))
                break
            except (ReplayError, ValueError, OSError) as exc:
                failures[str(exc) if isinstance(exc, ReplayError) else 'capture_decode_failed'] += 1
        if selected is None and all(not row.get('key_replayable') for row in values):
            try:
                selected = auth_sample(values[0])
            except ReplayError as exc:
                failures[str(exc)] += 1
        reason = next((r for r in failures if r != 'key_not_replayable'), 'key_not_replayable')
        if selected is not None:
            if corpus_bytes + len(selected['body']) > MAX_CORPUS_BYTES:
                reason = 'corpus_byte_budget_exceeded'
            elif len(samples) >= MAX_SAMPLES:
                reason = 'sample_budget_exceeded'
            else:
                samples.append(selected)
                corpus_bytes += len(selected['body'])
                continue
        gaps[reason] += 1
        if gap_results is not None:
            gap_results.append(dict(case_metadata(values[0]), passed=False, reason=reason,
                                    phase='collection', alternatives=len(values), alternative_failures=dict(failures)))
    # An auth rejection proves only auth. Each revoked user's business capability
    # must also have a complete positive sample with its own original valid key.
    capabilities = {stratum(s['row'])[1:] for s in samples if s['source'] != 'synthetic_auth_negative'}
    for sample in samples:
        if sample['source'] == 'synthetic_auth_negative' and stratum(sample['row'])[1:] not in capabilities:
            gaps['positive_capability_missing'] += 1
            if gap_results is not None:
                gap_results.append(dict(case_metadata(sample['row']), passed=False,
                                        reason='positive_capability_missing', phase='collection'))
    return samples, dict(gaps), len(groups)


def sample_manifest(sample):
    # No payload, protocol-header values, or key values in the persisted plan.
    return {k: v for k, v in sample.items() if k in ('row', 'source', 'envelope_sha256', 'captured_at')} | {
        'request_sha256': digest(encoded({'method': sample.get('method', 'POST'), 'path': sample['path'],
                                         'headers': sample.get('headers', {}), 'body_sha256': digest(sample['body'])}))}


def frozen_samples(root, candidate, gaps_out):
    capsules = load_capsules(root, candidate)
    path = root / 'bluegreen-replay-corpus.json'
    if path.exists():
        plan = json.loads(path.read_bytes())
        require(plan.get('schema') == 2 and 0 <= time.time() - plan['created_at'] < 86400,
                'frozen_corpus_expired_or_invalid')
        require(plan.get('observations_sha256') == digest((root / 'bluegreen-replay-observed.json').read_bytes()),
                'frozen_observations_changed')
        samples = []
        for entry in plan['samples']:
            row = entry['row']
            if entry['source'] == 'synthetic_auth_negative':
                sample = auth_sample(row)
            elif entry['source'] == 'encrypted':
                require(row['request_id'] in capsules, 'frozen_capsule_missing')
                sample = sample_from_capsule(row, capsules[row['request_id']])
            else:
                sample = read_legacy(row, root)
            require(sample_manifest(sample) == entry, 'frozen_capture_changed')
            samples.append(sample)
        require(0 < len(samples) <= MAX_SAMPLES and sum(len(s['body']) for s in samples) <= MAX_CORPUS_BYTES,
                'frozen_corpus_budget')
        return samples, {}, plan['observed_combinations'], digest(encoded(plan))
    samples, gaps, total = collect(root, gaps_out, capsules, pin_observations=True)
    if gaps:
        return samples, gaps, total, None
    plan = {'schema': 2, 'created_at': time.time(), 'observed_combinations': total,
            'samples': [sample_manifest(s) for s in samples],
            'observations_sha256': digest((root / 'bluegreen-replay-observed.json').read_bytes())}
    write_json(path, plan)
    return samples, gaps, total, digest(encoded(plan))


def case_metadata(row):
    return {k: row[k] for k in ('user_id', 'requested_model', 'inbound_endpoint', 'stream',
                                'tool_calls_present', 'multimodal_present')}


def sse_events(raw):
    data, event_type = [], ''
    for line in raw.decode('utf-8').splitlines(keepends=True):
        require(line.endswith('\n'), 'stream_frame_incomplete')
        line = line.rstrip('\r\n')
        if not line:
            if event_type == 'error':
                yield {'type': 'error'}
            if data:
                value = '\n'.join(data).strip()
                if value:
                    yield '[DONE]' if value == '[DONE]' else json.loads(value)
            data, event_type = [], ''
        else:
            field, _, value = line.partition(':')
            value = value.removeprefix(' ')
            if field == 'data':
                data.append(value)
            elif field == 'event':
                event_type = value
    require(not data and not event_type, 'stream_frame_incomplete')


def response_reason(status, ctype, raw, stream, path=""):
    """Return a bounded reason code; never return response text or error messages."""
    if not 200 <= status < 300:
        return 'http_status'
    if not raw:
        return 'response_empty'
    try:
        if stream or 'text/event-stream' in ctype:
            if 'text/event-stream' not in ctype:
                return 'stream_content_type'
            terminal = False
            for event in sse_events(raw):
                if event == '[DONE]':
                    terminal = True
                    continue
                if not isinstance(event, dict):
                    return 'stream_event_shape'
                if response_error_details(encoded(event)):
                    return 'upstream_error'
                terminal |= event.get('type') in ('message_stop', 'response.completed')
                terminal |= any(c.get('finish_reason') for c in event.get('choices', []))
                terminal |= any(c.get('finishReason') for c in event.get('candidates', []))
            return 'ok' if terminal else 'stream_terminal_missing'
        if 'json' in ctype:
            data = json.loads(raw)
            if response_error_details(raw):
                return 'upstream_error'
            return 'ok' if isinstance(data, (list, dict)) and data else 'response_shape'
        if urlsplit(path).path in ('/v1/audio/transcriptions','/audio/transcriptions') and ctype in ('text/plain','text/vtt','application/x-subrip'):
            return 'ok' if raw.strip() else 'response_empty'
        return 'ok' if ctype.startswith(('audio/', 'image/')) else 'response_content_type'
    except ReplayError:
        return 'stream_frame_incomplete'
    except (ValueError, TypeError, AttributeError):
        return 'response_invalid_json'


def response_error_details(raw):
    """Classify provider errors without persisting their free-form messages."""
    try:
        text = raw.decode('utf-8')
        try:
            events = [json.loads(text)]
        except ValueError:
            events = []
            for line in text.splitlines():
                if line.startswith('data:') and line[5:].strip() != '[DONE]':
                    try:
                        events.append(json.loads(line[5:].strip()))
                    except ValueError:
                        continue  # Malformed framing is already a failed response.
        for event in events:
            if not isinstance(event, dict):
                continue
            error = event.get('error')
            response = event.get('response')
            if not error and isinstance(response, dict):
                error = response.get('error')
            if not error and event.get('type') in ('error', 'response.failed', 'response.incomplete'):
                error = {'type': event['type']}
            if not error and event.get('status') in ('failed', 'incomplete'):
                error = {'status': event['status']}
            if not error:
                continue
            error_text = json.dumps(error, ensure_ascii=False).lower()
            categories = {'quota': ('quota', 'balance', '余额', '欠费'),
                          'rate_limit': ('rate limit', 'rate_limit', '频率'),
                          'timeout': ('timeout', 'timed out', '超时'),
                          'content_filter': ('content filter', 'content_filter', 'moderation', 'sensitive', '敏感'),
                          'authentication': ('authentication', 'unauthorized', 'invalid api key'),
                          'context_limit': ('context length', 'context_length', 'maximum context'),
                          'overloaded': ('overloaded', 'over capacity')}
            details = {'error_sha256': digest(encoded(error)),
                       'error_message_categories': [k for k, terms in categories.items()
                                                    if any(term in error_text for term in terms)]}
            if event.get('type') in ('error', 'response.failed', 'response.incomplete'):
                details['error_event_type'] = event['type']
            if isinstance(response, dict) and isinstance(response.get('incomplete_details'), dict):
                reason = response['incomplete_details'].get('reason')
                if reason in ('max_output_tokens', 'content_filter'):
                    details['incomplete_reason'] = reason
            if isinstance(error, dict):
                code = error.get('code')
                if isinstance(code, int) and not isinstance(code, bool) and 0 <= code <= 2**32:
                    details['error_numeric_code'] = code
            return details
    except (ValueError, TypeError):
        return {}  # Invalid payload remains red; never print parsing input.
    return {}


def execute(sample, key, port, replay_id):
    # Production non-stream p95 exceeds 50s. Give retained slow requests their
    # observed duration plus headroom, still bounded per request and per run.
    budget = min(900, max(300, sample['row'].get('duration_ms', 0) / 1000 * 2 + 30))
    started = time.monotonic()
    deadline = started + budget
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=min(300, budget))
    status, response_id, ctype = 0, None, ''
    phase, reason = 'request', 'request_deadline'
    raw = bytearray()
    try:
        # No proxy or redirects: credentials remain on the loopback connection.
        headers = {'Authorization': 'Bearer ' + key, 'x-api-key': key,
                   'x-goog-api-key': key, 'X-Client-Request-ID': replay_id}
        if sample.get('source') != 'encrypted':
            headers.update({'Content-Type': 'application/json', 'anthropic-version': '2023-06-01',
                            'User-Agent': 'tokenkey-private-replay'})
        headers.update(sample.get('headers', {}))
        connection.request(sample.get('method', 'POST'), sample['path'], body=sample['body'], headers=headers)
        phase = 'response_headers'
        response = connection.getresponse()
        status = response.status
        response_id = response.getheader('X-Request-ID')
        if not response_id or not re.fullmatch(r'[A-Za-z0-9_.:-]{1,128}', response_id):
            response_id = None
        ctype = response.getheader('Content-Type', '').split(';', 1)[0].strip().lower()
        phase = 'response_body'
        while time.monotonic() < deadline:
            # read1 is bounded by both the idle limit and the absolute deadline.
            # HTTP/1.0 close responses detach connection.sock after getresponse.
            if response.fp is not None:
                response.fp.raw._sock.settimeout(min(120, max(0.001, deadline - time.monotonic())))
            part = response.read1(min(65536, MAX_BYTES + 1 - len(raw)))
            if not part:
                reason = ('response_truncated' if response.length not in (None, 0) else
                          response_reason(status, ctype, raw, sample['row']['stream'], sample['path']))
                break
            raw.extend(part)
            if len(raw) > MAX_BYTES:
                reason = 'response_too_large'
                break
    except TimeoutError:
        reason = 'socket_timeout'
    except (OSError, http.client.HTTPException):
        reason = 'transport_error'
    finally:
        connection.close()
    if sample.get('source') == 'synthetic_auth_negative' and reason == 'http_status':
        try:
            data = json.loads(raw)
            error = data.get('error', {})
            code = data.get('code') or (error.get('code') if isinstance(error, dict) else None)
            if status == sample['expected_status'] and code == sample['expected_code']:
                reason = 'ok'
        except (ValueError, AttributeError):
            pass
    if reason == 'ok' and sample.get('method') == 'GET':
        try:
            data = json.loads(raw)
            path = urlsplit(sample['path']).path
            valid = isinstance(data, dict) and (isinstance(data.get('data'), list) if path == '/v1/models'
                    else data.get('mode') in ('quota_limited', 'unrestricted') and isinstance(data.get('isValid'), bool))
            if not valid:
                reason = 'get_response_shape'
        except ValueError:
            reason = 'get_response_shape'
    details = response_error_details(raw) if reason != 'ok' else {}
    return {**details, 'sample_sha256': digest(encoded(sample['row'])), 'http_status': status,
            'passed': reason == 'ok', 'reason': reason, 'phase': phase,
            'elapsed_ms': round((time.monotonic() - started) * 1000),
            'response_bytes': len(raw), 'response_sha256': digest(raw),
            'response_content_type': ctype if ctype in ('application/json', 'text/event-stream',
                'audio/mpeg', 'audio/wav', 'image/png', 'image/jpeg') else 'other',
            'body_sha256': digest(sample['body']), 'response_request_id': response_id}


def execute_auth(sample, key, app, request_id):
    result = json.loads(run(['docker', 'exec', '-i', app, '/app/replay-capsule', 'auth-check'],
                            input=encoded({'key': key, 'port': 8080, 'request_id': request_id,
                                           'body': base64.b64encode(sample['body']).decode()})))
    allowed = {'http_status', 'error_code', 'response_request_id', 'response_sha256', 'response_bytes', 'elapsed_ms'}
    require(set(result) == allowed, 'invalid_auth_check_result')
    passed = result['http_status'] == sample['expected_status'] and result['error_code'] == sample['expected_code']
    return dict(result, passed=passed, reason='ok' if passed else 'auth_rejection_mismatch',
                phase='auth_negative', body_sha256=digest(sample['body']), sample_sha256=digest(encoded(sample['row'])))


def production_usage_query(prefix, results):
    # Storage uses local:<server UUID> or legacy client:<client marker>.
    # Check all supported namespaces, including requests that timed out before
    # receiving a server ID. Identifiers are validated before SQL construction.
    require(re.fullmatch(r'[A-Za-z0-9-]+', prefix), 'invalid_replay_prefix')
    ids = {r['response_request_id'] for r in results if r.get('response_request_id')}
    require(all(re.fullmatch(r'[A-Za-z0-9_.:-]{1,128}', rid) for rid in ids), 'invalid_response_id')
    literals = ','.join("'" + ns + rid + "'" for rid in sorted(ids) for ns in ('', 'local:', 'client:')) or 'NULL'
    prefixes = ' OR '.join("request_id LIKE '" + ns + prefix + "%'" for ns in ('', 'local:', 'client:'))
    return ("SELECT count(*) FROM usage_logs WHERE created_at >= now()-interval '3 hours' "
            "AND (" + prefixes + " OR request_id IN (" + literals + "));")


def credential_snapshot_query():
    return f"""SELECT row_to_json(t) FROM (SELECT k.id,k.user_id,k.key,k.status,k.deleted_at,
    (k.expires_at IS NOT NULL AND k.expires_at<=now()) AS expired, {KEY_STATE_SQL} AS key_state FROM api_keys k
    -- ops-allow-soft-deleted: negative auth uses the original revoked key without reactivation.
    ) t;"""


def balance_snapshot_query():
    return """SELECT md5(coalesce(string_agg(id::text||':'||balance::text,',' ORDER BY id),'')) FROM users
    -- ops-allow-soft-deleted: rejection must not charge even deleted users.
    ;"""


SELF_CHECK_EXEMPT: dict[str, str] = {}


def iter_self_check_sql():
    return [('KEY_STATE_SQL', 'SELECT ' + KEY_STATE_SQL + ' FROM api_keys k /* ops-allow-soft-deleted: test the shared rejection-state expression */;'),
            ('credential_snapshot_query', credential_snapshot_query()),
            ('balance_snapshot_query', balance_snapshot_query()),
            ('capture_query', capture_query()), ('capture_query_capsules', capture_query(('retained-request',))),
            ('production_usage_query', production_usage_query('tk-replay-check-',
             [{'response_request_id': 'server-uuid'}]))]


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
    def __init__(self, candidate, root=ROOT, internal=False):
        self.internal = internal
        self.root = root
        self.candidate = candidate
        self.name = 'tk-replay-' + secrets.token_hex(6)
        self.path = root / self.name
        self.created = []
        self.network_created = False

    def start_container(self, suffix, arguments):
        name = self.name + suffix
        self.created.append(name)
        network = ('none' if suffix == '-pg' else 'container:' + self.name + '-pg') if self.internal else self.name
        run(['docker', 'run', '-d', '--name', name, '--network', network,
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
        if self.internal:
            env.update(DATABASE_HOST='127.0.0.1', REDIS_HOST='127.0.0.1')
        else:
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
                                          *([] if self.internal else ['-p', '127.0.0.1::8080']), '-v', str(data) + ':/app/data', self.candidate['Image']])
        self.app = app
        state = inspect(app)
        self.verify(state, env)
        port = 0 if self.internal else int(state['NetworkSettings']['Ports']['8080/tcp'][0]['HostPort'])
        for _ in range(120):
            if self.internal:
                try:
                    run(['docker', 'exec', app, 'wget', '-q', '-T', '2', '-O', '/dev/null', 'http://127.0.0.1:8080/health'])
                    self.env = env
                    return port
                except ReplayError:
                    time.sleep(1)
                    continue
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
        if self.internal:
            namespace = inspect(self.name + '-pg')
            require(namespace['HostConfig']['NetworkMode'] == 'none', 'negative_auth_network_not_none')
            expected_mode = 'container:' + namespace['Id']
            require(state['HostConfig']['NetworkMode'] == expected_mode
                    and inspect(self.name + '-redis')['HostConfig']['NetworkMode'] == expected_mode,
                    'negative_auth_namespace_changed')
        else:
            network = json.loads(run(['docker', 'network', 'inspect', self.name]))[0]
            require(not network.get('Internal'), 'replay_network_egress_changed')
        observed = environment(state)
        require(state['Image'] == self.candidate['Image'], 'replay_image_changed')
        if not self.internal:
            require(set(state['NetworkSettings']['Networks']) == {self.name}, 'replay_network_not_isolated')
        ports = state['NetworkSettings']['Ports'].get('8080/tcp')
        if self.internal:
            require(not any(state['NetworkSettings']['Ports'].values()), 'negative_auth_port_published')
        else:
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
        self.created = []
        self.network_created = False


def replay(tag, root=ROOT):
    prepared_sha, before = prepared(tag, root)
    record = {'schema': 2, 'tag': tag, 'prepared_receipt': prepared_sha,
              'verdict': 'red', 'cutover': False, 'approval_pending': True,
              'billing': 'isolated_snapshot', 'upstream_quota_consumed': False,
              'header_fidelity': 'encrypted allowlisted protocol headers; legacy reconstructed; auth from original key',
              'executor_sha256': digest(Path(__file__).read_bytes()), 'started_at': time.time()}
    receipt_path = root / 'bluegreen-replay.json'
    # Invalidate an older successful attempt before doing any paid work.
    write_json(receipt_path, seal(record))
    candidate = inspect('tokenkey-' + before['target'])
    sandbox = Sandbox(candidate, root, internal=True)
    results = []
    write_json(root / 'bluegreen-replay-results.json', results)
    try:
        samples, gaps, total, manifest_sha = frozen_samples(root, candidate, results)
        record['corpus_manifest_sha256'] = manifest_sha
        positives = [s for s in samples if s['source'] != 'synthetic_auth_negative']
        record['coverage'] = {'observed_combinations': total, 'selected': len(samples),
                              'users': len({s['row']['user_id'] for s in positives}),
                              'models': len({s['row']['requested_model'] for s in positives if s['row']['requested_model']}),
                              'protocols': len({s['row']['inbound_endpoint'] for s in positives}), 'gaps': gaps}
        require(not gaps, 'collection_not_ready')
        require(samples, 'no_replayable_captures')
        require(all(record['coverage'][k] >= 2 for k in ('users', 'models', 'protocols')), 'insufficient_diversity')
        prefix = sandbox.name + '-'
        for negative in (True, False):
            phase_samples = [s for s in samples if (s['source'] == 'synthetic_auth_negative') == negative]
            if not phase_samples:
                continue
            if not negative:
                sandbox.close()
                sandbox = Sandbox(candidate, root)
            port = sandbox.start()
            keys = {r['id']: r for r in (json.loads(line) for line in sql(
                credential_snapshot_query(),
                sandbox.pg, sandbox.database).splitlines())}
            balances_before = sql(balance_snapshot_query(), sandbox.pg, sandbox.database)
            for i, sample in enumerate(phase_samples):
                require(snapshot(root) == before, 'public_or_prepared_state_changed')
                sandbox.verify(inspect(sandbox.app), sandbox.env)
                row = sample['row']
                key = keys.get(row['api_key_id'])
                state = key['key_state'] if key else 'missing'
                expected = row.get('key_state', 'active')
                if not key or key['user_id'] != row['user_id'] or state != expected:
                    results.append({'sample_sha256': digest(encoded(row)), 'passed': False, 'reason': 'credential_state_changed'})
                else:
                    if not negative:
                        record['upstream_quota_consumed'] = True
                    request_id = prefix + str(negative) + '-' + str(i)
                    results.append(execute_auth(sample, key['key'], sandbox.app, request_id) if negative
                                   else execute(sample, key['key'], port, request_id))
                results[-1].update(case_metadata(row), test_kind='auth_negative' if negative else 'positive')
                if negative:
                    witness = next(s for s in positives if stratum(s['row'])[1:] == stratum(row)[1:])
                    results[-1]['business_positive_sample_sha256'] = digest(encoded(witness['row']))
                write_json(root / 'bluegreen-replay-results.json', results)
            if negative:
                run(['docker', 'stop', '--time', '30', sandbox.app])
                require(int(sql('SELECT count(*) FROM usage_logs;', sandbox.pg, sandbox.database).strip()) == 0,
                        'negative_auth_usage_written')
                require(balances_before == sql(balance_snapshot_query(), sandbox.pg, sandbox.database),
                        'negative_auth_balance_changed')
                record['negative_auth_egress'] = 'shared_network_none'
        production_writes = int(sql(production_usage_query(prefix, results)).strip())
        require(production_writes == 0, 'production_usage_written')
        record['production_usage_rows'] = production_writes
        record['corpus_sha256'] = digest(encoded([{'row': s['row'], 'body_sha256': digest(s['body'])} for s in samples]))
        # Counts are derived from complete execution, not caller-provided claims.
        require(not gaps and len(results) == total and all(r['passed'] for r in results), 'replay_failed_or_coverage_gap')
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
        record['positive_passed'] = sum(r['passed'] for r in results if r.get('test_kind') == 'positive')
        record['auth_negative_passed'] = sum(r['passed'] for r in results if r.get('test_kind') == 'auth_negative')
        record['passed'] = sum(r['passed'] for r in results)
        record['failed'] = len(results) - record['passed']
        record['results_sha256'] = digest(encoded(results))
        write_json(root / 'bluegreen-replay-results.json', results)
        record['finished_at'] = time.time()
        record = seal(record)
        write_json(receipt_path, record)
    return record


def main():
    p = argparse.ArgumentParser()
    p.add_argument('operation', choices=('status', 'collect', 'reset-corpus', 'run', 'gate'))
    p.add_argument('--tag', required=True)
    p.add_argument('--receipt', default='')
    p.add_argument('--corpus', default='', help='exact manifest hash required to start a new collection')
    args = p.parse_args()
    require(re.fullmatch(r'\d+\.\d+\.\d+', args.tag), 'invalid_tag')
    os.umask(0o077)
    with (ROOT / 'bluegreen-deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.operation == 'status' and not (ROOT / 'bluegreen-prepared.json').exists():
            # Refuse legacy and missing capture prerequisites BEFORE prepare.
            require((ROOT / 'active-color').read_text().strip() in ('blue', 'green'), 'requires_bluegreen_prod')
            require(shutil.which('zstd'), 'zstd_required')
            write_json(ROOT / 'bluegreen-replay.json', seal({'tag': args.tag, 'verdict': 'red',
                       'cutover': False, 'approval_pending': True, 'reason': 'prepare_pending'}))
            result = {'needs_prepare': True}
        elif args.operation == 'reset-corpus':
            path = ROOT / 'bluegreen-replay-corpus.json'
            if not path.exists():
                path = ROOT / 'bluegreen-replay-observed.json'
            plan = json.loads(path.read_bytes())
            require(digest(encoded(plan)) == args.corpus, 'corpus_reset_hash_mismatch')
            write_json(ROOT / 'bluegreen-replay.json', seal({'schema': 2, 'tag': args.tag,
                       'verdict': 'red', 'cutover': False, 'approval_pending': True, 'reason': 'new_collection_required'}))
            path.unlink()
            observed_path = ROOT / 'bluegreen-replay-observed.json'
            if observed_path.exists():
                observed_path.unlink()
            result = {'verdict': 'red', 'reason': 'new_collection_required', 'cutover': False}
        elif args.operation == 'collect':
            _, state = prepared(args.tag)
            gaps_out = []
            samples, gaps, total, manifest = frozen_samples(ROOT, inspect('tokenkey-' + state['target']), gaps_out)
            result = {'collection_ready': not gaps, 'observed_combinations': total, 'selected': len(samples),
                      'gaps': gaps, 'corpus_manifest_sha256': manifest,
                      'observations_sha256': digest(encoded(json.loads((ROOT / 'bluegreen-replay-observed.json').read_bytes()))),
                      'cutover': False}
            write_json(ROOT / 'bluegreen-replay-collection.json', result)
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
