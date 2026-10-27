#!/usr/bin/env python3
"""Repair legacy QA paths without changing retention or deleting capture contents.

Run plan first, then apply its SHA on the same host. The existing local cleanup
owner expires the resulting hourly directories. No gateway restart is needed:
Service.hourlyStorageCutover does not cache a missing setting.
Approved scope: docs/approved/qa-raw-only.md local retention + operator repair.
"""
from __future__ import annotations

import argparse
import ctypes
import ctypes.util
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import uuid

import local_maintenance_host as lifecycle_host

CUTOVER = 'qa_hourly_storage_cutover_utc'
QAMA = 0x51414D41  # archive.MaintenanceAdvisoryLockID
FIELDS = ('blob_uri', 'request_blob_uri', 'response_blob_uri', 'stream_blob_uri')
MAX_DECODED = 32 * 1024 * 1024


def require(ok, message):
    if not ok:
        raise ValueError(message)


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def utc(value):
    parsed = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
    require(parsed.tzinfo is not None, 'capture timestamp must include timezone')
    return parsed.astimezone(dt.timezone.utc)


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


class DB:
    def __init__(self, container):
        self.p = subprocess.Popen(
            ['docker', 'exec', '-i', container, 'psql', '-U', 'tokenkey', '-d', 'tokenkey',
             '-XAtq', '-v', 'ON_ERROR_STOP=1'], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        try:
            self.query("SET timezone='UTC'; SET lock_timeout='200ms'; SET statement_timeout='15s';")
        except Exception:
            self.close()
            raise

    def query(self, sql):
        marker = 'qa_end_' + uuid.uuid4().hex
        self.p.stdin.write(sql + '\n\\echo ' + marker + '\n')
        self.p.stdin.flush()
        lines = []
        while True:
            line = self.p.stdout.readline()
            require(line != '', 'database command failed; inspect retained repair journal')
            if line.strip() == marker:
                return '\n'.join(lines)
            lines.append(line.rstrip('\n'))

    def close(self):
        self.p.stdin.close()
        try:
            self.p.wait(timeout=15)
        except subprocess.TimeoutExpired:
            self.p.terminate()
            self.p.wait(timeout=5)
        finally:
            self.p.stdout.close()


class Decoder:
    def __init__(self):
        library = ctypes.util.find_library('zstd')
        require(library is not None, 'libzstd is required')
        self.lib = ctypes.CDLL(library)
        self.lib.ZSTD_getFrameContentSize.argtypes = [ctypes.c_void_p, ctypes.c_size_t]
        self.lib.ZSTD_getFrameContentSize.restype = ctypes.c_ulonglong
        self.lib.ZSTD_decompress.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_void_p, ctypes.c_size_t]
        self.lib.ZSTD_decompress.restype = ctypes.c_size_t
        self.lib.ZSTD_isError.argtypes = [ctypes.c_size_t]
        self.lib.ZSTD_isError.restype = ctypes.c_uint

    def __call__(self, raw):
        size = self.lib.ZSTD_getFrameContentSize(raw, len(raw))
        require(0 < size <= MAX_DECODED, 'unknown or excessive decoded capture size')
        output = ctypes.create_string_buffer(size)
        n = self.lib.ZSTD_decompress(output, size, raw, len(raw))
        require(not self.lib.ZSTD_isError(n) and n == size, 'invalid zstd capture')
        return json.loads(output.raw)


def checked_path(root, relative, *, exists=False):
    rel = Path(relative)
    require(not rel.is_absolute() and '..' not in rel.parts, 'noncanonical capture path')
    current = root
    require(root.is_dir() and not root.is_symlink(), 'noncanonical data root')
    for part in rel.parts:
        current = current / part
        require(not current.is_symlink(), 'symlink in capture path')
    if exists:
        require(current.is_file(), 'source capture missing')
    return current


