import base64
import copy
import http.client
import io
import json
from pathlib import Path
import subprocess
import sys
import threading
import time
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import worker
from PIL import Image


def wire(text='answer', images=False, completed=True, sparse=False):
    candidate = [None] * 38
    candidate[0], candidate[1], candidate[8] = 'rc_one', [text], [2 if completed else 1]
    if images:
        image = [[None, None, None, [None, None, 'description', 'https://lh3.googleusercontent.com/preview']], ['image-id']]
        rich = [None] * 8
        rich[7] = [[image]]
        candidate[12] = [{'8': [[image]]}] if sparse else rich
    candidate[37] = [['private thoughts must never be returned']]
    value = [None, ['c_one', 'r_one'], None, None, [candidate]]
    return ")]}'\n\n12\n" + json.dumps([['wrb.fr', None, json.dumps(value), None]]) + '\n'


def bundle(cookie='cookie-one'):
    return {'user_agent': 'test', 'cookies': [
        {'name': '__Secure-1PSID', 'value': cookie, 'domain': '.google.com',
         'path': '/', 'secure': True}]}


class Control:
    def __init__(self):
        self.records = {'28': {'account_id': 28, 'api_key': 'a' * 40, 'concurrency': 1,
                              'runtime': dict(bundle(), version=1, state={})}}
        self.leases = {}
        self.writes = []

    def load(self, account_id):
        return copy.deepcopy(self.records[account_id])

    def acquire(self, account_id, owner):
        if account_id in self.leases:
            raise worker.SessionVersionConflict()
        self.leases[account_id] = owner

    def release(self, account_id, owner):
        if self.leases.get(account_id) == owner:
            del self.leases[account_id]

    def save_runtime(self, account_id, version, runtime, owner):
        if (self.leases.get(account_id) != owner
                or self.records[account_id]['runtime']['version'] != version):
            raise worker.SessionVersionConflict()
        self.records[account_id]['runtime'] = dict(copy.deepcopy(runtime), version=version + 1)
        self.writes.append(version)
        return self.load(account_id)

    def warm_accounts(self):
        return {'accounts': [int(k) for k in self.records], 'protocol_version': 1}


