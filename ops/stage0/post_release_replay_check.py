#!/usr/bin/env python3
"""Plan/report capability coverage offline; paid execution is a separate explicit command."""
from __future__ import annotations

import argparse
import fcntl
import json
import os
from pathlib import Path
import signal
import sys

from gateway_capability_matrix import DEFAULT_MANIFEST, DEFAULT_INVENTORY, build, delta, from_tag, load, report, select


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
    execute = subs.add_parser('run', help='prod HOST only; existing prepared image, isolated snapshot, no cutover')
    execute.add_argument('--plan', type=Path, required=True)
    execute.add_argument('--previous', type=Path)
    execute.add_argument('--tag', required=True)
    execute.add_argument('--bindings', type=Path, required=True, help='reserved probe key IDs; account-class execution binding is not implemented')
    execute.add_argument('--limit', type=int, help='optional explicit execution cap; default is the complete eligible set')
    execute.add_argument('--out', type=Path, required=True)
    execute.add_argument('--allow-upstream-quota', action='store_true', required=True)
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
                          'execution_blocker': 'account_class_execution_binding_required',
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
    from gateway_capability_check import run
    import prod_replay as replay
    os.umask(0o077)
    # Invalidate local output before any paid work; never modify bluegreen receipts.
    write(args.out, {'execution_kind': 'not_completed', 'tag': args.tag, 'cutover': False})
    with (replay.ROOT / 'bluegreen-deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        def timeout(_sig, _frame):
            signal.alarm(0)  # cleanup must finish even at the deadline
            raise replay.ReplayDeadline()
        signal.signal(signal.SIGALRM, timeout)
        signal.signal(signal.SIGTERM, timeout)
        signal.alarm(4500)
        try:
            value = run(read(args.plan), args.tag, read(args.bindings), args.limit, read(args.previous))
            write(args.out, value)
        finally:
            signal.alarm(0)
    print(json.dumps({'executed': len(value['results']), 'cutover': False, 'deployment_gate': False}))
    return 0 if all(r['status'] == 'passed' for r in value['results']) else 1


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except Exception:
        # Credentials/response text/SQL must never escape via exceptions.
        print('capability check failed; no result may be treated as passed', file=sys.stderr)
        raise SystemExit(1) from None
