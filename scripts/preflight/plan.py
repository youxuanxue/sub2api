#!/usr/bin/env python3
"""Select preflight gates from declared inputs; unknown changes require full checks."""
from __future__ import annotations

import argparse
import fnmatch
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]


def git(root: Path, *args: str) -> bytes:
    return subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.PIPE)


def paths(root: Path, *args: str) -> set[str]:
    return {os.fsdecode(p) for p in git(root, *args).split(b'\0') if p}


def matches(path: str, patterns: list[str]) -> bool:
    return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)


def changed_paths(root: Path, scope: str, base: str) -> tuple[str, set[str]]:
    # --no-renames preserves both deleted and added paths, including cross-owner moves.
    staged = paths(root, 'diff', '--cached', '--name-only', '--no-renames', '-z')
    pending = paths(root, 'diff', '--name-only', '--no-renames', '-z')
    pending |= paths(root, 'ls-files', '--others', '--exclude-standard', '-z')
    if scope == 'auto':
        if os.environ.get('CI') or os.environ.get('GITHUB_ACTIONS'):
            scope = 'branch' if os.environ.get('GITHUB_EVENT_NAME') == 'pull_request' else 'full'
        elif os.environ.get('GIT_INDEX_FILE') or staged:
            scope = 'staged'
        elif pending:
            scope = 'worktree'
        else:
            scope = 'branch'
    if scope == 'full':
        return scope, staged | pending
    if scope == 'staged':
        overlap = staged & pending
        if overlap:
            raise ValueError('staged files differ from the tested working tree; stage or separate these edits: ' + ', '.join(sorted(overlap)))
        return scope, staged
    if scope == 'worktree':
        return scope, staged | pending
    # Resolve explicitly. A missing/shallow base must never become an empty diff.
    return scope, paths(root, 'diff', '--name-only', '--no-renames', '-z', f'{base}...HEAD') | staged | pending


def load_manifest(root: Path) -> dict:
    data = json.loads((root / '.preflight/gates.json').read_text())
    if data.get('version') != 1:
        raise ValueError('unsupported preflight manifest version')
    for field in ('known_inputs', 'full_inputs'):
        if not isinstance(data[field], list) or not data[field] or not all(isinstance(p, str) and p for p in data[field]):
            raise ValueError(f'invalid {field}')
    for gate, dependencies in data.get('requires', {}).items():
        if gate not in data['gates'] or not isinstance(dependencies, list) or any(dep not in data['gates'] for dep in dependencies):
            raise ValueError(f'invalid dependencies for {gate}')
    for key, gates in data.get('isolated_inputs', {}).items():
        if key not in data['input_sets'] or not isinstance(gates, list) or any(gate not in data['gates'] for gate in gates):
            raise ValueError(f'invalid isolated input set {key}')
    data['gates'] = {name: data['input_sets'][key] for name, key in data['gates'].items()}
    for name, patterns in data['gates'].items():
        if not isinstance(name, str) or not isinstance(patterns, list) or not all(isinstance(p, str) and p for p in patterns):
            raise ValueError(f'invalid inputs for gate {name!r}')
    # Sentinel registries already own the file list. Read it, do not copy it.
    def registry_paths(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key in {'path', 'source', 'route_source', 'capture_source', 'keys_source'} and isinstance(item, str):
                    yield item
                yield from registry_paths(item)
        elif isinstance(value, list):
            for item in value:
                yield from registry_paths(item)
    for gate, registry in data.get('sentinel_inputs', {}).items():
        inputs = list(registry_paths(json.loads((root / registry).read_text())))
        if not inputs:
            raise ValueError(f'no dependency paths in sentinel registry: {registry}')
        # Custom sentinel checks also sweep backend Go callers.
        data['gates'][gate] = [registry, 'backend/*', *inputs]
    for key, gate in data['backgrounds'].items():
        if gate not in data['gates']:
            raise ValueError(f'background {key} has no gate: {gate}')
    return data


def select(data: dict, changed: set[str], scope: str) -> dict:
    reason = ''
    if scope == 'full':
        reason = 'full scope'
    elif not changed:
        reason = 'empty change set: full verification'
    else:
        unknown = sorted(p for p in changed if not matches(p, data['known_inputs']))
        if unknown:
            reason = 'unclassified input: ' + unknown[0]
        machinery = sorted(p for p in changed if matches(p, data['full_inputs']))
        if machinery:
            reason = 'shared gate/tooling input: ' + machinery[0]
    gates = {}
    for name, patterns in data['gates'].items():
        excluded = [p for key, names in data.get('isolated_inputs', {}).items() if name in names for p in data['input_sets'][key]]
        hits = sorted(p for p in changed if matches(p, patterns) and not matches(p, excluded))
        if reason or not patterns or hits:
            gates[name] = reason or ('base check' if not patterns else hits[0])
    # Background launch/joins and shared flags must travel together.
    requires = dict(data.get('requires', {}))
    requires['observability test suite'] = [*requires.get('observability test suite', []), 'user billing watch probe']
    for _ in range(len(data['gates'])):
        before = set(gates)
        for gate, dependencies in requires.items():
            if gate in gates:
                for dependency in dependencies:
                    gates.setdefault(dependency, 'required by ' + gate)
        if set(gates) == before:
            break
    return {'scope': scope, 'full': bool(reason), 'reason': reason, 'paths': sorted(changed), 'gates': gates}


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--scope', choices=['auto', 'staged', 'worktree', 'branch', 'full'], default='auto')
    ap.add_argument('--base', default=os.environ.get('PREFLIGHT_BASE', 'origin/main'))
    ap.add_argument('--output', type=Path)
    args = ap.parse_args()
    try:
        data = load_manifest(ROOT)
        scope, changed = changed_paths(ROOT, args.scope, args.base)
        plan = select(data, changed, scope)
        # An edited local router must validate itself even during a staged-only commit.
        machinery = paths(ROOT, 'diff', '--name-only', '--no-renames', '-z')
        machinery |= paths(ROOT, 'ls-files', '--others', '--exclude-standard', '-z')
        if any(matches(p, data['full_inputs']) for p in machinery):
            plan = select(data, changed | machinery, 'full')
            plan['reason'] = 'uncommitted preflight/tooling changes'
        # New/unregistered shell gates remain unconditional, never silently skipped.
        labels = re.findall(r'^echo "=== sub2api: (.+) ==="$', (ROOT / 'scripts/preflight.sh').read_text(), re.M)
        for label in labels:
            if label not in data['gates']:
                plan['gates'][label] = 'unregistered gate: always run'
        if args.output:
            args.output.mkdir(parents=True, exist_ok=True)
            (args.output / 'plan.json').write_text(json.dumps(plan, ensure_ascii=False, indent=2) + '\n')
            selected = set(plan['gates'])
            selected |= {'bg:' + key for key, gate in data['backgrounds'].items() if gate in selected}
            registered = set(data['gates']) | {'bg:' + key for key in data['backgrounds']}
            (args.output / 'registered').write_text('\n'.join(sorted(registered)) + '\n')
            (args.output / 'selected').write_text('\n'.join(sorted(selected)) + '\n')
            (args.output / 'scope').write_text(scope)
            (args.output / 'full').write_text('1' if plan['full'] else '0')
            print(f"preflight: scope={scope}, {len(changed)} changed paths, {len(plan['gates'])}/{len(set(labels) | set(data['gates']))} gates" + (f" ({plan['reason']})" if plan['reason'] else ''))
        else:
            print(json.dumps(plan, ensure_ascii=False, indent=2))
        return 0
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as exc:
        print(f'preflight plan: FAIL: {exc}', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
