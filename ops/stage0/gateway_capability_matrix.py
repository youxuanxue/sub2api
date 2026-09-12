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

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_MANIFEST = Path(__file__).with_name('gateway-capability-matrix.json')
KEY_TYPES = {'direct', 'universal'}
PROTOCOLS = {'openai-chat', 'openai-responses', 'anthropic-messages', 'gemini-content',
             'openai-images', 'openai-embeddings', 'openai-audio', 'openai-video', 'openai-transcription'}
REQUEST_TYPES = {'plain', 'tool', 'thinking', 'multimodal', 'count_tokens'}
PATHS = {'/v1/chat/completions', '/v1/responses', '/v1/responses/input_tokens',
         '/v1/messages', '/v1/messages/count_tokens', '/v1/images/generations',
         '/v1/embeddings', '/v1/audio/speech'}
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
        require(profile.get('key_types') == ['direct', 'universal'], 'both key types are required')
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
        out.append({**profile, 'request': fixture, 'baseline_protocol_by_vendor': raw['baseline_protocol_by_vendor']})
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


def build(catalog, profiles):
    require(isinstance(catalog, dict) and catalog.get('schema') == 1, 'invalid catalog schema')
    require(set(catalog.get('protocols', [])) == {'messages', 'chat_completions', 'responses', 'gemini_generate_content'},
            'router protocol set changed; update capability profiles')
    models = catalog.get('models')
    require(isinstance(models, list) and models, 'empty catalog')
    entries, seen, representatives = [], set(), set()
    for model in sorted(models, key=lambda m: m['id']):
        ident = model.get('id')
        require(isinstance(ident, str) and ident.strip() and ident not in seen, 'invalid or duplicate model')
        require(isinstance(model.get('capabilities'), list), 'model capabilities missing')
        seen.add(ident)
        matched = [p for p in profiles if p['mode'] == model.get('mode') and
                   (not p.get('requires_capability') or p['requires_capability'] in model['capabilities'])]
        if not matched:
            matched = [{'id': 'unmapped-mode', 'protocol': 'unmapped', 'request_type': 'plain',
                        'key_types': ['direct', 'universal'], 'request': None,
                        'blocked_reason': 'catalog_mode_has_no_profile'}]
        for profile in matched:
            # Every concrete model gets a baseline for both key types. Feature and
            # cross-protocol tests use a representative per vendor/capability class.
            # This is an explicit sampling policy, not a new routing legality table.
            baseline = profile.get('baseline_protocol_by_vendor', {})
            native = baseline.get(model.get('vendor'), baseline.get('default'))
            is_baseline = (profile['request_type'] == 'plain' and profile.get('request')
                           and not profile['request']['stream']
                           and (model.get('mode') != 'chat' or profile['protocol'] == native))
            group = (model.get('vendor'), model.get('mode'), tuple(sorted(model['capabilities'])), profile['id'])
            if not is_baseline and not profile.get('blocked_reason'):
                if group in representatives:
                    continue
                representatives.add(group)
            for key_type in profile['key_types']:
                entry = {'model': ident, 'vendor': model.get('vendor', ''), 'profile': profile['id'],
                         'protocol': profile['protocol'], 'request_type': profile['request_type'],
                         'key_type': key_type, 'request': render(profile['request'], ident) if profile['request'] else None,
                         'blocked_reason': profile.get('blocked_reason'),
                         'selection': 'model_baseline' if is_baseline else 'capability_representative'}
                # Stable identity independent of template changes; request digest invalidates old evidence.
                entry['id'] = digest([ident, profile['id'], key_type])
                entry['case_sha256'] = digest(entry)
                entries.append(entry)
    result = {'schema': 1, 'catalog_sha256': digest(catalog), 'entries': entries}
    result['plan_sha256'] = digest(result)
    return result


def validate_plan(plan):
    require(isinstance(plan, dict) and plan.get('schema') == 1, 'invalid plan')
    require(plan.get('plan_sha256') == digest({k: v for k, v in plan.items() if k != 'plan_sha256'}), 'plan hash mismatch')
    require(isinstance(plan.get('entries'), list) and plan['entries'], 'empty plan')
    seen = set()
    for case in plan['entries']:
        require(case['id'] not in seen and case['key_type'] in KEY_TYPES, 'invalid case identity')
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


def select(cases, limit):
    """Deterministic bounded plan. Unselected cases remain untested in the report."""
    require(type(limit) is int and 0 < limit <= 200, 'request limit must be 1..200')
    selected, remaining = [], sorted(cases, key=lambda e: (e['profile'], e['model'], e['key_type']))
    # Cover each profile/key shape before spending the remaining budget on models.
    shapes = set()
    for case in remaining:
        shape = (case['profile'], case['key_type'])
        if not case.get('blocked_reason') and shape not in shapes and len(selected) < limit:
            shapes.add(shape)
            selected.append(case)
    selected_ids = {e['id'] for e in selected}
    for case in remaining:
        if len(selected) == limit:
            break
        if not case.get('blocked_reason') and case['id'] not in selected_ids:
            selected.append(case)
            selected_ids.add(case['id'])
    return selected


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
            require(result.get('status') in ('passed', 'failed', 'blocked-by-test-infrastructure'), 'invalid result status')
            status = result['status']
            if status == 'passed' and results['execution_kind'] == 'harness':
                status = 'harness-passed'  # Never gateway service evidence.
        else:
            status = 'blocked-by-test-infrastructure' if case.get('blocked_reason') else 'declared-but-untested'
        output.append({k: case[k] for k in ('id', 'model', 'protocol', 'request_type', 'key_type', 'profile')} |
                      {'status': status, 'reason': result.get('reason') if result else case.get('blocked_reason')})
    scope_ids = {e['id'] for e in scope}
    counts = {}
    for row in output:
        counts[row['status']] = counts.get(row['status'], 0) + 1
    complete = all(r['status'] == 'passed' for r in output if r['id'] in scope_ids)
    return {'schema': 1, 'plan_sha256': plan['plan_sha256'], 'coverage': counts,
            'total': len(output), 'delta': len(scope), 'scope_complete': complete,
            'verdict': 'no_changes' if not scope else 'passed' if complete else 'incomplete',
            'cutover': False, 'deployment_gate': False, 'entries': output}


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
    catalog = blob('ops/stage0/generated/gateway-catalog.json', optional=True)
    manifest = blob('ops/stage0/gateway-capability-matrix.json', optional=True)
    if catalog is None or manifest is None:
        return None  # Older release predates this check; current full plan is the baseline.
    with tempfile.TemporaryDirectory(prefix='tk-capability-baseline-') as directory:
        root = Path(directory)
        (root / 'matrix.json').write_bytes(manifest)
        for profile in json.loads(manifest)['profiles']:
            fixture = profile.get('fixture')
            if fixture:
                path = (root / fixture).resolve()
                require(path.is_relative_to(root), 'baseline fixture escapes directory')
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(blob('ops/stage0/' + fixture))
        return build(json.loads(catalog), load(root / 'matrix.json'))
