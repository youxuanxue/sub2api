"""Foreign-key guards for offline OLTP refresh, including retained log children.

Capture referenced keys before replacing parents. For excluded children, inspect
only references to removed keys; this avoids rescanning historical log contents.
Refreshed children receive a full anti-join check before the transaction commits.
"""
import json

try:
    from .fresh_host_delta import ident, table
except ImportError:
    from fresh_host_delta import ident, table


def catalog_sql():
    return """SELECT coalesce(json_agg(x ORDER BY child,parent,name),'[]'::json) FROM (
SELECT c.conname AS name, t.relname AS child, p.relname AS parent,
 c.confmatchtype AS match,
 ARRAY(SELECT a.attname FROM unnest(c.conkey) WITH ORDINALITY k(n,i)
       JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.n ORDER BY k.i) AS child_cols,
 ARRAY(SELECT a.attname FROM unnest(c.confkey) WITH ORDINALITY k(n,i)
       JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=k.n ORDER BY k.i) AS parent_cols
FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_class p ON p.oid=c.confrelid
WHERE c.contype='f'
AND t.relnamespace='public'::regnamespace AND p.relnamespace='public'::regnamespace) x"""


SELF_CHECK_EXEMPT = {"foreign_key_guard_sql": "candidate-only transactional FK guards; exercised by real PostgreSQL acceptance"}


def iter_self_check_sql():
    return [("catalog_sql", catalog_sql())]


def foreign_key_guard_sql(db, refreshed):
    before, after, saved = [], [], {}
    for fk in json.loads(db.run(catalog_sql())):
        child, parent = fk['child'], fk['parent']
        if child not in refreshed and parent not in refreshed:
            continue
        if fk['match'] != 's':
            raise ValueError('unsupported FK match semantics')
        cc, pc = fk['child_cols'], fk['parent_cols']
        join = ' AND '.join(f'c.{ident(c)}=p.{ident(p)}' for c, p in zip(cc, pc))
        nonnull = ' AND '.join(f'c.{ident(c)} IS NOT NULL' for c in cc)
        if child in refreshed:
            query = f'SELECT 1 FROM {table(child)} c WHERE {nonnull} AND NOT EXISTS (SELECT 1 FROM {table(parent)} p WHERE {join})'
        else:
            key = (parent, tuple(pc))
            if key not in saved:
                tmp = ident('_migration_fk_' + str(len(saved)))
                saved[key] = tmp
                cols = ','.join(ident(c) for c in pc)
                before.append(f'CREATE TEMP TABLE {tmp} ON COMMIT DROP AS SELECT {cols} FROM {table(parent)};')
            tmp = saved[key]
            cols = ','.join(ident(c) for c in pc)
            # Empty removed-key set short-circuits indexed probes into log tables.
            query = f'SELECT 1 FROM (SELECT {cols} FROM {tmp} EXCEPT SELECT {cols} FROM {table(parent)}) p WHERE EXISTS (SELECT 1 FROM {table(child)} c WHERE {join})'
        # Names come from the DB catalog; keep identifiers and errors separately safe.
        message = ('foreign key mismatch: ' + fk['name']).replace("'", "''")
        after.append(f"DO $fk$ BEGIN IF EXISTS ({query}) THEN RAISE EXCEPTION '{message}'; END IF; END $fk$;")
    return '\n'.join(before) + '\n', '\n'.join(after) + '\n'
