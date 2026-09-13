#!/usr/bin/env python3
"""Plan/report offline or execute the complete supply plan on the prepared blue/green candidate."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys

from gateway_capability_matrix import DEFAULT_MANIFEST, DEFAULT_INVENTORY, EXECUTION_BLOCKER, build, delta, from_tag, load, report, select


def read(path):
    return json.loads(path.read_text()) if path else None


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix('.tmp')
    tmp.write_text(json.dumps(value, indent=2) + '\n')
    tmp.replace(path)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    subs = parser.add_subparsers(dest='command', required=True)
    baseline = subs.add_parser('from-tag')
    baseline.add_argument('--tag', required=True)
    baseline.add_argument('--out', required=True, type=Path)
    plan = subs.add_parser('plan')
    plan.add_argument('--inventory', type=Path, default=DEFAULT_INVENTORY, help='sanitized account-supply representatives')
    plan.add_argument('--manifest', type=Path, default=DEFAULT_MANIFEST)
    plan.add_argument('--previous', type=Path, help='previous generated plan; unchanged rows are not delta')
    plan.add_argument('--out', type=Path, required=True)
    plan.add_argument('--limit', type=int, help='optional explicit execution cap; default is the complete eligible set')
    evaluate = subs.add_parser('report')
    evaluate.add_argument('--plan', type=Path, required=True)
    evaluate.add_argument('--previous', type=Path)
    evaluate.add_argument('--results', type=Path)
    evaluate.add_argument('--tag', help='require results from this release')
    evaluate.add_argument('--out', type=Path, required=True)
    evaluate.add_argument('--require-complete', action='store_true', help='explicit coverage gate only; never deploy/promote')
    execute = subs.add_parser('run', help='prod host only: serial candidate verification with the existing universal test key')
    execute.add_argument('--plan', type=Path)
    execute.add_argument('--previous', type=Path)
    execute.add_argument('--tag')
    execute.add_argument('--inventory', type=Path, default=DEFAULT_INVENTORY)
    execute.add_argument('--limit', type=int, help='optional explicit execution cap; default is the complete eligible set')
    execute.add_argument('--out', type=Path)
    execute.add_argument('--test-key-name', default='TK_FULLTEST_KEY',
                         help='prod api_keys.name of an active universal test key (not secrets.TK_FULLTEST_KEY material)')
    execute.add_argument('--allow-upstream-quota', action='store_true')
    args = parser.parse_args(argv)
    if args.command == 'from-tag':
        value = from_tag(args.tag)
        if value is not None:
            write(args.out, value)
        elif args.out.exists():
            args.out.unlink()
        print(json.dumps({'baseline_available': value is not None, 'tag': args.tag}))
        return 0
    if args.command == 'plan':
        value = build(read(args.inventory), load(args.manifest))
        scope = delta(value, read(args.previous))
        chosen = select(scope, args.limit)
        write(args.out, value)
        print(json.dumps({'plan_sha256': value['plan_sha256'], 'total': len(value['entries']),
                          'delta': len(scope), 'fixture_ready': len(chosen), 'execution': 'not_run',
                          'capability_coverage': report(value)['capability_coverage'],
                          'execution_blocker': EXECUTION_BLOCKER,
                          'cutover': False, 'deployment_gate': False}))
        return 0
    if args.command == 'report':
        results = read(args.results)
        if args.tag and results is not None and results.get('tag') != args.tag:
            raise ValueError('results release mismatch')
        value = report(read(args.plan), results, read(args.previous))
        write(args.out, value)
        print(json.dumps({k: v for k, v in value.items() if k != 'entries'}))
        return 1 if args.require_complete and not value['scope_complete'] else 0
    if not args.allow_upstream_quota:
        print(json.dumps({'execution': 'not_run', 'execution_blocker': EXECUTION_BLOCKER,
                          'cutover': False, 'deployment_gate': False}))
        return 2
    if not args.tag or not args.out or args.limit is not None or args.previous is not None:
        raise ValueError('execution requires tag/output and the complete plan')
    from gateway_capability_host import run_locked
    inventory = read(args.inventory)
    value = read(args.plan) if args.plan else build(inventory, load())
    receipt = run_locked(value, inventory, args.tag, args.test_key_name)
    write(args.out, receipt)
    return 0 if receipt['verdict'] == 'green' else 1


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except Exception:
        # Credentials/response text/SQL must never escape via exceptions.
        print('capability check failed; no result may be treated as passed', file=sys.stderr)
        raise SystemExit(1) from None
