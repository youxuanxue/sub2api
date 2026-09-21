import base64
import http.client
import io
import json
from pathlib import Path
import tempfile
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


def seed(root, name, cookie):
    directory = root / name
    directory.mkdir()
    (directory / 'bundle.json').write_text(json.dumps({'ua': 'test', 'cookies': [
        {'name': '__Secure-1PSID', 'value': cookie, 'domain': '.google.com', 'path': '/', 'secure': True}]}))
    return directory


class WorkerTests(unittest.TestCase):
    def test_pinned_transport_can_configure_profile_without_network(self):
        from curl_cffi import Curl
        curl = Curl()
        try:
            self.assertEqual(curl.impersonate(worker.BROWSER_PROFILE), 0)
        finally:
            curl.close()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.account = worker.Account(seed(self.root, 'one', 'cookie-one'))
        self.account.models = {'Flash': ('id-flash', 1, 2), 'Pro': ('id-pro', 3, 2)}
        self.account.ready_at = time.time()
        self.account.last_refresh = time.time()
        self.account.fields = {'SNlM0e': 'fixture-at', 'cfb2h': 'fixture-build', 'FdrFJe': 'fixture-session'}

    def tearDown(self):
        self.account.session.close()
        self.temp.cleanup()

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

    def test_isolation_atomic_persistence_and_restart(self):
        other = worker.Account(seed(self.root, 'two', 'cookie-two'))
        self.account.session.cookies.set('SIDCC', 'updated', domain='.google.com', path='/')
        self.account.persist()
        restored = worker.Account(self.root / 'one')
        self.assertEqual(restored.session.cookies.get('SIDCC'), 'updated')
        self.assertEqual(other.session.cookies.get('__Secure-1PSID'), 'cookie-two')
        self.assertIsNone(other.session.cookies.get('SIDCC'))
        self.assertEqual((self.root / 'one' / 'state.json').stat().st_mode & 0o777, 0o600)
        self.account.lock.acquire()
        with patch.object(self.account, 'call') as call:
            with self.assertRaisesRegex(worker.Failure, 'active request'):
                self.account.generate('gemini-web-flash', {'contents': [{'parts': [{'text': 'x'}]}]})
            call.assert_not_called()
        self.account.lock.release()
        restored.session.close()
        other.session.close()

    def test_auth_failure_persists_pause_and_transport_error_is_redacted(self):
        with patch.object(self.account.session, 'request', return_value=SimpleNamespace(status_code=403, headers={})):
            with self.assertRaises(worker.Failure):
                self.account.call('GET', worker.ORIGIN + '/app')
        restored = worker.Account(self.root / 'one')
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
        restored = worker.Account(self.root / 'one')
        with patch.object(restored, 'call') as call:
            with self.assertRaisesRegex(worker.Failure, 'paused'):
                restored.generate('gemini-web-flash', {'contents': [{'parts': [{'text': 'x'}]}]})
            call.assert_not_called()
        restored.session.close()

    def test_import_requires_renewal_and_refresh_uses_same_jar(self):
        imported = worker.Account(seed(self.root, 'fresh', 'cookie-fresh'))
        self.assertEqual(imported.last_refresh, 0)
        def rotate(*args, **kwargs):
            imported.session.cookies.set('__Secure-1PSIDTS', 'rotated', domain='.google.com', path='/')
            return None, b'[]'
        with patch.object(imported, 'call', side_effect=rotate) as call:
            with patch.object(imported, 'bootstrap') as bootstrap:
                imported.refresh()
        self.assertEqual(call.call_args.args[1], 'https://accounts.google.com/RotateCookies')
        bootstrap.assert_called_once()
        restored = worker.Account(self.root / 'fresh')
        self.assertEqual(restored.session.cookies.get('__Secure-1PSIDTS'), 'rotated')
        self.assertGreater(restored.last_refresh, 0)
        imported.session.close()
        restored.session.close()

    def test_keys_select_separate_accounts_and_second_owner_is_refused(self):
        seed(self.root, 'two', 'cookie-two')
        (self.root / 'accounts.json').write_text(json.dumps([
            {'id': 'one', 'api_key': 'a' * 40}, {'id': 'two', 'api_key': 'b' * 40}]))
        adapter = worker.Adapter(self.root)
        try:
            self.assertEqual(adapter.authorize('a' * 40).account.session.cookies.get('__Secure-1PSID'), 'cookie-one')
            self.assertEqual(adapter.authorize('b' * 40).account.session.cookies.get('__Secure-1PSID'), 'cookie-two')
            with self.assertRaises(worker.Failure):
                adapter.authorize('wrong-key')
            with self.assertRaises(BlockingIOError):
                worker.Adapter(self.root)
        finally:
            for account in adapter.accounts.values():
                account.close()
            adapter.lease.close()

    def test_hot_reload_replaces_only_the_changed_account(self):
        seed(self.root, 'two', 'cookie-two')
        (self.root / 'accounts.json').write_text(json.dumps([
            {'id': 'one', 'api_key': 'a' * 40}, {'id': 'two', 'api_key': 'b' * 40}]))
        adapter = worker.Adapter(self.root)
        try:
            one = adapter.authorize('a' * 40)
            two = adapter.authorize('b' * 40)
            previous_two = two.account
            # An importer discards worker-owned refreshed state before it makes
            # a newly exported browser session visible.
            worker.atomic_json(two.root / 'state.json', {'cookies': [{
                'name': '__Secure-1PSID', 'value': 'old-worker-cookie',
                'domain': '.google.com', 'path': '/', 'secure': True}]})
            (two.root / 'state.json').unlink()
            worker.atomic_json(two.root / 'bundle.json', {'ua': 'updated', 'cookies': [
                {'name': '__Secure-1PSID', 'value': 'cookie-two-updated',
                 'domain': '.google.com', 'path': '/', 'secure': True}]})
            # A request for account one must not prevent account two's session
            # update. Only the account being swapped acquires its operation lock.
            self.assertTrue(one.operation.acquire(blocking=False))
            try:
                adapter.reload_from_disk()
            finally:
                one.operation.release()
            self.assertIs(one, adapter.authorize('a' * 40))
            self.assertIs(two, adapter.authorize('b' * 40))
            self.assertIsNot(two.account, previous_two)
            self.assertEqual(two.account.session.headers['User-Agent'], 'updated')
            self.assertEqual(two.account.session.cookies.get('__Secure-1PSID'), 'cookie-two-updated')
        finally:
            for account in adapter.accounts.values():
                account.close()
            adapter.lease.close()

    def test_bad_hot_reload_preserves_live_account(self):
        (self.root / 'accounts.json').write_text(json.dumps([{'id': 'one', 'api_key': 'a' * 40}]))
        adapter = worker.Adapter(self.root)
        try:
            managed = adapter.authorize('a' * 40)
            previous = managed.account
            worker.atomic_json(managed.root / 'bundle.json', {'ua': '', 'cookies': []})
            with self.assertRaises((KeyError, ValueError)):
                adapter.reload_from_disk()
            self.assertIs(adapter.authorize('a' * 40).account, previous)
        finally:
            for account in adapter.accounts.values():
                account.close()
            adapter.lease.close()

    def test_http_auth_and_buffered_sse(self):
        key = 'a' * 40
        adapter = SimpleNamespace(authorize=lambda value: self.account if value == key else (_ for _ in ()).throw(worker.Failure(401, 'Invalid worker API key')))
        server = worker.Server(('127.0.0.1', 0), adapter)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            connection = http.client.HTTPConnection(*server.server_address)
            path = '/v1beta/models/gemini-web-flash:streamGenerateContent?alt=sse'
            request = json.dumps({'contents': [{'parts': [{'text': 'test'}]}]})
            connection.request('POST', path, request, {'x-goog-api-key': 'bad'})
            response = connection.getresponse()
            self.assertEqual(response.status, 401)
            response.read()
            with patch.object(self.account, 'call', return_value=(None, wire().encode())):
                connection.request('POST', path, request, {'x-goog-api-key': key})
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


if __name__ == '__main__':
    unittest.main()
