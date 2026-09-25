#!/usr/bin/env python3
"""Gemini Web HTTP adapter. Contract: docs/approved/gemini-web-channel.md.

One process owns all account jars; no browser, OAuth token, automatic regeneration,
preview fallback, or invented token usage. Web wire fields are an unstable protocol.
"""
import argparse
import base64
import hmac
import http.cookiejar
import io
import json
import os
import re
import secrets
import signal
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

from build_digest import build_digest
from curl_cffi import requests
from curl_cffi.requests.impersonate import BrowserType
from PIL import Image
from session_contract import ALLOWED_COOKIE_DOMAINS

ORIGIN = 'https://gemini.google.com'
BATCH = ORIGIN + '/_/BardChatUi/data/batchexecute'
GENERATE = ORIGIN + '/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate'
IMAGE_HOSTS = frozenset(('lh3.googleusercontent.com', 'lh3.google.com', 'work.fife.usercontent.google.com'))
MODELS = {'gemini-web-flash': ('Flash', False), 'gemini-web-pro': ('Pro', False),
          'gemini-web-pro-image': ('Pro', True)}
IMAGE_ASPECT_ENUM = {'9:16': 59, '3:4': 60, '1:1': 61, '4:3': 62, '16:9': 63}
MAX_BYTES = 24 * 1024 * 1024
BROWSER_PROFILE = BrowserType.chrome145.value  # Fail startup if dependency cannot provide it.
REFRESH_SECONDS = 600
POLL_SECONDS = 60
# Control-plane contract this build speaks; check_control rejects anything else.
CONTROL_PROTOCOL_VERSION = 1
MAX_IMAGE_PIXELS = 16_000_000


# Identity is owned by build_digest.py so CI tags the image with the exact value
# this process reports at runtime.
BUILD_DIGEST = build_digest()
Image.MAX_IMAGE_PIXELS = MAX_IMAGE_PIXELS


class NoControlRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # A deployment URL mistake must not send the edge admin key elsewhere.
        return None