class WorkerTests(unittest.TestCase):
    def test_scheduler_admission_contract_fixtures(self):
        cases = json.loads(Path(__file__).with_name('request_contract_cases.json').read_text())
        for case in cases:
            with self.subTest(case=case['name']):
                if case['accepted']:
                    prompt, modalities = worker.request_prompt(case['body'], case['model'])
                    self.assertTrue(prompt.strip())
                    self.assertEqual('IMAGE' in modalities, worker.MODELS[case['model']][1])
                else:
                    with self.assertRaises(worker.Failure):
                        worker.request_prompt(case['body'], case['model'])

    def test_pinned_transport_can_configure_profile_without_network(self):
        from curl_cffi import Curl
        curl = Curl()
        try:
            self.assertEqual(curl.impersonate(worker.BROWSER_PROFILE), 0)
        finally:
            curl.close()

    def setUp(self):
        self.saved = {}
        self.account = worker.Account(bundle(), persist_callback=self.save)
        self.account.models = {'Flash': ('id-flash', 1, 2), 'Pro': ('id-pro', 3, 2)}
        self.account.ready_at = time.time()
        self.account.last_refresh = time.time()
        self.account.fields = {'SNlM0e': 'fixture-at', 'cfb2h': 'fixture-build', 'FdrFJe': 'fixture-session'}

    def save(self, state):
        self.saved = copy.deepcopy(state)

    def restore(self):
        return worker.Account(bundle(), state=self.saved, persist_callback=self.save)

    def tearDown(self):
        self.account.session.close()

    def test_completed_candidates_and_sparse_images(self):
        self.assertEqual(worker.generation_result(wire('中文 answer')), ('中文 answer', []))
        for sparse in [True, False]:
            self.assertEqual(worker.generation_result(wire(images=True, sparse=sparse))[1],
                             [('image-id', 'c_one', 'r_one', 'rc_one')])
        with self.assertRaisesRegex(worker.Failure, 'incomplete'):
            worker.generation_result(wire(completed=False))
        with self.assertRaises(worker.Failure):
            worker.generation_result('12\n[["wrb.fr", null, "truncated')

    def test_official_response_omits_unobserved_usage_and_thoughts(self):
        with patch.object(self.account, 'call', return_value=(None, wire().encode())) as call:
            result = self.account.generate('gemini-web-flash', {'contents': [{'parts': [{'text': 'test'}]}]})
        self.assertEqual(result, {'candidates': [{'content': {'role': 'model', 'parts': [{'text': 'answer'}]}, 'finishReason': 'STOP', 'index': 0}]})
        self.assertEqual(call.call_count, 1)

    def test_bad_input_has_no_upstream_side_effect(self):
        for body in [{'contents': []}, {'contents': [{'parts': [{'inlineData': {}}]}]},
                     {'contents': [{'parts': [{'text': 'x'}]}], 'tools': []},
                     {'contents': [{'parts': [{'text': 'x'}]}], 'generationConfig': {'temperature': 0}}]:
            with patch.object(self.account, 'call') as call:
                with self.assertRaises(worker.Failure):
                    self.account.generate('gemini-web-flash', body)
                call.assert_not_called()

    def test_image_mode_uses_live_pro_selector_without_browser_tokens(self):
        original = {'inlineData': {'mimeType': 'image/jpeg', 'data': 'b3JpZ2luYWw='}}
        def upstream(method, url, **kwargs):
            inner = json.loads(json.loads(kwargs['data']['f.req'])[1])
            selector = json.loads(kwargs['headers']['x-goog-ext-525001261-jspb'])
            # Model an upstream that rejects the old text-shaped image request.
            if len(inner) != 99 or inner[49] != 14 or inner[68] != 2 or inner[98] != 1:
                raise worker.Failure(403, 'Image mode required')
            self.assertEqual(selector[4], 'id-pro')
            self.assertEqual((selector[14], inner[79]), (3, 3))
            self.assertEqual(inner[3:5], [None, None])
            self.assertEqual(inner[0][0], 'Draw a cube')
            self.assertEqual(inner[2][:3], ['', '', ''])
            return None, wire(images=True).encode()
        with patch.object(self.account, 'call', side_effect=upstream) as call:
            with patch.object(self.account, 'download', return_value=original) as download:
                result = self.account.generate('gemini-web-pro-image', {
                    'contents': [{'parts': [{'text': 'Draw a cube'}]}],
                    'generationConfig': {'responseModalities': ['IMAGE']}})
        self.assertEqual(result['candidates'][0]['content']['parts'], [original])
        download.assert_called_once_with(('image-id', 'c_one', 'r_one', 'rc_one'))
        self.assertEqual(call.call_count, 1)
        self.assertFalse(self.account.generation_pending)

    def test_text_request_retains_text_mode_and_current_model(self):
        with patch.object(self.account, 'call', return_value=(None, wire().encode())) as call:
            self.account.generate('gemini-web-flash', {
                'contents': [{'parts': [{'text': 'hello'}]}]})
        inner = json.loads(json.loads(call.call_args.kwargs['data']['f.req'])[1])
        selector = json.loads(call.call_args.kwargs['headers']['x-goog-ext-525001261-jspb'])
        self.assertEqual(len(inner), 81)
        self.assertEqual(inner[68], 1)
        self.assertIsNone(inner[49])
        self.assertEqual(selector[4], 'id-flash')

    def test_download_failure_does_not_regenerate_or_return_preview(self):
        with patch.object(self.account, 'call', return_value=(None, wire(images=True).encode())) as call:
            with patch.object(self.account, 'download', side_effect=worker.Failure(502, 'download failed')):
                with self.assertRaisesRegex(worker.Failure, 'download failed'):
                    self.account.generate('gemini-web-pro-image', {'contents': [{'parts': [{'text': 'image'}]}]})
        self.assertEqual(call.call_count, 1)

    def test_original_rpc_and_text_url_hops_return_real_bytes(self):
        output = io.BytesIO()
        Image.new('RGB', (16, 8), 'blue').save(output, 'JPEG')
        data = output.getvalue()
        responses = [(SimpleNamespace(status_code=200, headers={'content-type': 'text/plain'}), b'https://work.fife.usercontent.google.com/rd-gg-dl/authorize'),
                     (SimpleNamespace(status_code=200, headers={'content-type': 'text/plain'}), b'https://lh3.googleusercontent.com/rd-gg-dl/original'),
                     (SimpleNamespace(status_code=200, headers={'content-type': 'image/jpeg'}), data)]
        with patch.object(self.account, 'batch', return_value=['https://lh3.googleusercontent.com/gg-dl/ref']) as rpc:
            with patch.object(self.account, 'call', side_effect=responses) as call:
                part = self.account.download(('image-id', 'c', 'r', 'rc'))
        self.assertEqual(base64.b64decode(part['inlineData']['data']), data)
        self.assertEqual(rpc.call_args.args[0], 'c8o8Fe')
        self.assertTrue(call.call_args_list[0].args[1].endswith('=d-I?alr=yes'))

    def test_untrusted_hops_rejected_before_request(self):
        for url in ['http://lh3.google.com/a', 'https://lh3.google.com.evil.test/a',
                    'https://lh3.google.com@127.0.0.1/a', 'https://lh3.google.com:8443/a',
                    'https://169.254.169.254/a', 'https://lh3.google.com/a\r\nb', None]:
            with self.assertRaises(worker.Failure):
                worker.image_url(url)
        with patch.object(self.account, 'batch', return_value=['https://lh3.googleusercontent.com/gg-dl/ref']):
            with patch.object(self.account, 'call', return_value=(SimpleNamespace(status_code=302, headers={'location': 'http://127.0.0.1/'}), b'')) as call:
                with self.assertRaises(worker.Failure):
                    self.account.download(('i', 'c', 'r', 'rc'))
                self.assertEqual(call.call_count, 1)

    def test_cookie_deletion_persistence_and_restart(self):
        other = worker.Account(bundle('cookie-two'), persist_callback=lambda state: None)
        self.account.session.cookies.set('SIDCC', 'updated', domain='.google.com', path='/')
        self.account.persist()
        self.account.session.cookies.delete('__Secure-1PSID', domain='.google.com', path='/')
        self.account.persist()
        restored = self.restore()
        try:
            self.assertIsNone(restored.session.cookies.get('__Secure-1PSID'))
            self.assertEqual(restored.session.cookies.get('SIDCC'), 'updated')
            self.assertEqual(other.session.cookies.get('__Secure-1PSID'), 'cookie-two')
            self.assertIsNone(other.session.cookies.get('SIDCC'))
        finally:
            restored.close()
            other.close()

    def test_auth_failure_persists_pause_and_transport_error_is_redacted(self):
        with patch.object(self.account.session, 'request', return_value=SimpleNamespace(status_code=403, headers={})):
            with self.assertRaises(worker.Failure):
                self.account.call('GET', worker.ORIGIN + '/app')
        restored = self.restore()
        with patch.object(restored, 'call') as call:
            with self.assertRaisesRegex(worker.Failure, 'paused'):
                restored.generate('gemini-web-flash', {'contents': [{'parts': [{'text': 'x'}]}]})
            call.assert_not_called()
        with patch.object(self.account.session, 'request', side_effect=RuntimeError('secret cookie signed-url')):
            with self.assertRaises(worker.Failure) as error:
                self.account.call('GET', worker.ORIGIN)
            self.assertNotIn('secret', str(error.exception))
        restored.session.close()

    def test_uncertain_generation_is_not_repeated_after_restart(self):
        with patch.object(self.account, 'call', side_effect=worker.Failure(502, 'uncertain transport')) as call:
            with self.assertRaises(worker.Failure):
                self.account.generate('gemini-web-flash', {'contents': [{'parts': [{'text': 'x'}]}]})
            self.assertEqual(call.call_count, 1)
        restored = self.restore()
        with patch.object(restored, 'call') as call:
            with self.assertRaisesRegex(worker.Failure, 'paused'):
                restored.generate('gemini-web-flash', {'contents': [{'parts': [{'text': 'x'}]}]})
            call.assert_not_called()
        restored.session.close()

    def test_import_requires_renewal_and_refresh_uses_same_jar(self):
        imported = worker.Account(bundle('cookie-fresh'), persist_callback=self.save)
        self.assertEqual(imported.last_refresh, 0)
        def rotate(*args, **kwargs):
            imported.session.cookies.set('__Secure-1PSIDTS', 'rotated', domain='.google.com', path='/')
            return None, b'[]'
        with patch.object(imported, 'call', side_effect=rotate) as call:
            with patch.object(imported, 'bootstrap') as bootstrap:
                imported.refresh()
        self.assertEqual(call.call_args.args[1], 'https://accounts.google.com/RotateCookies')
        bootstrap.assert_called_once()
        restored = self.restore()
        self.assertEqual(restored.session.cookies.get('__Secure-1PSIDTS'), 'rotated')
        self.assertGreater(restored.last_refresh, 0)
        imported.session.close()
        restored.session.close()

    def test_memory_session_persists_complete_cookie_records(self):
        writes = []
        account = worker.Account(bundle={'user_agent': 'test', 'cookies': [{
            'name': '__Secure-1PSID', 'value': 'cookie', 'domain': '.google.com',
            'path': '/', 'secure': True, 'httpOnly': True, 'sameSite': 'Lax',
            'partitionKey': {'topLevelSite': 'https://gemini.google.com'}}]},
            persist_callback=writes.append)
        try:
            account.session.cookies.set('SIDCC', 'rotated', domain='.google.com', path='/')
            account.persist()
            self.assertEqual(writes[-1]['cookies'][0]['httpOnly'], True)
            self.assertEqual(writes[-1]['cookies'][0]['sameSite'], 'Lax')
            self.assertIn('partitionKey', writes[-1]['cookies'][0])
            self.assertEqual(next(c for c in writes[-1]['cookies'] if c['name'] == 'SIDCC')['value'], 'rotated')
        finally:
            account.close()

    def test_control_owner_keeps_its_saved_version_and_reloads_operator_import(self):
        control = Control()
        owner = worker.SessionOwner('28', control)
        with patch.object(worker.Account, 'refresh', autospec=True,
                          side_effect=lambda account: account.persist()):
            owner.run()
            account = owner.account
            self.assertEqual(owner.runtime['version'], 2)
            owner.run()
            self.assertIs(owner.account, account)
            self.assertEqual(control.writes, [1])  # unchanged state does not write again
            control.records['28']['runtime']['user_agent'] = 'new-browser'
            control.records['28']['runtime']['version'] += 1
            owner.run()
            self.assertIsNot(owner.account, account)
            self.assertEqual(owner.account.session.headers['User-Agent'], 'new-browser')
        owner.close()

    def test_control_maintenance_refreshes_compact_account_list(self):
        control = Control()
        adapter = worker.ControlAdapter(control)
        with patch.object(worker.Account, 'refresh', autospec=True,
                          side_effect=lambda account: account.persist()) as refresh:
            with patch.object(adapter.stopping, 'wait', side_effect=InterruptedError):
                with self.assertRaises(InterruptedError):
                    adapter.maintain()
        self.assertEqual(refresh.call_count, 1)
        self.assertEqual(control.writes, [1])
        adapter.accounts['28'].close()

    def test_idle_poll_is_read_only_and_paused_sessions_fail_deploy_check(self):
        control = Control()
        adapter = worker.ControlAdapter(control)
        with patch.object(control, 'warm_accounts', return_value={'accounts': [], 'protocol_version': 1}):
            with patch.object(control, 'acquire') as acquire, patch.object(control, 'load') as load:
                for _ in range(5):
                    self.assertEqual(adapter.check_control(), [])
                acquire.assert_not_called()
                load.assert_not_called()
        with patch.object(control, 'acquire') as acquire, patch.object(worker.Account, 'call') as upstream:
            adapter.check_accounts(['28'])
            self.assertTrue(adapter.ready())
            control.records['28']['concurrency'] = 20
            with self.assertRaisesRegex(worker.Failure, 'concurrency'):
                adapter.check_accounts(['28'])
            control.records['28']['concurrency'] = 1
            control.records['28']['runtime']['state']['generation_pending'] = True
            with self.assertRaisesRegex(worker.Failure, 'paused'):
                adapter.check_accounts(['28'])
            acquire.assert_not_called()
            upstream.assert_not_called()
        with patch.object(control, 'warm_accounts', return_value={'accounts': [28]}):
            with self.assertRaisesRegex(worker.Failure, 'Incompatible'):
                adapter.check_control()
            adapter.last_control_ok = time.monotonic() - 4 * worker.POLL_SECONDS
            self.assertFalse(adapter.ready())
        self.assertTrue(adapter.ready(), 'long maintenance must not mask a healthy control API')
        adapter.stopping.set()
        self.assertFalse(adapter.ready())

    def test_image_pixel_limit_rejects_before_decode(self):
        output = io.BytesIO()
        Image.new('RGB', (16, 8), 'blue').save(output, 'PNG')
        response = SimpleNamespace(status_code=200, headers={'content-type': 'image/png'})
        with patch.object(self.account, 'batch', return_value=['https://lh3.googleusercontent.com/gg-dl/ref']):
            with patch.object(self.account, 'call', return_value=(response, output.getvalue())) as call:
                with patch.object(worker, 'MAX_IMAGE_PIXELS', 100), patch.object(Image.Image, 'load') as decode:
                    with self.assertRaisesRegex(worker.Failure, 'Invalid original'):
                        self.account.download(('i', 'c', 'r', 'rc'))
                    decode.assert_not_called()
                    self.assertEqual(call.call_count, 1)

    def test_control_redirect_never_forwards_admin_key(self):
        paths = []
        class Redirect(worker.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def do_GET(self):
                paths.append(self.path)
                self.send_response(302)
                self.send_header('Location', '/credential-trap')
                self.end_headers()
        server = worker.ThreadingHTTPServer(('127.0.0.1', 0), Redirect)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            control = worker.SessionControl('http://127.0.0.1:%s' % server.server_port, 'a' * 40)
            with self.assertRaises(worker.Failure):
                control.load('28')
            self.assertEqual(paths, ['/edge/gemini-web/accounts/28/session'])
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_drain_finishes_inflight_request_and_releases_lease(self):
        control = Control()
        adapter = worker.ControlAdapter(control)
        server = worker.Server(('127.0.0.1', 0), adapter)
        serving = threading.Thread(target=server.serve_forever)
        serving.start()
        entered, complete, closed = threading.Event(), threading.Event(), threading.Event()
        statuses = []
        def generate(*_):
            entered.set()
            self.assertTrue(complete.wait(5))
            return {'candidates': []}
        def request():
            connection = http.client.HTTPConnection(*server.server_address, timeout=5)
            connection.request('POST', '/v1beta/models/gemini-web-flash:generateContent',
                json.dumps({'contents': [{'parts': [{'text': 'test'}]}]}),
                {'x-goog-api-key': 'a' * 40, 'x-tokenkey-gemini-web-account-id': '28'})
            response = connection.getresponse()
            statuses.append(response.status)
            response.read()
            connection.close()
        def close():
            server.server_close()
            closed.set()
        try:
            with patch.object(worker.Account, 'generate', side_effect=generate):
                client = threading.Thread(target=request)
                client.start()
                self.assertTrue(entered.wait(5))
                server.drain()
                serving.join(timeout=5)
                closing = threading.Thread(target=close)
                closing.start()
                self.assertFalse(closed.wait(0.05))
                self.assertFalse(adapter.ready())
                complete.set()
                client.join(timeout=5)
                closing.join(timeout=5)
                self.assertTrue(closed.is_set())
            self.assertEqual(statuses, [200])
            self.assertEqual(control.leases, {})
        finally:
            complete.set()
            server.shutdown()
            server.server_close()
            serving.join(timeout=5)
            for owner in adapter.accounts.values():
                owner.close()

    def test_large_image_decode_and_buffer_fit_container_budget(self):
        # Measure C-level Pillow/curl allocations too; tracemalloc omits them.
        script = '''
import io, json, resource, sys
from types import SimpleNamespace
from PIL import Image
import worker
output = io.BytesIO()
picture = Image.effect_noise((4000, 4000), 100).convert('RGB')
picture.save(output, 'JPEG', quality=95)
picture.close()
data = output.getvalue()
output.close()
account = worker.Account({'user_agent':'synthetic', 'cookies':[
    {'name':'SID','value':'synthetic','domain':'.google.com'}]}, persist_callback=lambda state: None)
account.batch = lambda *_: ['https://lh3.googleusercontent.com/gg-dl/ref']
def request(*args, **kwargs):
    kwargs['content_callback'](data)
    return SimpleNamespace(status_code=200, headers={'content-type':'image/jpeg'})
account.session.request = request
part = account.download(('i','c','r','rc'))
payload = json.dumps({'candidates':[{'content':{'parts':[part]}}]}).encode()
assert len(payload) > 1000000
account.close()
rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
rss_bytes = rss if sys.platform == 'darwin' else rss * 1024
print(rss_bytes)
assert rss_bytes < 384 * 1024 * 1024, rss_bytes
'''
        result = subprocess.run([sys.executable, '-c', script], cwd=worker.os.path.dirname(worker.__file__),
                                capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_control_lease_prevents_a_second_process_and_fences_stale_save(self):
        control = Control()
        one, two = worker.SessionOwner('28', control), worker.SessionOwner('28', control)
        def refresh(account):
            with self.assertRaisesRegex(worker.Failure, 'busy'):
                two.run()
            account.persist()
        with patch.object(worker.Account, 'refresh', side_effect=refresh, autospec=True):
            one.run()
        self.assertEqual(control.writes, [1])
        self.assertEqual(control.leases, {})
        with patch.object(worker.Account, 'refresh', side_effect=worker.SessionVersionConflict()):
            with self.assertRaises(worker.Failure):
                one.run()
        self.assertIsNone(one.account)
        self.assertEqual(control.leases, {})
        one.close()
        two.close()

    def test_expired_operation_cannot_contact_google_or_persist(self):
        self.account.deadline = time.monotonic() - 1
        with patch.object(self.account.session, 'request') as request:
            with self.assertRaisesRegex(worker.Failure, 'expired'):
                self.account.call('GET', worker.ORIGIN)
        request.assert_not_called()
        owner = worker.SessionOwner('28', Control())
        with self.assertRaisesRegex(worker.Failure, 'expired'):
            owner.persist({})
        with self.assertRaises(ValueError):
            worker.SessionControl('', 'a' * 40)

    def test_release_failure_does_not_turn_completed_generation_into_retry(self):
        control = Control()
        owner = worker.SessionOwner('28', control)
        with patch.object(control, 'release', side_effect=worker.Failure(503, 'unavailable')):
            with patch.object(worker.Account, 'generate', return_value={'completed': True}) as generate:
                result = owner.run(key='a' * 40, model='gemini-web-flash',
                                   body={'contents': [{'parts': [{'text': 'test'}]}]})
        self.assertEqual(result, {'completed': True})
        self.assertEqual(generate.call_count, 1)
        self.assertIsNone(owner.account)
        with self.assertRaises(worker.SessionVersionConflict):
            control.acquire('28', 'another-owner')

    def test_http_auth_and_buffered_sse(self):
        key = 'a' * 40
        control = Control()
        adapter = worker.ControlAdapter(control)
        server = worker.Server(('127.0.0.1', 0), adapter)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            connection = http.client.HTTPConnection(*server.server_address)
            path = '/v1beta/models/gemini-web-flash:streamGenerateContent?alt=sse'
            request = json.dumps({'contents': [{'parts': [{'text': 'test'}]}]})
            for invalid in ('', 'bad'):
                connection.request('POST', path, request, {
                    'x-goog-api-key': invalid, 'x-tokenkey-gemini-web-account-id': '28'})
                response = connection.getresponse()
                self.assertEqual(response.status, 401)
                response.read()
            with patch.object(worker.Account, 'generate', return_value={
                    'candidates': [{'content': {'parts': [{'text': 'answer'}]}}]}):
                connection.request('POST', path, request, {
                    'x-goog-api-key': key, 'x-tokenkey-gemini-web-account-id': '28'})
                response = connection.getresponse()
                self.assertEqual(response.status, 200)
                self.assertEqual(response.headers['Content-Type'], 'text/event-stream')
                value = json.loads(response.read().decode().removeprefix('data: ').strip())
                self.assertEqual(value['candidates'][0]['content']['parts'], [{'text': 'answer'}])
            held = []
            for _ in range(4):
                server.generations.acquire()
                held.append(True)
            try:
                with patch.object(worker.Account, 'generate') as generate:
                    connection.request('POST', path, request, {
                        'x-goog-api-key': key, 'x-tokenkey-gemini-web-account-id': '28'})
                    response = connection.getresponse()
                    self.assertEqual(response.status, 503)
                    self.assertEqual(response.headers['Retry-After'], '1')
                    response.read()
                    generate.assert_not_called()
                connection.request('GET', '/healthz')
                response = connection.getresponse()
                self.assertEqual(response.status, 200)
                response.read()
            finally:
                for _ in held:
                    server.generations.release()
            server.images.acquire()
            try:
                with patch.object(worker.Account, 'generate') as generate:
                    connection.request('POST', '/v1beta/models/gemini-web-pro-image:generateContent', request, {
                        'x-goog-api-key': key, 'x-tokenkey-gemini-web-account-id': '28'})
                    response = connection.getresponse()
                    self.assertEqual(response.status, 503)
                    response.read()
                    generate.assert_not_called()
            finally:
                server.images.release()
            adapter.stopping.set()
            connection.request('GET', '/readyz')
            response = connection.getresponse()
            self.assertEqual(response.status, 503)
            response.read()
            connection.close()
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
            for owner in adapter.accounts.values():
                owner.close()


if __name__ == '__main__':
    unittest.main()
