"""Compare usage COPY data in a plain pg_dump with its isolated SQL restore.

Only aggregates leave this module. The dump is the snapshot owner; a moving live
DB is never used as the expected usage count or cost baseline.
"""
from __future__ import annotations

import datetime as dt
from decimal import Decimal, InvalidOperation, localcontext
import gzip
import pathlib
import re
import zlib

TABLE = re.compile(r'usage_logs(?:_[A-Za-z0-9_]+)?')
COPY = re.compile(rb'COPY public\.(usage_logs[A-Za-z0-9_]*) \(([^\n]+)\) FROM stdin;\n')
COSTS = ('total_cost', 'actual_cost')
FIELDS = {'rows', 'min_created_at', 'max_created_at', *COSTS}


def timestamp(value: str) -> str:
    if not isinstance(value, str):
        raise ValueError('invalid usage timestamp')
    value = value.replace('Z', '+00:00')
    if re.search(r'[+-][0-9]{2}$', value):
        value += ':00'  # PostgreSQL COPY uses +00; Python 3.9 requires +00:00.
    parsed = dt.datetime.fromisoformat(value)
    if parsed.tzinfo is None:
        raise ValueError('usage timestamp has no timezone')
    return parsed.astimezone(dt.timezone.utc).isoformat(timespec='microseconds').replace('+00:00', 'Z')


def normalize(evidence: object) -> dict:
    if not isinstance(evidence, dict) or not evidence:
        raise ValueError('usage partition evidence is missing')
    normalized = {}
    for name, row in evidence.items():
        if not isinstance(name, str) or TABLE.fullmatch(name) is None:
            raise ValueError('invalid usage partition name')
        if not isinstance(row, dict) or set(row) != FIELDS:
            raise ValueError('incomplete usage partition aggregates')
        n = row['rows']
        if isinstance(n, bool) or not isinstance(n, int) or n < 0:
            raise ValueError('invalid usage row count')
        result = {'rows': n}
        for field in COSTS:
            if not isinstance(row[field], str):
                raise ValueError('usage cost must be exact decimal text')
            value = Decimal(row[field])
            if not value.is_finite():
                raise ValueError('invalid usage cost')
            text = format(value, 'f')
            result[field] = (text.rstrip('0').rstrip('.') if '.' in text else text) if value else '0'
        for field in ('min_created_at', 'max_created_at'):
            if n == 0:
                if row[field] is not None:
                    raise ValueError('empty usage partition has timestamps')
                result[field] = None
            else:
                result[field] = timestamp(row[field])
        if n == 0 and any(Decimal(result[field]) != 0 for field in COSTS):
            raise ValueError('empty usage partition has cost')
        if n and result['min_created_at'] > result['max_created_at']:
            raise ValueError('invalid usage timestamp range')
        normalized[name] = result
    return normalized


def verified(evidence: object) -> bool:
    try:
        return (isinstance(evidence, dict) and set(evidence) == {'source', 'restored'}
                and normalize(evidence['source']) == normalize(evidence['restored']))
    except (ValueError, TypeError, KeyError, InvalidOperation, OverflowError):
        return False


def inspect_dump(path: pathlib.Path) -> tuple[int, dict]:
    """Fully read gzip (including CRC), count bytes and summarize each COPY block."""
    total = 0
    tables = {}
    current = None
    # Never surface source rows in exceptions (they can contain user data).
    try:
        with localcontext() as context, gzip.open(path, 'rb') as stream:
            context.prec = 80  # exact sums of numeric(20,10), even at bigint row counts
            for line in stream:
                total += len(line)
                if current is not None:
                    row, columns = current
                    if line == b'\\.\n':
                        current = None
                        continue
                    values = line.rstrip(b'\n').split(b'\t')
                    if len(values) != len(columns):
                        raise ValueError('invalid usage COPY column count')
                    row['rows'] += 1
                    for field in COSTS:
                        row[field] += Decimal(values[columns.index(field)].decode('ascii'))
                    created = timestamp(values[columns.index('created_at')].decode('ascii'))
                    row['min_created_at'] = min(row['min_created_at'] or created, created)
                    row['max_created_at'] = max(row['max_created_at'] or created, created)
                elif line.startswith(b'COPY public.usage_logs'):
                    match = COPY.fullmatch(line)
                    if match is None:
                        raise ValueError('unsupported usage COPY format')
                    name = match[1].decode('ascii')
                    columns = match[2].decode('ascii').split(', ')
                    if name in tables or not {'created_at', *COSTS}.issubset(columns):
                        raise ValueError('duplicate or incomplete usage COPY')
                    row = {'rows': 0, 'total_cost': Decimal(0), 'actual_cost': Decimal(0),
                           'min_created_at': None, 'max_created_at': None}
                    tables[name] = row
                    current = row, columns
        if current is not None:
            raise ValueError('unfinished usage COPY')
        for row in tables.values():
            for field in COSTS:
                row[field] = str(row[field])
        return total, normalize(tables)
    except (OSError, EOFError, ValueError, TypeError, InvalidOperation, UnicodeError, zlib.error):
        raise ValueError('backup has invalid gzip or missing/malformed usage COPY evidence') from None


def restored_sql() -> str:
    # Executed only inside the disposable restore DB. Discover the full leaf set,
    # including an empty DEFAULT partition, so missing/extra tables cannot pass.
    return '''
SET timezone = 'UTC';
CREATE TEMP TABLE canary_usage_evidence(name text, evidence jsonb);
DO $verify$
DECLARE part record;
BEGIN
 FOR part IN
   SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='public' AND c.relkind='r'
     AND c.relname ~ '^usage_logs(_[A-Za-z0-9_]+)?$'
 LOOP
   EXECUTE format($query$
     INSERT INTO canary_usage_evidence
     SELECT %L, jsonb_build_object(
       'rows',count(*),'total_cost',coalesce(sum(total_cost),0)::text,
       'actual_cost',coalesce(sum(actual_cost),0)::text,
       'min_created_at',to_char(min(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
       'max_created_at',to_char(max(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))
     FROM ONLY public.%I
   $query$, part.relname, part.relname);
 END LOOP;
END $verify$;
SELECT jsonb_object_agg(name,evidence) FROM canary_usage_evidence;
'''


SELF_CHECK_EXEMPT: dict[str, str] = {}


def iter_self_check_sql() -> list[tuple[str, str]]:
    return [('restored_sql', restored_sql())]