def entry(root, source, decode, now):
    relative = source.relative_to(root)
    parts = relative.parts
    require(len(parts) == 6 and parts[0] == 'qa_blobs', 'not legacy blob layout')
    checked_path(root, str(relative), exists=True)
    raw = source.read_bytes()
    payload = decode(raw)
    rid = payload.get('request_id', '')
    require(bool(re.fullmatch(r'[A-Za-z0-9_-]{2,128}', rid)), 'unsafe request identity')
    captured = utc(payload['captured_at'])
    require(captured <= now, 'future capture timestamp')
    require(parts[1:4] == tuple(captured.strftime('%Y/%m/%d').split('/')),
            'capture date disagrees with legacy path')
    require(parts[4] == rid[:2] and parts[5] == rid + '.json.zst', 'capture identity mismatch')
    destination = 'qa_blobs/' + captured.strftime('%Y/%m/%d/%H/') + rid[:2] + '/' + parts[5]
    target = checked_path(root, destination)
    if target.exists():
        require(target.is_file() and digest(target.read_bytes()) == digest(raw), 'destination collision')
    return {'source': str(relative), 'destination': destination, 'sha256': digest(raw),
            'bytes': len(raw), 'request_id': rid, 'captured_at': captured.isoformat()}


def inventory(root, decode, now):
    # Hourly blobs have one additional directory and are excluded by this glob.
    # Leave recent writes for a subsequent plan, after their metadata has settled.
    return [entry(root, p, decode, now) for p in sorted((root / 'qa_blobs').glob('*/*/*/*/*.json.zst'))
            if p.stat().st_mtime <= now.timestamp() - 60]


def revalidate(root, items):
    for item in items:
        p = checked_path(root, item['source'])
        target = checked_path(root, item['destination'])
        require(p.is_file() or target.is_file(), 'both capture links missing')
        if p.exists():
            require(p.is_file() and digest(p.read_bytes()) == item['sha256'], 'source changed since plan')
        if target.exists():
            require(target.is_file() and digest(target.read_bytes()) == item['sha256'], 'destination collision')


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def link_targets(root, items):
    for item in items:
        source = checked_path(root, item['source'])
        target = checked_path(root, item['destination'])
        if not source.exists():
            require(target.is_file() and digest(target.read_bytes()) == item['sha256'], 'resumed target changed')
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        # The gateway is unprivileged. Preserve ownership of created parents.
        parent = target.parent
        while parent != root / 'qa_blobs':
            st = source.stat()
            if os.geteuid() == 0:
                os.chown(parent, st.st_uid, st.st_gid)
            parent = parent.parent
        if not target.exists():
            os.link(source, target)
        require(digest(target.read_bytes()) == item['sha256'], 'linked target changed')
        parent = target.parent
        while parent != root:
            sync_dir(parent)
            parent = parent.parent


def retire_source_links(root, items):
    # Called only after committed DB rewiring. The capture contents still exist
    # at destination; expiration belongs exclusively to lifecycle.RunLocalCleanup.
    for item in items:
        source = checked_path(root, item['source'])
        target = checked_path(root, item['destination'], exists=True)
        if not source.exists():
            require(digest(target.read_bytes()) == item['sha256'], 'resumed target changed')
            continue
        require(digest(source.read_bytes()) == item['sha256'] == digest(target.read_bytes()),
                'capture changed before source-link retirement')
        source.unlink()
        sync_dir(source.parent)


def inspect(app):
    return json.loads(subprocess.check_output(['docker', 'inspect', app]))[0]


def fingerprint(app):
    return [app['Id'], app['Image'], app['State']['StartedAt'], app['RestartCount']]


def guard(args, db):
    require(socket.gethostname() == args.expected_host, 'host mismatch')
    lifecycle_host.require_legacy_paused()
    app = inspect(args.app)
    require(app['State']['Running'] and app['State'].get('Health', {}).get('Status') == 'healthy', 'gateway not healthy')
    root = Path(args.data_dir)
    require(root.resolve() == root, 'data root must be canonical')
    require(any(m['Source'] == str(root) and m['Destination'] == '/app/data' for m in app['Mounts']), 'gateway data mount mismatch')
    require(db.query("SELECT NOT pg_is_in_recovery() AND pg_get_partkeydef('qa_records'::regclass)='RANGE (created_at)';") == 't', 'expected QA primary partition parent')
    require(db.query(f'SELECT pg_try_advisory_lock({QAMA});') == 't', 'QA lifecycle advisory lock busy')
    require(db.query("SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid WHERE i.inhparent='qa_records'::regclass AND pg_get_expr(c.relpartbound,c.oid)='DEFAULT';") == '0', 'DEFAULT QA partition present')
    receipt = json.loads(Path('/var/lib/tokenkey/qa-local/provision.json').read_text())
    require(receipt['ok'] and receipt['result']['provision']['ranges_covered'] >= 72, 'future QA partitions unverified')
    require(dt.datetime.now(dt.timezone.utc).timestamp()-receipt['observed_at'] < 7200, 'stale provision receipt')
    require(not any(p.is_file() for p in (root/'qa_dlq').rglob('*')), 'legacy DLQ requires separate repair')
    return root, app