class Failure(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


class SessionVersionConflict(Exception):
    """The operator installed a newer session while this owner was refreshing."""


class SessionControl:
    """Narrow client for the edge-local Gemini Web session control plane."""
    def __init__(self, base_url, token):
        if not base_url.startswith('http://') and not base_url.startswith('https://'):
            raise ValueError('Invalid Gemini Web control URL')
        if len(token) < 32:
            raise ValueError('Gemini Web control token is required')
        self.base_url = base_url.rstrip('/')
        self.token = token
        self.opener = build_opener(NoControlRedirects())

    def request(self, method, path, payload=None, owner=None):
        body = None if payload is None else json.dumps(payload, separators=(',', ':')).encode()
        request = Request(self.base_url + path, data=body, method=method,
                          headers={'Authorization': 'Bearer ' + self.token,
                                   'Content-Type': 'application/json',
                                   'X-Gemini-Web-Lease': owner or ''})
        try:
            with self.opener.open(request, timeout=15) as response:
                return json.loads(response.read())
        except HTTPError as exc:
            if exc.code == 409:
                raise SessionVersionConflict() from exc
            raise Failure(503, 'Gemini Web session control unavailable') from exc
        except (URLError, ValueError, TimeoutError) as exc:
            raise Failure(503, 'Gemini Web session control unavailable') from exc

    def load(self, account_id):
        return self.request('GET', '/edge/gemini-web/accounts/%s/session' % account_id)

    def save_runtime(self, account_id, version, state, owner):
        return self.request('PUT', '/edge/gemini-web/accounts/%s/runtime' % account_id,
                            {'expected_version': version, 'runtime': state}, owner)

    def acquire(self, account_id, owner):
        return self.request('POST', '/edge/gemini-web/accounts/%s/lease' % account_id, owner=owner)

    def release(self, account_id, owner):
        return self.request('DELETE', '/edge/gemini-web/accounts/%s/lease' % account_id, owner=owner)

    def warm_accounts(self):
        return self.request('GET', '/edge/gemini-web/warm-accounts')


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
        raise Failure(400, 'Supported request fields: contents and generationConfig')
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
    allowed_config = {'responseModalities'} | ({'imageConfig'} if MODELS[model][1] else set())
    if not isinstance(config, dict) or set(config) - allowed_config:
        raise Failure(400, 'Unsupported generationConfig field')
    modalities = config.get('responseModalities', ['TEXT', 'IMAGE'] if MODELS[model][1] else ['TEXT'])
    if (not isinstance(modalities, list) or not modalities
            or any(x not in ('TEXT', 'IMAGE') for x in modalities)
            or ('IMAGE' in modalities) != MODELS[model][1]):
        raise Failure(400, 'responseModalities must match the selected Web text/image model')
    aspect_ratio = None
    image_config = config.get('imageConfig')
    if image_config is not None:
        if not MODELS[model][1] or not isinstance(image_config, dict) or set(image_config) != {'aspectRatio'}:
            raise Failure(400, 'imageConfig.aspectRatio is supported only for the Web image model')
        aspect_ratio = image_config.get('aspectRatio')
        if not isinstance(aspect_ratio, str) or aspect_ratio not in IMAGE_ASPECT_ENUM:
            raise Failure(400, 'Unsupported imageConfig.aspectRatio')
    return prompt, modalities, aspect_ratio


class Account:
    def __init__(self, bundle, state=None, persist_callback=None, deadline=None):
        self.deadline = deadline
        if (not isinstance(bundle, dict) or not isinstance(bundle.get('user_agent'), str)
                or not bundle['user_agent'] or not isinstance(bundle.get('cookies'), list)
                or not bundle['cookies']):
            raise ValueError('Invalid Gemini Web session bundle')
        if state is None:
            state = {}
        if not isinstance(state, dict):
            raise ValueError('Invalid Gemini Web session state')
        self.persist_callback = persist_callback
        self.blocked = state.get('blocked', False)
        self.generation_pending = state.get('generation_pending', False)
        self.cooldown_until = state.get('cooldown_until', 0)
        self.last_refresh = state.get('last_refresh', 0)
        self.session = requests.Session(impersonate=BROWSER_PROFILE, timeout=180, trust_env=False)
        self.session.headers['User-Agent'] = bundle['user_agent']
        usable_cookies = 0
        self.cookie_records = {}
        for c in state.get('cookies', bundle['cookies']):
            if not isinstance(c, dict) or not all(isinstance(c.get(field), str) and c[field]
                                                  for field in ('name', 'value', 'domain')):
                raise ValueError('Invalid Gemini Web cookie')
            domain = c['domain']
            # Only Google auth/image domains, never arbitrary imported cookie scopes.
            if domain.lstrip('.') not in ALLOWED_COOKIE_DOMAINS:
                continue
            expires = c.get('expires', -1)
            if expires and expires > 0 and expires <= time.time():
                continue
            self.session.cookies.jar.set_cookie(http.cookiejar.Cookie(
                0, c['name'], c['value'], None, False, domain, domain.startswith('.'),
                domain.startswith('.'), c.get('path', '/'), True, c.get('secure', True),
                int(expires) if expires and expires > 0 else None, False, None, None, {}, False))
            self.cookie_records[(c['name'], domain, c.get('path', '/'))] = dict(c)
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
        records = {}
        for cookie in self.session.cookies.jar:
            if cookie.is_expired():
                continue
            key = (cookie.name, cookie.domain, cookie.path)
            updated = dict(self.cookie_records.get(key, {}))
            updated.update(name=cookie.name, value=cookie.value, domain=cookie.domain,
                           path=cookie.path, secure=cookie.secure,
                           expires=cookie.expires or -1)
            records[key] = updated
        state = dict(cookies=list(records.values()), blocked=self.blocked,
                     generation_pending=self.generation_pending,
                     last_refresh=self.last_refresh, cooldown_until=self.cooldown_until)
        self.cookie_records = {(c['name'], c['domain'], c.get('path', '/')): c for c in state['cookies']}
        if self.persist_callback is not None:
            self.persist_callback(state)
            return
        raise RuntimeError('Gemini Web session persistence is not configured')

    def call(self, method, url, **kwargs):
        data = bytearray()
        def collect(chunk):
            if len(data) + len(chunk) > MAX_BYTES:
                raise Failure(502, 'Upstream response exceeds size limit')
            data.extend(chunk)
            return len(chunk)
        if self.deadline is not None:
            remaining = self.deadline - time.monotonic()
            if remaining <= 0:
                raise Failure(503, 'Gemini Web operation lease expired')
            kwargs['timeout'] = min(180, remaining)
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
                        if image.width * image.height > MAX_IMAGE_PIXELS:
                            raise ValueError('image exceeds decoded pixel budget')
                        # The original-image RPC is mandatory; dimensions are evidence,
                        # never a reason to upscale a preview and call it an original.
                        image.verify()
                    with Image.open(io.BytesIO(data)) as image:
                        image.load()
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
        prompt, modalities, aspect_ratio = request_prompt(body, model)
        blocked_at_start = self.blocked
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
                if aspect_ratio is not None:
                    # Captured from the real Gemini Images page. The ratio is
                    # represented twice in the browser RPC: the string in the
                    # image request options and the numeric aspect enum.
                    inner[0] = [prompt, 0, None, None, None, None, 0, None, None,
                                [None, None, None, None, None, None,
                                 [None, [None, aspect_ratio]]]]
                    inner[55] = [[IMAGE_ASPECT_ENUM[aspect_ratio]]]
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
            elif exc.code in (401, 403) and self.blocked and not blocked_at_start:
                # Authentication/authorization failures are terminal for this
                # session: call() has already persisted blocked=True during this
                # operation when Google rejected the session.  A local pause
                # (generation_pending) also raises 403, but must retain the
                # marker so an uncertain upstream operation cannot be retried.
                self.generation_pending = False
                self.persist()
            raise


class SessionOwner:
    """One serialized lifecycle for generation, refresh, reload and persistence."""
    def __init__(self, account_id, control):
        self.account_id = account_id
        self.control = control
        self.operation = threading.Lock()
        self.account = None
        self.runtime = None
        self.owner = None
        self.deadline = 0

    def persist(self, updated):
        if time.monotonic() >= self.deadline:
            raise Failure(503, 'Gemini Web operation lease expired')
        runtime = dict(self.runtime)
        runtime['cookies'] = updated['cookies']
        runtime['state'] = {k: v for k, v in updated.items() if k != 'cookies'}
        if runtime == self.runtime:
            return
        response = self.control.save_runtime(
            self.account_id, self.runtime['version'], runtime, self.owner)
        self.runtime = response['runtime']

    def run(self, key=None, model=None, body=None):
        if model is not None:
            request_prompt(body, model)
        if not self.operation.acquire(blocking=False):
            raise Failure(409, 'Account already has an active operation')
        acquired = False
        owner = secrets.token_hex(16)
        # Start before acquisition: control-plane latency consumes the budget.
        deadline = time.monotonic() + 480
        try:
            self.control.acquire(self.account_id, owner)
            acquired = True
            record = self.control.load(self.account_id)
            if str(record.get('account_id')) != self.account_id:
                raise Failure(403, 'Gemini Web account reference mismatch')
            if key is not None:
                validate_key(key, record)
            runtime = record.get('runtime')
            if (not isinstance(runtime, dict)
                    or type(runtime.get('version')) is not int
                    or runtime['version'] < 0):
                raise Failure(503, 'Invalid Gemini Web runtime')
            # Read and replace only inside the operation lock and DB lease.
            if self.runtime != runtime:
                replacement = Account(
                    bundle=runtime, state=runtime.get('state', {}),
                    persist_callback=self.persist, deadline=deadline)
                if self.account is not None:
                    self.account.close()
                self.account = replacement
                self.runtime = runtime
            self.owner, self.deadline = owner, deadline
            self.account.deadline = deadline
            if model is not None:
                return self.account.generate(model, body)
            account = self.account
            if (not account.blocked and not account.generation_pending
                    and time.time() >= account.cooldown_until
                    and time.time() - account.last_refresh >= REFRESH_SECONDS):
                try:
                    account.refresh()
                except Failure:
                    account.cooldown_until = time.time() + REFRESH_SECONDS
                    account.persist()
                    raise
        except SessionVersionConflict as exc:
            self.invalidate()
            raise Failure(409, 'Gemini Web session is busy or changed') from exc
        except Exception:
            self.invalidate()
            raise
        finally:
            try:
                if acquired:
                    try:
                        self.control.release(self.account_id, owner)
                    except (Failure, SessionVersionConflict):
                        # Cleanup failure must not turn completed generation into
                        # a retryable error. The DB crash lease still fences peers.
                        self.invalidate()
                        print(json.dumps({'event': 'session_lease_release_failed',
                                          'account': self.account_id}), flush=True)
            finally:
                self.owner = None
                self.operation.release()

    def invalidate(self):
        if self.account is not None:
            self.account.close()
        self.account = None
        self.runtime = None

    def close(self):
        with self.operation:
            self.invalidate()


def validate_key(key, record):
    expected = record.get('api_key')
    if (not isinstance(expected, str) or not expected or not key
            or not hmac.compare_digest(key.encode(), expected.encode())):
        raise Failure(401, 'Invalid worker API key')


class AuthorizedAccount:
    def __init__(self, owner, key):
        self.owner, self.key = owner, key

    def generate(self, model, body):
        return self.owner.run(key=self.key, model=model, body=body)


class ControlAdapter:
    """Database-backed registry; maintenance never impersonates a data caller."""
    def __init__(self, control):
        self.control = control
        self.registry_lock = threading.Lock()
        self.accounts = {}
        self.stopping = threading.Event()
        self.last_control_ok = 0

    def check_control(self):
        response = self.control.warm_accounts()
        records = response.get('accounts')
        if (response.get('protocol_version') != CONTROL_PROTOCOL_VERSION or not isinstance(records, list)
                or any(type(x) is not int or x <= 0 for x in records)):
            raise Failure(503, 'Incompatible Gemini Web control API')
        self.last_control_ok = time.monotonic()
        return records

    def ready(self):
        if self.stopping.is_set():
            return False
        # Long maintenance operations must not age out a healthy control plane.
        if time.monotonic() - self.last_control_ok >= POLL_SECONDS:
            try:
                self.check_control()
            except (Failure, ValueError, TypeError):
                return False
        return self.last_control_ok > 0

    def check_accounts(self, account_ids):
        """Read-only deployment check: no lease, persistence or Google calls."""
        self.check_control()
        for account_id in account_ids:
            if not re.fullmatch(r'[1-9][0-9]*', account_id):
                raise Failure(400, 'Invalid account reference')
            record = self.control.load(account_id)
            if (str(record.get('account_id')) != account_id or record.get('concurrency') != 1
                    or not isinstance(record.get('api_key'), str) or len(record['api_key']) < 32):
                raise Failure(503, 'Account binding/key/concurrency is not ready: ' + account_id)
            runtime = record.get('runtime', {})
            if type(runtime.get('version')) is not int or runtime['version'] < 0:
                raise Failure(503, 'Account runtime version is invalid: ' + account_id)
            account = Account(runtime, state=runtime.get('state', {}))
            try:
                if account.blocked or account.generation_pending or account.cooldown_until > time.time():
                    raise Failure(503, 'Account session is paused or cooling down: ' + account_id)
            finally:
                account.close()

    def session_owner(self, account_id):
        if not isinstance(account_id, str) or not re.fullmatch(r'[1-9][0-9]*', account_id):
            raise Failure(401, 'Missing Gemini Web account reference')
        with self.registry_lock:
            if account_id not in self.accounts:
                self.accounts[account_id] = SessionOwner(account_id, self.control)
            return self.accounts[account_id]

    def authorize(self, key, account_id=None):
        if not key:
            raise Failure(401, 'Invalid worker API key')
        if not isinstance(account_id, str) or not re.fullmatch(r'[1-9][0-9]*', account_id):
            raise Failure(401, 'Missing Gemini Web account reference')
        validate_key(key, self.control.load(account_id))
        owner = self.session_owner(account_id)
        return AuthorizedAccount(owner, key)

    def maintain(self):
        while not self.stopping.is_set():
            try:
                records = self.check_control()
                for account_id in records:
                    if self.stopping.is_set():
                        break
                    try:
                        self.session_owner(str(account_id)).run()
                    except Exception as exc:
                        print(json.dumps({'event': 'session_maintenance_failed',
                                          'account': account_id, 'type': type(exc).__name__}), flush=True)
            except Exception as exc:
                print(json.dumps({'event': 'session_poll_failed',
                                  'type': type(exc).__name__}), flush=True)
            self.stopping.wait(POLL_SECONDS)


class Server(ThreadingHTTPServer):
    daemon_threads = False
    request_queue_size = 8

    def __init__(self, address, adapter):
        self.adapter = adapter
        # Leave handler capacity for health/readiness while generation is busy.
        self.slots = threading.BoundedSemaphore(8)
        self.generations = threading.BoundedSemaphore(4)
        self.images = threading.BoundedSemaphore(1)
        super().__init__(address, Handler)

    def process_request(self, request, address):
        if not self.slots.acquire(blocking=False):
            try:
                request.settimeout(1)
                request.sendall(b'HTTP/1.1 503 Service Unavailable\r\nRetry-After: 1\r\n'
                                b'Connection: close\r\nContent-Length: 0\r\n\r\n')
            except OSError:
                pass  # Peer may already have disconnected; no operation started.
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

    def drain(self):
        self.adapter.stopping.set()
        threading.Thread(target=self.shutdown, daemon=True).start()


class Handler(BaseHTTPRequestHandler):
    server_version = 'GeminiWebAdapter/' + BUILD_DIGEST

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
        if code in (409, 429, 503):
            self.send_header('Retry-After', '1')
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path == '/healthz':
            self.reply(200, {'status': 'ok'})
        elif self.path == '/readyz':
            ready = self.server.adapter.ready()
            # Identity travels with both outcomes: a fleet check must be able to
            # tell "all four are stale" from "all four are ready" either way.
            self.reply(200 if ready else 503, {
                'status': 'ready' if ready else 'not_ready',
                'build_digest': BUILD_DIGEST,
                'control_protocol_version': CONTROL_PROTOCOL_VERSION,
            })
        else:
            self.reply(404, {'error': {'code': 404, 'status': 'NOT_FOUND', 'message': 'Not found'}})

    def do_POST(self):
        started = time.monotonic()
        code = 500
        admitted = False
        image_admitted = False
        try:
            if self.server.adapter.stopping.is_set():
                raise Failure(503, 'Worker is draining')
            admitted = self.server.generations.acquire(blocking=False)
            if not admitted:
                raise Failure(503, 'Worker is at capacity')
            account = self.server.adapter.authorize(
                self.headers.get('x-goog-api-key', ''),
                self.headers.get('x-tokenkey-gemini-web-account-id'))
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
            request_prompt(body, match[1])
            if MODELS[match[1]][1]:
                image_admitted = self.server.images.acquire(blocking=False)
                if not image_admitted:
                    raise Failure(503, 'Image worker is at capacity')
            result = account.generate(match[1], body)
            code = 200
            self.reply(code, result, match[2] == 'streamGenerateContent')
        except Failure as exc:
            code = exc.code
            status = {400: 'INVALID_ARGUMENT', 401: 'UNAUTHENTICATED', 403: 'PERMISSION_DENIED',
                409: 'ABORTED',
                404: 'NOT_FOUND', 413: 'RESOURCE_EXHAUSTED', 429: 'RESOURCE_EXHAUSTED', 502: 'UNAVAILABLE'}.get(code, 'INTERNAL')
            self.reply(code, {'error': {'code': code, 'status': status, 'message': exc.message}})
        except (BrokenPipeError, ConnectionResetError, TimeoutError):
            code = 499
        except Exception:
            self.reply(500, {'error': {'code': 500, 'status': 'INTERNAL', 'message': 'Worker internal error'}})
        finally:
            if image_admitted:
                self.server.images.release()
            if admitted:
                self.server.generations.release()
            print(json.dumps({'event': 'request', 'status': code, 'duration_ms': round((time.monotonic() - started) * 1000)}), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', nargs='+', metavar='ACCOUNT_ID',
                        help='Read-only control/runtime/concurrency check before rollout')
    args = parser.parse_args()
    os.umask(0o077)
    control_url = os.environ.get('GEMINI_WEB_CONTROL_URL', '').strip()
    try:
        adapter = ControlAdapter(SessionControl(control_url, os.environ.pop('GEMINI_WEB_CONTROL_TOKEN', '')))
        if args.check:
            adapter.check_accounts(args.check)
            print(json.dumps({'status': 'ready', 'accounts': args.check,
                              'build_digest': BUILD_DIGEST,
                              'control_protocol_version': CONTROL_PROTOCOL_VERSION}), flush=True)
            return 0
        adapter.check_control()
    except (Failure, ValueError, TypeError) as exc:
        print(json.dumps({'event': 'startup_check_failed', 'type': type(exc).__name__}), flush=True)
        return 1
    server = Server(('0.0.0.0', 8091), adapter)
    signal.signal(signal.SIGTERM, lambda *_: server.drain())
    signal.signal(signal.SIGINT, lambda *_: server.drain())
    maintenance = threading.Thread(target=adapter.maintain)
    maintenance.start()
    try:
        server.serve_forever()
    finally:
        adapter.stopping.set()
        maintenance.join()
        server.server_close()
        for owner in adapter.accounts.values():
            owner.close()
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
