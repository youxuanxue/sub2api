#!/usr/bin/env python3
"""Classify physical backup health in daily diagnostics using the host-watch owner."""
import argparse
import json
from pathlib import Path
import time

from host_https_watch import physical_backup_fact


def evaluate(path: Path, target: str, now: float) -> dict:
    fact = physical_backup_fact(path, now)
    severity = fact.get('severity', 'info' if fact['healthy'] else 'error')
    return {'target_id': target, 'kind': 'physical_backup',
            'status': ('warning' if severity == 'warning' else 'ok') if fact['healthy'] else 'issue_candidate',
            'severity': severity,
            'title': f'Physical backup health: {target}',
            'summary': fact['reason'], 'signature': f'physical-backup|{target}'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--receipt', type=Path, required=True)
    parser.add_argument('--target-id', required=True)
    args = parser.parse_args()
    print(json.dumps(evaluate(args.receipt, args.target_id, time.time())))


if __name__ == '__main__':
    main()