def save(path, value):
    path = Path(path)
    require(not path.exists(), 'receipt already exists; use a new path')
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as out:
        out.write(encoded(value)); out.flush(); os.fsync(out.fileno())


def apply(db, root, plan):
    items = plan['entries']; revalidate(root, items)
    current = db.query('SELECT value FROM settings WHERE key=' + quote(CUTOVER) + ';')
    require(current == plan['existing_cutover'] or current == plan['cutover'], 'cutover changed since plan')
    db.query('BEGIN; INSERT INTO settings(key,value) VALUES ('+quote(CUTOVER)+','+quote(plan['cutover'])+') ON CONFLICT(key) DO NOTHING; COMMIT;')
    require(db.query('SELECT value FROM settings WHERE key='+quote(CUTOVER)+';') == plan['cutover'], 'cutover not active')
    link_targets(root, items)
    mapping = [{'old_uri':'file:///app/data/'+e['source'], 'new_uri':'file:///app/data/'+e['destination']} for e in items]
    db.query('CREATE TEMP TABLE qa_layout_map(old_uri text PRIMARY KEY,new_uri text NOT NULL); INSERT INTO qa_layout_map SELECT * FROM json_populate_recordset(NULL::qa_layout_map,'+quote(json.dumps(mapping))+');')
    db.query('BEGIN;')
    for field in FIELDS:
        db.query(f'UPDATE qa_records q SET {field}=m.new_uri FROM qa_layout_map m WHERE q.{field}=m.old_uri;')
    for field in FIELDS:
        require(db.query(f'SELECT count(*) FROM qa_records q JOIN qa_layout_map m ON q.{field}=m.old_uri;') == '0', 'legacy references remain')
    db.query('COMMIT;')
    retire_source_links(root, items)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['plan','apply'])
    parser.add_argument('--expected-host', required=True)
    parser.add_argument('--app', required=True)
    parser.add_argument('--postgres', required=True)
    parser.add_argument('--data-dir', required=True)
    parser.add_argument('--plan', required=True)
    parser.add_argument('--plan-sha256')
    args = parser.parse_args()
    with lifecycle_host.HOST_LOCK.open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        db = DB(args.postgres)
        try:
            root, app = guard(args, db)
            now = utc(db.query('SELECT clock_timestamp();'))
            if args.mode == 'plan':
                current = db.query('SELECT value FROM settings WHERE key='+quote(CUTOVER)+';')
                cutover = utc(current) if current else now.replace(minute=0,second=0,microsecond=0)
                require(cutover <= now and cutover.minute == 0 and cutover.second == 0 and cutover.microsecond == 0, 'invalid cutover')
                plan = {'version':1,'hostname':args.expected_host,'root':str(root), 'app':fingerprint(app),
                        'existing_cutover':current,'cutover':current or cutover.strftime('%Y-%m-%dT%H:00:00Z'),
                        'created_at':now.isoformat(),'entries':inventory(root,Decoder(),now)}
                save(args.plan,plan)
            else:
                raw = Path(args.plan).read_bytes()
                require(args.plan_sha256 and digest(raw)==args.plan_sha256,'exact plan SHA required')
                plan=json.loads(raw)
                require(plan['hostname']==args.expected_host and plan['root']==str(root) and plan['app']==fingerprint(app),'plan target changed')
                require(dt.timedelta(0)<=now-utc(plan['created_at'])<dt.timedelta(minutes=30),'plan expired')
                apply(db,root,plan)
                require(fingerprint(inspect(args.app))==plan['app'],'gateway changed during repair')
                save(args.plan+'.applied.json',{'ok':True,'plan_sha256':args.plan_sha256,'files':len(plan['entries']),'gateway_unchanged':True,'completed_at':dt.datetime.now(dt.timezone.utc).isoformat()})
            print(json.dumps({'mode':args.mode,'files':len(plan['entries']),'compressed_bytes':sum(e['bytes'] for e in plan['entries']),'plan_sha256':digest(encoded(plan)),'cutover':plan['cutover'],'gateway_unchanged':True}))
        finally:
            db.close()


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        print(json.dumps({'ok':False,'error_type':type(exc).__name__,'error':str(exc) if isinstance(exc,ValueError) else 'repair failed; source data and journal retained'}))
        sys.exit(1)
