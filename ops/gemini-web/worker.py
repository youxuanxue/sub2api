#!/usr/bin/env python3
"""Private Gemini Web HTTP adapter. Contract: docs/approved/gemini-web-channel.md.

One process owns all account jars; no browser, OAuth token, automatic regeneration,
preview fallback, or invented token usage. Web wire fields are an unstable protocol.
"""
import base64
import fcntl
import hashlib
import hmac
import http.cookiejar
import io
import json
import os
from pathlib import Path
import re
import secrets
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

from curl_cffi import requests
from curl_cffi.requests.impersonate import BrowserType
from PIL import Image

ORIGIN = 'https://gemini.google.com'
BATCH = ORIGIN + '/_/BardChatUi/data/batchexecute'
GENERATE = ORIGIN + '/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate'
IMAGE_HOSTS = frozenset(('lh3.googleusercontent.com', 'lh3.google.com', 'work.fife.usercontent.google.com'))
MODELS = {'gemini-web-flash': ('Flash', False), 'gemini-web-pro': ('Pro', False),
          'gemini-web-pro-image': ('Pro', True)}
MAX_BYTES = 24 * 1024 * 1024
BROWSER_PROFILE = BrowserType.chrome145.value  # Fail startup if dependency cannot provide it.
REFRESH_SECONDS = 600
HOT_RELOAD_SECONDS = 5
Image.MAX_IMAGE_PIXELS = 32_000_000


class Failure(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


def nested(value, *indices, default=None):
    try:
        for index in indices:
            value = value[index]
        return value
    except (TypeError, KeyError, IndexError):
        return default


def field(value, index):
    if not isinstance(value, list):
        return None
    if len(value) > index and not isinstance(value[index], dict):
        return value[index]
    return value[-1].get(str(index + 1)) if value and isinstance(value[-1], dict) else None


def frames(raw):
    # JSON uses escaped newlines. Length framing counts UTF-16 code units; do not
    # use Python character counts to slice those frames. Parse complete JSON lines.
    for line in raw.splitlines():
        if not line.startswith('['):
            continue
        try:
            batch = json.loads(line)
        except ValueError as exc:
            raise Failure(502, 'Invalid upstream frame') from exc
        if not isinstance(batch, list):
            raise Failure(502, 'Invalid upstream frame')
        for entry in batch:
            if isinstance(entry, list):
                yield entry


def generation_result(raw):
    metadata = None
    candidates = None
    for frame in frames(raw):
        error = nested(frame, 5, 2, 0, 1, 0)
        if error:
            raise Failure(429 if error == 1037 else 502, 'Upstream generation rejected (%s)' % error)
        if nested(frame, 0) != 'wrb.fr' or not isinstance(nested(frame, 2), str):
            continue
        try:
            value = json.loads(frame[2])
        except ValueError as exc:
            raise Failure(502, 'Invalid upstream generation payload') from exc
        if nested(value, 1):
            metadata = value[1]
        if nested(value, 4):
            candidates = value[4]
    candidate = nested(candidates, 0)
    if not candidate or nested(candidate, 8, 0) != 2:
        raise Failure(502, 'Upstream generation incomplete; request was not retried')
    text = nested(candidate, 1, 0, default='')
    if not isinstance(text, str):
        raise Failure(502, 'Invalid upstream text')
    text = re.sub(r'https?://googleusercontent\.com/(?:\w+/)+\d+\n*', '', text).strip()
    images = nested(field(nested(candidate, 12), 7), 0, default=[]) or []
    refs = []
    for image in images:
        image_id = nested(image, 1, 0)
        if not isinstance(image_id, str) or not image_id or not metadata:
            raise Failure(502, 'Missing full-size image reference')
        refs.append((image_id, nested(metadata, 0), nested(metadata, 1), nested(candidate, 0)))
    if len(refs) > 4:
        raise Failure(502, 'Too many upstream images')
    if not text and not refs:
        raise Failure(502, 'Empty upstream generation')
    return text, refs


def image_url(url):
    if not isinstance(url, str):
        raise Failure(502, 'Invalid image URL')
    parsed = urlsplit(url)
    try:
        valid = (parsed.scheme == 'https' and parsed.hostname in IMAGE_HOSTS
                 and parsed.port in (None, 443) and not parsed.username and not parsed.password
                 and not parsed.fragment and not any(c.isspace() for c in url))
    except ValueError:
        valid = False
    if not valid:
        raise Failure(502, 'Untrusted image URL')
    return url


def request_prompt(body, model):
    if model not in MODELS:
        raise Failure(404, 'Unknown Gemini Web model')
    if not isinstance(body, dict) or set(body) - {'contents', 'generationConfig'}:
        raise Failure(400, 'Supported request fields: contents and generationConfig.responseModalities')
    contents = body.get('contents')
    if not isinstance(contents, list) or len(contents) != 1:
        raise Failure(400, 'This adapter currently supports one user turn per request')
    turn = contents[0]
    if (not isinstance(turn, dict) or set(turn) - {'role', 'parts'}
            or turn.get('role', 'user') != 'user' or not isinstance(turn.get('parts'), list)):
        raise Failure(400, 'Expected user content with text parts')
    texts = []
    for part in turn['parts']:
        if not isinstance(part, dict) or set(part) != {'text'} or not isinstance(part['text'], str):
            raise Failure(400, 'Only text input parts are supported')
        texts.append(part['text'])
    prompt = '\n'.join(texts)
    if not prompt.strip() or len(prompt) > 32000:
        raise Failure(400, 'Prompt must contain 1 to 32000 characters')
    config = body.get('generationConfig', {})
    if not isinstance(config, dict) or set(config) - {'responseModalities'}:
        raise Failure(400, 'Unsupported generationConfig field')
    modalities = config.get('responseModalities', ['TEXT', 'IMAGE'] if MODELS[model][1] else ['TEXT'])
    if (not isinstance(modalities, list) or not modalities
            or any(x not in ('TEXT', 'IMAGE') for x in modalities)
            or ('IMAGE' in modalities) != MODELS[model][1]):
        raise Failure(400, 'responseModalities must match the selected Web text/image model')
    return prompt, modalities


def atomic_json(path, value):
    temporary = path.with_name(path.name + '.' + secrets.token_hex(6))
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, 'w') as output:
            json.dump(value, output, separators=(',', ':'))
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)


