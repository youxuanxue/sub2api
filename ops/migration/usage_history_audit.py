#!/usr/bin/env python3
"""Compare frozen usage history by tenant/day without exposing request contents."""
import argparse
from decimal import Decimal
import json
from pathlib import Path

FIELDS = (
    'rows', 'first', 'last', 'input_tokens', 'output_tokens',
    'cache_creation_tokens', 'cache_read_tokens', 'total_cost', 'actual_cost',
    'id_sum', 'row_fingerprint',
)


def read_manifest(path):
    result = {}
    for line in Path(path).read_text().splitlines():
        if line in ('', 'BEGIN', 'SET', 'ROLLBACK'):
            continue
        row = json.loads(line, parse_float=Decimal)
        if set(row) != {'user_id', 'day', *FIELDS}:
            raise ValueError('incomplete usage-history manifest')
        key = (row['user_id'], row['day'])
        if key in result or type(row['rows']) is not int or row['rows'] <= 0:
            raise ValueError('duplicate or invalid usage-history group')
        result[key] = row
    if not result:
        raise ValueError('empty usage history cannot satisfy migration acceptance')
    return result


def compare(source, candidate):
    expected, actual = read_manifest(source), read_manifest(candidate)
    missing = set(expected) - set(actual)
    extra = set(actual) - set(expected)
    changed = [key for key in expected.keys() & actual.keys()
               if expected[key] != actual[key]]
    return {
        'matched': not (missing or extra or changed),
        'source_users': len({key[0] for key in expected}),
        'source_user_days': len(expected),
        'source_rows': sum(row['rows'] for row in expected.values()),
        'missing_user_days': len(missing), 'extra_user_days': len(extra),
        'changed_user_days': len(changed),
        'final_freeze_verified': False,
    }


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source', type=Path, required=True)
    p.add_argument('--candidate', type=Path, required=True)
    args = p.parse_args()
    result = compare(args.source, args.candidate)
    print(json.dumps(result))
    raise SystemExit(0 if result['matched'] else 1)


if __name__ == '__main__':
    main()
