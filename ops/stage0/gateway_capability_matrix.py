#!/usr/bin/env python3
"""Finite verification obligations; no gateway imports, captures, credentials or I/O to prod."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import quote

from gateway_capability_scenarios import media_request

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_MANIFEST = Path(__file__).with_name('gateway-capability-matrix.json')
DEFAULT_INVENTORY = Path(__file__).with_name('gateway-account-supply.json')
KEY_TYPES = {'universal'}
EXECUTION_BLOCKER = 'upstream_execution_not_requested'
PROTOCOLS = {'openai-chat', 'openai-responses', 'anthropic-messages', 'gemini-content',
             'openai-images', 'openai-embeddings', 'openai-audio', 'openai-video', 'openai-transcription'}
REQUEST_TYPES = {'plain', 'tool', 'thinking', 'multimodal', 'count_tokens'}
PATHS = {'/v1/chat/completions', '/v1/responses', '/v1/responses/input_tokens',
         '/v1/messages', '/v1/messages/count_tokens', '/v1/images/generations',
         '/v1/embeddings', '/v1/audio/speech', '/v1/audio/transcriptions', '/v1/videos'}
ASSERTIONS = {'protocol_envelope', 'no_error', 'terminal', 'usage', 'tool_call'}


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode()


def digest(value):
    return hashlib.sha256(encoded(value)).hexdigest()


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def valid_path(path):
    return path in PATHS or bool(re.fullmatch(
        r'/v1beta/models/[^/?#\s]+:(generateContent|countTokens|streamGenerateContent)(\?alt=sse)?', path))


def load(path=DEFAULT_MANIFEST):
    path = Path(path)
    raw = json.loads(path.read_text())
    require(isinstance(raw, dict) and raw.get('schema') == 1, 'invalid matrix schema')
    profiles = raw.get('profiles')
    require(isinstance(profiles, list) and profiles, 'empty profiles')
    seen = set()
    out = []
    for profile in profiles:
        require(isinstance(profile, dict), 'invalid profile')
        ident = profile.get('id')
        require(isinstance(ident, str) and re.fullmatch(r'[a-z0-9_.-]+', ident)
                and ident not in seen, 'invalid or duplicate profile id')
        seen.add(ident)
        require(profile.get('protocol') in PROTOCOLS and profile.get('request_type') in REQUEST_TYPES,
                'unknown profile dimensions')
        require(profile.get('key_types') == ['universal'], 'universal-only profiles required')
        require(isinstance(profile.get('mode'), str), 'missing model mode')
        require(not {'status', 'tested', 'passed'} & profile.keys(), 'coverage status must be derived')
        fixture = None
        if profile.get('fixture'):
            fixture_path = (path.parent / profile['fixture']).resolve()
            require(fixture_path.is_relative_to(path.parent.resolve()), 'fixture escapes matrix directory')
            fixture = json.loads(fixture_path.read_text())
            require(fixture.get('method') == 'POST' and valid_path(fixture.get('path', '')), 'invalid fixture endpoint')
            require(isinstance(fixture.get('body'), dict) and fixture['body'], 'fixture body required')
            require(type(fixture.get('stream')) is bool, 'fixture stream flag required')
            require(set(fixture.get('assertions', [])) <= ASSERTIONS and
                    {'protocol_envelope', 'no_error', 'terminal', 'usage'} <= set(fixture.get('assertions', [])),
                    'fixture assertions required')
            require('${model}' in json.dumps(fixture), 'fixture must bind a concrete model')
        else:
            require(bool(profile.get('blocked_reason')), 'missing fixture must remain an infrastructure gap')
        out.append({**profile, 'request': fixture})
    return out


def render(fixture, model):
    def replace(value):
        if isinstance(value, str):
            return value.replace('${model}', model)
        if isinstance(value, list):
            return [replace(v) for v in value]
        if isinstance(value, dict):
            return {k: replace(v) for k, v in value.items()}
        return value
    result = replace(fixture)
    # Model IDs can contain vendor slashes; encode them as one Gemini path segment.
    result['path'] = fixture['path'].replace('${model}', quote(model, safe=''))
    require(valid_path(result['path']), 'rendered endpoint invalid')
    return result


def validate_inventory(inventory):
    require(isinstance(inventory, dict) and inventory.get('schema') == 1 and
            inventory.get('kind') == 'account-supply-representatives', 'account supply inventory required')
    require(set(inventory) <= {'schema', 'kind', 'source', 'classes'}, 'unknown inventory fields')
    classes = inventory.get('classes')
    require(isinstance(classes, list) and classes, 'empty account supply inventory')
    seen = set()
    for cls in classes:
        require(set(cls) == {'id', 'platform', 'auth_type', 'channel_type', 'dialect', 'native_protocols',
                             'exclusive_endpoints', 'branch_family', 'representatives'}, 'invalid account class fields')
        require(isinstance(cls['id'], str) and re.fullmatch(r'[a-z0-9.-]+', cls['id']) and
                cls['id'] not in seen, 'invalid or duplicate account class')
        seen.add(cls['id'])
        require(type(cls['channel_type']) is int and cls['channel_type'] >= 0 and
                type(cls['exclusive_endpoints']) is bool, 'invalid account class declaration')
        require(isinstance(cls['native_protocols'], list) and
                set(cls['native_protocols']) <= GENERATION_PROTOCOLS, 'invalid native protocols')
        require(isinstance(cls['representatives'], list) and cls['representatives'], 'empty representatives')
        families = set()
        generation_families = set()
        for rep in cls['representatives']:
            require(set(rep) == {'family', 'model', 'upstream_model', 'operation', 'baseline_protocol',
                                 'represented_models'}, 'invalid representative fields')
            for field in ('family', 'model', 'upstream_model'):
                require(isinstance(rep[field], str) and re.fullmatch(r'[a-zA-Z0-9_./:+@\[\]-]+', rep[field]),
                        'invalid representative identity')
            require(rep['family'] not in families, 'duplicate model family')
            families.add(rep['family'])
            require(rep['operation'] in OPERATION_PROTOCOLS and
                    rep['baseline_protocol'] in OPERATION_PROTOCOLS[rep['operation']],
                    'unknown operation or protocol')
            require(isinstance(rep['represented_models'], list) and rep['model'] in rep['represented_models'] and
                    all(isinstance(m, str) for m in rep['represented_models']), 'representative missing from model set')
            if rep['operation'] == 'generation':
                generation_families.add(rep['family'])
                require(rep['baseline_protocol'] in cls['native_protocols'], 'baseline must use declared native protocol')
        require(cls['branch_family'] in generation_families if generation_families else cls['branch_family'] is None,
                'branch representative must be a generation family')
    # These lists represent sets, not selection priority. Reordering a reviewed
    # inventory must not invalidate release evidence or create a false delta.
    return {**inventory, 'classes': [
        {**cls, 'native_protocols': sorted(set(cls['native_protocols'])), 'representatives': [
            {**rep, 'represented_models': sorted(set(rep['represented_models']))}
            for rep in sorted(cls['representatives'], key=lambda r: r['family'])]}
        for cls in sorted(classes, key=lambda c: c['id'])]}


GENERATION_PROTOCOLS = {'anthropic-messages', 'openai-chat', 'openai-responses', 'gemini-content'}
OPERATION_PROTOCOLS = {'generation': GENERATION_PROTOCOLS, 'embedding': {'openai-embeddings'},
                       'image': {'openai-images'}, 'video': {'openai-video'}, 'speech': {'openai-audio'},
                       'transcription': {'openai-transcription'}, 'content-image': {'gemini-content'}}


def build(inventory, profiles):
    """One representative per account/model family, with class-level branch coverage.

    Inventory is a reviewed, sanitized projection, not a live availability claim.
    No account IDs, credentials, traffic counters or public catalog are needed.
    """
    inventory = validate_inventory(inventory)
    fixtures = {p['id']: p for p in profiles}
    entries = []

    def emit(cls, rep, protocol, scenario, layer):
        suffix = {'plain-stream': 'plain.stream', 'tool-roundtrip': 'tool', 'thinking': 'thinking',
                  'vision': 'multimodal', 'count-tokens': 'count_tokens'}.get(scenario, 'plain')
        profile_id = protocol + '.' + suffix
        profile = fixtures.get(profile_id)
        request = render(profile['request'], rep['model']) if profile and profile.get('request') else None
        reason = profile.get('blocked_reason') if profile else 'fixture_required'
        if scenario in ('image', 'content-image', 'speech', 'video', 'transcription'):
            audio = next((p['request']['body']['audio_base64'] for p in profiles
                          if p['id'] == 'openai-transcription.plain'), '')
            request = media_request(scenario, rep['model'], audio)
            reason = None
        if scenario == 'count-tokens' and protocol == 'openai-chat':
            reason = 'protocol_operation_not_defined'
        entry = {'account_class': cls['id'], 'model_family': rep['family'], 'model': rep['model'],
                 'upstream_model': rep['upstream_model'], 'profile': profile_id, 'protocol': protocol,
                 'request_type': {'plain-stream': 'plain', 'tool-roundtrip': 'tool', 'thinking': 'thinking',
                                  'vision': 'multimodal', 'count-tokens': 'count_tokens'}.get(scenario, 'plain'),
                 'scenario': scenario, 'key_type': 'universal', 'request': request,
                 'blocked_reason': reason, 'selection': layer, 'plan_validation': 'required'}
        # Equivalence semantics invalidate evidence even if the chosen model is unchanged.
        entry['supply_sha256'] = digest(cls)
        entry['scenario_sha256'] = hashlib.sha256(Path(__file__).with_name('gateway_capability_scenarios.py').read_bytes()).hexdigest()
        entry['id'] = digest([cls['id'], rep['family'], protocol, scenario, 'universal'])
        entry['case_sha256'] = digest(entry)
        entries.append(entry)

    for cls in sorted(inventory['classes'], key=lambda c: c['id']):
        representatives = sorted(cls['representatives'], key=lambda r: r['family'])
        for rep in representatives:
            emit(cls, rep, rep['baseline_protocol'],
                 'plain-buffered' if rep['operation'] == 'generation' else rep['operation'], 'model-family-baseline')
        if cls['branch_family'] is None:
            continue
        branch = next(r for r in representatives if r['family'] == cls['branch_family'])
        for protocol in sorted(GENERATION_PROTOCOLS):
            if not any(e['account_class'] == cls['id'] and e['protocol'] == protocol and
                       e['scenario'] == 'plain-buffered' for e in entries):
                emit(cls, branch, protocol, 'plain-buffered', 'protocol-branch')
        for scenario in ('plain-stream', 'tool-roundtrip', 'thinking', 'vision', 'count-tokens'):
            emit(cls, branch, branch['baseline_protocol'], scenario, 'request-branch')
    result = {'schema': 1, 'inventory_sha256': digest(inventory), 'entries': entries}
    result['plan_sha256'] = digest(result)
    return validate_plan(result)


def validate_plan(plan):
    require(isinstance(plan, dict) and plan.get('schema') == 1 and
            isinstance(plan.get('inventory_sha256'), str), 'account-supply plan required')
    require(plan.get('plan_sha256') == digest({k: v for k, v in plan.items() if k != 'plan_sha256'}), 'plan hash mismatch')
    require(isinstance(plan.get('entries'), list) and plan['entries'], 'empty plan')
    seen = set()
    for case in plan['entries']:
        require(case['id'] not in seen and case['key_type'] in KEY_TYPES and
                isinstance(case.get('account_class'), str) and bool(case['account_class']) and
                case.get('plan_validation') == 'required', 'invalid case identity')
        require(case['case_sha256'] == digest({k: v for k, v in case.items() if k != 'case_sha256'}), 'case hash mismatch')
        require(case['request'] is None and case.get('blocked_reason') or isinstance(case['request'], dict) and
                valid_path(case['request'].get('path', '')) and isinstance(case['request'].get('body'), dict),
                'unsafe or incomplete execution request')
        seen.add(case['id'])
    return plan


def delta(plan, previous=None):
    validate_plan(plan)
    if previous is None:
        return plan['entries']
    validate_plan(previous)
    old = {e['id']: e['case_sha256'] for e in previous['entries']}
    return [e for e in plan['entries'] if old.get(e['id']) != e['case_sha256']]


def select(cases, limit=None):
    """Default to the full executable set; only an explicit limit permits truncation."""
    require(limit is None or type(limit) is int and limit > 0, 'request limit must be positive')
    selected = sorted((c for c in cases if not c.get('blocked_reason')), key=lambda c: c['id'])
    return selected if limit is None else selected[:limit]


def report(plan, results=None, previous=None):
    scope = delta(plan, previous)
    records = {}
    if results is not None:
        require(results.get('plan_sha256') == plan['plan_sha256'], 'results belong to a different plan')
        require(results.get('execution_kind') in ('isolated_gateway', 'harness'), 'result execution kind required')
        for result in results.get('results', []):
            require(result['id'] not in records, 'duplicate result')
            records[result['id']] = result
        require(set(records) <= {c['id'] for c in plan['entries']}, 'unknown result case')
    output = []
    for case in plan['entries']:
        result = records.get(case['id'])
        if result:
            require(result.get('case_sha256') == case['case_sha256'], 'stale fixture result')
            require(result.get('status') in ('passed', 'failed', 'unsupported', 'blocked-by-test-infrastructure'), 'invalid result status')
            status = result['status']
            if status == 'passed' and results['execution_kind'] == 'isolated_gateway':
                proof = result.get('execution_proof', {})
                require(results.get('isolation_verified') is True and results.get('cleanup_verified') is True
                        and results.get('cutover') is False and results.get('production_usage_rows') == 0,
                        'isolated_execution_not_verified')
                require(proof.get('account_class') == case['account_class'] and
                        proof.get('key_type') == 'universal' and type(proof.get('bound_account_id')) is int
                        and proof['bound_account_id'] > 0 and proof.get('request_ids') and
                        proof.get('routing_validation') in ('canonical_gateway', 'media_handler', 'tokenizer_endpoint'),
                        'account_class_execution_binding_required')
                require(proof.get('observed_account_ids') == [proof['bound_account_id']]
                        or case['request_type'] == 'count_tokens' and proof.get('attribution') == 'unmetered_endpoint',
                        'account_attribution_missing')
            if status == 'passed' and results['execution_kind'] == 'harness':
                status = 'harness-passed'  # Never gateway service evidence.
        else:
            status = 'blocked-by-test-infrastructure' if case.get('blocked_reason') else 'declared-but-untested'
        output.append({k: case[k] for k in ('id', 'account_class', 'model_family', 'model', 'protocol', 'request_type', 'key_type', 'profile', 'scenario')} |
                      {'status': status, 'reason': result.get('reason') if result else case.get('blocked_reason')})
    scope_ids = {e['id'] for e in scope}
    counts = {}
    for row in output:
        counts[row['status']] = counts.get(row['status'], 0) + 1
    complete = all(r['status'] == 'passed' for r in output if r['id'] in scope_ids)
    return {'schema': 1, 'plan_sha256': plan['plan_sha256'], 'coverage': counts,
            'total': len(output), 'delta': len(scope), 'scope_complete': complete,
            'verdict': 'no_changes' if not scope else 'passed' if complete else 'incomplete',
            'cutover': False, 'deployment_gate': False,
            'execution_blocker': None if results and results['execution_kind'] == 'isolated_gateway' else EXECUTION_BLOCKER,
            'entries': output}


def from_tag(tag):
    """Read the previous release projection and fixtures; never switch a checkout."""
    require(isinstance(tag, str) and re.fullmatch(r'\d+\.\d+\.\d+', tag), 'invalid baseline tag')
    ref = 'v' + tag
    subprocess.run(['git', 'rev-parse', '--verify', ref + '^{commit}'], cwd=ROOT,
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    def blob(path, optional=False):
        result = subprocess.run(['git', 'show', ref + ':' + path], cwd=ROOT, capture_output=True)
        if result.returncode:
            if optional:
                return None
            raise ValueError('previous release fixture missing')
        return result.stdout
    inventory = blob('ops/stage0/gateway-account-supply.json', optional=True)
    manifest = blob('ops/stage0/gateway-capability-matrix.json', optional=True)
    if inventory is None or manifest is None:
        return None  # Older release predates this check; current full plan is the baseline.
    generator = blob('ops/stage0/gateway_capability_matrix.py', optional=True)
    if generator != Path(__file__).read_bytes():
        # Rebuilding with changed rules would retrofit new obligations into the
        # previous release and silently erase their delta. Use a full baseline.
        return None
    if blob('ops/stage0/gateway_capability_scenarios.py', optional=True) != Path(__file__).with_name('gateway_capability_scenarios.py').read_bytes():
        return None
    with tempfile.TemporaryDirectory(prefix='tk-capability-baseline-') as directory:
        root = Path(directory).resolve()
        (root / 'matrix.json').write_bytes(manifest)
        for profile in json.loads(manifest)['profiles']:
            fixture = profile.get('fixture')
            if fixture:
                path = (root / fixture).resolve()
                require(path.is_relative_to(root), 'baseline fixture escapes directory')
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(blob('ops/stage0/' + fixture))
        return build(json.loads(inventory), load(root / 'matrix.json'))