class Account:
    def __init__(self, root):
        self.root = Path(root)
        bundle = json.loads((self.root / 'bundle.json').read_text())
        if (not isinstance(bundle, dict) or not isinstance(bundle.get('ua'), str)
                or not bundle['ua'] or not isinstance(bundle.get('cookies'), list)
                or not bundle['cookies']):
            raise ValueError('Invalid Gemini Web session bundle')
        saved = self.root / 'state.json'
        state = json.loads(saved.read_text()) if saved.exists() else {}
        self.blocked = state.get('blocked', False)
        self.generation_pending = state.get('generation_pending', False)
        self.cooldown_until = state.get('cooldown_until', 0)
        self.last_refresh = state.get('last_refresh', 0)
        self.lock = threading.Lock()
        self.session = requests.Session(impersonate=BROWSER_PROFILE, timeout=180, trust_env=False)
        self.session.headers['User-Agent'] = bundle['ua']
        usable_cookies = 0
        for c in state.get('cookies', bundle['cookies']):
            if not isinstance(c, dict) or not all(isinstance(c.get(field), str) and c[field]
                                                  for field in ('name', 'value', 'domain')):
                raise ValueError('Invalid Gemini Web cookie')
            domain = c['domain']
            # Only Google auth/image domains, never arbitrary imported cookie scopes.
            if domain.lstrip('.') not in ('google.com', 'gemini.google.com', 'accounts.google.com',
                                           'lh3.google.com', 'lh3.googleusercontent.com',
                                           'work.fife.usercontent.google.com'):
                continue
            expires = c.get('expires', -1)
            self.session.cookies.jar.set_cookie(http.cookiejar.Cookie(
                0, c['name'], c['value'], None, False, domain, domain.startswith('.'),
                domain.startswith('.'), c.get('path', '/'), True, c.get('secure', True),
                int(expires) if expires and expires > 0 else None, False, None, None, {}, False))
            usable_cookies += 1
        if not usable_cookies:
            raise ValueError('Gemini Web session bundle contains no supported cookies')
        self.fields = {}
        self.models = {}
        self.ready_at = 0
        self.session_id = str(uuid.uuid4())

    def close(self):
        self.session.close()

    def persist(self):
        cookies = [dict(name=c.name, value=c.value, domain=c.domain, path=c.path,
                        secure=c.secure, expires=c.expires or -1) for c in self.session.cookies.jar]
        atomic_json(self.root / 'state.json', dict(cookies=cookies, blocked=self.blocked,
                    generation_pending=self.generation_pending,
                    last_refresh=self.last_refresh, cooldown_until=self.cooldown_until))

    def call(self, method, url, **kwargs):
        data = bytearray()
        def collect(chunk):
            if len(data) + len(chunk) > MAX_BYTES:
                raise Failure(502, 'Upstream response exceeds size limit')
            data.extend(chunk)
            return len(chunk)
        try:
            response = self.session.request(method, url, allow_redirects=False,
                                             content_callback=collect, **kwargs)
        except Exception as exc:
            # curl exceptions include signed URLs. Never expose their text.
            self.cooldown_until = time.time() + 60
            print(json.dumps({'event': 'upstream_transport_error', 'type': type(exc).__name__}), flush=True)
            raise Failure(502, 'Upstream transport failed; request was not retried') from exc
        finally:
            self.persist()
        code = response.status_code
        if code in (401, 403) or (300 <= code < 400 and 'accounts.google.com' in response.headers.get('location', '')):
            self.blocked = True
            self.persist()
            raise Failure(403, 'Google session requires operator verification or re-import')
        if code == 429:
            self.cooldown_until = time.time() + 300
            self.persist()
            raise Failure(429, 'Google Web quota exceeded')
        if code != 200 and code not in (301, 302, 303, 307, 308):
            raise Failure(502, 'Google Web HTTP %d' % code)
        return response, bytes(data)

    def params(self):
        return {'hl': 'en', '_reqid': secrets.randbelow(9_000_000) + 100000, 'rt': 'c',
                'bl': self.fields['cfb2h'], 'f.sid': self.fields['FdrFJe']}

    def batch(self, rpc, payload):
        _, data = self.call('POST', BATCH, params=dict(self.params(), rpcids=rpc),
            headers={'Origin': ORIGIN, 'Referer': ORIGIN + '/', 'X-Same-Domain': '1'},
            data={'at': self.fields['SNlM0e'], 'f.req': json.dumps([[[rpc, json.dumps(payload), None, 'generic']]])})
        for frame in frames(data.decode('utf-8')):
            if nested(frame, 0) == 'wrb.fr' and nested(frame, 1) == rpc and isinstance(nested(frame, 2), str):
                try:
                    return json.loads(frame[2])
                except ValueError as exc:
                    raise Failure(502, 'Invalid upstream RPC payload') from exc
        raise Failure(502, 'Missing upstream RPC result')

    def bootstrap(self):
        _, data = self.call('GET', ORIGIN + '/app')
        html = data.decode('utf-8')
        fields = {}
        for name in ('SNlM0e', 'cfb2h', 'FdrFJe'):
            match = re.search(r'"' + name + r'"\s*:\s*("(?:[^"\\]|\\.)*")', html)
            if match:
                fields[name] = json.loads(match[1])
        if len(fields) != 3:
            self.blocked = True
            self.persist()
            raise Failure(403, 'Google session is no longer authenticated; re-import required')
        self.fields = fields
        status = self.batch('otAQ7b', [])
        if nested(status, 14) not in (None, 1000):
            self.blocked = True
            self.persist()
            raise Failure(403, 'Google Web account is not available')
        tiers, capabilities = nested(status, 16, default=[]) or [], nested(status, 17, default=[]) or []
        if 21 in tiers or 22 in tiers:
            raise Failure(502, 'Unsupported Google Web account tier')
        capacity = 4 if 115 in capabilities else 3 if 16 in tiers or 106 in capabilities else 2 if 8 in tiers or 19 in capabilities else 1
        models = {}
        for model in nested(status, 15, default=[]) or []:
            name, identifier, number = nested(model, 1), nested(model, 0), nested(model, 17)
            if isinstance(name, str) and isinstance(identifier, str) and isinstance(number, int):
                models[name] = (identifier, number, capacity)
        if not models:
            raise Failure(502, 'Google Web model discovery failed')
        self.models = models
        self.ready_at = time.time()

    def refresh(self):
        self.call('POST', 'https://accounts.google.com/RotateCookies',
                  headers={'Content-Type': 'application/json', 'Origin': 'https://accounts.google.com'},
                  data='[000,"-0000000000000000000"]')
        self.bootstrap()
        self.last_refresh = time.time()
        self.persist()

    def download(self, reference):
        image_id, cid, rid, rcid = reference
        if not all(isinstance(x, str) and x for x in reference):
            raise Failure(502, 'Incomplete full-size image reference')
        payload = [[[None, None, None, [None, None, None, None, None, '']], [image_id, 0],
                    None, [19, ''], None, None, None, None, None, ''],
                   [rid, rcid, cid, None, ''], 1, 0, 1]
        url = image_url(nested(self.batch('c8o8Fe', payload), 0))
        if '=d' not in url:
            url += '=d-I'
        if 'alr=' not in url:
            url += ('&' if '?' in url else '?') + 'alr=yes'
        for _ in range(6):
            response, data = self.call('GET', image_url(url), headers={'Referer': ORIGIN + '/'})
            if response.status_code in (301, 302, 303, 307, 308):
                url = response.headers.get('location')
                continue
            mime = response.headers.get('content-type', '').split(';')[0]
            if mime.startswith('image/'):
                try:
                    with Image.open(io.BytesIO(data)) as image:
                        actual = Image.MIME.get(image.format)
                        if actual not in ('image/jpeg', 'image/png', 'image/webp') or actual != mime:
                            raise ValueError('image type mismatch')
                        # The original-image RPC is mandatory; dimensions are evidence,
                        # never a reason to upscale a preview and call it an original.
                        image.verify()
                except Exception as exc:
                    raise Failure(502, 'Invalid original image bytes') from exc
                return {'inlineData': {'mimeType': mime, 'data': base64.b64encode(data).decode('ascii')}}
            if len(data) > 16384:
                raise Failure(502, 'Invalid image authorization response')
            try:
                url = data.decode('utf-8').strip()
            except UnicodeDecodeError as exc:
                raise Failure(502, 'Invalid image authorization response') from exc
        raise Failure(502, 'Full-size image authorization did not finish; no preview fallback')

    def generate(self, model, body):
        prompt, modalities = request_prompt(body, model)
        if not self.lock.acquire(blocking=False):
            raise Failure(429, 'This account already has an active request')
        try:
            if self.blocked or self.generation_pending:
                raise Failure(403, 'Session paused; operator verification or re-import required')
            if time.time() < self.cooldown_until:
                raise Failure(429, 'Account is cooling down')
            if time.time() - self.last_refresh > REFRESH_SECONDS:
                self.refresh()
            elif time.time() - self.ready_at > 300:
                self.bootstrap()
            selector = self.models.get(MODELS[model][0])
            if selector is None:
                raise Failure(404, 'Requested Web model is unavailable for this account')
            identifier, number, capacity = selector
            wants_image = MODELS[model][1]
            inner = [None] * (99 if wants_image else 81)
            inner[0] = [prompt, 0, None, None, None, None, 0]
            inner[1] = ['en']
            inner[2] = ['', '', '', None, None, None, None, None, None, '']
            for index, value in {6: [1], 7: 1, 10: 1, 11: 0, 17: [[0]], 18: 0, 27: 1,
                30: [4], 41: [1], 53: 0, 61: [], 68: 1, 79: number, 80: 1}.items():
                inner[index] = value
            if wants_image:
                # Current Web image mode (us4 replay, 2026-09-21). The text
                # shape is insufficient for this operation. Browser-only opaque
                # fields 3/4 are deliberately absent: Pro + original download
                # was verified without them or the browser's timing header.
                for index, value in {49: 14, 54: [], 55: [], 68: 2,
                                     91: 0, 96: 0, 98: 1}.items():
                    inner[index] = value
            request_id = str(uuid.uuid4()).upper()
            inner[59] = request_id
            headers = {'Origin': ORIGIN, 'Referer': ORIGIN + '/', 'X-Same-Domain': '1',
                'x-goog-ext-525001261-jspb': json.dumps([1, None, None, None, identifier, None,
                    None, 0, [4, 5, 6, 8], None, None, capacity, None, None, number, 1, self.session_id]),
                'x-goog-ext-525005358-jspb': json.dumps([request_id, 1]),
                'x-goog-ext-73010989-jspb': '[0]', 'x-goog-ext-73010990-jspb': '[0,0,0]'}
            self.generation_pending = True
            self.persist()
            _, data = self.call('POST', GENERATE, params=self.params(), headers=headers,
                data={'at': self.fields['SNlM0e'], 'f.req': json.dumps([None, json.dumps(inner)])})
            text, references = generation_result(data.decode('utf-8'))
            if MODELS[model][1] and not references:
                raise Failure(502, 'Google returned no generated image; request was not retried')
            if references and 'IMAGE' not in modalities:
                raise Failure(502, 'Google returned images for a text-only request')
            parts = [{'text': text}] if text and 'TEXT' in modalities else []
            encoded_bytes = 0
            for reference in references:
                part = self.download(reference)
                encoded_bytes += len(part['inlineData']['data'])
                if encoded_bytes > MAX_BYTES:
                    raise Failure(502, 'Combined original images exceed response size limit')
                parts.append(part)
            self.generation_pending = False
            self.persist()
            return {'candidates': [{'content': {'role': 'model', 'parts': parts},
                                    'finishReason': 'STOP', 'index': 0}]}
        except Failure as exc:
            if exc.code == 429:
                self.cooldown_until = time.time() + 300
                self.generation_pending = False
                self.persist()
            raise
        finally:
            self.lock.release()


