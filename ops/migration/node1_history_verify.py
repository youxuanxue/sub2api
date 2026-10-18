#!/usr/bin/env python3
"""Check tenant history through the isolated rehearsal API, never live prod.

Database fingerprints are verified separately. This checks the user-visible list,
date filters and cross-tenant access. It does not claim browser/CSV acceptance.
"""
import json
import os
import re
from urllib.parse import urlencode

if __package__:
    from . import node1_history_rehearsal as rehearsal
    from .node1_candidate import Policy, CONTAINER
    from .usage_history_audit import read_manifest
else:
    import node1_history_rehearsal as rehearsal
    from node1_candidate import Policy, CONTAINER
    from usage_history_audit import read_manifest


def verify():
    policy = Policy()
    policy.guard()
    pg = rehearsal.inspect(CONTAINER)
    for name in (rehearsal.APP, rehearsal.REDIS):
        rehearsal.validate(rehearsal.inspect(name), pg['Id'])
    receipt = json.loads((rehearsal.WORK / 'receipt.json').read_text())
    if not receipt.get('external_egress_denied') or not receipt.get('upstream_credentials_cleared'):
        raise RuntimeError('rehearsal isolation receipt missing')
    rows = read_manifest(policy.work / 'usage-by-user-day.source.jsonl')
    password = json.loads((rehearsal.WORK / 'login.private.json').read_text())['password']
    tokens, samples = {}, {}

    def request(path, token=None, body=None):
        response = rehearsal.http(path, body=body, token=token)
        if response['status'] != 200:
            raise RuntimeError('history API check failed; HTTP ' + str(response['status']))
        return json.loads(response['body'])['data']

    # Product days are owned by the server timezone; the legacy query parameter
    # is intentionally ignored. Keep the CST retention audit separate from API days.
    timezone = request('/api/v1/settings/public')['server_timezone']
    if not re.fullmatch(r'[A-Za-z0-9_+./-]+', timezone):
        raise RuntimeError('unexpected server timezone')
    expected_days = json.loads(rehearsal.sql(
        "SELECT json_agg(x ORDER BY user_id,day) FROM (SELECT user_id,"
        "(created_at AT TIME ZONE '" + timezone + "')::date AS day,count(*) AS rows "
        "FROM usage_logs WHERE created_at>='2026-08-31T16:00:00Z' GROUP BY user_id,day) x",
        database='tokenkey'))
    expected_rows = sum(r['rows'] for r in rows.values())
    if sum(r['rows'] for r in expected_days) != expected_rows:
        raise RuntimeError('API day baseline does not cover the reconciled history')
    (rehearsal.WORK / 'api-day-manifest.json').write_text(json.dumps(expected_days))
    for uid in sorted({key[0] for key in rows}):
        data = request('/api/v1/auth/login', body={
            'email': f'user-{uid}@node1.invalid', 'password': password})
        if data['user']['id'] != uid:
            raise RuntimeError('login tenant mismatch')
        tokens[uid] = data['access_token']
    checked = 0
    for expected in expected_days:
        uid, day = expected['user_id'], expected['day']
        query = urlencode(dict(start_date=day, end_date=day,
                               page=1, page_size=1))
        data = request('/api/v1/usage?' + query, token=tokens[uid])
        if data['total'] != expected['rows'] or len(data['items']) != 1:
            raise RuntimeError(f'per-user/day history count mismatch: user={uid}, day={day}')
        item = data['items'][0]
        if item['user_id'] != uid:
            raise RuntimeError('list returned another tenant history')
        samples[uid] = item['id']
        checked += 1
        if checked % 32 == 0:
            print(json.dumps({'checked_user_days': checked}), flush=True)
    denied = 0
    for uid, token in tokens.items():
        foreign = next((sid for other, sid in samples.items() if other != uid), None)
        if foreign is None:
            raise RuntimeError('multiple tenants required for isolation acceptance')
        if rehearsal.http('/api/v1/usage/' + str(foreign), token=token)['status'] != 403:
            raise RuntimeError('foreign usage detail was not forbidden')
        denied += 1
    result = dict(api_verified=True, users=len(tokens), user_days=checked,
                  server_timezone=timezone, historical_rows=expected_rows,
                  cross_tenant_denied=denied, ui_verified=False, csv_verified=False,
                  final_freeze_verified=False)
    (rehearsal.WORK / 'api-verification.json').write_text(json.dumps(result, indent=2))
    return result


if __name__ == '__main__':
    os.umask(0o077)
    print(json.dumps(verify()))
