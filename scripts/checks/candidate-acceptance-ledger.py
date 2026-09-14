#!/usr/bin/env python3
"""Validate per-criterion acceptance evidence against the US-050 Story."""
from __future__ import annotations

import ast
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LEDGER = Path('ops/observability/candidate-eligibility-acceptance-ledger.json')
STORY = Path('.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md')
STATUSES = {'contract-tested', 'live-verified', 'blocked'}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f'duplicate JSON key: {key}')
        result[key] = value
    return result


def section(text: str, title: str) -> str:
    match = re.search(rf'^## {re.escape(title)}\s*\n(.*?)(?=^## |\Z)', text, re.M | re.S)
    return match[1] if match else ''


def evidence_exists(root: Path, reference: str) -> bool:
    relative, separator, symbol = reference.partition('::')
    path = (root / relative).resolve()
    if not separator or not path.is_relative_to(root.resolve()) or not path.is_file():
        return False
    source = path.read_text(encoding='utf-8')
    if path.name.endswith('_test.go'):
        return bool(re.search(rf'^func {re.escape(symbol)}\(\w+ \*testing\.T\)', source, re.M))
    if path.name.startswith('test_') and path.suffix == '.py':
        nodes = ast.parse(source).body
        for part in symbol.split('.'):
            node = next((item for item in nodes if isinstance(item, (ast.ClassDef, ast.FunctionDef)) and item.name == part), None)
            if node is None:
                return False
            nodes = node.body
        return isinstance(node, ast.FunctionDef) and node.name.startswith('test_')
    return False


def check(root: Path = ROOT) -> list[str]:
    try:
        data = json.loads((root / LEDGER).read_text(), object_pairs_hook=unique_object)
        story = (root / STORY).read_text()
    except (OSError, ValueError) as exc:
        return [f'cannot read acceptance sources: {exc}']
    if not isinstance(data, dict) or data.get('schema_version') != 2 or data.get('contract') != 'US-050':
        return ['expected US-050 acceptance ledger schema_version 2']
    errors = []
    if set(data) != {'schema_version', 'contract', 'criteria', 'operational_observations'}:
        errors.append('ledger fields must keep operational observations separate from criterion status')
    if data.get('operational_observations') != f'{STORY.as_posix()}#coverage-boundaries' or '### Coverage Boundaries' not in story:
        errors.append('operational observations must link the Story Coverage Boundaries')
    declared = re.findall(r'^\d+\. (AC-\d{3})\b', section(story, 'Acceptance Criteria'), re.M)
    if not declared or len(declared) != len(set(declared)):
        errors.append('Story must declare unique acceptance criteria')
    criteria = data.get('criteria')
    if not isinstance(criteria, dict):
        return errors + ['criteria must be an object']
    missing, extra = set(declared) - criteria.keys(), criteria.keys() - set(declared)
    if missing:
        errors.append('missing criteria: ' + ', '.join(sorted(missing)))
    if extra:
        errors.append('unknown criteria: ' + ', '.join(sorted(extra)))
    linked = {f'{path}::{symbol}' for path, symbol in re.findall(r'`([^`]+)`::`([^`]+)`', section(story, 'Linked Tests'))}
    for key, entry in criteria.items():
        if not isinstance(entry, dict) or not isinstance(entry.get('status'), str) or entry['status'] not in STATUSES:
            errors.append(f'{key}: invalid criterion status')
            continue
        if set(entry) - {'status', 'evidence', 'reason', 'live_evidence'}:
            errors.append(f'{key}: unknown criterion fields')
        reason = entry.get('reason')
        if entry['status'] == 'blocked' and (not isinstance(reason, str) or not reason.strip()):
            errors.append(f'{key}: blocked requires a reason')
        evidence = entry.get('evidence', [])
        if not isinstance(evidence, list) or any(not isinstance(ref, str) for ref in evidence):
            errors.append(f'{key}: evidence must be a list of test references')
            continue
        if entry['status'] != 'blocked' and not evidence:
            errors.append(f'{key}: tested criterion requires evidence')
        for reference in evidence:
            try:
                valid = reference in linked and evidence_exists(root, reference)
            except (OSError, SyntaxError):
                valid = False
            if not valid:
                errors.append(f'{key}: unlinked or missing test evidence: {reference}')
        if entry['status'] == 'live-verified':
            # A test reference never implies production verification. The receipt
            # is reviewed evidence, linked from the Story's operational narrative.
            receipt = entry.get('live_evidence')
            receipts = set(re.findall(r'\[[^\]]+\]\(([^)\s]+)\)', story))
            if not isinstance(receipt, str) or receipt not in receipts or receipt.startswith('#'):
                errors.append(f'{key}: live-verified requires a receipt linked from the Story')
    return errors


if __name__ == '__main__':
    failures = check()
    for failure in failures:
        print(f'FAIL: {failure}')
    if not failures:
        print('candidate acceptance ledger: all Story criteria have valid status and evidence references')
    raise SystemExit(bool(failures))