def file_signature(path):
    stat = path.stat()
    return stat.st_mtime_ns, stat.st_size


class ManagedAccount:
    """One swappable session owner. Its lock isolates one account only."""
    def __init__(self, account_id, api_key, root, signature):
        self.account_id = account_id
        self.api_key = api_key
        self.root = Path(root)
        self.signature = signature
        self.operation = threading.Lock()
        self.account = Account(self.root)

    def generate(self, model, body):
        with self.operation:
            return self.account.generate(model, body)

    def maintain(self):
        if not self.operation.acquire(blocking=False):
            return
        try:
            account = self.account
            if (not account.blocked and not account.generation_pending
                    and time.time() >= account.cooldown_until
                    and time.time() - account.last_refresh >= REFRESH_SECONDS):
                try:
                    account.refresh()
                    print(json.dumps({'event': 'session_refreshed', 'account': self.account_id}), flush=True)
                except Failure as exc:
                    account.cooldown_until = time.time() + REFRESH_SECONDS
                    account.persist()
                    print(json.dumps({'event': 'session_refresh_failed', 'account': self.account_id,
                                      'status': exc.code}), flush=True)
        finally:
            self.operation.release()

    def replace(self, replacement, signature):
        with self.operation:
            previous, self.account = self.account, replacement
            self.signature = signature
        previous.close()

    def close(self):
        with self.operation:
            self.account.close()


