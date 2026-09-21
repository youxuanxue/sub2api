import base64
import copy
import http.client
import io
import json
import threading
import time
import unittest
from unittest.mock import patch
from types import SimpleNamespace

from PIL import Image
import worker


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
        self.records = {'28': {'account_id': 28, 'api_key': 'a' * 40,
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
        return {'accounts': [int(k) for k in self.records]}


class WorkerTests(unittest.TestCase):
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
            self.assertEqual(control.writes, [1, 2])
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
            with patch.object(worker.time, 'sleep', side_effect=InterruptedError):
                with self.assertRaises(InterruptedError):
                    adapter.maintain()
        self.assertEqual(refresh.call_count, 1)
        self.assertEqual(control.writes, [1])
        adapter.accounts['28'].close()

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
            connection.close()
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
            for owner in adapter.accounts.values():
                owner.close()


if __name__ == '__main__':
    unittest.main()
