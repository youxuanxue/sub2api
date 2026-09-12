"""Fixed evidence, collection readiness and negative-auth behavior (US-054)."""
import base64
import contextlib
import http.server
import json
from pathlib import Path
import tempfile
import subprocess
import sys
import threading
import time
import unittest
from unittest.mock import MagicMock, patch

import prod_replay as replay
from test_prod_replay import row, sample, server


def capsule(r=None, body=b'{"model":"m1","api_key":"original-business-field"}'):
    r = r or row()
    return {'envelope_sha256': 'a'*64, 'request': dict(r, body=base64.b64encode(body).decode(),
            method='POST', path=r['inbound_endpoint'], headers={'Anthropic-Beta':'real-beta'},
            captured_at='2026-09-12T00:00:00Z')}


class CorpusTest(unittest.TestCase):
    def test_encrypted_bytes_preserve_business_secrets_but_headers_reject_auth(self):
        item = capsule()
        result = replay.sample_from_capsule(row(), item)
        self.assertIn(b'original-business-field', result['body'])
        self.assertEqual(result['headers'], {'Anthropic-Beta':'real-beta'})
        self.assertNotIn('original-business-field', json.dumps(replay.sample_manifest(result)))
        item['request']['headers']['Authorization'] = 'secret'
        with self.assertRaisesRegex(replay.ReplayError, 'headers_invalid'):
            replay.sample_from_capsule(row(), item)
        item = capsule(); item['request']['api_key_id'] = 123
        with self.assertRaisesRegex(replay.ReplayError, 'identity_mismatch'):
            replay.sample_from_capsule(row(), item)

    def test_get_needs_recorded_method_and_path_and_gemini_needs_model_action(self):
        r = row(model='', endpoint='/v1/models')
        payload = {'request_id':r['request_id'], 'request':{'method':'GET','original_path':'/v1/models','body':None}}
        result = replay.sample_from_capture(r, payload)
        self.assertEqual((result['method'],result['body']), ('GET',b''))
        del payload['request']['method']
        with self.assertRaises(replay.ReplayError): replay.sample_from_capture(r,payload)
        r = row(model='gemini-2.5',endpoint='/v1beta/models')
        item = capsule(r,b'{"contents":[{"parts":[{"text":"actual"}]}]}')
        item['request']['path']='/v1beta/models/gemini-2.5:generateContent'
        self.assertEqual(replay.sample_from_capsule(r,item)['path'],item['request']['path'])
        item['request']['path']='/v1beta/models/wrong:generateContent'
        with self.assertRaisesRegex(replay.ReplayError,'model_mismatch'): replay.sample_from_capsule(r,item)

    def test_frozen_corpus_ignores_new_traffic_and_fails_tamper_missing_expiry(self):
        item = capsule(); selected = replay.sample_from_capsule(row(),item)
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            replay.write_json(root/"bluegreen-replay-observed.json", {"rows":[row()]})
            with patch.object(replay,'load_capsules',return_value={row()['request_id']:item}), \
                 patch.object(replay,'collect',return_value=([selected],{},1)) as collect:
                first=replay.frozen_samples(root,{},[])
                second=replay.frozen_samples(root,{},[])
                self.assertEqual(first,second);self.assertEqual(collect.call_count,1)
                item['request']['body']=base64.b64encode(b'{"model":"m1","input":"changed"}').decode()
                with self.assertRaisesRegex(replay.ReplayError,'frozen_capture_changed'): replay.frozen_samples(root,{},[])
            with patch.object(replay,'load_capsules',return_value={}):
                with self.assertRaisesRegex(replay.ReplayError,'frozen_capsule_missing'): replay.frozen_samples(root,{},[])
            plan=json.loads((root/'bluegreen-replay-corpus.json').read_bytes());plan['created_at']=1
            replay.write_json(root/'bluegreen-replay-corpus.json',plan)
            with patch.object(replay,'load_capsules',return_value={}):
                with self.assertRaisesRegex(replay.ReplayError,'expired'): replay.frozen_samples(root,{},[])

    def test_revoked_auth_requires_real_positive_business_coverage(self):
        revoked=dict(row(),key_state='disabled',key_replayable=False)
        valid=row(user=2)
        with patch.object(replay,'sql',return_value=json.dumps(revoked)):
            samples,gaps,total=replay.collect(Path('/unused'),[],{})
        self.assertEqual(total,1);self.assertEqual(samples[0]['source'],'synthetic_auth_negative')
        self.assertEqual(gaps,{'positive_capability_missing':1})
        with patch.object(replay,'sql',return_value='\n'.join(map(json.dumps,[revoked,valid]))):
            samples,gaps,total=replay.collect(Path('/unused'),[],{valid['request_id']:capsule(valid)})
        self.assertEqual(gaps,{});self.assertEqual(total,2)
        self.assertEqual(samples[1]['row']['api_key_id'],valid['api_key_id'])

    def test_gaps_do_not_freeze_or_start_paid_execution(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(replay,'load_capsules',return_value={}), \
             patch.object(replay,'collect',return_value=([sample()],{'capture_missing':1},2)):
            root=Path(tmp)
            self.assertIsNone(replay.frozen_samples(root,{},[])[3])
            self.assertFalse((root/'bluegreen-replay-corpus.json').exists())
        # Existing orchestration assertion plus a direct no-start check.
        with tempfile.TemporaryDirectory() as tmp, patch.object(replay,'prepared',return_value=('b'*64,{'target':'green'})), \
             patch.object(replay,'inspect',return_value={}), patch.object(replay,'snapshot',return_value={'target':'green'}), \
             patch.object(replay,'Sandbox') as box, patch.object(replay,'frozen_samples',return_value=([sample()],{'missing':1},2,None)):
            receipt=replay.replay('1.2.3',Path(tmp))
            box.return_value.start.assert_not_called();self.assertEqual(receipt['reason'],'collection_not_ready')

    def test_observed_gaps_do_not_disappear_when_window_moves(self):
        r = row()
        with tempfile.TemporaryDirectory() as tmp, patch.object(replay, 'read_legacy', side_effect=replay.ReplayError('capture_missing')):
            root = Path(tmp)
            with patch.object(replay, 'sql', return_value=json.dumps(r)):
                _, gaps, total = replay.collect(root, [], {}, pin_observations=True)
                self.assertEqual(total, 1)
            with patch.object(replay, 'sql', return_value=json.dumps(row(2, 'new-model'))):
                _, gaps, total = replay.collect(root, [], {}, pin_observations=True)
                self.assertEqual(total, 1)
                self.assertEqual(gaps, {'capture_missing': 1})
            with patch.object(replay, 'sql', return_value=json.dumps(dict(r, request_id='new-real-request'))):
                item = capsule(dict(r, request_id='new-real-request'))
                samples, gaps, total = replay.collect(root, [], {'new-real-request': item}, pin_observations=True)
                self.assertEqual(total, 1); self.assertEqual(gaps, {})
                self.assertEqual(samples[0]['row']['request_id'], 'new-real-request')

    def test_negative_auth_requires_exact_code_and_original_state(self):
        s=replay.auth_sample(dict(row(),key_state='disabled',key_replayable=False))
        for code,passed in [('API_KEY_DISABLED',True),('ACCESS_DENIED',False),('INVALID_API_KEY',False)]:
            with server(401,json.dumps({'code':code}).encode()) as (port,requests):
                result=replay.execute(s,'original-revoked-key',port,'auth-test')
                self.assertEqual(result['passed'],passed)
                self.assertEqual(requests[0][2]['Authorization'],'Bearer original-revoked-key')
        expired=replay.auth_sample(dict(row(endpoint='/v1/models'),key_state='expired'))
        self.assertEqual(expired['method'],'POST');self.assertEqual(expired['expected_status'],403)
        with self.assertRaises(replay.ReplayError):replay.auth_sample(dict(row(),key_state='missing'))

    def test_audio_multipart_replays_exact_binary_and_plain_text_contract(self):
        r = row(model='whisper-1', endpoint='/v1/audio/transcriptions')
        r['multimodal_present'] = True
        raw = b'--real-boundary\r\nContent-Disposition: form-data; name="file"; filename="input.wav"\r\n\r\nRIFF\0private-audio\r\n--real-boundary--\r\n'
        item = capsule(r,raw);item['request']['headers']={'Content-Type':'multipart/form-data; boundary=real-boundary'}
        selected = replay.sample_from_capsule(r,item)
        with server(200,b'recognized speech','text/plain') as (port,requests):
            result = replay.execute(selected,'original-key',port,'audio-replay-test')
            self.assertTrue(result['passed']);self.assertEqual(requests[0][1],raw)
            self.assertEqual(requests[0][2]['Content-Type'],item['request']['headers']['Content-Type'])
        with self.assertRaisesRegex(replay.ReplayError,'body_missing_or_truncated'):
            replay.sample_from_capture(r,{'request_id':r['request_id'],'request':{'path':r['inbound_endpoint'],'body':{'_qa_body_omitted':True}}})
        self.assertEqual(replay.response_reason(200,'text/plain',b'plain',False,'/v1/messages'),'response_content_type')

    def test_metadata_get_cannot_supply_missing_model_diversity(self):
        samples = [sample(), dict(sample(2, '', '/v1/models'), method='GET', body=b'')]
        before = {'target':'green'}
        with tempfile.TemporaryDirectory() as tmp, patch.object(replay,'prepared',return_value=('b'*64,before)), \
             patch.object(replay,'inspect',return_value={}), patch.object(replay,'snapshot',return_value=before), \
             patch.object(replay,'Sandbox') as box, patch.object(replay,'frozen_samples',return_value=(samples,{},2,'f'*64)):
            receipt = replay.replay('1.2.3',Path(tmp))
            self.assertEqual(receipt['reason'],'insufficient_diversity')
            self.assertEqual(receipt['coverage']['models'],1)
            self.assertFalse(receipt['upstream_quota_consumed'])
            box.return_value.start.assert_not_called()

    def test_decrypt_pipe_is_bounded_private_and_cleans_up(self):
        actual_popen = subprocess.Popen
        captured = []
        def child(args, **kwargs):
            captured.extend(args)
            return actual_popen([sys.executable, '-c', 'import sys;sys.stdout.write(' + repr(json.dumps(capsule()) + '\n') + ')'], **kwargs)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root/'app/release_replay').mkdir(parents=True, mode=0o700)
            (root/'replay-keys').mkdir(mode=0o700)
            key = root/'replay-keys/private.pem'; key.write_text('private-test-key'); key.chmod(0o600)
            with patch.object(replay.Path,'read_text',return_value='MemAvailable: 4096000'), \
                 patch.object(replay.subprocess,'Popen',side_effect=child), patch.object(replay,'run',return_value=b'') as cleanup:
                items = replay.load_capsules(root, {'Image':'sha256:immutable'})
            self.assertEqual(items[row()['request_id']],capsule())
            self.assertEqual(captured[captured.index('--network')+1],'none')
            self.assertEqual(captured[captured.index('--log-driver')+1],'none')
            self.assertIn('--read-only',captured);self.assertNotIn('private-test-key',repr(captured))
            self.assertEqual(cleanup.call_args.args[0][:3],['docker','rm','-f'])

    def test_sse_requires_dispatched_frame_and_accepts_multiline_json(self):
        self.assertEqual(replay.response_reason(200,'text/event-stream',b'data: [DONE]\n',True),'stream_frame_incomplete')
        self.assertEqual(replay.response_reason(200,'text/event-stream',b'data: {\ndata: "type":"message_stop"}\n\n',True),'ok')
        self.assertEqual(replay.response_reason(200,'text/event-stream',b'data: [DONE]\n\nevent:error\n\n',True),'upstream_error')

    def test_old_green_receipt_cannot_satisfy_new_policy(self):
        receipt=replay.seal({'schema':1,'tag':'1.2.3','prepared_receipt':'b'*64,'verdict':'green',
                            'cutover':False,'approval_pending':True,'finished_at':time.time()})
        with self.assertRaisesRegex(replay.ReplayError,'policy_outdated'):
            replay.validate_receipt(receipt,receipt['receipt_sha256'],'b'*64,'1.2.3')

if __name__=='__main__': unittest.main()