class Adapter:
    def __init__(self, root):
        root = Path(root)
        self.root = root
        self.lease = (root / 'owner.lock').open('a')
        fcntl.flock(self.lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.registry_lock = threading.RLock()
        self.accounts = {}
        self.accounts_by_id = {}
        self.reload_from_disk(initial=True)

    def configured_accounts(self):
        entries = json.loads((self.root / 'accounts.json').read_text())
        if not isinstance(entries, list):
            raise ValueError('Invalid account configuration')
        configured = []
        digests = set()
        account_ids = set()
        for entry in entries:
            if not isinstance(entry, dict):
                raise ValueError('Invalid account configuration')
            account_id, key = entry['id'], entry['api_key']
            if not re.fullmatch(r'[a-zA-Z0-9_-]+', account_id) or len(key) < 32:
                raise ValueError('Invalid account configuration')
            digest = hashlib.sha256(key.encode()).digest()
            if digest in digests or account_id in account_ids:
                raise ValueError('Duplicate API key')
            bundle = self.root / account_id / 'bundle.json'
            configured.append((account_id, key, digest, bundle, file_signature(bundle)))
            digests.add(digest)
            account_ids.add(account_id)
        return configured

    def reload_from_disk(self, initial=False):
        """Apply account manifest/bundle changes without replacing unrelated sessions."""
        configured = self.configured_accounts()
        replacements = []
        additions = []
        with self.registry_lock:
            current = dict(self.accounts_by_id)
        for account_id, key, digest, bundle, signature in configured:
            managed = current.get(account_id)
            if managed is None or managed.api_key != key:
                additions.append((account_id, digest, ManagedAccount(account_id, key, bundle.parent, signature)))
            elif managed.signature != signature:
                replacements.append((managed, signature))

        # Read every candidate before replacing any live session. A malformed
        # multi-account import leaves the current set untouched.
        prepared = [(managed, signature, Account(managed.root)) for managed, signature in replacements]
        # A bundle replacement waits only for that account's active request.
        for managed, signature, replacement in prepared:
            managed.replace(replacement, signature)

        with self.registry_lock:
            next_by_id = {}
            next_by_digest = {}
            added = {account_id: (digest, managed) for account_id, digest, managed in additions}
            for account_id, key, digest, _bundle, _signature in configured:
                if account_id in added:
                    _, managed = added[account_id]
                else:
                    managed = self.accounts_by_id[account_id]
                next_by_id[account_id] = managed
                next_by_digest[digest] = managed
            retired = [managed for account_id, managed in self.accounts_by_id.items()
                       if account_id not in next_by_id or next_by_id[account_id] is not managed]
            self.accounts_by_id = next_by_id
            self.accounts = next_by_digest
        for managed in retired:
            # A replaced key/removed account is no longer selectable. Let a
            # request that already held it finish before releasing its session.
            threading.Thread(target=managed.close, daemon=True).start()
        if not initial and (replacements or additions or retired):
            print(json.dumps({'event': 'accounts_hot_reloaded', 'reloaded': len(replacements),
                              'added': len(additions), 'retired': len(retired)}), flush=True)

    def maintain(self):
        # A background owner renews idle accounts too. Browser sessions may rotate
        # independently; a six-hour, request-only renewal cannot preserve a clone.
        while True:
            try:
                self.reload_from_disk()
            except Exception as exc:
                # Preserve existing sessions when an import is incomplete or invalid.
                print(json.dumps({'event': 'accounts_hot_reload_failed', 'type': type(exc).__name__}), flush=True)
            with self.registry_lock:
                accounts = list(self.accounts.values())
            for account in accounts:
                account.maintain()
            time.sleep(HOT_RELOAD_SECONDS)

    def authorize(self, key):
        digest = hashlib.sha256(key.encode()).digest()
        with self.registry_lock:
            accounts = list(self.accounts.items())
        for expected, account in accounts:
            if hmac.compare_digest(digest, expected):
                return account
        raise Failure(401, 'Invalid worker API key')


class Server(ThreadingHTTPServer):
    daemon_threads = True
    request_queue_size = 8

    def __init__(self, address, adapter):
        self.adapter = adapter
        self.slots = threading.BoundedSemaphore(4)
        super().__init__(address, Handler)

    def process_request(self, request, address):
        if not self.slots.acquire(blocking=False):
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, address)
        except Exception:
            self.slots.release()
            raise

    def process_request_thread(self, request, address):
        try:
            super().process_request_thread(request, address)
        finally:
            self.slots.release()


class Handler(BaseHTTPRequestHandler):
    server_version = 'GeminiWebAdapter'

    def setup(self):
        super().setup()
        self.connection.settimeout(30)

    def log_message(self, *args):
        pass  # HTTP paths/headers may contain credentials; emit only controlled events.

    def reply(self, code, body, stream=False):
        payload = json.dumps(body, ensure_ascii=False, separators=(',', ':')).encode()
        if stream:
            payload = b'data: ' + payload + b'\n\n'
        self.send_response(code)
        self.send_header('Content-Type', 'text/event-stream' if stream else 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path == '/healthz':
            self.reply(200, {'status': 'ok'})
        else:
            self.reply(404, {'error': {'code': 404, 'status': 'NOT_FOUND', 'message': 'Not found'}})

    def do_POST(self):
        started = time.monotonic()
        code = 500
        try:
            account = self.server.adapter.authorize(self.headers.get('x-goog-api-key', ''))
            match = re.fullmatch(r'/v1beta/models/([a-z0-9-]+):(generateContent|streamGenerateContent)(?:\?alt=sse)?', self.path)
            if not match:
                raise Failure(404, 'Unsupported endpoint')
            if self.headers.get('Transfer-Encoding') or len(self.headers.get_all('Content-Length', [])) != 1:
                raise Failure(400, 'A single Content-Length is required')
            try:
                size = int(self.headers['Content-Length'])
            except ValueError as exc:
                raise Failure(400, 'Invalid Content-Length') from exc
            if not 0 < size <= 128 * 1024:
                raise Failure(413, 'Request exceeds size limit')
            raw = self.rfile.read(size)
            if len(raw) != size:
                raise Failure(400, 'Incomplete request body')
            try:
                body = json.loads(raw)
            except (ValueError, UnicodeDecodeError) as exc:
                raise Failure(400, 'Invalid JSON') from exc
            result = account.generate(match[1], body)
            code = 200
            self.reply(code, result, match[2] == 'streamGenerateContent')
        except Failure as exc:
            code = exc.code
            status = {400: 'INVALID_ARGUMENT', 401: 'UNAUTHENTICATED', 403: 'PERMISSION_DENIED',
                404: 'NOT_FOUND', 413: 'RESOURCE_EXHAUSTED', 429: 'RESOURCE_EXHAUSTED', 502: 'UNAVAILABLE'}.get(code, 'INTERNAL')
            self.reply(code, {'error': {'code': code, 'status': status, 'message': exc.message}})
        except (BrokenPipeError, ConnectionResetError, TimeoutError):
            code = 499
        except Exception:
            self.reply(500, {'error': {'code': 500, 'status': 'INTERNAL', 'message': 'Worker internal error'}})
        finally:
            print(json.dumps({'event': 'request', 'status': code, 'duration_ms': round((time.monotonic() - started) * 1000)}), flush=True)


if __name__ == '__main__':
    os.umask(0o077)
    adapter = Adapter(os.environ.get('GEMINI_WEB_STATE_DIR', '/state'))
    threading.Thread(target=adapter.maintain, daemon=True).start()
    Server(('0.0.0.0', 8091), adapter).serve_forever()
